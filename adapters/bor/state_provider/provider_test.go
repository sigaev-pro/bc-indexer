package stateprovider

// Shared tests: the same bytes run in the Geth, BSC and Bor module graphs.
// Family hooks live in family_test.go. Every test needs the accepted private
// fixture in EXTRACTOR_PINS_INPUTS and a writable EXTRACTOR_PINS_WORK; the
// driver test/harness/pins.py supplies both inside an owned container.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

const (
	testStore   = "0123456789abcdef0123456789abcdef"
	otherStore  = "fedcba9876543210fedcba9876543210"
	testProfile = "1111111111111111111111111111111111111111111111111111111111111111"
	childEnv    = "EXTRACTOR_PINS_CHILD"
)

// workDir creates a new directory for one named part of the current test
// below the run's owned work root. An existing directory is an error.
func workDir(t testing.TB, name string) string {
	t.Helper()
	root := os.Getenv("EXTRACTOR_PINS_WORK")
	if root == "" {
		t.Fatal("EXTRACTOR_PINS_WORK is required")
	}
	parent := filepath.Join(root, strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create owned work directory: %v", err)
	}
	return dir
}

// record appends one observation to the report that the driver retains.
func record(t testing.TB, name string, value any) {
	t.Helper()
	path := os.Getenv("EXTRACTOR_PINS_REPORT")
	if path == "" {
		t.Fatal("EXTRACTOR_PINS_REPORT is required")
	}
	line, err := json.Marshal(map[string]any{"family": Family, "test": t.Name(), "name": name, "value": value})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(append(line, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func identityOf(t testing.TB, db ethdb.Database, store string) StoreIdentity {
	t.Helper()
	identity, err := ReadIdentity(db)
	if err != nil {
		t.Fatal(err)
	}
	identity.StoreID = store
	return identity
}

func pinFor(id string, identity StoreIdentity, header *types.Header, codes ...common.Hash) Pin {
	if codes == nil {
		codes = []common.Hash{}
	}
	return Pin{ID: id, Store: identity, Profile: testProfile, ParentRoot: header.Root,
		Headers: []HeaderRef{{Hash: header.Hash(), Number: header.Number.Uint64()}}, Codes: codes}
}

func mustErr(t testing.TB, err error, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error %v, want %v", err, target)
	}
}

// stateWalk counts a complete native trie walk; any missing node fails it.
type stateWalk struct {
	Accounts int `json:"accounts"`
	Slots    int `json:"slots"`
	Codes    int `json:"codes"`
}

func walkState(db ethdb.Database, root common.Hash) (stateWalk, error) {
	tdb := triedb.NewDatabase(db, triedb.HashDefaults)
	defer tdb.Close()
	var walk stateWalk
	accounts, err := trie.New(trie.StateTrieID(root), tdb)
	if err != nil {
		return walk, err
	}
	iterator, err := accounts.NodeIterator(nil)
	if err != nil {
		return walk, err
	}
	for iterator.Next(true) {
		if !iterator.Leaf() {
			continue
		}
		walk.Accounts++
		account, err := types.FullAccount(iterator.LeafBlob())
		if err != nil {
			return walk, err
		}
		if account.Root != types.EmptyRootHash {
			owner := common.BytesToHash(iterator.LeafKey())
			storage, err := trie.New(trie.StorageTrieID(root, owner, account.Root), tdb)
			if err != nil {
				return walk, err
			}
			slots, err := storage.NodeIterator(nil)
			if err != nil {
				return walk, err
			}
			for slots.Next(true) {
				if slots.Leaf() {
					walk.Slots++
				}
			}
			if slots.Error() != nil {
				return walk, slots.Error()
			}
		}
		if hash := common.BytesToHash(account.CodeHash); hash != types.EmptyCodeHash {
			if len(rawdb.ReadCode(db, hash)) == 0 {
				return walk, fmt.Errorf("missing code %s", hash)
			}
			walk.Codes++
		}
	}
	return walk, iterator.Error()
}

// inventory hashes every file below dir so a check can prove no mutation.
func inventory(t testing.TB, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected file type %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		relative, _ := filepath.Rel(dir, path)
		files[relative] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func sameInventory(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for path, digest := range left {
		if right[path] != digest {
			return false
		}
	}
	return true
}

func TestUnitBindIsDurableIdempotentAndExclusive(t *testing.T) {
	db, _, _ := newMemoryStore(t)
	if _, ok, err := BoundIdentity(db); err != nil || ok {
		t.Fatalf("fresh store binding %v %v", ok, err)
	}
	first, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Bind(db, testStore)
	if err != nil || again != first {
		t.Fatalf("repeated bind %v %v", again, err)
	}
	_, err = Bind(db, otherStore)
	mustErr(t, err, ErrWrongStore)
	_, err = Bind(db, "not-hex")
	mustErr(t, err, ErrWrongStore)
	bound, ok, err := BoundIdentity(db)
	if err != nil || !ok || bound != first || bound.StateScheme != rawdb.HashScheme {
		t.Fatalf("bound identity %v %v %v", bound, ok, err)
	}
}

func TestUnitPinCommitsDirtyRootBeforeRecording(t *testing.T) {
	db, tdb, genesis := newMemoryStore(t)
	identity, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	side, code := commitSideState(t, db, tdb, genesis, 1)
	if rawdb.HasLegacyTrieNode(db, side.Root) {
		t.Fatal("side root reached disk before the pin")
	}
	provider, err := New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	pin := pinFor("obs-1", identity, side, code)
	if err := provider.Pin(pin); err != nil {
		t.Fatal(err)
	}
	if !rawdb.HasLegacyTrieNode(db, side.Root) {
		t.Fatal("pin did not commit the root")
	}
	walk, err := walkState(db, side.Root)
	if err != nil || walk.Codes == 0 || walk.Slots == 0 {
		t.Fatalf("pinned state walk %v %v", walk, err)
	}
	pins, err := Pins(db)
	if err != nil || len(pins) != 1 || !samePin(pins[0], pin) {
		t.Fatalf("ledger %v %v", pins, err)
	}
	if err := provider.Pin(pin); err != nil {
		t.Fatalf("repeated identical pin: %v", err)
	}
	// A repeated pin re-verifies its references: lose the pinned code.
	rawdb.DeleteCode(db, code)
	mustErr(t, provider.Pin(pin), ErrPinUnavailable)
	changed := pin
	changed.Profile = strings.Repeat("2", 64)
	mustErr(t, provider.Pin(changed), ErrPinConflict)
}

func TestUnitPinFailureLeavesNoRecord(t *testing.T) {
	db, tdb, genesis := newMemoryStore(t)
	identity, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	side, code := commitSideState(t, db, tdb, genesis, 2)
	missingRoot := pinFor("missing-root", identity, side, code)
	missingRoot.ParentRoot = common.HexToHash("0x01")
	missingHeader := pinFor("missing-header", identity, side, code)
	missingHeader.Headers = append(missingHeader.Headers, HeaderRef{Hash: common.HexToHash("0x02"), Number: 7})
	wrongRoot := pinFor("wrong-root", identity, genesis)
	wrongRoot.Headers[0] = HeaderRef{Hash: side.Hash(), Number: side.Number.Uint64()}
	foreign := pinFor("foreign", identity, side, code)
	foreign.Store.StoreID = otherStore
	cases := []struct {
		pin  Pin
		want error
	}{
		{missingRoot, ErrPinUnavailable},
		{missingHeader, ErrPinUnavailable},
		{wrongRoot, ErrInvalidPin},
		{pinFor("missing-code", identity, side, common.HexToHash("0x03")), ErrPinUnavailable},
		{foreign, ErrWrongStore},
		{pinFor("Bad ID", identity, side), ErrInvalidPin},
		{Pin{ID: "no-headers", Store: identity, Profile: testProfile, ParentRoot: side.Root}, ErrInvalidPin},
		{func() Pin { p := pinFor("short-profile", identity, side); p.Profile = "11"; return p }(), ErrInvalidPin},
		{func() Pin { p := pinFor("empty-root", identity, side); p.ParentRoot = common.Hash{}; return p }(), ErrInvalidPin},
		{func() Pin { p := pinFor("empty-header", identity, side); p.Headers[0].Hash = common.Hash{}; return p }(), ErrInvalidPin},
		{pinFor("empty-code", identity, side, common.Hash{}), ErrInvalidPin},
		{func() Pin { p := pinFor("bad-store", identity, side); p.Store.StoreID = "x"; return p }(), ErrInvalidPin},
		{func() Pin {
			p := pinFor("padded-chain", identity, side)
			p.Store.ChainID = "0" + identity.ChainID
			return p
		}(), ErrInvalidPin},
		{func() Pin { p := pinFor("zero-chain", identity, side); p.Store.ChainID = "0"; return p }(), ErrInvalidPin},
		{func() Pin { p := pinFor("no-genesis", identity, side); p.Store.GenesisHash = common.Hash{}; return p }(), ErrInvalidPin},
		{func() Pin {
			p := pinFor("path-store", identity, side)
			p.Store.StateScheme = rawdb.PathScheme
			return p
		}(), ErrInvalidPin},
	}
	for _, item := range cases {
		mustErr(t, provider.Pin(item.pin), item.want)
	}
	if pins, err := Pins(db); err != nil || len(pins) != 0 {
		t.Fatalf("failed pins left records %v %v", pins, err)
	}
	// A pin may also name ancestor headers; only the first must carry the root.
	ancestors := pinFor("with-ancestor", identity, side, code)
	ancestors.Headers = append(ancestors.Headers, HeaderRef{Hash: genesis.Hash(), Number: 0})
	if err := provider.Pin(ancestors); err != nil {
		t.Fatal(err)
	}
	// Nil and empty code lists are the same pin.
	empty := pinFor("no-codes", identity, side)
	empty.Codes = nil
	if err := provider.Pin(empty); err != nil {
		t.Fatal(err)
	}
	empty.Codes = []common.Hash{}
	if err := provider.Pin(empty); err != nil {
		t.Fatalf("empty codes conflict with nil codes: %v", err)
	}
}

func TestUnitReleaseAndDeletionGuards(t *testing.T) {
	db, tdb, genesis := newMemoryStore(t)
	identity, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	side, code := commitSideState(t, db, tdb, genesis, 3)
	if err := RequireNoLivePins(db); err != nil {
		t.Fatal(err)
	}
	if err := provider.Pin(pinFor("obs-3", identity, side, code)); err != nil {
		t.Fatal(err)
	}
	mustErr(t, RequireNoLivePins(db), ErrLivePins)
	mustErr(t, RequireRewindAllowed(db, side.Number.Uint64()-1), ErrLivePins)
	if err := RequireRewindAllowed(db, side.Number.Uint64()); err != nil {
		t.Fatalf("rewind to the pinned header: %v", err)
	}
	mustErr(t, provider.Release("obs-unknown"), ErrUnknownPin)
	mustErr(t, provider.Release("Bad ID"), ErrInvalidPin)
	if err := provider.Release("obs-3"); err != nil {
		t.Fatal(err)
	}
	mustErr(t, provider.Release("obs-3"), ErrUnknownPin)
	if err := RequireNoLivePins(db); err != nil {
		t.Fatal(err)
	}
	if err := RequireRewindAllowed(db, 0); err != nil {
		t.Fatal(err)
	}
}

func TestUnitMalformedLedgerFailsClosed(t *testing.T) {
	db, tdb, _ := newMemoryStore(t)
	identity, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	side, code := commitSideState(t, db, tdb, rawdb.ReadHeader(db, identity.GenesisHash, 0), 5)
	valid, err := json.Marshal(pinFor("trailing", identity, side, code))
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(append([]byte{}, valid[:len(valid)-1]...), []byte(`,"extra":1}`)...)
	for _, bad := range [][]byte{append(append([]byte{}, valid...), []byte(" {}")...),
		append(append([]byte{}, valid...), '}'), unknown} {
		if err := db.Put(pinKey("trailing"), bad); err != nil {
			t.Fatal(err)
		}
		_, err = Pins(db)
		mustErr(t, err, ErrInvalidPin)
	}
	if err := db.Put(pinKey("trailing"), valid); err != nil {
		t.Fatal(err)
	}
	if pins, err := Pins(db); err != nil || len(pins) != 1 {
		t.Fatalf("valid record %v %v", pins, err)
	}
	if err := db.Put(pinKey("broken"), []byte(`{"id":"broken"}`)); err != nil {
		t.Fatal(err)
	}
	_, err = Pins(db)
	mustErr(t, err, ErrInvalidPin)
	mustErr(t, RequireNoLivePins(db), ErrInvalidPin)
	mustErr(t, RequireRewindAllowed(db, 0), ErrInvalidPin)
	_, err = New(db, tdb, identity)
	mustErr(t, err, ErrInvalidPin)
	if err := db.Put([]byte(storeKey), []byte(`{"store_id":"x","extra":1}`)); err != nil {
		t.Fatal(err)
	}
	_, _, err = BoundIdentity(db)
	mustErr(t, err, ErrWrongStore)
}

func TestUnitMisfiledLedgerRecordFailsClosed(t *testing.T) {
	db, tdb, genesis := newMemoryStore(t)
	identity, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	side, code := commitSideState(t, db, tdb, genesis, 4)
	if err := provider.Pin(pinFor("obs-4", identity, side, code)); err != nil {
		t.Fatal(err)
	}
	data, err := db.Get(pinKey("obs-4"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put(pinKey("obs-5"), data); err != nil {
		t.Fatal(err)
	}
	_, err = Pins(db)
	mustErr(t, err, ErrInvalidPin)
	mustErr(t, provider.Release("obs-5"), ErrInvalidPin)
}

func TestUnitLedgerPinOfAnotherStoreRejects(t *testing.T) {
	db, tdb, genesis := newMemoryStore(t)
	identity, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	side, code := commitSideState(t, db, tdb, genesis, 6)
	if err := tdb.Commit(side.Root, false); err != nil {
		t.Fatal(err)
	}
	foreign := pinFor("foreign", identity, side, code)
	foreign.Store.StoreID = otherStore
	data, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put(pinKey("foreign"), data); err != nil {
		t.Fatal(err)
	}
	_, err = checkStore(db, identity)
	mustErr(t, err, ErrWrongStore)
	_, err = New(db, tdb, identity)
	mustErr(t, err, ErrWrongStore)
	// A ledger pin without a binding is not an unbound fresh store either.
	if err := db.Delete([]byte(storeKey)); err != nil {
		t.Fatal(err)
	}
	unbound := identity
	unbound.StoreID = ""
	_, err = checkStore(db, unbound)
	mustErr(t, err, ErrWrongStore)
}

// TestUnitFreezeLimit covers the chain freezer path: it deletes side-chain
// blocks at frozen numbers, so a pinned side header caps the freeze limit at
// its fork point, while a canonical pinned header does not.
func TestUnitFreezeLimit(t *testing.T) {
	db, tdb, genesis := newMemoryStore(t)
	identity, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	if limit, err := FreezeLimit(db, 100); err != nil || limit != 100 {
		t.Fatalf("no pins: %d %v", limit, err)
	}
	side, code := commitSideState(t, db, tdb, genesis, 8)
	child, childCode := commitSideState(t, db, tdb, side, 9)
	if err := provider.Pin(pinFor("obs-side-child", identity, child, childCode)); err != nil {
		t.Fatal(err)
	}
	if limit, err := FreezeLimit(db, 100); err != nil || limit != 1 {
		t.Fatalf("side pin above genesis fork: %d %v", limit, err)
	}
	if limit, err := FreezeLimit(db, 1); err != nil || limit != 1 {
		t.Fatalf("lower native limit: %d %v", limit, err)
	}
	rawdb.WriteCanonicalHash(db, side.Hash(), 1)
	if limit, err := FreezeLimit(db, 100); err != nil || limit != 2 {
		t.Fatalf("side pin above block 1 fork: %d %v", limit, err)
	}
	if limit, err := FreezeLimit(db, 1); err != nil || limit != 1 {
		t.Fatalf("native limit below the fork: %d %v", limit, err)
	}
	rawdb.WriteCanonicalHash(db, child.Hash(), 2)
	if limit, err := FreezeLimit(db, 100); err != nil || limit != 100 {
		t.Fatalf("canonical pin: %d %v", limit, err)
	}
	if err := provider.Pin(pinFor("obs-side", identity, side, code)); err != nil {
		t.Fatal(err)
	}
	rawdb.DeleteCanonicalHash(db, 2)
	rawdb.DeleteCanonicalHash(db, 1)
	rawdb.DeleteHeader(db, side.Hash(), 1)
	_, err = FreezeLimit(db, 100)
	mustErr(t, err, ErrPinUnavailable)
}

func TestUnitGuardedPrunerPaths(t *testing.T) {
	db, tdb, genesis := newMemoryStore(t)
	identity, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	dir := workDir(t, "datadir")
	if err := RecoverPruning(db, dir); err != nil {
		t.Fatalf("recovery without pins or bloom: %v", err)
	}
	side, code := commitSideState(t, db, tdb, genesis, 7)
	if err := provider.Pin(pinFor("obs-7", identity, side, code)); err != nil {
		t.Fatal(err)
	}
	mustErr(t, RecoverPruning(db, dir), ErrLivePins)
	mustErr(t, PruneState(db, dir, 256, genesis.Root), ErrLivePins)
}

func TestUnitLedgerKeysAreNotNativeStateKeys(t *testing.T) {
	for _, key := range [][]byte{[]byte(storeKey), pinKey("a"), pinKey(strings.Repeat("z", 128))} {
		if isCode, _ := rawdb.IsCodeKey(key); isCode || len(key) == common.HashLength {
			t.Fatalf("ledger key %q matches the native pruner key space", key)
		}
	}
}

func TestUnitStoreChecks(t *testing.T) {
	db, tdb, _ := newMemoryStore(t)
	unbound := identityOf(t, db, "")
	if report, err := checkStore(db, unbound); err != nil || report.Bound {
		t.Fatalf("unbound start %v %v", report, err)
	}
	_, err := New(db, tdb, unbound)
	mustErr(t, err, ErrWrongStore)
	identity, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	_, err = checkStore(db, unbound)
	mustErr(t, err, ErrWrongStore)
	if report, err := checkStore(db, identity); err != nil || !report.Bound || len(report.Pins) != 0 {
		t.Fatalf("bound start %v %v", report, err)
	}
	for _, change := range []func(*StoreIdentity){
		func(i *StoreIdentity) { i.StoreID = otherStore },
		func(i *StoreIdentity) { i.ChainID = "1" },
		func(i *StoreIdentity) { i.GenesisHash = common.HexToHash("0x04") },
	} {
		wrong := identity
		change(&wrong)
		_, err = checkStore(db, wrong)
		mustErr(t, err, ErrWrongStore)
		_, err = New(db, tdb, wrong)
		mustErr(t, err, ErrWrongStore)
	}
	for _, bad := range []func(*StoreIdentity){
		func(i *StoreIdentity) { i.StateScheme = rawdb.PathScheme },
		func(i *StoreIdentity) { i.ChainID = "01" },
		func(i *StoreIdentity) { i.ChainID = "0" },
		func(i *StoreIdentity) { i.GenesisHash = common.Hash{} },
	} {
		wrong := identity
		bad(&wrong)
		_, err = checkStore(db, wrong)
		mustErr(t, err, ErrWrongStore)
	}
	_, err = New(db, nil, identity)
	mustErr(t, err, ErrUnsupportedBackend)
	_, err = New(db, newPathTrieDB(db), identity)
	mustErr(t, err, ErrUnsupportedBackend)
}

// TestChildProcess is the subprocess half of TestNativeVolatileTrieLost. It
// imports the fixture, optionally pins block 1 and then kills itself, so the
// native trie cache is lost without any shutdown flush.
func TestChildProcess(t *testing.T) {
	dir := os.Getenv(childEnv)
	if dir == "" {
		return
	}
	node := openNode(t, dir, false)
	blocks := familyBlocks(t)
	if err := node.insert(blocks); err != nil {
		t.Fatal(err)
	}
	identity, err := Bind(node.db(), testStore)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(node.db(), node.trieDB(), identity)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"head": node.head().Number.Uint64(),
		"block1_root_on_disk_before": rawdb.HasLegacyTrieNode(node.db(), blocks[0].Root())}
	if os.Getenv("EXTRACTOR_PINS_CHILD_PIN") == "1" {
		if err := provider.Pin(pinFor("obs-volatile", identity, blocks[0].Header())); err != nil {
			t.Fatal(err)
		}
	}
	state["block1_root_on_disk_after"] = rawdb.HasLegacyTrieNode(node.db(), blocks[0].Root())
	data, err := json.Marshal(state)
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "child.json"), data, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {}
}

func runChild(t *testing.T, dir string, pin bool) map[string]any {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestChildProcess$", "-test.count=1")
	command.Env = append(os.Environ(), childEnv+"="+dir, fmt.Sprintf("EXTRACTOR_PINS_CHILD_PIN=%d", map[bool]int{false: 0, true: 1}[pin]))
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("child was not killed: %v\n%s", err, output)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child did not end by SIGKILL: %v\n%s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(dir, "child.json"))
	if err != nil {
		t.Fatalf("child state: %v\n%s", err, output)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// TestNativeVolatileTrieLost covers AC-CAP-03-01 volatile_trie_lost: a pinned
// parent root survives loss of the native trie cache; without the pin the same
// root is lost, so the trie cache was its only copy.
func TestNativeVolatileTrieLost(t *testing.T) {
	blocks := familyBlocks(t)
	genesis := familyGenesis(t)
	for _, pinned := range []bool{true, false} {
		dir := workDir(t, map[bool]string{true: "pinned", false: "control"}[pinned])
		child := runChild(t, dir, pinned)
		if child["block1_root_on_disk_before"] != false || child["head"] != float64(2) {
			t.Fatalf("block 1 state was not volatile before the pin: %v", child)
		}
		expected := StoreIdentity{StoreID: testStore, ChainID: genesis.Config.ChainID.String(),
			GenesisHash: genesis.ToBlock().Hash(), StateScheme: rawdb.HashScheme}
		before := inventory(t, dir)
		report, preflightErr := Preflight(dir, expected)
		if !sameInventory(before, inventory(t, dir)) {
			t.Fatal("preflight changed the store")
		}
		db, closeDB := openStore(t, dir)
		onDisk := rawdb.HasLegacyTrieNode(db, blocks[0].Root())
		walk, walkErr := walkState(db, blocks[0].Root())
		closeDB()
		// Reopen with native snapshots: after the crash the Bor backend
		// selects snap sync, which needs them; Geth and BSC accept either.
		node := openNode(t, dir, true)
		head := node.head()
		recovered := map[string]any{"pinned": pinned, "child": child, "preflight_pins": report.Pins,
			"block1_root_on_disk": onDisk, "walk": walk, "walk_error": fmt.Sprint(walkErr),
			"recovered_head": head.Number.Uint64(), "recovered_root": head.Root.Hex()}
		if pinned {
			if child["block1_root_on_disk_after"] != true || preflightErr != nil || len(report.Pins) != 1 ||
				!onDisk || walkErr != nil || walk.Accounts == 0 || head.Root != blocks[0].Root() {
				t.Fatalf("pinned recovery failed: %v %v", recovered, preflightErr)
			}
			want := uint64(1)
			if blocks[1].Root() == blocks[0].Root() {
				want = 2
			}
			if head.Number.Uint64() != want {
				t.Fatalf("native recovery head %d, want %d", head.Number.Uint64(), want)
			}
			provider, err := New(node.db(), node.trieDB(), expected)
			if err != nil {
				t.Fatal(err)
			}
			if err := provider.Release("obs-volatile"); err != nil {
				t.Fatal(err)
			}
		} else {
			// The control proves the trie cache was the only copy.
			if child["block1_root_on_disk_after"] != false || onDisk || walkErr == nil || head.Number.Uint64() != 0 ||
				preflightErr != nil || len(report.Pins) != 0 {
				t.Fatalf("unpinned control did not lose block 1 state: %v %v", recovered, preflightErr)
			}
		}
		node.stop()
		record(t, map[bool]string{true: "pinned", false: "control"}[pinned], recovered)
	}
}

// TestNativePinnedStatePruning covers AC-CAP-03-01 pinned_state_pruning_attempt
// and the missing-provider half of AC-CAP-03-02: the guarded native pruner
// refuses while a pin is live, the same native pruner without the guard
// deletes the pinned state, and after release the guarded prune proceeds.
func TestNativePinnedStatePruning(t *testing.T) {
	guarded, unguarded := workDir(t, "guarded"), filepath.Join(workDir(t, "control"), "unguarded")
	blocks := familyBlocks(t)
	// Native snapshots stay on here: the native pruner rebuilds its target
	// from the snapshot diff layer of the head, as on a running node.
	node := openNode(t, guarded, true)
	if err := node.insert(blocks); err != nil {
		t.Fatal(err)
	}
	node.stop()
	db, closeDB := openStore(t, guarded)
	head := rawdb.ReadHeadHeader(db)
	genesisHeader := rawdb.ReadHeader(db, rawdb.ReadCanonicalHash(db, 0), 0)
	tdb := triedb.NewDatabase(db, triedb.HashDefaults)
	identity, err := Bind(db, testStore)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	side, code := commitSideState(t, db, tdb, genesisHeader, 9)
	sidePin := pinFor("obs-side", identity, side, code)
	blockPin := pinFor("obs-block1", identity, blocks[0].Header())
	for _, pin := range []Pin{sidePin, blockPin} {
		if err := provider.Pin(pin); err != nil {
			t.Fatal(err)
		}
	}
	if err := tdb.Close(); err != nil {
		t.Fatal(err)
	}
	closeDB()
	if err := os.CopyFS(unguarded, os.DirFS(guarded)); err != nil {
		t.Fatal(err)
	}
	before := inventory(t, guarded)

	// The guard refuses before the pruner needs write access, so a
	// read-only store suffices and proves that nothing was written.
	readOnly, _, err := openReadOnly(filepath.Join(guarded, "chaindata"))
	if err != nil {
		t.Fatal(err)
	}
	refused := PruneState(readOnly, guarded, 256, head.Root)
	readOnly.Close()
	mustErr(t, refused, ErrLivePins)
	if !sameInventory(before, inventory(t, guarded)) {
		t.Fatal("refused prune changed the store")
	}
	db, closeDB = openStore(t, guarded)
	sideWalk, err := walkState(db, side.Root)
	if err != nil || sideWalk.Codes == 0 || sideWalk.Slots == 0 {
		t.Fatalf("pinned side state after refused prune: %v %v", sideWalk, err)
	}
	closeDB()

	db, closeDB = openStore(t, unguarded)
	if err := nativePrune(db, unguarded, head.Root); err != nil {
		t.Fatalf("unguarded native prune: %v", err)
	}
	sideGone := !rawdb.HasLegacyTrieNode(db, side.Root) && !rawdb.HasCode(db, code)
	_, sideErr := walkState(db, side.Root)
	closeDB()
	_, missingErr := Preflight(unguarded, identity)
	if !sideGone || sideErr == nil || !errors.Is(missingErr, ErrPinUnavailable) {
		t.Fatalf("unguarded prune did not delete the pinned state: %v %v %v", sideGone, sideErr, missingErr)
	}

	db, closeDB = openStore(t, guarded)
	tdb = triedb.NewDatabase(db, triedb.HashDefaults)
	provider, err = New(db, tdb, identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"obs-block1", "obs-side"} {
		if err := provider.Release(id); err != nil {
			t.Fatal(err)
		}
	}
	tdb.Close()
	if err := PruneState(db, guarded, 256, head.Root); err != nil {
		t.Fatalf("guarded prune after release: %v", err)
	}
	released := !rawdb.HasLegacyTrieNode(db, side.Root) && !rawdb.HasCode(db, code)
	headWalk, headErr := walkState(db, head.Root)
	closeDB()
	if !released || headErr != nil || headWalk.Accounts == 0 {
		t.Fatalf("guarded prune after release: %v %v %v", released, headWalk, headErr)
	}
	after, err := Preflight(guarded, identity)
	if err != nil || len(after.Pins) != 0 {
		t.Fatalf("preflight after release %v %v", after, err)
	}
	record(t, "pruning", map[string]any{"head_root": head.Root.Hex(), "side_root": side.Root.Hex(),
		"block1_root": blocks[0].Root().Hex(), "refused": refused.Error(), "pinned_side_walk": sideWalk,
		"unguarded_side_deleted": sideGone, "unguarded_preflight": missingErr.Error(),
		"released_side_deleted": released, "head_walk": headWalk})
}

// TestNativeRewindDeletesPinnedHeader shows that native SetHead is a header
// deletion path: the guard refuses the rewind, and the same native rewind
// without the guard deletes the pinned header.
func TestNativeRewindDeletesPinnedHeader(t *testing.T) {
	dir := workDir(t, "node")
	blocks := familyBlocks(t)
	node := openNode(t, dir, false)
	if err := node.insert(blocks); err != nil {
		t.Fatal(err)
	}
	identity, err := Bind(node.db(), testStore)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(node.db(), node.trieDB(), identity)
	if err != nil {
		t.Fatal(err)
	}
	pin := pinFor("obs-rewind", identity, blocks[0].Header())
	if err := provider.Pin(pin); err != nil {
		t.Fatal(err)
	}
	refused := RequireRewindAllowed(node.db(), 0)
	mustErr(t, refused, ErrLivePins)
	if err := RequireRewindAllowed(node.db(), 1); err != nil {
		t.Fatal(err)
	}
	if err := node.setHead(0); err != nil {
		t.Fatal(err)
	}
	deleted := Verify(node.db(), pin)
	mustErr(t, deleted, ErrPinUnavailable)
	node.stop()
	record(t, "rewind", map[string]any{"refused": refused.Error(), "unguarded_verify": deleted.Error()})
}

// TestNativeStorePreflight covers AC-CAP-03-02 wrong_datadir, wrong_backend
// and forbidden_pruning, and AC-OPS-03-02: every incompatible home is
// rejected before capture and left byte-for-byte unchanged.
func TestNativeStorePreflight(t *testing.T) {
	genesis := familyGenesis(t)
	expected := StoreIdentity{StoreID: testStore, ChainID: genesis.Config.ChainID.String(),
		GenesisHash: genesis.ToBlock().Hash(), StateScheme: rawdb.HashScheme}
	unbound := expected
	unbound.StoreID = ""
	prepare := func(name string, setup func(dir string)) string {
		dir := workDir(t, name)
		setup(dir)
		return dir
	}
	fresh := prepare("fresh", func(dir string) { initStore(t, dir, genesis, false) })
	bound := prepare("bound", func(dir string) {
		initStore(t, dir, genesis, false)
		db, closeDB := openStore(t, dir)
		defer closeDB()
		if _, err := Bind(db, testStore); err != nil {
			t.Fatal(err)
		}
	})
	foreignGenesis := prepare("foreign-genesis", func(dir string) {
		other := *genesis
		other.ExtraData = append(append([]byte{}, genesis.ExtraData...), 0x01)
		other.Timestamp++
		initStore(t, dir, &other, false)
	})
	pathStore := prepare("path-store", func(dir string) { initStore(t, dir, genesis, true) })
	interrupted := prepare("interrupted-prune", func(dir string) {
		initStore(t, dir, genesis, false)
		name := "statebloom." + genesis.ToBlock().Root().Hex() + ".bf.gz"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("synthetic leftover"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	empty := prepare("empty-home", func(string) {})
	cases := []struct {
		name     string
		dir      string
		expected StoreIdentity
		want     error
	}{
		{"unbound_fresh_store", fresh, unbound, nil},
		{"bound_store", bound, expected, nil},
		{"foreign_identity", bound, func() StoreIdentity { i := expected; i.StoreID = otherStore; return i }(), ErrWrongStore},
		{"unbound_start_on_bound_store", bound, unbound, ErrWrongStore},
		{"bound_expectation_on_unbound_store", fresh, expected, ErrWrongStore},
		{"foreign_genesis", foreignGenesis, expected, ErrWrongStore},
		{"wrong_chain", fresh, func() StoreIdentity { i := unbound; i.ChainID = "1"; return i }(), ErrWrongStore},
		{"path_initialized_store", pathStore, unbound, ErrUnsupportedBackend},
		{"forbidden_pruning_leftover", interrupted, unbound, ErrForbiddenPruning},
		{"wrong_datadir_empty", empty, unbound, ErrWrongStore},
	}
	results := map[string]string{}
	for _, item := range cases {
		before := inventory(t, item.dir)
		_, err := Preflight(item.dir, item.expected)
		if !sameInventory(before, inventory(t, item.dir)) {
			t.Fatalf("%s: preflight changed the home", item.name)
		}
		if item.want == nil && err != nil || item.want != nil && !errors.Is(err, item.want) {
			t.Fatalf("%s: error %v, want %v", item.name, err, item.want)
		}
		results[item.name] = fmt.Sprint(err)
	}
	// The native parser also refuses to reinterpret the path store as hash,
	// and selects path when an empty store has no explicit scheme.
	db, closeDB := openStore(t, pathStore)
	_, conversion := rawdb.ParseStateScheme(rawdb.HashScheme, db)
	closeDB()
	if conversion == nil {
		t.Fatal("native parser converted a path store to hash")
	}
	emptyDB := rawdb.NewMemoryDatabase()
	defaultScheme, err := rawdb.ParseStateScheme("", emptyDB)
	emptyDB.Close()
	if err != nil {
		t.Fatal(err)
	}
	results["native_hash_override_on_path_store"] = conversion.Error()
	results["native_default_scheme_for_empty_store"] = defaultScheme
	record(t, "preflight", results)
}

// TestNativeEffectiveProfile covers AC-OPS-01-02 and AC-OPS-01-03 with the
// family's native configuration parser.
func TestNativeEffectiveProfile(t *testing.T) {
	results := map[string]any{}
	names := []string{}
	for _, item := range profileCases(t) {
		accepted, detail := item.run(t)
		if accepted != (item.reason == "") {
			t.Fatalf("%s: accepted %v, want rejection reason %q: %v", item.name, accepted, item.reason, detail)
		}
		if !accepted && !strings.Contains(fmt.Sprint(detail), item.reason) {
			t.Fatalf("%s: rejected for another reason, want %q: %v", item.name, item.reason, detail)
		}
		results[item.name] = map[string]any{"accepted": accepted, "reason": item.reason, "detail": detail}
		names = append(names, item.name)
	}
	sort.Strings(names)
	record(t, "profile", map[string]any{"cases": names, "results": results})
}

// profileCase is accepted when reason is empty; a rejection must name reason.
type profileCase struct {
	name   string
	reason string
	run    func(t *testing.T) (bool, any)
}
