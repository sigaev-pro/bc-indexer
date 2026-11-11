package bsc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc/retention"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/triedb"
)

// ---- observation reader ----

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

func unitReader(t *testing.T, n int, log *sourceinputs.Log, fetches *int, handoffs *[]uint64) (*Reader, []*types.Header) {
	t.Helper()
	genesis, _ := unitGenesis(t)
	headers := unitChain(genesis.Block.Header(), n)
	static, err := DecodeStatic(unitItems(t, genesis, headers, n), genesis, "commit")
	if err != nil {
		t.Fatal(err)
	}
	r := newReader("obs-1", static, genesis.Block.Header(), log, func(Event) {}, func() { *fetches++ },
		func(_ string, sequence uint64, _ []byte) { *handoffs = append(*handoffs, sequence) })
	return r, headers
}

func TestUnitReaderServesOnlyCapsuleHeaders(t *testing.T) {
	var fetches int
	var handoffs []uint64
	r, headers := unitReader(t, 300, nil, &fetches, &handoffs)
	parent := headers[299]
	same := func(a, b *types.Header) bool { return a != nil && a.Hash() == b.Hash() }
	if !same(r.GetHeader(parent.Hash(), 299), parent) || !same(r.GetHeaderByHash(headers[44].Hash()), headers[44]) ||
		!same(r.GetHeaderByNumber(44), headers[44]) || !same(r.CurrentHeader(), parent) ||
		r.GenesisHeader().Hash() != headers[0].Hash() || r.Err() != nil {
		t.Fatalf("capsule headers not served: %v", r.Err())
	}
	refusals := map[string]struct {
		read func(*Reader) bool
		want error
	}{
		"below the window": {func(r *Reader) bool { return r.GetHeaderByNumber(43) == nil }, ErrUndeclaredHeader},
		"hash below":       {func(r *Reader) bool { return r.GetHeaderByHash(headers[43].Hash()) == nil }, ErrUndeclaredHeader},
		"wrong number":     {func(r *Reader) bool { return r.GetHeader(parent.Hash(), 298) == nil }, ErrUndeclaredHeader},
		"the block itself": {func(r *Reader) bool { return r.GetHeader(headers[300].Hash(), 300) == nil }, ErrUndeclaredHeader},
		"td":               {func(r *Reader) bool { return r.GetTd(parent.Hash(), 299) == nil }, ErrUndeclaredRead},
		"highest verified": {func(r *Reader) bool { return r.GetHighestVerifiedHeader() == nil }, ErrUndeclaredRead},
		"verified block":   {func(r *Reader) bool { return r.GetVerifiedBlockByHash(parent.Hash()) == nil }, ErrUndeclaredRead},
	}
	for name, c := range refusals {
		r, _ := unitReader(t, 300, nil, &fetches, &handoffs)
		if !c.read(r) || !errors.Is(r.Err(), c.want) {
			t.Fatalf("%s: %v", name, r.Err())
		}
		// The first error stays; later reads of capsule headers still work.
		if !same(r.GetHeaderByNumber(299), parent) || !errors.Is(r.Err(), c.want) {
			t.Fatalf("%s: error replaced: %v", name, r.Err())
		}
	}
}

