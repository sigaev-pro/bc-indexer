// Package bsc wires the admission core (E01-02.admission) and the source
// inputs core (E01-02.inputs) into the BSC native import path.
//
// The extractor patch set adds the import seam to core.BlockChain; Seam
// implements it. The driver test/harness/bsc_admission.py installs this
// package at extractor/bsc of the accepted BSC source tree, next to the shared
// packages extractor/admission, extractor/source_inputs and
// extractor/state_provider, and applies the patches in ../patches.
package bsc

// The consumed native dependency of BSC Parlia import is the database handle
// that parlia.New receives. In the pinned BSC source, the engine reads that
// handle only through Get, to load a checkpoint snapshot ("parlia-" plus the
// block hash), and writes it only through Put, to store one. ParliaDB wraps
// that handle (it started as the E01-02.inputs BSC probe wrapper): inside an
// observation every read goes through the dependency log, so the response is
// durable before Parlia receives it and a retry receives the saved bytes.
// Outside an observation, reads go to the native database: they serve RPC,
// metrics and the fork choice after EXEC_READY, not execution.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/ethdb"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
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

var (
	// ErrDependencyNotFound is what Parlia receives for a missing key.
	ErrDependencyNotFound = errors.New("not found")
	// ErrUnloggedRead refuses a read method that has no logged form.
	ErrUnloggedRead = errors.New("unlogged read through the Parlia database handle")
	// ErrUndeclaredRead refuses a read of a key that the static capsule does
	// not declare as a Parlia snapshot dependency.
	ErrUndeclaredRead = errors.New("Parlia read of an undeclared key")
	// ErrWiring reports a misuse of the wrapper.
	ErrWiring = errors.New("Parlia database wiring error")
	// ErrMalformedResponse reports a saved response that does not decode.
	ErrMalformedResponse = errors.New("malformed saved Parlia response")
)

// ParliaRequest returns the logged request for one read.
func ParliaRequest(tag string, key []byte) []byte {
	request := append([]byte(tag), 0)
	return append(request, key...)
}

// Event is one trace record of the wrapper or the seam.
type Event map[string]any

// ParliaDB is the database handle given to parlia.New. It is safe for
// concurrent use; reads inside an observation are logged in the order they
// take the lock. The core log expects native order, so concurrent reads of
// different keys in a nondeterministic order would fail closed on replay with
// ErrPrefixConflict (the BSC Parlia-inputs PR of E01-02.admission covers
// them).
type ParliaDB struct {
	ethdb.Database

	mu       sync.Mutex
	log      *sourceinputs.Log
	id       string
	declared map[string]bool
	err      error
	fetches  int
	// emit runs while the lock is held; it must not call ParliaDB.
	emit func(Event)
	// Handoff, when set, runs after a response is durable and before Parlia
	// receives it, with the lock held, so it must not call ParliaDB.
	Handoff func(observation string, sequence uint64, response []byte)
}

// NewParliaDB wraps the native database.
func NewParliaDB(native ethdb.Database, emit func(Event)) *ParliaDB {
	if emit == nil {
		emit = func(Event) {}
	}
	return &ParliaDB{Database: native, emit: emit}
}

// Attach routes reads to the dependency log of one observation and declares
// the keys it may read: the Parlia snapshot dependencies of its capsule.
// Inside an observation a read of any other key stops the observation. A
// wiring error of an earlier observation is cleared: its observation stayed
// ADMITTED and is resolved by a new attempt.
func (d *ParliaDB) Attach(id string, log *sourceinputs.Log, keys [][]byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if log == nil || d.log != nil {
		return fmt.Errorf("%w: attach %s", ErrWiring, id)
	}
	d.log, d.id, d.err = log, id, nil
	d.declared = make(map[string]bool, len(keys))
	for _, key := range keys {
		d.declared[string(key)] = true
	}
	return nil
}

// Detach ends the observation and returns its first wiring error, if any.
// Parlia turns a failed snapshot read into a regenerated snapshot, so the
// caller must check this error before it trusts the import result.
func (d *ParliaDB) Detach() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log, d.id, d.declared = nil, "", nil
	return d.err
}

// Fetches returns how many logged reads went to the native database.
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

// consume returns the response for one read inside an observation, and
// passes through to fetch outside one.
func (d *ParliaDB) consume(tag string, key []byte, fetch func() ([]byte, error)) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.log == nil {
		return fetch()
	}
	if d.err != nil {
		return nil, d.err
	}
	if !d.declared[string(key)] {
		return nil, d.stop(fmt.Errorf("%w: %s %x", ErrUndeclaredRead, tag, key))
	}
	request := ParliaRequest(tag, key)
	response, err := d.log.Next(request, func() ([]byte, error) {
		d.fetches++
		d.emit(Event{"event": "fetch", "observation": d.id, "tag": tag})
		return fetch()
	})
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
	return d.stop(fmt.Errorf("%w: %s", ErrMalformedResponse, tag))
}

// NewIterator has no logged form, and the pinned Parlia never calls it.
// Inside an observation the call stops the observation, so Detach reports it
// as failed; the native iterator is still returned.
func (d *ParliaDB) NewIterator(prefix []byte, start []byte) ethdb.Iterator {
	d.mu.Lock()
	if d.log != nil {
		d.stop(fmt.Errorf("%w: NewIterator", ErrUnloggedRead))
	}
	d.mu.Unlock()
	return d.Database.NewIterator(prefix, start)
}
