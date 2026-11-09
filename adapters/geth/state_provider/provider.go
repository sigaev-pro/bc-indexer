// Package stateprovider keeps native hash-scheme state and its inputs
// available from capture admission until an allowed release.
//
// This file is shared: the same bytes compile inside the Geth, BSC and Bor
// module graphs, which stay separate (E01-01.builds). Family differences live
// in family.go. A portable test checks that the three copies are identical.
package stateprovider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/triedb"
)

// Native key-value keys of the ledger. Neither key is 32 bytes long or starts
// with the native code prefix, so the native offline pruner never deletes them.
const (
	storeKey  = "extractor-state-provider-store-v1"
	pinPrefix = "extractor-state-provider-pin-v1-"
	// unboundProbe stands in for an absent StoreID while the other expected
	// fields are validated; it is never written.
	unboundProbe = "00000000000000000000000000000000"
)

var (
	// ErrUnsupportedBackend rejects any state backend other than the native
	// hash scheme on a supported key-value layout.
	ErrUnsupportedBackend = errors.New("unsupported state backend")
	// ErrWrongStore rejects a datadir whose genesis, chain or binding differs.
	ErrWrongStore = errors.New("native store identity mismatch")
	// ErrForbiddenPruning rejects a pruning profile or state that can delete
	// pinned data.
	ErrForbiddenPruning = errors.New("forbidden pruning profile")
	// ErrPinUnavailable reports a live pin whose referenced data is missing.
	ErrPinUnavailable = errors.New("pinned native data is unavailable")
	// ErrUnknownPin reports a release or lookup of a pin that is not live.
	ErrUnknownPin = errors.New("unknown pin")
	// ErrPinConflict reports a different record under an existing pin ID.
	ErrPinConflict = errors.New("conflicting pin record")
	// ErrLivePins refuses a native deletion that would remove pinned data.
	ErrLivePins = errors.New("live pins forbid this native deletion")
	// ErrInvalidPin rejects a malformed pin request or ledger record.
	ErrInvalidPin = errors.New("invalid pin")
	// ErrProfile rejects an effective native profile that breaks the capture
	// constraints.
	ErrProfile = errors.New("native profile violates capture constraints")
)

