package sourceinputs

// BSC wiring of the source inputs core (E01-02.inputs, BSC block).
//
// This file is a private local probe, not product code. The driver
// test/harness/bsc_source_inputs.py copies it next to the shared core
// (inputs.go) into extractor/source_inputs of the accepted BSC source tree
// and compiles it there with the accepted offline compiler.
//
// The consumed native dependency of BSC Parlia import is the database handle
// that parlia.New receives. In the pinned BSC source, the engine reads that
// handle only through Get, to load a checkpoint snapshot ("parlia-" plus the
// block hash), and writes it only through Put, to store one. ParliaDB wraps
// that handle: every read goes through the dependency log of the current
// observation, so the response is durable before Parlia receives it and a
// retry receives the saved bytes. Writes and every other method pass through
// to the native database unchanged.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
)

// Request tags of the two logged reads. A request is the tag, one zero byte
// and the exact key.
const (
	ParliaGetTag = "bsc-parlia-db-get-v1"
	ParliaHasTag = "bsc-parlia-db-has-v1"
)

// Response status bytes. A Get response is statusMissing, or statusPresent
// followed by the exact value. A Has response is one status byte.
const (
	statusMissing = 0x00
	statusPresent = 0x01
)

// StateProfile is the static native profile item of every BSC observation:
// the capture profile of E01-02.pins with native snapshots off.
const StateProfile = "state.scheme=hash;gcmode=full;snapshots=off"

var (
	// ErrDependencyNotFound is what Parlia receives for a missing key.
	ErrDependencyNotFound = errors.New("not found")
	// ErrNoObservation refuses a read while no dependency log is attached.
	ErrNoObservation = errors.New("Parlia read outside an observation")
	// ErrUnloggedRead refuses a read method that has no logged form.
	ErrUnloggedRead = errors.New("unlogged read through the Parlia database handle")
)

// ParliaRequest returns the logged request for one read.
func ParliaRequest(tag string, key []byte) []byte {
	request := append([]byte(tag), 0)
	return append(request, key...)
}

// Event is one probe trace record.
type Event map[string]any

// ParliaDB is the database handle given to parlia.New. It is safe for
// concurrent use; reads are logged in the order they take the lock. The core
// log expects native order, so concurrent reads of different keys in a
// nondeterministic order would fail closed on replay with ErrPrefixConflict.
// The private fixture's reads all use one key, so that case is not exercised
// here; it belongs to E01-02.admission.
type ParliaDB struct {
	ethdb.Database

	mu      sync.Mutex
	log     *Log
	id      string
	live    bool
	err     error
	fetches int
	// emit runs while the lock is held; it must not call ParliaDB.
	emit func(Event)
	// Handoff, when set, runs after a response is durable and before Parlia
	// receives it. It runs while the lock is held, so it must not call any
	// ParliaDB method.
	Handoff func(observation string, sequence uint64, response []byte)
}

// NewParliaDB wraps the native database. With live set, a read without an
// attached log goes straight to the native database; this mode exists only
// for the control run that shows what an unlogged read would return.
func NewParliaDB(native ethdb.Database, live bool, emit func(Event)) *ParliaDB {
	if emit == nil {
		emit = func(Event) {}
	}
	return &ParliaDB{Database: native, live: live, emit: emit}
}

// Attach routes reads to the dependency log of one observation.
func (d *ParliaDB) Attach(id string, log *Log) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if log == nil || d.log != nil {
		return fmt.Errorf("%w: attach %s", ErrInvalid, id)
	}
	d.log, d.id = log, id
	return nil
}

// Detach ends the observation and returns the first wiring error, if any.
// Parlia turns a failed snapshot read into a fresh snapshot, so the caller
// must check this error; it is never cleared.
func (d *ParliaDB) Detach() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log, d.id = nil, ""
	return d.err
}

// Err returns the first wiring error.
func (d *ParliaDB) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// Fetches returns how many reads went to the native database.
func (d *ParliaDB) Fetches() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fetches
}

func (d *ParliaDB) stop(err error) error {
	if d.err == nil {
		d.err = err
	}
	return err
}

// consume returns the response for one read: a logged response inside an
// observation, a native read in live mode, and an error otherwise.
func (d *ParliaDB) consume(tag string, key []byte, fetch func() ([]byte, error)) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	native := func() ([]byte, error) {
		d.fetches++
		d.emit(Event{"event": "fetch", "observation": d.id, "tag": tag})
		return fetch()
	}
	if d.log == nil {
		if d.live {
			return native()
		}
		return nil, d.stop(fmt.Errorf("%w: %s", ErrNoObservation, tag))
	}
	request := ParliaRequest(tag, key)
	response, err := d.log.Next(request, native)
	if err != nil {
		// Any error ends the observation, including a native read error:
		// Parlia would otherwise hide it behind a regenerated snapshot.
		return nil, d.stop(err)
	}
	sequence := d.log.Cursor() - 1
	digest := sha256.Sum256(response)
	d.emit(Event{"event": "handoff", "observation": d.id, "sequence": sequence,
		"request_hex": hex.EncodeToString(request), "response_hex": hex.EncodeToString(response),
		"response_sha256": hex.EncodeToString(digest[:])})
	if d.Handoff != nil {
		d.Handoff(d.id, sequence, response)
	}
	return response, nil
}

