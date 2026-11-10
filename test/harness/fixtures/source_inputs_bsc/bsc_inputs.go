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
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
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
	// ErrUndeclaredRead refuses a read of a key that the static capsule does
	// not declare as a Parlia snapshot dependency.
	ErrUndeclaredRead = errors.New("Parlia read of an undeclared key")
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

	mu       sync.Mutex
	log      *Log
	id       string
	declared map[string]bool
	live     bool
	err      error
	fetches  int
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

// Declare sets the keys that the observation may read: the Parlia snapshot
// dependencies of its static capsule. Inside an observation, a read of any
// other key, or any read before Declare, stops the wrapper.
func (d *ParliaDB) Declare(keys [][]byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.declared = make(map[string]bool, len(keys))
	for _, key := range keys {
		d.declared[string(key)] = true
	}
}

// Detach ends the observation and returns the first wiring error, if any.
// Parlia turns a failed snapshot read into a fresh snapshot, so the caller
// must check this error; it is never cleared.
func (d *ParliaDB) Detach() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log, d.id, d.declared = nil, "", nil
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
	if !d.declared[string(key)] {
		return nil, d.stop(fmt.Errorf("%w: %s %x", ErrUndeclaredRead, tag, key))
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

func sumHex(data []byte) string {
	digest := sum(data)
	return hex.EncodeToString(digest[:])
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

// ---- static manifest ----

// StaticItemNames lists the static capsule items of one BSC block
// observation, in their sealed order.
var StaticItemNames = []string{"block-rlp", "parent-hash", "parent-header-rlp", "parent-root",
	"ancestor-headers-rlp", "genesis-json", "fork-id", "parlia-snapshot-keys", "native-commit",
	"normalization-profile", "state-profile"}

const (
	// NormalizationProfile is the test-only normalization profile item, the
	// same value as in the Geth block.
	NormalizationProfile = "test-only-probe-v1"
	// CheckpointInterval is checkpointInterval of the pinned Parlia source:
	// the snapshot walk stops at a block number that it divides.
	CheckpointInterval = 1024
	// BlockHashWindow is the number of ancestors that BLOCKHASH can read.
	BlockHashWindow = 256
	// SnapshotPrefix is the key prefix of a stored Parlia snapshot.
	SnapshotPrefix = "parlia-"
)

var (
	// ErrStaticInput reports a static capsule that is incomplete or
	// inconsistent, or a home that does not match it.
	ErrStaticInput = errors.New("static input check failed")
	// ErrParentPin reports parent data in the pinned home that is missing or
	// does not match the static capsule.
	ErrParentPin = errors.New("parent pin data is missing or does not match")
	// ErrParentState reports that the replay home does not hold the complete
	// parent state.
	ErrParentState = errors.New("parent state is missing from the home")
)

// AncestorFloor returns the lowest ancestor number whose header the import
// of block n (n >= 2) may read through the chain: the Parlia snapshot walk
// reads headers from the parent down to the checkpoint below it, and
// BLOCKHASH reads the 256 most recent ancestors. The window is derived from
// the pinned source; a read below it finds no header in a replay home and
// fails the import.
func AncestorFloor(n uint64) uint64 {
	floor := (n - 1) / CheckpointInterval * CheckpointInterval
	if n > BlockHashWindow && n-BlockHashWindow < floor {
		floor = n - BlockHashWindow
	}
	return floor
}

// SnapshotDependency names the stored Parlia snapshot that the import may
// read: the snapshot of the checkpoint at or below the parent. Its content
// is not static; it is a logged dependency response.
type SnapshotDependency struct {
	Checkpoint uint64   `json:"checkpoint"`
	Keys       []string `json:"keys"`
}

// SnapshotKey returns the key of the stored Parlia snapshot of a block.
func SnapshotKey(hash common.Hash) []byte {
	return append([]byte(SnapshotPrefix), hash.Bytes()...)
}

// SnapshotFor returns the snapshot dependency of the block after parent.
func SnapshotFor(parent *types.Header, ancestors []*types.Header) (SnapshotDependency, error) {
	checkpoint := parent.Number.Uint64() / CheckpointInterval * CheckpointInterval
	for _, header := range append([]*types.Header{parent}, ancestors...) {
		if header.Number.Uint64() == checkpoint {
			return SnapshotDependency{Checkpoint: checkpoint, Keys: []string{hex.EncodeToString(SnapshotKey(header.Hash()))}}, nil
		}
	}
	return SnapshotDependency{}, fmt.Errorf("%w: no header of checkpoint %d", ErrStaticInput, checkpoint)
}

// KeyBytes returns the declared keys as bytes.
func (s SnapshotDependency) KeyBytes() ([][]byte, error) {
	var keys [][]byte
	for _, key := range s.Keys {
		data, err := hex.DecodeString(key)
		if err != nil || !bytes.HasPrefix(data, []byte(SnapshotPrefix)) {
			return nil, fmt.Errorf("%w: snapshot key %q", ErrStaticInput, key)
		}
		keys = append(keys, data)
	}
	return keys, nil
}

// ForkID returns the fork-id item: the EIP-2124 fork identifier of the
// block's own rules, as {"hash","next"} JSON.
func ForkID(genesis *core.Genesis, number, time uint64) ([]byte, error) {
	id := forkid.NewID(genesis.Config, genesis.ToBlock(), number, time)
	return json.Marshal(map[string]string{"hash": hex.EncodeToString(id.Hash[:]), "next": fmt.Sprint(id.Next)})
}

// BlockItems returns the static capsule items of the observation of block,
// in their sealed order. header returns the canonical header of a number
// below the block, and genesis is genesisJSON decoded.
func BlockItems(blockRLP []byte, block *types.Block, header func(uint64) *types.Header, genesisJSON []byte,
	genesis *core.Genesis, nativeCommit string) ([]Item, error) {
	n := block.NumberU64()
	if n == 0 {
		return nil, fmt.Errorf("%w: block 0 has no parent", ErrStaticInput)
	}
	parent := header(n - 1)
	if parent == nil || parent.Hash() != block.ParentHash() {
		return nil, fmt.Errorf("%w: parent header of block %d", ErrStaticInput, n)
	}
	ancestors := []*types.Header{}
	for number := n - 1; n >= 2 && number > AncestorFloor(n); {
		number--
		ancestor := header(number)
		if ancestor == nil {
			return nil, fmt.Errorf("%w: ancestor header %d", ErrStaticInput, number)
		}
		ancestors = append(ancestors, ancestor)
	}
	parentRLP, err := rlp.EncodeToBytes(parent)
	if err != nil {
		return nil, err
	}
	ancestorsRLP, err := rlp.EncodeToBytes(ancestors)
	if err != nil {
		return nil, err
	}
	fork, err := ForkID(genesis, n, block.Time())
	if err != nil {
		return nil, err
	}
	dependency, err := SnapshotFor(parent, ancestors)
	if err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(dependency)
	if err != nil {
		return nil, err
	}
	return []Item{
		{Name: "block-rlp", Data: blockRLP},
		{Name: "parent-hash", Data: parent.Hash().Bytes()},
		{Name: "parent-header-rlp", Data: parentRLP},
		{Name: "parent-root", Data: parent.Root.Bytes()},
		{Name: "ancestor-headers-rlp", Data: ancestorsRLP},
		{Name: "genesis-json", Data: genesisJSON},
		{Name: "fork-id", Data: fork},
		{Name: "parlia-snapshot-keys", Data: snapshot},
		{Name: "native-commit", Data: []byte(nativeCommit)},
		{Name: "normalization-profile", Data: []byte(NormalizationProfile)},
		{Name: "state-profile", Data: []byte(StateProfile)},
	}, nil
}

// Static is the checked static manifest of one block observation.
type Static struct {
	Block     *types.Block
	Parent    *types.Header
	Ancestors []*types.Header // descending, from the grandparent
	Root      common.Hash
	Snapshot  SnapshotDependency
}

// ReadStatic reads and checks the stored capsule of an observation.
func ReadStatic(db Store, id string) (Static, error) {
	capsule, err := ReadCapsule(db, id)
	if err != nil {
		return Static{}, err
	}
	items := make(map[string][]byte, len(capsule.Items))
	for index, ref := range capsule.Items {
		if items[ref.Name], err = ReadItem(db, id, index); err != nil {
			return Static{}, err
		}
	}
	return DecodeStatic(items)
}

// DecodeStatic decodes the items of one block observation and checks that
// the block, the parent, the parent root, the ancestor window and the
// declared snapshot dependency agree with each other.
func DecodeStatic(items map[string][]byte) (Static, error) {
	bad := func(format string, args ...any) (Static, error) {
		return Static{}, fmt.Errorf("%w: "+format, append([]any{ErrStaticInput}, args...)...)
	}
	for _, name := range StaticItemNames {
		if _, ok := items[name]; !ok {
			return bad("missing item %s", name)
		}
	}
	static := Static{Block: new(types.Block), Parent: new(types.Header)}
	if err := rlp.DecodeBytes(items["parent-header-rlp"], static.Parent); err != nil {
		return bad("parent header: %v", err)
	}
	if !bytes.Equal(items["parent-hash"], static.Parent.Hash().Bytes()) {
		return bad("parent header hash differs from parent-hash")
	}
	if !bytes.Equal(items["parent-root"], static.Parent.Root.Bytes()) {
		return bad("parent-root differs from the parent header root")
	}
	if err := rlp.DecodeBytes(items["block-rlp"], static.Block); err != nil {
		return bad("block: %v", err)
	}
	n := static.Parent.Number.Uint64() + 1
	if static.Block.NumberU64() != n || static.Block.ParentHash() != static.Parent.Hash() {
		return bad("block %d is not the child of the parent header", static.Block.NumberU64())
	}
	if err := rlp.DecodeBytes(items["ancestor-headers-rlp"], &static.Ancestors); err != nil {
		return bad("ancestor headers: %v", err)
	}
	want := uint64(0)
	if n >= 2 {
		want = n - 1 - AncestorFloor(n)
	}
	if uint64(len(static.Ancestors)) != want {
		return bad("%d ancestor headers, want %d", len(static.Ancestors), want)
	}
	child := static.Parent
	for _, ancestor := range static.Ancestors {
		if ancestor.Hash() != child.ParentHash || ancestor.Number.Uint64()+1 != child.Number.Uint64() {
			return bad("ancestor header %d does not link", ancestor.Number.Uint64())
		}
		child = ancestor
	}
	dependency, err := SnapshotFor(static.Parent, static.Ancestors)
	if err != nil {
		return Static{}, err
	}
	if encoded, err := json.Marshal(dependency); err != nil || !bytes.Equal(encoded, items["parlia-snapshot-keys"]) {
		return bad("parlia-snapshot-keys differs from the headers")
	}
	static.Root, static.Snapshot = static.Parent.Root, dependency
	return static, nil
}

// SeedOptions withholds a part of the parent seed for a negative control.
type SeedOptions struct {
	WithholdHeaders bool
	WithholdState   bool
}

// SeedReport counts what SeedParent wrote into the home.
type SeedReport struct {
	Headers    int    `json:"headers"`
	BodySHA256 string `json:"body_sha256"`
	StateNodes int    `json:"state_nodes"`
	Codes      int    `json:"codes"`
}

// SeedParent prepares a home that holds only the genesis for a native-only
// replay of the block after static.Parent, without importing its
// predecessors:
//
//   - The parent and ancestor headers come from the static capsule, with
//     canonical marks and total difficulty. The total difficulty starts from
//     the home's value below the lowest capsule header (here the genesis).
//   - The parent body and the parent state come from pin, the home that holds
//     the parent pin of E01-02.pins. The native import needs them, but they
//     are native-owned data, not static inputs. The body must match the
//     capsule's parent header, and each state node and code must match its
//     hash.
//
// Every check fails closed: SeedParent writes nothing before the headers
// check, and the caller must run CheckParentState before execution.
func SeedParent(home, pin ethdb.Database, static Static, options SeedOptions) (SeedReport, error) {
	var report SeedReport
	parent := static.Parent
	number := parent.Number.Uint64()
	headers := append([]*types.Header{parent}, static.Ancestors...)
	lowest := headers[len(headers)-1]
	start := len(headers) - 1
	var td *big.Int
	if lowest.Number.Uint64() == 0 {
		if rawdb.ReadCanonicalHash(home, 0) != lowest.Hash() {
			return report, fmt.Errorf("%w: the home genesis differs from the capsule", ErrStaticInput)
		}
		if number == 0 {
			return report, nil
		}
		td, start = rawdb.ReadTd(home, lowest.Hash(), 0), start-1
	} else {
		td = rawdb.ReadTd(home, lowest.ParentHash, lowest.Number.Uint64()-1)
	}
	if td == nil {
		return report, fmt.Errorf("%w: the home has no total difficulty below the capsule headers", ErrStaticInput)
	}
	td = new(big.Int).Set(td)
	for index := start; index >= 0; index-- {
		header := headers[index]
		td.Add(td, header.Difficulty)
		if options.WithholdHeaders {
			continue
		}
		rawdb.WriteHeader(home, header)
		rawdb.WriteTd(home, header.Hash(), header.Number.Uint64(), td)
		rawdb.WriteCanonicalHash(home, header.Hash(), header.Number.Uint64())
		report.Headers++
	}
	body := rawdb.ReadBody(pin, parent.Hash(), number)
	if body == nil {
		return report, fmt.Errorf("%w: no parent body", ErrParentPin)
	}
	if types.DeriveSha(types.Transactions(body.Transactions), trie.NewStackTrie(nil)) != parent.TxHash ||
		types.CalcUncleHash(body.Uncles) != parent.UncleHash {
		return report, fmt.Errorf("%w: the parent body does not match the parent header", ErrParentPin)
	}
	if parent.WithdrawalsHash == nil && len(body.Withdrawals) != 0 || parent.WithdrawalsHash != nil &&
		types.DeriveSha(types.Withdrawals(body.Withdrawals), trie.NewStackTrie(nil)) != *parent.WithdrawalsHash {
		return report, fmt.Errorf("%w: the parent withdrawals do not match the parent header", ErrParentPin)
	}
	rawdb.WriteBody(home, parent.Hash(), number, body)
	encoded, err := rlp.EncodeToBytes(body)
	if err != nil {
		return report, err
	}
	report.BodySHA256 = sumHex(encoded)
	if !options.WithholdState {
		err := walkState(pin, static.Root, func(hash common.Hash, blob []byte) error {
			if crypto.Keccak256Hash(blob) != hash {
				return fmt.Errorf("%w: state node %x", ErrParentPin, hash)
			}
			rawdb.WriteLegacyTrieNode(home, hash, blob)
			report.StateNodes++
			return nil
		}, func(hash common.Hash, code []byte) error {
			if crypto.Keccak256Hash(code) != hash {
				return fmt.Errorf("%w: code %x", ErrParentPin, hash)
			}
			rawdb.WriteCode(home, hash, code)
			report.Codes++
			return nil
		})
		if err != nil && !errors.Is(err, ErrParentPin) {
			err = fmt.Errorf("%w: parent state: %v", ErrParentPin, err)
		}
		if err != nil {
			return report, err
		}
	}
	if !options.WithholdHeaders {
		rawdb.WriteHeadHeaderHash(home, parent.Hash())
		rawdb.WriteHeadBlockHash(home, parent.Hash())
		rawdb.WriteHeadFastBlockHash(home, parent.Hash())
	}
	return report, home.SyncKeyValue()
}

// CheckParentState fails unless the home holds every node and code of the
// parent state. The native import would otherwise fail later, or a
// different pruning profile could make it read state the pin no longer
// protects; this check stops the replay before execution.
func CheckParentState(home ethdb.Database, root common.Hash) error {
	err := walkState(home, root, func(common.Hash, []byte) error { return nil },
		func(common.Hash, []byte) error { return nil })
	if err != nil {
		return fmt.Errorf("%w: root %x: %v", ErrParentState, root, err)
	}
	return nil
}

// walkState visits every hashed node of the account trie and of each
// storage trie under root, and the code of each contract.
func walkState(db ethdb.Database, root common.Hash, node func(common.Hash, []byte) error,
	code func(common.Hash, []byte) error) error {
	nodes := triedb.NewDatabase(db, triedb.HashDefaults)
	defer nodes.Close()
	walk := func(id *trie.ID, leaf func(key, blob []byte) error) error {
		tr, err := trie.New(id, nodes)
		if err != nil {
			return err
		}
		it, err := tr.NodeIterator(nil)
		if err != nil {
			return err
		}
		for it.Next(true) {
			if hash := it.Hash(); hash != (common.Hash{}) {
				if err := node(hash, it.NodeBlob()); err != nil {
					return err
				}
			}
			if it.Leaf() && leaf != nil {
				if err := leaf(it.LeafKey(), it.LeafBlob()); err != nil {
					return err
				}
			}
		}
		return it.Error()
	}
	return walk(trie.StateTrieID(root), func(key, blob []byte) error {
		account, err := types.FullAccount(blob)
		if err != nil {
			return err
		}
		if account.Root != types.EmptyRootHash {
			if err := walk(trie.StorageTrieID(root, common.BytesToHash(key), account.Root), nil); err != nil {
				return err
			}
		}
		if hash := common.BytesToHash(account.CodeHash); hash != types.EmptyCodeHash {
			blob := rawdb.ReadCode(db, hash)
			if len(blob) == 0 {
				return fmt.Errorf("missing code %x", hash)
			}
			return code(hash, blob)
		}
		return nil
	})
}