var (
	storeIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	pinIDPattern   = regexp.MustCompile(`^[0-9a-z][0-9a-z._-]{0,127}$`)
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// StoreIdentity binds a native datadir to one capture producer.
type StoreIdentity struct {
	StoreID     string      `json:"store_id"`
	ChainID     string      `json:"chain_id"`
	GenesisHash common.Hash `json:"genesis_hash"`
	StateScheme string      `json:"state_scheme"`
}

// HeaderRef names one native header that must stay readable.
type HeaderRef struct {
	Hash   common.Hash `json:"hash"`
	Number uint64      `json:"number"`
}

// Pin is one durable retention record. ParentRoot is the state root of the
// first header. Codes lists contract code that must stay readable. Consensus
// inputs that live outside the native store are copied by E01-02.inputs.
type Pin struct {
	ID         string        `json:"id"`
	Store      StoreIdentity `json:"store"`
	Profile    string        `json:"profile"`
	ParentRoot common.Hash   `json:"parent_root"`
	Headers    []HeaderRef   `json:"headers"`
	Codes      []common.Hash `json:"codes"`
}

// Provider writes and releases pins on an open native store. Callers hold the
// native observation guard; the mutex only protects the ledger itself.
type Provider struct {
	mu       sync.Mutex
	db       ethdb.Database
	triedb   *triedb.Database
	identity StoreIdentity
}

// ReadIdentity derives the identity fields that the native store records.
// The returned StoreID is empty; Bind and the ledger supply it.
func ReadIdentity(db ethdb.Database) (StoreIdentity, error) {
	scheme := rawdb.ReadStateScheme(db)
	if scheme != rawdb.HashScheme {
		return StoreIdentity{}, fmt.Errorf("%w: state scheme %q", ErrUnsupportedBackend, scheme)
	}
	genesis := rawdb.ReadCanonicalHash(db, 0)
	config := rawdb.ReadChainConfig(db, genesis)
	if genesis == (common.Hash{}) || config == nil || config.ChainID == nil || config.ChainID.Sign() <= 0 {
		return StoreIdentity{}, fmt.Errorf("%w: missing genesis or chain config", ErrWrongStore)
	}
	return StoreIdentity{ChainID: config.ChainID.String(), GenesisHash: genesis, StateScheme: scheme}, nil
}

// BoundIdentity returns the recorded binding, or false if the store is unbound.
func BoundIdentity(db ethdb.KeyValueReader) (StoreIdentity, bool, error) {
	ok, err := db.Has([]byte(storeKey))
	if err != nil || !ok {
		return StoreIdentity{}, false, err
	}
	data, err := db.Get([]byte(storeKey))
	if err != nil {
		return StoreIdentity{}, false, err
	}
	var identity StoreIdentity
	if err := strictDecode(data, &identity); err != nil || validIdentity(identity) != nil {
		return StoreIdentity{}, false, fmt.Errorf("%w: malformed binding", ErrWrongStore)
	}
	return identity, true, nil
}

// Bind records the producer binding once and syncs it. Repeating the same
// binding is idempotent; any different binding is a foreign store.
func Bind(db ethdb.Database, storeID string) (StoreIdentity, error) {
	identity, err := ReadIdentity(db)
	if err != nil {
		return StoreIdentity{}, err
	}
	identity.StoreID = storeID
	if err := validIdentity(identity); err != nil {
		return StoreIdentity{}, err
	}
	bound, ok, err := BoundIdentity(db)
	if err != nil {
		return StoreIdentity{}, err
	}
	if ok {
		if bound != identity {
			return StoreIdentity{}, fmt.Errorf("%w: store is bound to %s", ErrWrongStore, bound.StoreID)
		}
		return bound, nil
	}
	if err := writeSynced(db, []byte(storeKey), identity); err != nil {
		return StoreIdentity{}, err
	}
	return identity, nil
}

// New opens the provider on a bound hash-scheme store.
func New(db ethdb.Database, tdb *triedb.Database, expected StoreIdentity) (*Provider, error) {
	if tdb == nil || tdb.Scheme() != rawdb.HashScheme {
		return nil, fmt.Errorf("%w: trie database is not the hash provider", ErrUnsupportedBackend)
	}
	if err := validIdentity(expected); err != nil {
		return nil, err
	}
	if _, err := checkStore(db, expected); err != nil {
		return nil, err
	}
	return &Provider{db: db, triedb: tdb, identity: expected}, nil
}

// Pin makes the parent root durable and then records the pin. The order is
// the native-capture protocol: commit the root from the trie cache, sync the
// key-value store, write the ledger record, and sync again. A RAM reference
// alone is never a pin. On error no ledger record is written.
//
// Precondition: the caller holds the native observation guard, so the native
// import cannot dereference the parent root from the trie cache between the
// admission decision and this call (protocol step 2). A root that is neither
// in the trie cache nor on disk fails with ErrPinUnavailable.
func (p *Provider) Pin(pin Pin) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pin.Codes == nil {
		pin.Codes = []common.Hash{}
	}
	if err := validPin(pin); err != nil {
		return err
	}
	if pin.Store != p.identity {
		return fmt.Errorf("%w: pin %s names another store", ErrWrongStore, pin.ID)
	}
	if existing, ok, err := readPin(p.db, pin.ID); err != nil {
		return err
	} else if ok {
		if !samePin(existing, pin) {
			return fmt.Errorf("%w: %s", ErrPinConflict, pin.ID)
		}
		return Verify(p.db, existing)
	}
	if err := p.triedb.Commit(pin.ParentRoot, false); err != nil {
		return fmt.Errorf("commit pinned root %s: %w", pin.ParentRoot, err)
	}
	if err := p.db.SyncKeyValue(); err != nil {
		return fmt.Errorf("sync pinned root %s: %w", pin.ParentRoot, err)
	}
	if err := Verify(p.db, pin); err != nil {
		return err
	}
	return writeSynced(p.db, pinKey(pin.ID), pin)
}

// Release durably removes a live pin. After release the native store no
// longer promises the old state; physical deletion may happen later.
func (p *Provider) Release(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !pinIDPattern.MatchString(id) {
		return fmt.Errorf("%w: pin ID %q", ErrInvalidPin, id)
	}
	if _, ok, err := readPin(p.db, id); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPin, id)
	}
	if err := p.db.Delete(pinKey(id)); err != nil {
		return err
	}
	return p.db.SyncKeyValue()
}