// Get is the logged form of the native Get.
func (d *ParliaDB) Get(key []byte) ([]byte, error) {
	response, err := d.consume(ParliaGetTag, key, func() ([]byte, error) {
		value, err := d.Database.Get(key)
		if err == nil {
			return append([]byte{statusPresent}, value...), nil
		}
		present, hasErr := d.Database.Has(key)
		if hasErr != nil {
			return nil, hasErr
		}
		if present {
			return nil, err
		}
		return []byte{statusMissing}, nil
	})
	if err != nil {
		return nil, err
	}
	switch {
	case len(response) == 1 && response[0] == statusMissing:
		return nil, ErrDependencyNotFound
	case len(response) >= 1 && response[0] == statusPresent:
		return bytes.Clone(response[1:]), nil
	}
	return nil, d.malformed(ParliaGetTag)
}

// Has is the logged form of the native Has.
func (d *ParliaDB) Has(key []byte) (bool, error) {
	response, err := d.consume(ParliaHasTag, key, func() ([]byte, error) {
		present, err := d.Database.Has(key)
		if err != nil {
			return nil, err
		}
		if present {
			return []byte{statusPresent}, nil
		}
		return []byte{statusMissing}, nil
	})
	if err != nil {
		return false, err
	}
	if len(response) == 1 && (response[0] == statusMissing || response[0] == statusPresent) {
		return response[0] == statusPresent, nil
	}
	return false, d.malformed(ParliaHasTag)
}

func (d *ParliaDB) malformed(tag string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stop(fmt.Errorf("%w: malformed %s response", ErrCorrupt, tag))
}

// NewIterator has no logged form, and the pinned Parlia never calls it. It
// still returns the native iterator, so its data could reach the import. The
// call stops the wrapper, so Detach and Err report the observation as failed
// and it cannot finish cleanly.
func (d *ParliaDB) NewIterator(prefix []byte, start []byte) ethdb.Iterator {
	d.mu.Lock()
	d.stop(fmt.Errorf("%w: NewIterator", ErrUnloggedRead))
	d.mu.Unlock()
	return d.Database.NewIterator(prefix, start)
}

// TracedStore is the core Store over the native database. It reports every
// write and sync, so the probe can show the order of durability steps.
type TracedStore struct {
	DB   ethdb.Database
	Emit func(Event)
}

// Has implements Store.
func (s TracedStore) Has(key []byte) (bool, error) { return s.DB.Has(key) }

// Get implements Store.
func (s TracedStore) Get(key []byte) ([]byte, error) { return s.DB.Get(key) }

// Put implements Store.
func (s TracedStore) Put(key, value []byte) error {
	observation, record := RecordKind(key)
	s.Emit(Event{"event": "put", "observation": observation, "record": record})
	return s.DB.Put(key, value)
}

// SyncKeyValue implements Store.
func (s TracedStore) SyncKeyValue() error {
	s.Emit(Event{"event": "sync"})
	return s.DB.SyncKeyValue()
}

// RecordKind names the core record that a key holds: capsule,
// capsule-chunk, entry, entry-chunk, frontier, closed, or other.
func RecordKind(key []byte) (string, string) {
	rest, ok := strings.CutPrefix(string(key), keyPrefix)
	if !ok {
		return "", "other"
	}
	parts := strings.Split(rest, "/")
	kind := "other"
	switch {
	case len(parts) == 2 && parts[1] == "capsule":
		kind = "capsule"
	case len(parts) == 4 && parts[1] == "capsule":
		kind = "capsule-chunk"
	case len(parts) == 3 && parts[1] == "log":
		kind = "entry"
	case len(parts) == 4 && parts[1] == "log":
		kind = "entry-chunk"
	case len(parts) == 2 && parts[1] == "frontier":
		kind = "frontier"
	case len(parts) == 2 && parts[1] == "closed":
		kind = "closed"
	}
	return parts[0], kind
}

// BlockItems returns the static capsule items of one BSC block observation,
// in their sealed order.
func BlockItems(blockRLP []byte, parent common.Hash, genesisJSON []byte, nativeCommit string) []Item {
	return []Item{
		{Name: "block-rlp", Data: blockRLP},
		{Name: "parent-hash", Data: parent.Bytes()},
		{Name: "genesis-json", Data: genesisJSON},
		{Name: "native-commit", Data: []byte(nativeCommit)},
		{Name: "state-profile", Data: []byte(StateProfile)},
	}
}
