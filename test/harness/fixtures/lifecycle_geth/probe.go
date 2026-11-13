// Command extractor-lifecycle-geth is the measured probe of the Geth block of
// E01-02.lifecycle_seams. The preparation installs it as
// cmd/extractor-lifecycle-geth/main.go in the patched Geth tree. It writes one
// JSON report and no expected value; test/harness/fixtures/lifecycle_geth/
// lifecycle_geth.py judges the report.
//
// Usage:
//
//	extractor-lifecycle-geth run GENESIS WORKDIR REPORT
//	extractor-lifecycle-geth crash GENESIS CONFIG DATADIR SCENARIO   (child process)
package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/catalyst"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/syncer"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	extractorgeth "github.com/ethereum/go-ethereum/extractor/admission/native"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
)

const gwei = 1_000_000_000

var (
	key1, _  = crypto.ToECDSA(common.LeftPadBytes([]byte{1}, 32))
	coinbase = common.HexToAddress("0x2000000000000000000000000000000000000000")
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func errText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func hexHash(h common.Hash) string { return hex.EncodeToString(h[:]) }

// ---------------------------------------------------------------- chains

// blocks are the generated fixture chains. A1..A6 extend genesis; the
// branches B, C, S, T (two blocks each) and G (one block) fork from A2, each
// with its own recipient, so every state root differs.
type blocks struct {
	genesis *core.Genesis
	a       []*types.Block // a[i] is A(i+1)
	fork    map[string][]*types.Block
}

func transfer(config *params.ChainConfig, key *ecdsa.PrivateKey, nonce uint64, to common.Address) *types.Transaction {
	tx, err := types.SignTx(types.NewTx(&types.LegacyTx{Nonce: nonce, To: &to, Value: big.NewInt(int64(1000 + nonce)),
		Gas: 21_000, GasPrice: big.NewInt(2 * gwei)}), types.LatestSigner(config), key)
	check(err)
	return tx
}

func generate(genesis *core.Genesis) *blocks {
	engine := beacon.New(ethash.NewFaker())
	db, a, _ := core.GenerateChainWithGenesis(genesis, engine, 6, func(i int, g *core.BlockGen) {
		g.SetCoinbase(coinbase)
		g.AddTx(transfer(genesis.Config, key1, uint64(i), common.HexToAddress("0x4000000000000000000000000000000000000001")))
	})
	out := &blocks{genesis: genesis, a: a, fork: map[string][]*types.Block{}}
	// name: parent A number, count, recipient byte.
	for _, f := range []struct {
		name   string
		parent int
		count  int
		to     byte
	}{{"B", 2, 2, 0x0b}, {"C", 2, 2, 0x0c}, {"S", 2, 2, 0x05}, {"T", 2, 2, 0x06}, {"G", 2, 1, 0x07}} {
		parent := a[f.parent-1]
		chain, _ := core.GenerateChain(genesis.Config, parent, engine, db, f.count, func(i int, g *core.BlockGen) {
			g.SetCoinbase(coinbase)
			g.AddTx(transfer(genesis.Config, key1, uint64(f.parent+i), common.BytesToAddress([]byte{0x40, f.to})))
		})
		out.fork[f.name] = chain
	}
	return out
}

func (b *blocks) named(name string) *types.Block {
	if name[0] == 'A' {
		return b.a[name[1]-'1']
	}
	return b.fork[name[:1]][int(name[1]-'0')-b.forkBase(name[:1])-1]
}

// forkBase is the A number that a fork branch extends.
func (b *blocks) forkBase(branch string) int {
	return int(b.fork[branch][0].NumberU64()) - 1
}

// ---------------------------------------------------------------- stores

type config struct {
	StoreID     string      `json:"store_id"`
	ProducerID  string      `json:"producer_id"`
	ChainID     string      `json:"chain_id"`
	GenesisHash common.Hash `json:"genesis_hash"`
	Commit      string      `json:"native_commit"`
	Profile     string      `json:"normalization_profile"`
}

func writeConfig(path string, genesis *core.Genesis, storeID, commit string) {
	cfg := config{StoreID: storeID, ProducerID: "geth-lifecycle", ChainID: genesis.Config.ChainID.String(),
		GenesisHash: genesis.ToBlock().Hash(), Commit: commit, Profile: "e01-02-lifecycle-fixture"}
	data, err := json.Marshal(cfg)
	check(err)
	check(os.WriteFile(path, data, 0o600))
}

// nodeStore is one shipped Geth node (eth.New with the capture config) in
// this process: the native caller path.
type nodeStore struct {
	dir   string
	stack *node.Node
	eth   *eth.Ethereum
	api   *catalyst.ConsensusAPI
	sync  *syncer.Syncer
}

func openNode(dir string, genesis *core.Genesis, mode ethconfig.SyncMode) (*nodeStore, error) {
	stack, err := node.New(&node.Config{Name: "geth", DataDir: dir, DBEngine: "pebble",
		P2P: p2p.Config{MaxPeers: 0, NoDiscovery: true, ListenAddr: ""}})
	if err != nil {
		return nil, err
	}
	cfg := ethconfig.Defaults
	cfg.Genesis = genesis
	cfg.NetworkId = genesis.Config.ChainID.Uint64()
	cfg.SyncMode = mode
	cfg.StateScheme = rawdb.HashScheme
	cfg.SnapshotCache = 0
	cfg.LogNoHistory = true
	backend, err := eth.New(stack, &cfg)
	if err != nil {
		stack.Close()
		return nil, err
	}
	s := &nodeStore{dir: dir, stack: stack, eth: backend}
	if s.sync, err = syncer.Register(stack, backend, syncer.Config{}); err != nil {
		stack.Close()
		return nil, err
	}
	if err := stack.Start(); err != nil {
		stack.Close()
		return nil, err
	}
	s.api = catalyst.NewConsensusAPI(backend)
	return s, nil
}

func (s *nodeStore) chain() *core.BlockChain { return s.eth.BlockChain() }

func (s *nodeStore) lifecycle() *extractorgeth.Lifecycle { return extractorgeth.LifecycleOf(s.chain()) }

func (s *nodeStore) close() { check(s.stack.Close()) }

// driver is one way to reach the native callers: the Engine API of the node
// (native caller path) or direct calls of the BlockChain methods that the
// Engine API calls (harness path).
type driver interface {
	newPayload(block *types.Block) error
	forkchoice(head, safe, finalized common.Hash) error
}

type engineDriver struct{ s *nodeStore }

func (d engineDriver) newPayload(block *types.Block) error {
	payload := engine.BlockToExecutableData(block, new(big.Int), nil, nil).ExecutionPayload
	status, err := d.s.api.NewPayloadV1(context.Background(), *payload)
	if err != nil {
		return err
	}
	if status.Status != engine.VALID {
		return fmt.Errorf("newPayload %d: %s %v", block.NumberU64(), status.Status, status.ValidationError)
	}
	return nil
}

func (d engineDriver) forkchoice(head, safe, finalized common.Hash) error {
	resp, err := d.s.api.ForkchoiceUpdatedV1(context.Background(),
		engine.ForkchoiceStateV1{HeadBlockHash: head, SafeBlockHash: safe, FinalizedBlockHash: finalized}, nil)
	if err != nil {
		return err
	}
	if resp.PayloadStatus.Status != engine.VALID {
		return fmt.Errorf("forkchoice: %s %v", resp.PayloadStatus.Status, resp.PayloadStatus.ValidationError)
	}
	return nil
}

type harnessDriver struct{ s *nodeStore }

func (d harnessDriver) newPayload(block *types.Block) error {
	if d.s.chain().HasBlockAndState(block.Hash(), block.NumberU64()) {
		return nil
	}
	_, err := d.s.chain().InsertBlockWithoutSetHead(context.Background(), block, false)
	return err
}

func (d harnessDriver) forkchoice(head, safe, finalized common.Hash) error {
	chain := d.s.chain()
	block := chain.GetBlockByHash(head)
	if block == nil {
		return errors.New("Invalid forkchoice state")
	}
	if chain.GetCanonicalHash(block.NumberU64()) != head {
		if _, err := chain.SetCanonical(block); err != nil {
			return err
		}
	}
	for _, ref := range []struct {
		hash common.Hash
		set  func(*types.Header)
	}{{finalized, chain.SetFinalized}, {safe, chain.SetSafe}} {
		if ref.hash == (common.Hash{}) {
			continue
		}
		header := chain.GetHeaderByHash(ref.hash)
		if header == nil || chain.GetCanonicalHash(header.Number.Uint64()) != ref.hash {
			// The Engine API answers an unknown or non-canonical safe or
			// finalized block with this error, after the setters before it.
			return errors.New("Invalid forkchoice state")
		}
		ref.set(header)
	}
	return nil
}

// ---------------------------------------------------------------- records

type decisionRow struct {
	Sequence uint64   `json:"sequence"`
	Kind     string   `json:"kind"`
	OldHead  uint64   `json:"old_head"`
	NewHead  uint64   `json:"new_head"`
	OldHash  string   `json:"old_hash"`
	NewHash  string   `json:"new_hash"`
	Members  []string `json:"members"`
	Evidence string   `json:"evidence"`
	State    string   `json:"state"`
}

// readDecisions reads every decision record of the store, keyed by
// sequence.
func readDecisions(db ethdb.KeyValueStore) map[uint64]decisionRow {
	out := map[uint64]decisionRow{}
	it := db.NewIterator([]byte("extractor-decision-v1/"), nil)
	defer it.Release()
	for it.Next() {
		parts := strings.Split(string(it.Key()), "/")
		if len(parts) != 4 {
			continue
		}
		var seq uint64
		if _, err := fmt.Sscanf(parts[2], "%016x", &seq); err != nil {
			continue
		}
		row := out[seq]
		row.Sequence = seq
		switch parts[3] {
		case "intent":
			var rec decision.IntentRecord
			check(json.Unmarshal(it.Value(), &rec))
			row.Kind, row.OldHead, row.NewHead = string(rec.Kind), rec.OldHead.Number, rec.NewHead.Number
			row.OldHash, row.NewHash, row.Evidence = rec.OldHead.Hash, rec.NewHead.Hash, rec.EvidenceDigest
			row.Members = []string{}
			for _, m := range rec.Members {
				row.Members = append(row.Members, m.Block)
			}
			if row.State == "" {
				row.State = "INTENT"
			}
		case "decided":
			row.State = "NATIVE_DECIDED"
		case "rejected":
			row.State = "NATIVE_REJECTED"
		}
		out[seq] = row
	}
	return out
}

// countRecords counts the per-sequence records (prefix/run/sequence/kind)
// under prefix: observations of the admission, decisions of the bridge.
func countRecords(db ethdb.KeyValueStore, prefix string) int {
	it := db.NewIterator([]byte(prefix), nil)
	defer it.Release()
	n := 0
	for it.Next() {
		if len(strings.Split(string(it.Key()), "/")) == 4 {
			n++
		}
	}
	return n
}

type refs struct {
	Head      uint64 `json:"head"`
	HeadHash  string `json:"head_hash"`
	HeadRoot  string `json:"head_root"`
	Finalized string `json:"finalized"`
	Safe      string `json:"safe"`
	StoredFin string `json:"stored_finalized"`
	// Canonical names the canonical hash of numbers 1 to 6 ("" when none).
	Canonical []string `json:"canonical"`
}

func readRefs(chain *core.BlockChain, db ethdb.Reader) refs {
	head := chain.CurrentBlock()
	r := refs{Head: head.Number.Uint64(), HeadHash: hexHash(head.Hash()), HeadRoot: hexHash(head.Root),
		StoredFin: hexHash(rawdb.ReadFinalizedBlockHash(db))}
	if f := chain.CurrentFinalBlock(); f != nil {
		r.Finalized = hexHash(f.Hash())
	}
	if s := chain.CurrentSafeBlock(); s != nil {
		r.Safe = hexHash(s.Hash())
	}
	for n := uint64(1); n <= 6; n++ {
		hash := rawdb.ReadCanonicalHash(db, n)
		if hash == (common.Hash{}) {
			r.Canonical = append(r.Canonical, "")
		} else {
			r.Canonical = append(r.Canonical, hexHash(hash))
		}
	}
	return r
}

// step is one scenario step: the call, its result, the native references
// after it and the decisions it added or settled.
type step struct {
	Name      string        `json:"name"`
	Error     any           `json:"error"`
	Refs      refs          `json:"refs"`
	Decisions []decisionRow `json:"decisions"`
	Phase     string        `json:"phase"`
}

type recorder struct {
	s     *nodeStore
	names map[string]string // block hash -> fixture name
	steps []step
	seen  map[uint64]string
}

func newRecorder(s *nodeStore, b *blocks) *recorder {
	r := &recorder{s: s, names: map[string]string{}, seen: map[uint64]string{}}
	for i, block := range b.a {
		r.names[hexHash(block.Hash())] = fmt.Sprintf("A%d", i+1)
	}
	for name, chain := range b.fork {
		for _, block := range chain {
			r.names[hexHash(block.Hash())] = fmt.Sprintf("%s%d", name, block.NumberU64())
		}
	}
	r.names[hexHash(b.genesis.ToBlock().Hash())] = "G0"
	r.names[strings.Repeat("0", 64)] = "-"
	return r
}

func (r *recorder) name(hash string) string {
	if n, ok := r.names[hash]; ok {
		return n
	}
	return hash
}

func (r *recorder) record(name string, err error) step {
	db := r.s.eth.ChainDb()
	st := step{Name: name, Error: errText(err), Refs: readRefs(r.s.chain(), db), Decisions: []decisionRow{}}
	if lc := r.s.lifecycle(); lc != nil {
		st.Phase = string(lc.Gate().Phase())
	}
	rows := readDecisions(db)
	seqs := make([]uint64, 0, len(rows))
	for seq := range rows {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for _, seq := range seqs {
		row := rows[seq]
		if r.seen[seq] == row.State {
			continue
		}
		r.seen[seq] = row.State
		row.OldHash, row.NewHash = r.name(row.OldHash), r.name(row.NewHash)
		for i, m := range row.Members {
			row.Members[i] = r.name(m)
		}
		st.Decisions = append(st.Decisions, row)
	}
	for _, field := range []*string{&st.Refs.HeadHash, &st.Refs.Finalized, &st.Refs.Safe, &st.Refs.StoredFin} {
		if *field != "" {
			*field = r.name(*field)
		}
	}
	for i, hash := range st.Refs.Canonical {
		if hash != "" {
			st.Refs.Canonical[i] = r.name(hash)
		}
	}
	r.steps = append(r.steps, st)
	return st
}

func coverage(s *nodeStore) map[string]lifecycle.Reach { return s.lifecycle().Gate().Coverage() }

func addCoverage(total, part map[string]lifecycle.Reach) {
	for id, reach := range part {
		t := total[id]
		t.Passes += reach.Passes
		t.Intents += reach.Intents
		t.Refusals += reach.Refusals
		total[id] = t
	}
}

// waitLive waits until the node's live service switched the gate to LIVE.
func waitLive(s *nodeStore, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if s.lifecycle().Gate().Phase() == lifecycle.Live {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("the gate is %s after %s", s.lifecycle().Gate().Phase(), limit)
}

// ---------------------------------------------------------------- scenario

// bootstrap imports A1..A3 during the initial native sync (BOOTSTRAP) and
// goes live: through the Engine API and the node's live service, or through
// direct calls and GoLive.
func bootstrap(r *recorder, d driver, b *blocks, engineAPI bool) map[string]any {
	out := map[string]any{"phase_at_open": string(r.s.lifecycle().Gate().Phase())}
	for _, name := range []string{"A1", "A2", "A3", "S3", "T3"} {
		r.record("bootstrap newPayload "+name, d.newPayload(b.named(name)))
	}
	r.record("bootstrap forkchoice A3", d.forkchoice(b.named("A3").Hash(), common.Hash{}, common.Hash{}))
	db := r.s.eth.ChainDb()
	out["admission_records_before_live"] = countRecords(db, "extractor-admission-v1/")
	out["decision_records_before_live"] = countRecords(db, "extractor-decision-v1/")
	if engineAPI {
		out["live_service_error"] = errText(waitLive(r.s, 20*time.Second))
	} else {
		_, err := r.s.lifecycle().GoLive()
		out["golive_error"] = errText(err)
	}
	boundary, ok := r.s.lifecycle().Gate().Boundary()
	out["boundary_present"] = ok
	out["boundary"] = map[string]any{"head": boundary.Head.Number, "hash": r.name(boundary.Head.Hash),
		"root": boundary.Root}
	out["boundary_root_is_head_root"] = boundary.Root == hexHash(b.named("A3").Root())
	durable, err := extractorgeth.Roots{DB: db}.Durable(boundary.Root)
	out["boundary_root_durable"] = durable && err == nil
	out["phase_after"] = string(r.s.lifecycle().Gate().Phase())
	return out
}

// shared is the LIVE scenario that AC-CAP-01-01 runs through both paths.
func shared(r *recorder, d driver, b *blocks) {
	h := func(name string) common.Hash { return b.named(name).Hash() }
	zero := common.Hash{}
	r.record("newPayload A4", d.newPayload(b.named("A4")))
	r.record("forkchoice head A4", d.forkchoice(h("A4"), zero, zero))
	r.record("forkchoice safe A2 finalized A1", d.forkchoice(h("A4"), h("A2"), h("A1")))
	r.record("AC-LIF-05-02 forkchoice zero safe and finalized", d.forkchoice(h("A4"), zero, zero))
	r.record("forkchoice unchanged safe A2 finalized A1", d.forkchoice(h("A4"), h("A2"), h("A1")))
	unknown := common.HexToHash("0x00000000000000000000000000000000000000000000000000000000000000ff")
	r.record("AC-LIF-05-03 forkchoice finalized A2 then unknown safe", d.forkchoice(h("A4"), unknown, h("A2")))
	r.record("newPayload B3", d.newPayload(b.named("B3")))
	r.record("newPayload B4", d.newPayload(b.named("B4")))
	r.record("forkchoice reorg to B4 safe B3", d.forkchoice(h("B4"), h("B3"), h("A2")))
	r.record("forkchoice back to A4 without corpus", d.forkchoice(h("A4"), zero, zero))
}

type pathSummary struct {
	Decisions [][]any `json:"decisions"`
	Final     refs    `json:"final"`
	Errors    []bool  `json:"errors"`
}

// summary is what AC-CAP-01-01 compares: per step, the decisions (kind, old
// and new head, member blocks, state) and whether the step failed, and the
// native result at the end. Evidence digests name observation keys, which
// differ between stores, so they are not compared.
func summary(steps []step) pathSummary {
	out := pathSummary{Decisions: [][]any{}, Errors: []bool{}}
	for _, st := range steps {
		rows := []any{}
		for _, row := range st.Decisions {
			rows = append(rows, []any{row.Kind, row.OldHash, row.NewHash, row.Members, row.State})
		}
		out.Decisions = append(out.Decisions, []any{st.Name, rows})
		out.Errors = append(out.Errors, st.Error != nil)
		out.Final = st.Refs
	}
	return out
}

// deadlockSmoke runs the guarded callers concurrently on a live node; every
// call must return within the limit (guard, then chain mutex, in every
// caller).
func deadlockSmoke(s *nodeStore, b *blocks, head *types.Block) map[string]any {
	d := engineDriver{s}
	chain := s.chain()
	safe, final := chain.CurrentSafeBlock(), chain.CurrentFinalBlock()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	work := []func() error{
		func() error { return d.forkchoice(head.Hash(), safe.Hash(), final.Hash()) },
		func() error { _, err := chain.InsertChain(types.Blocks{head}); return err },
		func() error { chain.SetSafe(safe); return nil },
		func() error { chain.SetFinalized(final); return nil },
		func() error { _, err := chain.SetCanonical(head); return err },
	}
	start := time.Now()
	for _, w := range work {
		wg.Add(1)
		go func(w func() error) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if err := w(); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	out := map[string]any{"callers": len(work), "iterations": 10}
	select {
	case <-done:
		out["completed"] = true
	case <-time.After(60 * time.Second):
		out["completed"] = false
	}
	out["seconds"] = time.Since(start).Seconds()
	close(errs)
	list := []string{}
	for err := range errs {
		list = append(list, err.Error())
	}
	out["errors"] = list
	return out
}

// ---------------------------------------------------------------- crash scenarios

func openStore(dir string) ethdb.Database {
	chaindata := filepath.Join(dir, "chaindata")
	kv, err := pebble.New(chaindata, 16, 16, "", false)
	check(err)
	db, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: filepath.Join(chaindata, "ancient")})
	check(err)
	return db
}

func directChain(db ethdb.Database, genesis *core.Genesis) *core.BlockChain {
	cfg := core.DefaultConfig()
	cfg.SnapshotLimit = 0
	chain, err := core.NewBlockChain(db, genesis, beacon.New(ethash.NewFaker()), cfg)
	check(err)
	return chain
}

func loadConfig(path string) extractorgeth.Config {
	cfg, err := extractorgeth.LoadConfig(path)
	check(err)
	return cfg
}

// crashChild runs one crash scenario on a direct chain with the shipped
// attach and a crash point; it exits 3 at the point.
func crashChild(genesisPath, configPath, dir, scenario string) {
	genesis := loadGenesis(genesisPath)
	b := generate(genesis)
	db := openStore(dir)
	chain := directChain(db, genesis)
	want := map[string]string{"finality": "intended", "rewind": "rewound", "canonical": "intended"}[scenario]
	armed := false
	l, err := extractorgeth.AttachLifecycle(chain, db, loadConfig(configPath), extractorgeth.Options{},
		extractorgeth.DecisionOptions{}, extractorgeth.LifecycleOptions{Point: func(name string) {
			if armed && name == want {
				os.Exit(3)
			}
		}})
	check(err)
	switch scenario {
	case "finality":
		_, err = chain.InsertChain(types.Blocks{b.named("A1"), b.named("A2"), b.named("A3")})
		check(err)
		_, err = l.GoLive()
		check(err)
		armed = true
		chain.SetFinalized(b.named("A2").Header())
	case "rewind":
		_, err = chain.InsertChain(types.Blocks{b.named("A4"), b.named("A5")})
		check(err)
		armed = true
		check(chain.SetHead(4))
	case "canonical":
		_, err = chain.InsertBlockWithoutSetHead(context.Background(), b.named("A5"), false)
		check(err)
		armed = true
		_, err = chain.SetCanonical(b.named("A5"))
		check(err)
	}
	os.Exit(0)
}

// inspect reopens the store of a crash scenario with the shipped attach (no
// crash point) and reports how recovery settled the decisions.
func inspect(genesis *core.Genesis, configPath, dir string, b *blocks) map[string]any {
	db := openStore(dir)
	chain := directChain(db, genesis)
	// A settled intent that no retained block holds is pruned by the
	// cleanup, so the settlement is read from the recovery trace.
	settled := []map[string]any{}
	trace := func(event string, fields map[string]any) {
		if event == "settling" || event == "lifecycle_decided" || event == "lifecycle_rejected" {
			row := map[string]any{"event": event}
			for k, v := range fields {
				row[k] = v
			}
			settled = append(settled, row)
		}
	}
	_, attachErr := extractorgeth.AttachLifecycle(chain, db, loadConfig(configPath), extractorgeth.Options{Trace: trace},
		extractorgeth.DecisionOptions{}, extractorgeth.LifecycleOptions{})
	r := &recorder{names: map[string]string{}}
	for i, block := range b.a {
		r.names[hexHash(block.Hash())] = fmt.Sprintf("A%d", i+1)
	}
	r.names[strings.Repeat("0", 64)] = "-"
	rows := readDecisions(db)
	list := []decisionRow{}
	for _, row := range rows {
		row.OldHash, row.NewHash = r.name(row.OldHash), r.name(row.NewHash)
		for i, m := range row.Members {
			row.Members[i] = r.name(m)
		}
		list = append(list, row)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Sequence < list[j].Sequence })
	ref := readRefs(chain, db)
	ref.HeadHash = r.name(ref.HeadHash)
	ref.Finalized = r.name(ref.Finalized)
	ref.StoredFin = r.name(ref.StoredFin)
	chain.Stop()
	check(db.Close())
	return map[string]any{"attach_error": errText(attachErr), "decisions": list, "refs": ref, "settled": settled}
}

func crashes(genesisPath string, genesis *core.Genesis, b *blocks, work string) []map[string]any {
	dir := filepath.Join(work, "crash")
	check(os.MkdirAll(dir, 0o700))
	configPath := filepath.Join(work, "crash-config.json")
	commit, err := extractorgeth.NativeIdentity()
	check(err)
	writeConfig(configPath, genesis, "cccccccccccccccccccccccccccccccc", commit)
	out := []map[string]any{}
	for _, scenario := range []string{"finality", "rewind", "canonical"} {
		cmd := exec.Command(os.Args[0], "crash", genesisPath, configPath, dir, scenario)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		err := cmd.Run()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			check(err)
		}
		row := map[string]any{"scenario": scenario, "exit_code": code}
		for k, v := range inspect(genesis, configPath, dir, b) {
			row[k] = v
		}
		out = append(out, row)
	}
	return out
}

