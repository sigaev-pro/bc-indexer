package bsc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc/retention"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/triedb"
)

// ---- Parlia handle ----

func sealedLog(t *testing.T, db ethdb.Database, id string) *sourceinputs.Log {
	t.Helper()
	if _, err := sourceinputs.SealCapsule(db, id, []sourceinputs.Item{{Name: "x", Data: []byte("y")}}); err != nil {
		t.Fatal(err)
	}
	log, err := sourceinputs.OpenLog(db, id)
	if err != nil {
		t.Fatal(err)
	}
	return log
}

func TestUnitParliaDBReadsOutsideAnObservationPassThrough(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	db.Put([]byte("parlia-a"), []byte("value"))
	var events []Event
	handle := NewParliaDB(db, func(e Event) { events = append(events, e) })
	if value, err := handle.Get([]byte("parlia-a")); err != nil || string(value) != "value" {
		t.Fatalf("get: %q %v", value, err)
	}
	if _, err := handle.Get([]byte("parlia-b")); err == nil {
		t.Fatal("a missing key read outside an observation returned no error")
	}
	if ok, err := handle.Has([]byte("parlia-a")); err != nil || !ok {
		t.Fatalf("has: %v %v", ok, err)
	}
	it := handle.NewIterator([]byte("parlia-"), nil)
	it.Release()
	if handle.Fetches() != 0 || len(events) != 0 || handle.Detach() != nil {
		t.Fatalf("outside reads were logged: %d %v", handle.Fetches(), events)
	}
}

