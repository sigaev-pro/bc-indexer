package bscdecision

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	"github.com/ethereum/go-ethereum/extractor/decision"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestUnitRoots(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	roots := NewRoots(db)
	root := common.HexToHash("0x01")
	other := common.HexToHash("0x02")
	rawdb.WriteLegacyTrieNode(db, other, []byte{0xc0})
	for _, c := range []struct {
		name string
		hash common.Hash
		want bool
	}{{"neither synced nor on disk", root, false}, {"on disk, not synced", other, false}} {
		if got, err := roots.Durable(hex.EncodeToString(c.hash.Bytes())); err != nil || got != c.want {
			t.Fatalf("%s: %v %v", c.name, got, err)
		}
	}
	roots.Synced(root)
	if got, _ := roots.Durable(hex.EncodeToString(root.Bytes())); got {
		t.Fatal("a synced root without its node is durable")
	}
	rawdb.WriteLegacyTrieNode(db, root, []byte{0xc0})
	if got, _ := roots.Durable(hex.EncodeToString(root.Bytes())); !got {
		t.Fatal("a synced root on disk is not durable")
	}
	for _, bad := range []string{"zz", "01", hex.EncodeToString(make([]byte, 33))} {
		if _, err := roots.Durable(bad); !errors.Is(err, decision.ErrInvalid) {
			t.Fatalf("root %q: %v", bad, err)
		}
	}
}

// unitHeaders returns the headers 0..n of a linked chain.
func unitHeaders(n int) []*types.Header {
	headers := []*types.Header{{Number: big.NewInt(0), Difficulty: big.NewInt(1), Extra: []byte("genesis")}}
	for i := 1; i <= n; i++ {
		headers = append(headers, &types.Header{ParentHash: headers[i-1].Hash(), Number: big.NewInt(int64(i)),
			Difficulty: big.NewInt(2)})
	}
	return headers
}

// unitStatic is the capsule window of block n: the parent and the 256
// ancestors below it.
func unitStatic(headers []*types.Header, n int) bsc.Static {
	static := bsc.Static{Parent: headers[n-1]}
	for i := n - 2; i >= 0 && i >= n-1-256; i-- {
		static.Ancestors = append(static.Ancestors, headers[i])
	}
	return static
}

// fakeNative is the native chain of the unit tests.
type fakeNative struct {
	headers map[common.Hash]*types.Header
}

func (f fakeNative) Config() *params.ChainConfig                   { return params.TestChainConfig }
func (f fakeNative) CurrentHeader() *types.Header                  { return nil }
func (f fakeNative) GenesisHeader() *types.Header                  { return nil }
func (f fakeNative) GetHeaderByNumber(number uint64) *types.Header { return nil }
func (f fakeNative) GetHeaderByHash(hash common.Hash) *types.Header {
	return f.headers[hash]
}
func (f fakeNative) GetHeader(hash common.Hash, number uint64) *types.Header { return f.headers[hash] }
func (f fakeNative) GetTd(common.Hash, uint64) *big.Int                      { return nil }
func (f fakeNative) GetHighestVerifiedHeader() *types.Header                 { return nil }
func (f fakeNative) GetVerifiedBlockByHash(common.Hash) *types.Header        { return nil }
func (f fakeNative) Engine() consensus.Engine                                { return nil }

func unitReader(n int) (*EvidenceReader, []*types.Header) {
	headers := unitHeaders(n)
	native := fakeNative{headers: map[common.Hash]*types.Header{}}
	for _, header := range headers[:n] {
		native.headers[header.Hash()] = header
	}
	return NewEvidenceReader(native, unitStatic(headers, n), headers[n], headers[0]), headers
}

func TestUnitEvidenceReaderServesOnlyTheWindow(t *testing.T) {
	const n = 300
	reader, headers := unitReader(n)
	// The block, the parent, the window down to n-257 and the genesis.
	for _, i := range []int{n, n - 1, n - 2, n - 257, 0} {
		h := headers[i]
		if reader.GetHeader(h.Hash(), uint64(i)) != h || reader.GetHeaderByHash(h.Hash()) != h ||
			reader.GetHeaderByNumber(uint64(i)) != h {
			t.Fatalf("header %d is not served", i)
		}
	}
	if reader.CurrentHeader() != headers[n-1] || reader.GenesisHeader() != headers[0] || reader.Err() != nil {
		t.Fatal("current or genesis header")
	}
	refusals := []func(r *EvidenceReader){
		func(r *EvidenceReader) { r.GetHeader(headers[n-258].Hash(), uint64(n-258)) },
		func(r *EvidenceReader) { r.GetHeader(headers[n-1].Hash(), uint64(n-2)) },
		func(r *EvidenceReader) { r.GetHeaderByHash(headers[1].Hash()) },
		func(r *EvidenceReader) { r.GetHeaderByNumber(uint64(n - 258)) },
		func(r *EvidenceReader) { r.GetTd(headers[n-1].Hash(), uint64(n-1)) },
		func(r *EvidenceReader) { r.GetHighestVerifiedHeader() },
		func(r *EvidenceReader) { r.GetVerifiedBlockByHash(headers[n-1].Hash()) },
		func(r *EvidenceReader) {
			r.ExtractorSnapshot(uint64(n-258), headers[n-258].Hash(), func(consensus.ChainHeaderReader) ([]byte,
				error) {
				t.Fatal("snapshot below the window computed")
				return nil, nil
			})
		},
	}
	for i, refuse := range refusals {
		fresh, _ := unitReader(n)
		refuse(fresh)
		if err := fresh.Err(); !errors.Is(err, bsc.ErrUndeclaredHeader) && !errors.Is(err, bsc.ErrUndeclaredRead) {
			t.Fatalf("refusal %d: %v", i, err)
		}
	}
}