// ---------------------------------------------------------------- run

func loadGenesis(path string) *core.Genesis {
	data, err := os.ReadFile(path)
	check(err)
	genesis := new(core.Genesis)
	check(json.Unmarshal(data, genesis))
	return genesis
}

func run(genesisPath, work, reportPath string) {
	started := time.Now()
	check(os.MkdirAll(work, 0o700))
	genesis := loadGenesis(genesisPath)
	b := generate(genesis)
	commit, err := extractorgeth.NativeIdentity()
	report := map[string]any{"schema_version": 1, "kind": "lifecycle-geth-probe", "native_identity": commit,
		"native_identity_error": errText(err)}
	check(err)

	// Profile and identity refusals outside the gate.
	profile := map[string]any{}
	badConfig := filepath.Join(work, "config-wrong-commit.json")
	writeConfig(badConfig, genesis, "dddddddddddddddddddddddddddddddd", strings.Repeat("0", 40))
	goodConfig := filepath.Join(work, "config.json")
	writeConfig(goodConfig, genesis, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", commit)
	check(os.Setenv(extractorgeth.ConfigEnv, goodConfig))
	profile["flags_dev"] = errText(extractorgeth.CheckFlags(true, false, "full"))
	profile["flags_synctarget"] = errText(extractorgeth.CheckFlags(false, true, "full"))
	profile["flags_snap"] = errText(extractorgeth.CheckFlags(false, false, "snap"))
	profile["flags_full"] = errText(extractorgeth.CheckFlags(false, false, "full"))
	_, snapErr := openNode(filepath.Join(work, "snap"), genesis, ethconfig.SnapSync)
	profile["eth_new_snap_sync"] = errText(snapErr)
	check(os.Setenv(extractorgeth.ConfigEnv, badConfig))
	_, idErr := openNode(filepath.Join(work, "wrong-commit"), genesis, ethconfig.FullSync)
	profile["eth_new_wrong_commit"] = errText(idErr)
	report["profile"] = profile

	// Native caller path: the shipped node and the Engine API.
	total := map[string]lifecycle.Reach{}
	check(os.Setenv(extractorgeth.ConfigEnv, goodConfig))
	s1, err := openNode(filepath.Join(work, "native"), genesis, ethconfig.FullSync)
	check(err)
	r1 := newRecorder(s1, b)
	native := map[string]any{"bootstrap": bootstrap(r1, engineDriver{s1}, b, true)}
	sharedFrom := len(r1.steps)
	shared(r1, engineDriver{s1}, b)
	nativeShared := summary(r1.steps[sharedFrom:])

	// The rest of the caller map on the native store.
	d := engineDriver{s1}
	chain := s1.chain()
	h := func(name string) common.Hash { return b.named(name).Hash() }
	r1.record("newPayload C3", d.newPayload(b.named("C3")))
	r1.record("newPayload C4", d.newPayload(b.named("C4")))
	_, err = chain.InsertChain(types.Blocks{b.named("C3"), b.named("C4")})
	r1.record("InsertChain known C3 C4", err)
	_, err = chain.InsertReceiptChain(types.Blocks{}, nil, 0)
	r1.record("InsertReceiptChain (snap sync)", err)
	r1.record("syncer Sync (sync target)", s1.sync.Sync(h("C4")))
	_, err = catalyst.NewSimulatedBeacon(0, coinbase, s1.eth)
	r1.record("NewSimulatedBeacon (developer beacon)", err)
	r1.record("Reset", chain.Reset())
	r1.record("forkchoice safe C4 finalized A2", d.forkchoice(h("C4"), h("C4"), h("A2")))
	native["deadlock"] = deadlockSmoke(s1, b, b.named("C4"))
	r1.record("after deadlock smoke", nil)
	addCoverage(total, coverage(s1))
	native["exit_errors_first_open"] = s1.lifecycle().ExitErrors()
	s1.close()

	// Restart: RECOVERY, startup reconciliation, live at the stored H.
	s1, err = openNode(filepath.Join(work, "native"), genesis, ethconfig.FullSync)
	check(err)
	r1.s = s1
	d = engineDriver{s1}
	chain = s1.chain()
	r1.record("restart", nil)
	boundary, _ := s1.lifecycle().Gate().Boundary()
	native["restart_boundary"] = map[string]any{"head": boundary.Head.Number, "hash": r1.name(boundary.Head.Hash)}
	// The BOOTSTRAP side blocks S3 and T3 were never pinned (no admitted
	// block extends them) nor flushed, so the restart dropped their state;
	// A2 and A3 keep theirs as pinned parents.
	stateAfter := map[string]bool{}
	for _, name := range []string{"A1", "A2", "A3", "S3", "T3"} {
		stateAfter[name] = chain.HasState(b.named(name).Root())
	}
	native["state_after_restart"] = stateAfter
	_, err = chain.InsertChain(types.Blocks{b.named("T4")})
	r1.record("InsertChain T4 on pruned T3 (side chain)", err)
	_, err = chain.InsertBlockWithoutSetHead(context.Background(), b.named("S4"), false)
	r1.record("InsertBlockWithoutSetHead S4 on pruned S3 (recover ancestors)", err)
	r1.record("forkchoice head S4", d.forkchoice(h("S4"), common.Hash{}, common.Hash{}))
	r1.record("forkchoice safe S4 finalized A2", d.forkchoice(h("S4"), h("S4"), h("A2")))
	r1.record("SetHead 3", chain.SetHead(3))
	r1.record("newPayload G3 (retained side block)", d.newPayload(b.named("G3")))
	addCoverage(total, coverage(s1))
	native["exit_errors_restart"] = s1.lifecycle().ExitErrors()
	s1.close()

	// The manual database commands run in their own process without a gate;
	// their guard refuses this store while a pin is live.
	db := openStore(filepath.Join(work, "native", "geth"))
	manualErr := extractorgeth.RequireNoOpenCapture(db)
	check(db.Close())
	outside := map[string]any{"geth.manual-commands": errText(manualErr),
		"geth.dev-sealing": profile["flags_dev"], "geth.sync-target": profile["flags_synctarget"],
		"geth.snap-sync": profile["eth_new_snap_sync"]}
	for id, refusal := range outside {
		if refusal != nil {
			t := total[id]
			t.Refusals++
			total[id] = t
		}
	}
	native["outside_gate_refusals"] = outside
	native["steps"] = r1.steps
	report["native_caller"] = native

	// Harness path: the same LIVE scenario through direct calls.
	s2, err := openNode(filepath.Join(work, "harness"), genesis, ethconfig.FullSync)
	check(err)
	r2 := newRecorder(s2, b)
	harness := map[string]any{"bootstrap": bootstrap(r2, harnessDriver{s2}, b, false)}
	sharedFrom = len(r2.steps)
	shared(r2, harnessDriver{s2}, b)
	harness["steps"] = r2.steps
	harnessShared := summary(r2.steps[sharedFrom:])
	s2.close()
	report["harness"] = harness
	report["ac_cap_01_01"] = map[string]any{"native": nativeShared, "harness": harnessShared}

	// Coverage of the native store and the gaps of AC-CAP-01-01.
	m, err := lifecycle.Callers()
	check(err)
	cov := map[string]any{}
	for id, reach := range total {
		cov[id] = map[string]uint64{"passes": reach.Passes, "intents": reach.Intents, "refusals": reach.Refusals}
	}
	report["coverage"] = cov
	report["gaps"] = lifecycle.Gaps(m, "geth", total)
	routes := map[string]string{}
	for _, c := range m.Callers {
		if c.Client == "geth" {
			routes[c.ID] = string(c.Route)
		}
	}
	report["routes"] = routes

	report["crashes"] = crashes(genesisPath, genesis, b, work)
	report["seconds"] = time.Since(started).Seconds()
	data, err := json.MarshalIndent(report, "", " ")
	check(err)
	check(os.WriteFile(reportPath, append(data, '\n'), 0o644))
}

func main() {
	if len(os.Args) == 5 && os.Args[1] == "run" {
		run(os.Args[2], os.Args[3], os.Args[4])
		return
	}
	if len(os.Args) == 6 && os.Args[1] == "crash" {
		crashChild(os.Args[2], os.Args[3], os.Args[4], os.Args[5])
		return
	}
	fmt.Fprintln(os.Stderr, "usage: extractor-lifecycle-geth run GENESIS WORKDIR REPORT")
	os.Exit(2)
}