// Pins lists the live pins in ID order.
func Pins(db ethdb.Database) ([]Pin, error) {
	iterator := db.NewIterator([]byte(pinPrefix), nil)
	defer iterator.Release()
	var pins []Pin
	for iterator.Next() {
		var pin Pin
		if err := strictDecode(iterator.Value(), &pin); err != nil || validPin(pin) != nil ||
			!bytes.Equal(iterator.Key(), pinKey(pin.ID)) {
			return nil, fmt.Errorf("%w: malformed ledger record %q", ErrInvalidPin, iterator.Key())
		}
		pins = append(pins, pin)
	}
	if err := iterator.Error(); err != nil {
		return nil, err
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].ID < pins[j].ID })
	return pins, nil
}

// Verify checks that every reference of a pin is readable from the native
// store: the state root node, each header, the root binding and each code.
// Like the native pruner, the root node stands for its whole trie; the
// runtime tests walk the complete trie separately.
func Verify(db ethdb.Database, pin Pin) error {
	if !rawdb.HasLegacyTrieNode(db, pin.ParentRoot) {
		return fmt.Errorf("%w: pin %s state root %s", ErrPinUnavailable, pin.ID, pin.ParentRoot)
	}
	for index, ref := range pin.Headers {
		header := rawdb.ReadHeader(db, ref.Hash, ref.Number)
		if header == nil {
			return fmt.Errorf("%w: pin %s header %d %s", ErrPinUnavailable, pin.ID, ref.Number, ref.Hash)
		}
		if index == 0 && header.Root != pin.ParentRoot {
			return fmt.Errorf("%w: pin %s parent header root %s", ErrInvalidPin, pin.ID, header.Root)
		}
	}
	for _, code := range pin.Codes {
		if !rawdb.HasCode(db, code) {
			return fmt.Errorf("%w: pin %s code %s", ErrPinUnavailable, pin.ID, code)
		}
	}
	return nil
}

// RequireNoLivePins is the ledger check that every native state deletion
// path must pass. It fails closed on a malformed ledger.
func RequireNoLivePins(db ethdb.Database) error {
	pins, err := Pins(db)
	if err != nil {
		return err
	}
	if len(pins) != 0 {
		return fmt.Errorf("%w: %d live, first %s", ErrLivePins, len(pins), pins[0].ID)
	}
	return nil
}

// RequireRewindAllowed is the ledger check for a native head rewind to
// newHead. Native SetHead deletes every header above the new head, so a
// rewind below any pinned header is refused.
func RequireRewindAllowed(db ethdb.Database, newHead uint64) error {
	pins, err := Pins(db)
	if err != nil {
		return err
	}
	for _, pin := range pins {
		for _, ref := range pin.Headers {
			if ref.Number > newHead {
				return fmt.Errorf("%w: rewind to %d deletes header %d of pin %s", ErrLivePins, newHead, ref.Number, pin.ID)
			}
		}
	}
	return nil
}

// FreezeLimit returns the highest exclusive block number up to which the
// native chain freezer may freeze, given its own limit. The freezer deletes
// every side-chain block at a frozen number and their dangling descendants,
// so it must stay at or below the fork point of every pinned side-branch
// header. Canonical pinned headers move to the freezer and stay readable, so
// they impose no limit. A pinned header whose ancestry cannot be read fails.
// Both limit and the result are exclusive; the native freezer threshold is
// inclusive, so a patched freezer uses min(threshold, result-1).
func FreezeLimit(db ethdb.Database, limit uint64) (uint64, error) {
	pins, err := Pins(db)
	if err != nil {
		return 0, err
	}
	for _, pin := range pins {
		for _, ref := range pin.Headers {
			hash, number := ref.Hash, ref.Number
			for rawdb.ReadCanonicalHash(db, number) != hash {
				header := rawdb.ReadHeader(db, hash, number)
				if header == nil || number == 0 {
					return 0, fmt.Errorf("%w: pin %s header %d %s", ErrPinUnavailable, pin.ID, number, hash)
				}
				hash, number = header.ParentHash, number-1
			}
			if number != ref.Number && number+1 < limit {
				limit = number + 1
			}
		}
	}
	return limit, nil
}