func TestUnitEvidenceReaderSnapshotSeesTheBlock(t *testing.T) {
	const n = 300
	reader, headers := unitReader(n)
	blob, err := reader.ExtractorSnapshot(uint64(n), headers[n].Hash(), func(native consensus.ChainHeaderReader) (
		[]byte, error) {
		// The native lookup sees the unwritten block and the stored chain.
		if native.GetHeader(headers[n].Hash(), uint64(n)) != headers[n] ||
			native.GetHeaderByHash(headers[n].Hash()) != headers[n] ||
			native.GetHeader(headers[1].Hash(), 1) != headers[1] ||
			native.GetHeaderByHash(headers[1].Hash()) != headers[1] {
			t.Fatal("native lookup headers")
		}
		return []byte("snapshot"), nil
	})
	if err != nil || string(blob) != "snapshot" || reader.Err() != nil {
		t.Fatalf("snapshot of the block: %q %v", blob, err)
	}
}

// fakeFinality reads the justified and finalized blocks like Parlia, from
// the given numbers; a nil number gives an error or no finalized block.
type fakeFinality struct {
	headers   []*types.Header
	justified *int
	finalized *int
	// peek, when set, is a header that GetFinalizedHeader reads first and
	// whose absence it ignores, like the VotePool branch of Parlia.
	peek *int
}

func (f fakeFinality) GetJustifiedNumberAndHash(chain consensus.ChainHeaderReader, _ []*types.Header) (uint64,
	common.Hash, error) {
	if f.justified == nil {
		return 0, common.Hash{}, errors.New("no snapshot")
	}
	h := chain.GetHeaderByNumber(uint64(*f.justified))
	if h == nil {
		return 0, common.Hash{}, errors.New("unknown justified block")
	}
	return h.Number.Uint64(), h.Hash(), nil
}

func (f fakeFinality) GetFinalizedHeader(chain consensus.ChainHeaderReader, _ *types.Header) *types.Header {
	if f.peek != nil {
		chain.GetHeaderByNumber(uint64(*f.peek))
	}
	if f.finalized == nil {
		return nil
	}
	h := f.headers[*f.finalized]
	return chain.GetHeader(h.Hash(), h.Number.Uint64())
}

func ptr(value int) *int { return &value }