func TestUnitParliaDBLogsDeclaredReads(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	db.Put([]byte("parlia-a"), []byte("value"))
	var handoffs []string
	handle := NewParliaDB(db, nil)
	handle.Handoff = func(id string, seq uint64, response []byte) {
		handoffs = append(handoffs, hex.EncodeToString(response))
	}
	log := sealedLog(t, db, "obs-1")
	if err := handle.Attach("obs-1", log, [][]byte{[]byte("parlia-a"), []byte("parlia-b")}); err != nil {
		t.Fatal(err)
	}
	if err := handle.Attach("obs-1", log, nil); !errors.Is(err, ErrWiring) {
		t.Fatalf("second attach: %v", err)
	}
	if value, err := handle.Get([]byte("parlia-a")); err != nil || string(value) != "value" {
		t.Fatalf("get: %q %v", value, err)
	}
	if _, err := handle.Get([]byte("parlia-b")); !errors.Is(err, ErrDependencyNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if ok, err := handle.Has([]byte("parlia-a")); err != nil || !ok {
		t.Fatalf("has: %v %v", ok, err)
	}
	if handle.Fetches() != 3 || log.Frontier() != 3 {
		t.Fatalf("fetches %d frontier %d", handle.Fetches(), log.Frontier())
	}
	want := []string{"0176616c7565", "00", "01"}
	if strings.Join(handoffs, ",") != strings.Join(want, ",") {
		t.Fatalf("handoffs %v", handoffs)
	}
	if err := handle.Detach(); err != nil {
		t.Fatal(err)
	}
	// A retry on a reopened log hands over the saved bytes, not the live value.
	db.Put([]byte("parlia-a"), []byte("changed"))
	log, err := sourceinputs.OpenLog(db, "obs-1")
	if err != nil {
		t.Fatal(err)
	}
	handle.Attach("obs-1", log, [][]byte{[]byte("parlia-a"), []byte("parlia-b")})
	if value, err := handle.Get([]byte("parlia-a")); err != nil || string(value) != "value" {
		t.Fatalf("replay: %q %v", value, err)
	}
	if handle.Fetches() != 3 {
		t.Fatalf("replay fetched: %d", handle.Fetches())
	}
	handle.Detach()
}

func TestUnitParliaDBStopsTheObservation(t *testing.T) {
	cases := map[string]func(*ParliaDB) error{
		"undeclared": func(h *ParliaDB) error { _, err := h.Get([]byte("parlia-z")); return err },
		"iterator": func(h *ParliaDB) error {
			h.NewIterator(nil, nil).Release()
			return nil
		},
	}
	for name, read := range cases {
		db := rawdb.NewMemoryDatabase()
		handle := NewParliaDB(db, nil)
		handle.Attach("obs-1", sealedLog(t, db, "obs-1"), [][]byte{[]byte("parlia-a")})
		read(handle)
		if _, err := handle.Get([]byte("parlia-a")); err == nil {
			t.Errorf("%s: a read after the stop succeeded", name)
		}
		err := handle.Detach()
		if name == "undeclared" && !errors.Is(err, ErrUndeclaredRead) || name == "iterator" && !errors.Is(err, ErrUnloggedRead) {
			t.Errorf("%s: detach %v", name, err)
		}
		// The next observation starts clean.
		handle.Attach("obs-2", sealedLog(t, db, "obs-2"), [][]byte{[]byte("parlia-a")})
		if _, err := handle.Get([]byte("parlia-a")); !errors.Is(err, ErrDependencyNotFound) || handle.Detach() != nil {
			t.Errorf("%s: next observation: %v", name, err)
		}
	}
}

func TestUnitParliaDBMalformedSavedResponse(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	handle := NewParliaDB(db, nil)
	log := sealedLog(t, db, "obs-1")
	if _, err := log.Next(ParliaRequest(ParliaGetTag, []byte("parlia-a")), func() ([]byte, error) { return []byte{7}, nil }); err != nil {
		t.Fatal(err)
	}
	log, _ = sourceinputs.OpenLog(db, "obs-1")
	handle.Attach("obs-1", log, [][]byte{[]byte("parlia-a")})
	if _, err := handle.Get([]byte("parlia-a")); !errors.Is(err, ErrMalformedResponse) || !errors.Is(handle.Detach(), ErrMalformedResponse) {
		t.Fatalf("malformed: %v", err)
	}
}

// ---- static manifest ----

const unitGenesisJSON = `{"config":{"chainId":714,"homesteadBlock":0,"eip150Block":0,"eip155Block":0,
 "eip158Block":0,"byzantiumBlock":0,"constantinopleBlock":0,"petersburgBlock":0,"istanbulBlock":0,
 "muirGlacierBlock":0,"ramanujanBlock":0,"nielsBlock":0,"mirrorSyncBlock":1,"brunoBlock":5,
 "parlia":{"period":3,"epoch":200}},"difficulty":"0x1","gasLimit":"0x2625a00","timestamp":"0x5e9da7ce",
 "extraData":"0x00","alloc":{"0x0000000000000000000000000000000000001000":{"balance":"0x1"}}}`

func unitGenesis(t *testing.T) (*Genesis, ethdb.Database) {
	t.Helper()
	var genesis core.Genesis
	if err := json.Unmarshal([]byte(unitGenesisJSON), &genesis); err != nil {
		t.Fatal(err)
	}
	db := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(db, triedb.HashDefaults)
	block, err := genesis.Commit(db, tdb, nil)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadGenesis([]byte(unitGenesisJSON), block)
	if err != nil {
		t.Fatal(err)
	}
	return loaded, db
}

func unitChain(genesis *types.Header, count int) []*types.Header {
	headers := []*types.Header{genesis}
	for i := 1; i <= count; i++ {
		parent := headers[i-1]
		headers = append(headers, &types.Header{ParentHash: parent.Hash(), Number: big.NewInt(int64(i)),
			Root: common.Hash{byte(i)}, Difficulty: big.NewInt(2), GasLimit: parent.GasLimit, Time: parent.Time + 3,
			Extra: []byte{byte(i)}})
	}
	return headers
}

func unitItems(t *testing.T, genesis *Genesis, headers []*types.Header, n int) map[string][]byte {
	t.Helper()
	block := types.NewBlockWithHeader(headers[n])
	items, err := BlockItems(block, func(number uint64) *types.Header { return headers[number] }, genesis, "commit")
	if err != nil {
		t.Fatal(err)
	}
	return itemMap(items)
}

func TestUnitStaticGenesisIsCanonical(t *testing.T) {
	genesis, _ := unitGenesis(t)
	canonical, err := admission.CanonicalJSON([]byte(unitGenesisJSON))
	if err != nil || !bytes.Equal(genesis.Canonical, canonical) || bytes.Contains(canonical, []byte(" ")) {
		t.Fatalf("canonical genesis: %s %v", genesis.Canonical, err)
	}
	if _, err := LoadGenesis([]byte(strings.Replace(unitGenesisJSON, `"0x1"}}}`, `"0x1"}},"x":1.5}`, 1)), genesis.Block); !errors.Is(err, ErrStaticInput) {
		t.Errorf("a float was accepted: %v", err)
	}
	if _, err := LoadGenesis([]byte(strings.Replace(unitGenesisJSON, `"0x2625a00"`, `"0x2625a01"`, 1)), genesis.Block); !errors.Is(err, ErrStaticInput) {
		t.Errorf("a genesis of another block was accepted: %v", err)
	}
	if _, err := LoadGenesis([]byte(`{"difficulty":"0x1"}`), genesis.Block); !errors.Is(err, ErrStaticInput) {
		t.Errorf("a genesis without config was accepted: %v", err)
	}
}

func TestUnitStaticValueItems(t *testing.T) {
	genesis, _ := unitGenesis(t)
	headers := unitChain(genesis.Block.Header(), 6)
	items := unitItems(t, genesis, headers, 6)
	static, err := DecodeStatic(items, genesis, "commit")
	if err != nil || static.Block.NumberU64() != 6 || len(static.Ancestors) != 5 {
		t.Fatalf("decode: %v", err)
	}
	// The fork id comes from the decoded config: brunoBlock 5 is passed at 6,
	// mirrorSyncBlock 1 too, so next is 0.
	var fork map[string]string
	if err := json.Unmarshal(items["fork-id"], &fork); err != nil || fork["next"] != "0" {
		t.Fatalf("fork id %s", items["fork-id"])
	}
	early := unitItems(t, genesis, headers, 2)
	if err := json.Unmarshal(early["fork-id"], &fork); err != nil || fork["next"] != "5" {
		t.Fatalf("fork id at 2: %s", early["fork-id"])
	}
	tamper := map[string][]byte{
		"genesis-json":          []byte(unitGenesisJSON),
		"fork-id":               early["fork-id"],
		"native-commit":         []byte("other"),
		"normalization-profile": []byte("other"),
		"state-profile":         []byte("other"),
	}
	for name, value := range tamper {
		changed := map[string][]byte{}
		for k, v := range items {
			changed[k] = v
		}
		changed[name] = value
		if _, err := DecodeStatic(changed, genesis, "commit"); !errors.Is(err, ErrStaticInput) {
			t.Errorf("tampered %s accepted: %v", name, err)
		}
	}
	if _, err := DecodeStatic(items, genesis, "another-commit"); !errors.Is(err, ErrStaticInput) {
		t.Errorf("another home commit accepted: %v", err)
	}
}

// ---- run record ----

func TestUnitRunRecord(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	identity := stateprovider.StoreIdentity{StoreID: strings.Repeat("a", 32)}
	producer := admission.Producer{ProducerID: "bsc-node", RunID: "run-1"}
	if _, err := openRun(db, Config{Identity: identity}); !errors.Is(err, ErrRunIdentity) {
		t.Fatalf("no run: %v", err)
	}
	if got, err := openRun(db, Config{Identity: identity, Producer: producer}); err != nil || got != producer {
		t.Fatalf("save: %v %v", got, err)
	}
	if got, err := openRun(db, Config{Identity: identity}); err != nil || got != producer {
		t.Fatalf("reopen: %v %v", got, err)
	}
	if _, err := openRun(db, Config{Identity: identity, Producer: admission.Producer{ProducerID: "bsc-node", RunID: "run-2"}}); !errors.Is(err, ErrRunIdentity) {
		t.Fatalf("another run: %v", err)
	}
	if _, err := openRun(db, Config{Identity: stateprovider.StoreIdentity{StoreID: strings.Repeat("b", 32)}}); !errors.Is(err, ErrRunIdentity) {
		t.Fatalf("another store: %v", err)
	}
	db.Put([]byte(RunRecordKey), []byte("{"))
	if _, err := openRun(db, Config{Identity: identity}); !errors.Is(err, ErrRunIdentity) {
		t.Fatalf("malformed: %v", err)
	}
}

// ---- pin adapter ----

func unitProvider(t *testing.T) (*Genesis, ethdb.Database, *stateprovider.Provider, stateprovider.StoreIdentity) {
	t.Helper()
	genesis, db := unitGenesis(t)
	identity, err := stateprovider.Bind(db, strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := stateprovider.New(db, triedb.NewDatabase(db, triedb.HashDefaults), identity)
	if err != nil {
		t.Fatal(err)
	}
	return genesis, db, provider, identity
}

func TestUnitPinAdapter(t *testing.T) {
	genesis, db, provider, identity := unitProvider(t)
	root := genesis.Block.Root()
	pin := stateprovider.Pin{ID: "bsc-node.run-1.0000000000000000", Store: identity, Profile: ProfileDigest(),
		ParentRoot: root, Headers: []stateprovider.HeaderRef{{Hash: genesis.Block.Hash(), Number: 0}}, Codes: []common.Hash{}}
	if err := provider.Pin(pin); err != nil {
		t.Fatal(err)
	}
	pins := LedgerPins{DB: db, Provider: provider}
	digest, err := pins.Verify(pin.ID, hexHash(root))
	if err != nil {
		t.Fatal(err)
	}
	record, _ := db.Get([]byte(PinRecordPrefix + pin.ID))
	want := sha256.Sum256(record)
	var decoded stateprovider.Pin
	if digest != hex.EncodeToString(want[:]) || json.Unmarshal(record, &decoded) != nil || decoded.ID != pin.ID {
		t.Fatalf("digest %s of record %s", digest, record)
	}
	if listed, _ := stateprovider.Pins(db); len(listed) != 1 || listed[0].ID != pin.ID {
		t.Fatalf("the record under PinRecordPrefix is not the listed pin: %v", listed)
	}
	if _, err := pins.Verify(pin.ID, hexHash(common.Hash{1})); !errors.Is(err, ErrPinMismatch) {
		t.Errorf("another root: %v", err)
	}
	if _, err := pins.Verify("bsc-node.run-1.0000000000000001", hexHash(root)); !errors.Is(err, ErrPinMismatch) {
		t.Errorf("absent pin: %v", err)
	}
	if err := pins.Release(pin.ID); err != nil {
		t.Fatal(err)
	}
	if err := pins.Release(pin.ID); err != nil {
		t.Errorf("releasing an absent pin: %v", err)
	}
	if err := pins.Release("BAD ID"); err == nil {
		t.Errorf("an invalid ID was released")
	}
	// Abandon of an INPUT_PREPARING observation that never got a pin.
	guard := admission.NewGuard()
	token := guard.Acquire()
	defer token.Release()
	producer := admission.Producer{ProducerID: "bsc-node", RunID: "run-1"}
	adm, err := admission.Open(token, db, producer, pins, Inputs{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if err := adm.GoLive(token); err != nil {
		t.Fatal(err)
	}
	obs, err := adm.Begin(token, Candidate(types.NewBlockWithHeader(unitChain(genesis.Block.Header(), 1)[1]), genesis.Block.Header()))
	if err != nil {
		t.Fatal(err)
	}
	if err := adm.Abandon(token, obs.Key); err != nil {
		t.Fatalf("abandon without a pin: %v", err)
	}
	if left, _ := adm.Observations(token); len(left) != 0 {
		t.Fatalf("observations left: %v", left)
	}
}

func TestUnitInputsAdapter(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	log := sealedLog(t, db, "obs-1")
	inputs := Inputs{DB: db}
	if _, err := inputs.Consumed("obs-1"); !errors.Is(err, ErrNotClosed) {
		t.Fatalf("open log: %v", err)
	}
	closed, err := log.Finish()
	if err != nil {
		t.Fatal(err)
	}
	got, err := inputs.Consumed("obs-1")
	capsule, _ := inputs.CapsuleDigest("obs-1")
	if err != nil || got.Digest != closed.Digest || got.CapsuleDigest != capsule {
		t.Fatalf("consumed %+v %v", got, err)
	}
	if _, err := inputs.CapsuleDigest("obs-2"); err == nil {
		t.Fatal("absent capsule")
	}
}

// ---- digests ----

func TestUnitDigestsFollowTheirFormula(t *testing.T) {
	header := &types.Header{Number: big.NewInt(1), Root: common.Hash{2}, ReceiptHash: common.Hash{3}, Difficulty: big.NewInt(1)}
	block := types.NewBlockWithHeader(header)
	receipt := &types.Receipt{Status: 1, CumulativeGasUsed: 21000, Logs: []*types.Log{}}
	encoded, _ := receipt.MarshalBinary()
	data := append([]byte("extractor-bsc-exec-result-v1\x00"), block.Hash().Bytes()...)
	data = append(append(append(data, header.Root.Bytes()...), header.ReceiptHash.Bytes()...), u64(1)...)
	data = append(data, str(encoded)...)
	want := sha256.Sum256(data)
	got, err := ExecDigest(block, &core.ProcessResult{Receipts: types.Receipts{receipt}})
	if err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("exec digest %s", got)
	}
	empty, _ := ExecDigest(block, nil)
	if empty == got {
		t.Fatal("receipts do not change the digest")
	}
	cause := errors.New("bad block")
	data = append(append([]byte("extractor-bsc-rejection-v1\x00"), block.Hash().Bytes()...), str([]byte("bad block"))...)
	want = sha256.Sum256(data)
	if RejectionEvidence(block, cause) != hex.EncodeToString(want[:]) {
		t.Fatal("rejection evidence")
	}
}

// ---- retention guards ----

func TestUnitRetentionInstall(t *testing.T) {
	core.ExtractorRewindGuard, rawdb.ExtractorFreezeLimit = nil, nil
	retention.Install()
	genesis, db, provider, identity := unitProvider(t)
	if err := core.ExtractorRewindGuard(db, 0); err != nil {
		t.Fatalf("no pin: %v", err)
	}
	headers := unitChain(genesis.Block.Header(), 1)
	rawdb.WriteHeader(db, headers[1])
	pin := stateprovider.Pin{ID: "bsc-node.run-1.0000000000000000", Store: identity, Profile: ProfileDigest(),
		ParentRoot: genesis.Block.Root(), Headers: []stateprovider.HeaderRef{{Hash: genesis.Block.Hash(), Number: 0},
			{Hash: headers[1].Hash(), Number: 1}}}
	// The second header has no state; the ledger record is written directly.
	record, _ := json.Marshal(pin)
	db.Put([]byte(PinRecordPrefix+pin.ID), record)
	if err := core.ExtractorRewindGuard(db, 0); !errors.Is(err, stateprovider.ErrLivePins) {
		t.Fatalf("rewind below a pinned header: %v", err)
	}
	if err := core.ExtractorRewindGuard(db, 1); err != nil {
		t.Fatalf("rewind to the pinned header: %v", err)
	}
	// Header 1 is not canonical, so it is a side header forked at the
	// genesis: the freezer may freeze only block 0.
	if limit, err := rawdb.ExtractorFreezeLimit(db, 10); err != nil || limit != 1 {
		t.Fatalf("side header forked at 0: %d %v", limit, err)
	}
	if err := provider.Release(pin.ID); err != nil {
		t.Fatal(err)
	}
	if limit, err := rawdb.ExtractorFreezeLimit(db, 10); err != nil || limit != 10 {
		t.Fatalf("released: %d %v", limit, err)
	}
}

// freezerChain builds a 12-block ethash-faker chain on a freezer database with
// a two-block side branch forked at block 4.
func freezerChain(t *testing.T) (ethdb.Database, []*types.Block, []*types.Block) {
	t.Helper()
	genesis := &core.Genesis{Config: params.AllEthashProtocolChanges, BaseFee: big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{}}
	_, blocks, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 12, func(int, *core.BlockGen) {})
	_, side, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 6, func(i int, b *core.BlockGen) {
		if i >= 4 {
			b.SetExtra([]byte("side"))
		}
	})
	kv := rawdb.NewMemoryDatabase()
	db, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotLimit = rawdb.HashScheme, true, 0
	chain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chain.InsertChain(blocks); err != nil {
		t.Fatal(err)
	}
	if _, err := chain.InsertChain(side[4:]); err != nil {
		t.Fatal(err)
	}
	chain.Stop()
	env := &ethdb.FreezerEnv{ChainCfg: genesis.Config}
	if err := db.(interface {
		SetupFreezerEnv(*ethdb.FreezerEnv, uint64) error
	}).SetupFreezerEnv(env, 0); err != nil {
		t.Fatal(err)
	}
	return db, blocks, side[4:]
}

func freeze(t *testing.T, db ethdb.Database) uint64 {
	t.Helper()
	if err := db.(interface{ Freeze(uint64) error }).Freeze(2); err != nil {
		t.Fatal(err)
	}
	frozen, err := db.Ancients()
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func TestRetentionFreezerKeepsAPinnedSideBranch(t *testing.T) {
	defer retention.Install()
	// Control: without the patch's limit the freezer deletes the side branch.
	core.ExtractorRewindGuard, rawdb.ExtractorFreezeLimit = nil, nil
	db, _, side := freezerChain(t)
	if frozen := freeze(t, db); frozen != 11 || rawdb.ReadHeader(db, side[0].Hash(), 5) != nil {
		t.Fatalf("control: frozen %d, side header kept %v", frozen, rawdb.ReadHeader(db, side[0].Hash(), 5) != nil)
	}
	// With the guards, a pin on the side header at 5 holds the freezer at
	// the fork point 4 until the pin is released.
	retention.Install()
	db, _, side = freezerChain(t)
	identity := stateprovider.StoreIdentity{StoreID: strings.Repeat("d", 32), ChainID: "1337",
		GenesisHash: rawdb.ReadCanonicalHash(db, 0), StateScheme: rawdb.HashScheme}
	pin := stateprovider.Pin{ID: "bsc-node.run-1.0000000000000000", Store: identity, Profile: ProfileDigest(),
		ParentRoot: side[0].Root(), Headers: []stateprovider.HeaderRef{{Hash: side[0].Hash(), Number: 5}}, Codes: []common.Hash{}}
	record, _ := json.Marshal(pin)
	db.Put([]byte(PinRecordPrefix+pin.ID), record)
	if frozen := freeze(t, db); frozen != 5 || rawdb.ReadHeader(db, side[0].Hash(), 5) == nil {
		t.Fatalf("pinned: frozen %d", frozen)
	}
	if frozen := freeze(t, db); frozen != 5 {
		t.Fatalf("pinned, second cycle: frozen %d", frozen)
	}
	db.Delete([]byte(PinRecordPrefix + pin.ID))
	if frozen := freeze(t, db); frozen != 11 || rawdb.ReadHeader(db, side[0].Hash(), 5) != nil {
		t.Fatalf("released: frozen %d", frozen)
	}
}

func TestUnitBlockItemsRefuseBadInputs(t *testing.T) {
	genesis, _ := unitGenesis(t)
	headers := unitChain(genesis.Block.Header(), 3)
	lookup := func(number uint64) *types.Header { return headers[number] }
	if _, err := BlockItems(types.NewBlockWithHeader(headers[0]), lookup, genesis, "c"); !errors.Is(err, ErrStaticInput) {
		t.Errorf("genesis block: %v", err)
	}
	orphan := types.CopyHeader(headers[3])
	orphan.ParentHash = common.Hash{9}
	if _, err := BlockItems(types.NewBlockWithHeader(orphan), lookup, genesis, "c"); !errors.Is(err, ErrStaticInput) {
		t.Errorf("orphan: %v", err)
	}
	missing := func(number uint64) *types.Header {
		if number == 1 {
			return nil
		}
		return headers[number]
	}
	if _, err := BlockItems(types.NewBlockWithHeader(headers[3]), missing, genesis, "c"); !errors.Is(err, ErrStaticInput) {
		t.Errorf("missing ancestor: %v", err)
	}
	items := unitItems(t, genesis, headers, 3)
	var ancestors []*types.Header
	rlp.DecodeBytes(items["ancestor-headers-rlp"], &ancestors)
	if len(ancestors) != 2 || ancestors[1].Number.Uint64() != 0 {
		t.Fatalf("window of block 3: %d", len(ancestors))
	}
}

// ---- observation exits and the safe point ----

func TestUnitValidatedRefusesAWiringError(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	handle := NewParliaDB(db, nil)
	handle.Attach("obs-1", sealedLog(t, db, "obs-1"), nil)
	handle.Get([]byte("parlia-undeclared"))
	header := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)}
	block := types.NewBlockWithHeader(header)
	// The wiring error must stop Validated before it touches the log or the
	// admission, which this observation does not have.
	o := &observation{seam: &Seam{parlia: handle, emit: func(Event) {}}, key: "obs-1", hash: block.Hash()}
	if err := o.Validated(block, nil, nil); !errors.Is(err, ErrUndeclaredRead) {
		t.Fatalf("validated: %v", err)
	}
	if err := o.End(nil); !errors.Is(err, ErrUndeclaredRead) {
		t.Fatalf("end after a failed validation: %v", err)
	}
	other := &observation{seam: &Seam{parlia: handle, emit: func(Event) {}}, key: "obs-2", hash: block.Hash()}
	if err := other.Validated(types.NewBlockWithHeader(&types.Header{Number: big.NewInt(2)}), nil, nil); !errors.Is(err, ErrWiring) {
		t.Fatalf("validation of another block: %v", err)
	}
}