// StoreReport is the read-only preflight result.
type StoreReport struct {
	Engine   string        `json:"engine"`
	Identity StoreIdentity `json:"identity"`
	Bound    bool          `json:"bound"`
	Pins     []string      `json:"pins"`
}

// checkStore validates an opened store against the expected identity. An
// empty expected StoreID admits only an unbound store without pins.
func checkStore(db ethdb.Database, expected StoreIdentity) (StoreReport, error) {
	probe := expected
	if probe.StoreID == "" {
		probe.StoreID = unboundProbe
	}
	if err := validIdentity(probe); err != nil {
		return StoreReport{}, err
	}
	identity, err := ReadIdentity(db)
	if err != nil {
		return StoreReport{}, err
	}
	if identity.ChainID != expected.ChainID || identity.GenesisHash != expected.GenesisHash ||
		identity.StateScheme != expected.StateScheme {
		return StoreReport{}, fmt.Errorf("%w: chain %s genesis %s scheme %s", ErrWrongStore,
			identity.ChainID, identity.GenesisHash, identity.StateScheme)
	}
	bound, ok, err := BoundIdentity(db)
	if err != nil {
		return StoreReport{}, err
	}
	pins, err := Pins(db)
	if err != nil {
		return StoreReport{}, err
	}
	report := StoreReport{Identity: identity, Bound: ok, Pins: []string{}}
	if expected.StoreID == "" {
		if ok || len(pins) != 0 {
			return StoreReport{}, fmt.Errorf("%w: unbound start found a bound store", ErrWrongStore)
		}
		return report, nil
	}
	if !ok || bound != expected {
		return StoreReport{}, fmt.Errorf("%w: expected binding %s", ErrWrongStore, expected.StoreID)
	}
	report.Identity = bound
	for _, pin := range pins {
		if pin.Store != bound {
			return StoreReport{}, fmt.Errorf("%w: pin %s names another store", ErrWrongStore, pin.ID)
		}
		if err := Verify(db, pin); err != nil {
			return StoreReport{}, err
		}
		report.Pins = append(report.Pins, pin.ID)
	}
	return report, nil
}

func validIdentity(identity StoreIdentity) error {
	chain, ok := new(big.Int).SetString(identity.ChainID, 10)
	if !storeIDPattern.MatchString(identity.StoreID) || !ok || chain.Sign() <= 0 || chain.String() != identity.ChainID ||
		identity.GenesisHash == (common.Hash{}) || identity.StateScheme != rawdb.HashScheme {
		return fmt.Errorf("%w: invalid store identity", ErrWrongStore)
	}
	return nil
}

func validPin(pin Pin) error {
	if !pinIDPattern.MatchString(pin.ID) || !digestPattern.MatchString(pin.Profile) ||
		pin.ParentRoot == (common.Hash{}) || len(pin.Headers) == 0 || validIdentity(pin.Store) != nil {
		return fmt.Errorf("%w: %q", ErrInvalidPin, pin.ID)
	}
	for _, ref := range pin.Headers {
		if ref.Hash == (common.Hash{}) {
			return fmt.Errorf("%w: %q has an empty header hash", ErrInvalidPin, pin.ID)
		}
	}
	for _, code := range pin.Codes {
		if code == (common.Hash{}) {
			return fmt.Errorf("%w: %q has an empty code hash", ErrInvalidPin, pin.ID)
		}
	}
	return nil
}

func samePin(left, right Pin) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func pinKey(id string) []byte { return []byte(pinPrefix + id) }

func readPin(db ethdb.KeyValueReader, id string) (Pin, bool, error) {
	key := pinKey(id)
	ok, err := db.Has(key)
	if err != nil || !ok {
		return Pin{}, false, err
	}
	data, err := db.Get(key)
	if err != nil {
		return Pin{}, false, err
	}
	var pin Pin
	if err := strictDecode(data, &pin); err != nil || validPin(pin) != nil || pin.ID != id {
		return Pin{}, false, fmt.Errorf("%w: malformed ledger record %s", ErrInvalidPin, id)
	}
	return pin, true, nil
}

func writeSynced(db ethdb.KeyValueStore, key []byte, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := db.Put(key, data); err != nil {
		return err
	}
	return db.SyncKeyValue()
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}