func TestUnitReaderLogsSnapshots(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	var fetches int
	var handoffs []uint64
	r, headers := unitReader(t, 300, sealedLog(t, db, "obs-1"), &fetches, &handoffs)
	parent := headers[299]
	compute := func(response string) func(consensus.ChainHeaderReader) ([]byte, error) {
		return func(consensus.ChainHeaderReader) ([]byte, error) { return []byte(response), nil }
	}
	got, err := r.ExtractorSnapshot(299, parent.Hash(), compute("first"))
	if err != nil || string(got) != "first" || fetches != 1 || len(handoffs) != 1 || handoffs[0] != 0 {
		t.Fatalf("first request: %q %v fetches %d handoffs %v", got, err, fetches, handoffs)
	}
	if got, err := r.ExtractorSnapshot(44, headers[44].Hash(), compute("second")); err != nil || string(got) != "second" || fetches != 2 {
		t.Fatalf("second request: %q %v", got, err)
	}
	// A repeated request receives the first response, with no new entry.
	if got, err := r.ExtractorSnapshot(299, parent.Hash(), compute("again")); err != nil || string(got) != "first" ||
		fetches != 2 || len(handoffs) != 2 || r.log.Cursor() != 2 {
		t.Fatalf("repeated request: %q %v fetches %d", got, err, fetches)
	}
	// A retry receives the saved bytes in order, without the native lookup.
	replay, _ := unitReader(t, 300, func() *sourceinputs.Log {
		log, err := sourceinputs.OpenLog(db, "obs-1")
		if err != nil {
			t.Fatal(err)
		}
		return log
	}(), &fetches, &handoffs)
	if got, err := replay.ExtractorSnapshot(299, parent.Hash(), compute("changed")); err != nil || string(got) != "first" || fetches != 2 {
		t.Fatalf("replay: %q %v fetches %d", got, err, fetches)
	}
	if !bytes.Equal(SnapshotRequest(299, parent.Hash())[:len(SnapshotTag)+1], append([]byte(SnapshotTag), 0)) ||
		len(SnapshotRequest(299, parent.Hash())) != len(SnapshotTag)+1+8+32 {
		t.Fatal("request layout")
	}
	// A snapshot of a header outside the capsule is refused before the lookup.
	outside, _ := unitReader(t, 300, sealedLog(t, db, "obs-2"), &fetches, &handoffs)
	if _, err := outside.ExtractorSnapshot(43, headers[43].Hash(), compute("x")); !errors.Is(err, ErrUndeclaredHeader) ||
		fetches != 2 || !errors.Is(outside.Err(), ErrUndeclaredHeader) {
		t.Fatalf("outside snapshot: %v fetches %d", err, fetches)
	}
	if _, err := outside.ExtractorSnapshot(299, parent.Hash(), compute("x")); !errors.Is(err, ErrUndeclaredHeader) || fetches != 2 {
		t.Fatalf("a stopped reader looked up: %v", err)
	}
	// A failed native lookup stops the observation and saves nothing.
	failing, _ := unitReader(t, 300, sealedLog(t, db, "obs-3"), &fetches, &handoffs)
	lookup := errors.New("unknown ancestor")
	if _, err := failing.ExtractorSnapshot(299, parent.Hash(), func(consensus.ChainHeaderReader) ([]byte, error) {
		return nil, lookup
	}); !errors.Is(err, lookup) || !errors.Is(failing.Err(), lookup) {
		t.Fatalf("lookup error: %v", err)
	}
}

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

// The block encoding drops the blob sidecars, so the capsule keeps them as an
// item and the decoded block carries them again; a sidecar of another block
// is refused.
func TestUnitStaticKeepsBlobSidecars(t *testing.T) {
	genesis, _ := unitGenesis(t)
	headers := unitChain(genesis.Block.Header(), 3)
	lookup := func(number uint64) *types.Header { return headers[number] }
	plain := types.NewBlockWithHeader(headers[3])
	sidecar := func(hash common.Hash) *types.BlobSidecar {
		return &types.BlobSidecar{
			BlobTxSidecar: types.BlobTxSidecar{Blobs: []kzg4844.Blob{{1}}, Commitments: []kzg4844.Commitment{{2}},
				Proofs: []kzg4844.Proof{{3}}},
			BlockNumber: big.NewInt(3), BlockHash: hash, TxIndex: 0, TxHash: common.Hash{4}}
	}
	block := plain.WithSidecars(types.BlobSidecars{sidecar(plain.Hash())})
	items, err := BlockItems(block, lookup, genesis, "c")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStatic(itemMap(items), genesis, "c")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := rlp.EncodeToBytes(block.Sidecars())
	got, _ := rlp.EncodeToBytes(decoded.Block.Sidecars())
	if len(decoded.Block.Sidecars()) != 1 || !bytes.Equal(got, want) || decoded.Block.Hash() != block.Hash() {
		t.Fatalf("decoded sidecars %x, want %x", got, want)
	}
	// Control: the block item alone has no sidecars.
	var bare types.Block
	if err := rlp.DecodeBytes(itemMap(items)["block-rlp"], &bare); err != nil || len(bare.Sidecars()) != 0 {
		t.Fatalf("block-rlp carries %d sidecars: %v", len(bare.Sidecars()), err)
	}
	// No sidecars: an empty list.
	if empty := unitItems(t, genesis, headers, 3)["blob-sidecars"]; !bytes.Equal(empty, []byte{0xc0}) {
		t.Errorf("no sidecars encoded as %x", empty)
	}
	foreign, err := BlockItems(plain.WithSidecars(types.BlobSidecars{sidecar(common.Hash{5})}), lookup, genesis, "c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeStatic(itemMap(foreign), genesis, "c"); !errors.Is(err, ErrStaticInput) {
		t.Errorf("a sidecar of another block was accepted: %v", err)
	}
	broken := itemMap(items)
	broken["blob-sidecars"] = []byte{0x01}
	if _, err := DecodeStatic(broken, genesis, "c"); !errors.Is(err, ErrStaticInput) {
		t.Errorf("a malformed sidecar item was accepted: %v", err)
	}
}