func TestUnitRejectedWithAWiringErrorStaysAdmitted(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	handle := NewParliaDB(db, nil)
	handle.Attach("obs-1", sealedLog(t, db, "obs-1"), nil)
	handle.Get([]byte("parlia-undeclared"))
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	o := &observation{seam: &Seam{parlia: handle, emit: func(Event) {}}, key: "obs-1", hash: block.Hash()}
	o.Rejected(block, nil, errors.New("unauthorized validator"))
	if o.resolved != "" || !errors.Is(o.End(nil), ErrUndeclaredRead) {
		t.Fatalf("a rejection after a wiring error was trusted: %q", o.resolved)
	}
}

func TestUnitEndOfAnUnresolvedObservation(t *testing.T) {
	handle := NewParliaDB(rawdb.NewMemoryDatabase(), nil)
	o := &observation{seam: &Seam{parlia: handle, emit: func(Event) {}}, key: "obs-1"}
	if err := o.End(nil); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("end without an exit: %v", err)
	}
	done := &observation{seam: &Seam{parlia: handle, emit: func(Event) {}}, key: "obs-2", resolved: admission.ExecReady}
	if err := done.End(nil); err != nil {
		t.Fatalf("end after EXEC_READY: %v", err)
	}
}

func TestUnitSafePointsCountOnlyTheImportTracer(t *testing.T) {
	importTracer, rpcTracer := &tracing.Hooks{}, &tracing.Hooks{}
	var forwarded int
	s := &Seam{tracer: importTracer, safePoint: func(vm.OpCode, *vm.ScopeContext) { forwarded++ }}
	s.onSafePoint(rpcTracer, vm.ADD, nil)
	s.onSafePoint(nil, vm.ADD, nil)
	if s.SafePointsOutside() != 0 || forwarded != 0 {
		t.Fatalf("another tracer counted: %d %d", s.SafePointsOutside(), forwarded)
	}
	s.onSafePoint(importTracer, vm.ADD, nil)
	if s.SafePointsOutside() != 1 || forwarded != 0 {
		t.Fatalf("outside an observation: %d %d", s.SafePointsOutside(), forwarded)
	}
	o := &observation{seam: s}
	s.active.Store(o)
	s.onSafePoint(importTracer, vm.ADD, nil)
	if o.safePoints.Load() != 1 || forwarded != 1 || s.SafePointsOutside() != 1 {
		t.Fatalf("inside an observation: %d %d", o.safePoints.Load(), forwarded)
	}
}