// independentDigest is the evidence digest written out from the README.
func independentDigest(kind string, heads ...*types.Header) string {
	data := append([]byte("extractor-bsc-decision-evidence-v1"), 0)
	data = binary.BigEndian.AppendUint64(data, uint64(len(kind)))
	data = append(data, kind...)
	for _, h := range heads {
		data = binary.BigEndian.AppendUint64(data, h.Number.Uint64())
		data = append(data, h.Hash().Bytes()...)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func TestUnitEvaluate(t *testing.T) {
	const n = 300
	reader, headers := unitReader(n)
	engine := fakeFinality{headers: headers, justified: ptr(n - 1), finalized: ptr(n - 2)}
	e, err := Evaluate(engine, reader, headers[n], headers[n-1], true)
	if err != nil {
		t.Fatal(err)
	}
	want := independentDigest("IMPORT", headers[n-1], headers[n], headers[n-1], headers[n-2])
	if e.Kind != decision.Import || e.NewHead != head(headers[n]) || e.Digest != want || e.finalized != headers[n-2] {
		t.Fatalf("IMPORT evidence %+v, want digest %s", e, want)
	}
	reader, _ = unitReader(n)
	side, err := Evaluate(engine, reader, headers[n], headers[n-1], false)
	want = independentDigest("SIDE", headers[n-1], headers[n-1], headers[n-1], headers[n-2])
	if err != nil || side.Kind != decision.Side || side.NewHead != side.OldHead || side.Digest != want {
		t.Fatalf("SIDE evidence %+v %v", side, err)
	}
	// The genesis is a finality result inside the reader (no attestation).
	reader, _ = unitReader(n)
	if e, err := Evaluate(fakeFinality{headers: headers, justified: ptr(0), finalized: ptr(0)}, reader, headers[n],
		headers[n-1], true); err != nil || e.Finalized != head(headers[0]) {
		t.Fatalf("genesis finality %+v %v", e, err)
	}
	// Fail closed: a read below the window, no result, or an error.
	for name, engine := range map[string]fakeFinality{
		"finalized below the window": {headers: headers, justified: ptr(n - 1), finalized: ptr(n - 258)},
		"justified below the window": {headers: headers, justified: ptr(n - 258), finalized: ptr(n - 2)},
		"no finalized block":         {headers: headers, justified: ptr(n - 1)},
		"refused read with a result": {headers: headers, justified: ptr(n - 1), finalized: ptr(n - 2), peek: ptr(n - 258)},
		"justified error":            {headers: headers, finalized: ptr(n - 2)},
	} {
		reader, _ := unitReader(n)
		if _, err := Evaluate(engine, reader, headers[n], headers[n-1], true); !errors.Is(err, ErrFinality) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestUnitSameTransition(t *testing.T) {
	block := hex.EncodeToString(common.HexToHash("0xb1").Bytes())
	h := func(n uint64, b byte) decision.Head {
		return decision.Head{Number: n, Hash: hex.EncodeToString(common.BytesToHash([]byte{b}).Bytes())}
	}
	member := decision.Member{Observation: "obs", ExecReadyDigest: "e", ChildRoot: "r"}
	in := decision.Intent{Kind: decision.Import, OldHead: h(1, 1), NewHead: h(2, 2), Members: []decision.Member{member},
		EvidenceDigest: "d", Events: true}
	saved := decision.IntentRecord{Kind: in.Kind, OldHead: in.OldHead, NewHead: in.NewHead, EvidenceDigest: "d",
		Events: true, Members: []decision.Member{{Observation: "obs", Block: block, ExecReadyDigest: "e", ChildRoot: "r"}}}
	if !sameTransition(saved, in, block) {
		t.Fatal("the same transition differs")
	}
	changes := []func(*decision.IntentRecord){
		func(r *decision.IntentRecord) { r.Kind = decision.Side },
		func(r *decision.IntentRecord) { r.OldHead = h(1, 3) },
		func(r *decision.IntentRecord) { r.NewHead = h(2, 3) },
		func(r *decision.IntentRecord) { r.EvidenceDigest = "x" },
		func(r *decision.IntentRecord) { r.Events = false },
		func(r *decision.IntentRecord) { r.Members = append(r.Members, r.Members[0]) },
		func(r *decision.IntentRecord) {
			r.Members = []decision.Member{{Observation: "other", Block: block, ExecReadyDigest: "e", ChildRoot: "r"}}
		},
		func(r *decision.IntentRecord) {
			r.Members = []decision.Member{{Observation: "obs", Block: "b2", ExecReadyDigest: "e", ChildRoot: "r"}}
		},
		func(r *decision.IntentRecord) {
			r.Members = []decision.Member{{Observation: "obs", Block: block, ExecReadyDigest: "x", ChildRoot: "r"}}
		},
		func(r *decision.IntentRecord) {
			r.Members = []decision.Member{{Observation: "obs", Block: block, ExecReadyDigest: "e", ChildRoot: "x"}}
		},
	}
	for i, change := range changes {
		changed := saved
		changed.Members = append([]decision.Member{}, saved.Members...)
		change(&changed)
		if sameTransition(changed, in, block) {
			t.Fatalf("change %d is the same transition", i)
		}
	}
	twice := in
	twice.Members = []decision.Member{member, member}
	if sameTransition(saved, twice, block) {
		t.Fatal("two proposed members are the same transition")
	}
}

func TestUnitBadBlockEvidence(t *testing.T) {
	block := types.NewBlockWithHeader(unitHeaders(1)[1])
	encoded, err := rlp.EncodeToBytes(block)
	if err != nil {
		t.Fatal(err)
	}
	body := sha256.Sum256(encoded)
	data := append([]byte("extractor-bsc-bad-block-v1\x00"), block.Hash().Bytes()...)
	want := sha256.Sum256(append(data, body[:]...))
	if got, err := BadBlockEvidence(block); err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatalf("bad-block evidence %s %v", got, err)
	}
}

func TestUnitPurger(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	id := "bsc-probe.run-1.0000000000000000"
	if _, err := sourceinputs.SealCapsule(db, id, []sourceinputs.Item{{Name: "block-rlp", Data: []byte{1}}}); err != nil {
		t.Fatal(err)
	}
	if err := (Purger{DB: db}).Purge(id); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceinputs.ReadCapsule(db, id); !errors.Is(err, sourceinputs.ErrNoCapsule) {
		t.Fatalf("capsule after Purge: %v", err)
	}
}

// readOnly is a store without Delete.
type readOnly struct{ sourceinputs.Store }

func TestUnitStartRefusals(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	if _, err := Start(nil, db, nil, Config{}); !errors.Is(err, decision.ErrInvalid) {
		t.Fatalf("no mirror: %v", err)
	}
	cfg := Config{Seam: bsc.Config{Store: readOnly{db}}, Mirror: probeMirror{db: db}}
	if _, err := Start(nil, db, nil, cfg); !errors.Is(err, decision.ErrInvalid) {
		t.Fatalf("store without Delete: %v", err)
	}
}

func TestUnitIntendBeforeTheBridgeOpens(t *testing.T) {
	o := &observation{hook: &Hook{}}
	if _, err := o.Intend(nil, nil, nil, true); !errors.Is(err, ErrDeferred) {
		t.Fatalf("Intend without the bridge: %v", err)
	}
}