// ---- observation exits and the safe point ----

func stoppedReader() *Reader {
	r := &Reader{emit: func(Event) {}}
	r.refuse(fmt.Errorf("%w: number 1", ErrUndeclaredHeader))
	return r
}

func TestUnitValidatedRefusesAWiringError(t *testing.T) {
	header := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)}
	block := types.NewBlockWithHeader(header)
	// The wiring error must stop Validated before it touches the log or the
	// admission, which this observation does not have.
	o := &observation{seam: &Seam{emit: func(Event) {}}, key: "obs-1", hash: block.Hash(), reader: stoppedReader()}
	if err := o.Validated(block, nil, nil); !errors.Is(err, ErrUndeclaredHeader) {
		t.Fatalf("validated: %v", err)
	}
	if err := o.End(nil); !errors.Is(err, ErrUndeclaredHeader) {
		t.Fatalf("end after a failed validation: %v", err)
	}
	other := &observation{seam: &Seam{emit: func(Event) {}}, key: "obs-2", hash: block.Hash(), reader: &Reader{}}
	if err := other.Validated(types.NewBlockWithHeader(&types.Header{Number: big.NewInt(2)}), nil, nil); !errors.Is(err, ErrWiring) {
		t.Fatalf("validation of another block: %v", err)
	}
}

func TestUnitRejectedWithAWiringErrorStaysAdmitted(t *testing.T) {
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	o := &observation{seam: &Seam{emit: func(Event) {}}, key: "obs-1", hash: block.Hash(), reader: stoppedReader()}
	o.Rejected(block, nil, errors.New("unauthorized validator"))
	if o.resolved != "" || !errors.Is(o.End(nil), ErrUndeclaredHeader) {
		t.Fatalf("a rejection after a wiring error was trusted: %q", o.resolved)
	}
	// Input failures marked by the patches are not authoritative either.
	for _, cause := range []error{
		fmt.Errorf("invalid merkle root (%w: missing trie node)", core.ErrExtractorParentState),
		fmt.Errorf("verify validators: %w", parlia.ErrExtractorSystemCall),
	} {
		o := &observation{seam: &Seam{emit: func(Event) {}}, key: "obs-1", hash: block.Hash(), reader: &Reader{}}
		o.Rejected(block, nil, cause)
		if o.resolved != "" || !errors.Is(o.End(nil), cause) {
			t.Fatalf("an input failure was trusted: %v", cause)
		}
	}
}

func TestUnitEndOfAnUnresolvedObservation(t *testing.T) {
	o := &observation{seam: &Seam{emit: func(Event) {}}, key: "obs-1", reader: &Reader{}}
	if err := o.End(nil); !errors.Is(err, ErrUnresolved) {
		t.Fatalf("end without an exit: %v", err)
	}
	done := &observation{seam: &Seam{emit: func(Event) {}}, key: "obs-2", resolved: admission.ExecReady, reader: &Reader{}}
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

func TestUnitStaticWindow(t *testing.T) {
	genesis, _ := unitGenesis(t)
	headers := unitChain(genesis.Block.Header(), 1100)
	for n, want := range map[int]int{2: 1, 100: 99, 257: 255, 1100: 255} {
		static, err := DecodeStatic(unitItems(t, genesis, headers, n), genesis, "commit")
		if err != nil || len(static.Ancestors) != want {
			t.Fatalf("window of %d: %d ancestors, %v", n, len(static.Ancestors), err)
		}
	}
	items := unitItems(t, genesis, headers, 1100)
	static, err := DecodeStatic(items, genesis, "commit")
	if err != nil || len(static.Ancestors) != 255 || static.Ancestors[254].Number.Uint64() != 1100-256 {
		t.Fatalf("window of 1100: %d ancestors, %v", len(static.Ancestors), err)
	}
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
	changed["parlia-snapshots"] = []byte(SnapshotTag + ";all")
	if _, err := DecodeStatic(changed, genesis, "commit"); err == nil || !strings.Contains(err.Error(), "parlia-snapshots") {
		t.Fatalf("another snapshot declaration accepted: %v", err)
	}
}