func TestUnitPinVerifyNeedsTheState(t *testing.T) {
	genesis, db, provider, identity := unitProvider(t)
	pin := stateprovider.Pin{ID: "bsc-node.run-1.0000000000000000", Store: identity, Profile: ProfileDigest(),
		ParentRoot: genesis.Block.Root(), Headers: []stateprovider.HeaderRef{{Hash: genesis.Block.Hash(), Number: 0}}}
	if err := provider.Pin(pin); err != nil {
		t.Fatal(err)
	}
	rawdb.DeleteLegacyTrieNode(db, genesis.Block.Root())
	if _, err := (LedgerPins{DB: db, Provider: provider}).Verify(pin.ID, hexHash(genesis.Block.Root())); !errors.Is(err, stateprovider.ErrPinUnavailable) {
		t.Fatalf("a pin without its state verified: %v", err)
	}
}

func TestUnitStaticWindowAboveACheckpoint(t *testing.T) {
	genesis, _ := unitGenesis(t)
	headers := unitChain(genesis.Block.Header(), 1100)
	items := unitItems(t, genesis, headers, 1100)
	static, err := DecodeStatic(items, genesis, "commit")
	if err != nil || len(static.Ancestors) != 1100-1-844 || static.Snapshot.Checkpoint != 1024 {
		t.Fatalf("window of 1100: %d ancestors, %v", len(static.Ancestors), err)
	}
	// One header short: the checkpoint is still inside, the window is not full.
	short, _ := rlp.EncodeToBytes(static.Ancestors[:len(static.Ancestors)-1])
	changed := map[string][]byte{}
	for k, v := range items {
		changed[k] = v
	}
	changed["ancestor-headers-rlp"] = short
	if _, err := DecodeStatic(changed, genesis, "commit"); err == nil || !strings.Contains(err.Error(), "ancestor headers, want") {
		t.Fatalf("short window accepted: %v", err)
	}
	changed = map[string][]byte{}
	for k, v := range items {
		changed[k] = v
	}
	changed["parlia-snapshot-keys"] = []byte(`{"checkpoint":1024,"keys":["7061726c69612d00"]}`)
	if _, err := DecodeStatic(changed, genesis, "commit"); err == nil || !strings.Contains(err.Error(), "parlia-snapshot-keys") {
		t.Fatalf("another snapshot key accepted: %v", err)
	}
}
