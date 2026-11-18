// This test-only probe runs the E01-04.early_faults catalog on the accepted
// patched Geth tree (E01-02.admission, E01-02.decision_bridge and
// E01-02.lifecycle_seams patch sets, plus the E01-04.early_faults patch set
// that lets ethdb/pebble open on a Pebble in-memory file system).
// Preparation copies it to cmd/extractor-faults-geth.
//
// Each case opens a fresh chaindata store on vfs.NewCrashableMem, wraps the
// key-value store in a fault store, builds a direct chain with the shipped
// lifecycle attach, and runs one scenario on the smoke corpus blocks 1 and 2
// (E01-03.smoke_corpus ETH inputs). The fault store stops the scenario at the
// catalog point by panicking inside the store call, which unwinds the native
// caller without running any further code of the step. The probe abandons the
// node (no BlockChain.Stop), reopens the store on a CrashClone of the file
// system (UnsyncedDataPercent 0 for a power loss, 100 for a crash), reads the
// durable states, lets recovery finish, imports the corpus blocks, re-imports
// them, and exports the native output in the corpus observation format. It
// writes no expected value; faults_judge.py and corpus_compare.py judge it.
//
// Usage:
//
//	extractor-faults-geth run GENESIS WORKDIR REPORT
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/tracers"
	_ "github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	extractorgeth "github.com/ethereum/go-ethereum/extractor/admission/native"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

const (
	producerID = "geth-faults"
	profile    = "test-only-faults-v1"
	gwei       = 1_000_000_000
	memDir     = "/mem/chaindata"

	admissionPrefix = "extractor-admission-v1/"
	decisionPrefix  = "extractor-decision-v1/"
	markerPrefix    = "extractor-decision-marker-v1/"
	lifecyclePrefix = "extractor-lifecycle-v1/"
	pinPrefix       = "extractor-state-provider-pin-v1-"
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

func plainHash(text string) string { return strings.ToLower(strings.TrimPrefix(text, "0x")) }

// ---------------------------------------------------------------- corpus inputs

// The corpus accounts and contracts of test/native_corpus/ORACLE.md, as in
// test/native_corpus/eth/probe.go.
var (
	coinbase  = common.HexToAddress("0x2000000000000000000000000000000000000000")
	parent    = common.HexToAddress("0x3000000000000000000000000000000000000001")
	child     = common.HexToAddress("0x3000000000000000000000000000000000000002")
	recipient = common.HexToAddress("0x4000000000000000000000000000000000000001")
	sender    = common.HexToAddress("0x7e5f4552091a69125d5dfcb7b8c2659029395bdf")
	listed    = []common.Address{coinbase, parent, child, recipient, sender}
	withSlots = map[common.Address]bool{parent: true, child: true}
)

const (
	childCode  = "600160005560c160006000a160ee60005260206000fd"
	parentCode = "6001600055602060006000600034733000000000000000000000000000000000000002617530f1156001553d6002556000516003553460005260a160206000a100"
)

func mustHex(text string) []byte {
	data, err := hex.DecodeString(text)
	check(err)
	return data
}

func corpusInputs(path string) (*core.Genesis, []*types.Block) {
	data, err := os.ReadFile(path)
	check(err)
	genesis := new(core.Genesis)
	check(json.Unmarshal(data, genesis))
	for _, address := range []common.Address{parent, child, recipient} {
		if _, ok := genesis.Alloc[address]; ok {
			panic(fmt.Sprintf("corpus address %s is already in the fixture allocation", address.Hex()))
		}
	}
	if account, ok := genesis.Alloc[sender]; !ok || account.Balance.Cmp(new(big.Int).Mul(big.NewInt(10), big.NewInt(1e18))) != 0 {
		panic("the fixture must hold the sender with 10 ETH")
	}
	genesis.Alloc[parent] = types.Account{Balance: new(big.Int), Code: mustHex(parentCode)}
	genesis.Alloc[child] = types.Account{Balance: new(big.Int), Code: mustHex(childCode)}
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	check(err)
	signer := types.LatestSigner(genesis.Config)
	sign := func(inner types.TxData) *types.Transaction {
		tx, err := types.SignNewTx(key, signer, inner)
		check(err)
		return tx
	}
	price := big.NewInt(2 * gwei)
	ether := big.NewInt(1e18)
	txs := []*types.Transaction{
		sign(&types.LegacyTx{Nonce: 0, To: &recipient, Value: ether, Gas: 21_000, GasPrice: price}),
		sign(&types.DynamicFeeTx{ChainID: genesis.Config.ChainID, Nonce: 1, To: &recipient, Value: big.NewInt(gwei),
			Gas: 21_000, GasFeeCap: big.NewInt(2_200_000_000), GasTipCap: big.NewInt(1_500_000_000)}),
		sign(&types.LegacyTx{Nonce: 2, To: &parent, Value: big.NewInt(5), Gas: 200_000, GasPrice: price}),
		sign(&types.LegacyTx{Nonce: 3, To: &child, Value: big.NewInt(7), Gas: 100_000, GasPrice: price}),
	}
	_, blocks, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 2, func(index int, block *core.BlockGen) {
		block.SetCoinbase(coinbase)
		if index == 0 {
			for _, tx := range txs {
				block.AddTx(tx)
			}
		}
	})
	return genesis, blocks
}

// ---------------------------------------------------------------- fault store

// faultHit is the panic value of a reached fault point.
type faultHit struct{ point string }

// arm describes when the fault store stops a step.
//   - before: after a write whose key matches, the next SyncKeyValue panics
//     without syncing, so that write stays unsynced;
//   - after: after a matching write, the next SyncKeyValue syncs and then
//     panics;
//   - native: after a matching write and its completed sync, the next native
//     (non-extractor) write panics before it is applied.
type arm struct {
	point  string
	match  func(key string, deleted bool) bool
	mode   string // "before", "after" or "native"
	seen   bool
	synced bool
}

// faultStore wraps the native key-value store. It counts the key writes made
// since the last completed sync (an upper bound of what a power loss drops:
// Pebble also syncs the WAL on its own) and fires the armed point.
type faultStore struct {
	ethdb.KeyValueStore
	mu      sync.Mutex
	pending int
	armed   *arm
	hit     string
}

func (s *faultStore) note(key []byte, deleted bool) {
	if s.armed != nil && s.hit == "" && !s.armed.seen && s.armed.match(string(key), deleted) {
		s.armed.seen = true
	}
}

// nativeWrite panics when the armed native point is reached.
func (s *faultStore) nativeWrite(keys []string) {
	if s.armed == nil || s.hit != "" || s.armed.mode != "native" || !s.armed.synced {
		return
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, "extractor-") {
			s.hit = s.armed.point
			panic(faultHit{s.armed.point})
		}
	}
}

func (s *faultStore) record(keys []string, deleted []bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nativeWrite(keys)
	s.pending += len(keys)
	for i, key := range keys {
		s.note([]byte(key), deleted[i])
	}
}

func (s *faultStore) Put(key, value []byte) error {
	s.record([]string{string(key)}, []bool{false})
	return s.KeyValueStore.Put(key, value)
}

func (s *faultStore) Delete(key []byte) error {
	s.record([]string{string(key)}, []bool{true})
	return s.KeyValueStore.Delete(key)
}

func (s *faultStore) SyncKeyValue() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.armed
	if a != nil && s.hit == "" && a.seen && !a.synced {
		switch a.mode {
		case "before":
			s.hit = a.point
			panic(faultHit{a.point})
		case "after":
			if err := s.KeyValueStore.SyncKeyValue(); err != nil {
				return err
			}
			s.pending = 0
			s.hit = a.point
			panic(faultHit{a.point})
		case "native":
			a.synced = true
		}
	}
	if err := s.KeyValueStore.SyncKeyValue(); err != nil {
		return err
	}
	s.pending = 0
	return nil
}

func (s *faultStore) NewBatch() ethdb.Batch {
	return &faultBatch{Batch: s.KeyValueStore.NewBatch(), store: s}
}

func (s *faultStore) NewBatchWithSize(size int) ethdb.Batch {
	return &faultBatch{Batch: s.KeyValueStore.NewBatchWithSize(size), store: s}
}

// faultBatch records its keys; its Write counts them as pending and applies
// the arming rules to each key.
type faultBatch struct {
	ethdb.Batch
	store *faultStore
	keys  []string
	dels  []bool
}

func (b *faultBatch) Put(key, value []byte) error {
	b.keys, b.dels = append(b.keys, string(key)), append(b.dels, false)
	return b.Batch.Put(key, value)
}

func (b *faultBatch) Delete(key []byte) error {
	b.keys, b.dels = append(b.keys, string(key)), append(b.dels, true)
	return b.Batch.Delete(key)
}

func (b *faultBatch) Reset() {
	b.keys, b.dels = nil, nil
	b.Batch.Reset()
}

func (b *faultBatch) Write() error {
	b.store.record(b.keys, b.dels)
	return b.Batch.Write()
}

// ---------------------------------------------------------------- catalog points

func suffix(prefix, end string) func(string, bool) bool {
	return func(key string, deleted bool) bool {
		return !deleted && strings.HasPrefix(key, prefix) && strings.HasSuffix(key, end)
	}
}

func pinPut(key string, deleted bool) bool  { return !deleted && strings.HasPrefix(key, pinPrefix) }
func pinDrop(key string, deleted bool) bool { return deleted && strings.HasPrefix(key, pinPrefix) }

// arms gives the arming rule of every catalog point except the one the probe
// fires itself (h.after-golive-before-first-import).
func arms(point string) *arm {
	table := map[string]*arm{
		"h.before-boundary-sync":      {match: suffix(lifecyclePrefix, "/boundary"), mode: "before"},
		"h.after-boundary-sync":       {match: suffix(lifecyclePrefix, "/boundary"), mode: "after"},
		"adm.before-counter-sync":     {match: suffix(admissionPrefix, "/counter"), mode: "before"},
		"adm.before-admitted-sync":    {match: suffix(admissionPrefix, "/admitted"), mode: "before"},
		"adm.after-admitted-sync":     {match: suffix(admissionPrefix, "/admitted"), mode: "after"},
		"pin.before-pin-sync":         {match: pinPut, mode: "before"},
		"pin.after-pin-sync":          {match: pinPut, mode: "after"},
		"pin.release-before-sync":     {match: pinDrop, mode: "before"},
		"exec.before-exec-ready-sync": {match: suffix(admissionPrefix, "/exec-ready"), mode: "before"},
		"exec.after-exec-ready-sync":  {match: suffix(admissionPrefix, "/exec-ready"), mode: "after"},
		"dec.before-intent-sync":      {match: suffix(decisionPrefix, "/counter"), mode: "before"},
		"dec.in-native-mutation":      {match: suffix(decisionPrefix, "/intent"), mode: "native"},
		"dec.before-decided-sync":     {match: suffix(decisionPrefix, "/decided"), mode: "before"},
		"dec.after-decided-sync":      {match: suffix(decisionPrefix, "/decided"), mode: "after"},
	}
	a := table[point]
	if a != nil {
		a.point = point
	}
	return a
}

// points is the catalog order of test/native_faults/faults.json.
var points = []string{
	"h.before-boundary-sync", "h.after-boundary-sync", "h.after-golive-before-first-import",
	"adm.before-counter-sync", "adm.before-admitted-sync", "adm.after-admitted-sync",
	"pin.before-pin-sync", "pin.after-pin-sync", "pin.release-before-sync",
	"exec.before-exec-ready-sync", "exec.after-exec-ready-sync",
	"dec.before-intent-sync", "dec.in-native-mutation", "dec.before-decided-sync", "dec.after-decided-sync",
}

// ---------------------------------------------------------------- stores

// node is one open store: the fault store, the database, the chain and the
// lifecycle attach.
type node struct {
	fs    *faultStore
	db    ethdb.Database
	chain *core.BlockChain
	life  *extractorgeth.Lifecycle
}

func syncDirs(mem *vfs.MemFS) {
	for _, dir := range []string{"/", "/mem", memDir} {
		f, err := mem.OpenDir(dir)
		check(err)
		check(f.Sync())
		check(f.Close())
	}
}

// open opens the store on mem. The freezer stays on the OS file system in a
// new directory per open (the short chain never reaches it).
func open(mem *vfs.MemFS, work string, genesis *core.Genesis, cfg extractorgeth.Config,
	trace func(string, map[string]any)) (*node, error) {
	return openHandoff(mem, work, genesis, cfg, trace, extractorgeth.LifecycleOptions{}, nil)
}

func openHandoff(mem *vfs.MemFS, work string, genesis *core.Genesis, cfg extractorgeth.Config,
	trace func(string, map[string]any), opts extractorgeth.LifecycleOptions,
	wrap func(ethdb.KeyValueStore) ethdb.KeyValueStore) (*node, error) {
	pebble.ExtractorFS = mem
	kv, err := pebble.New(memDir, 16, 16, "", false)
	if err != nil {
		return nil, err
	}
	fs := &faultStore{KeyValueStore: kv}
	var store ethdb.KeyValueStore = fs
	if wrap != nil {
		store = wrap(store)
	}
	ancient, err := os.MkdirTemp(work, "ancient-")
	check(err)
	db, err := rawdb.Open(store, rawdb.OpenOptions{Ancient: ancient})
	if err != nil {
		return nil, err
	}
	n := &node{fs: fs, db: db}
	chain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), core.DefaultConfig())
	if err != nil {
		return n, err
	}
	n.chain = chain
	n.life, err = extractorgeth.AttachLifecycle(chain, db, cfg, extractorgeth.Options{Trace: trace},
		extractorgeth.DecisionOptions{}, opts)
	return n, err
}

func phaseText(p lifecycle.Phase) string { return string(p) }

// settleWAL waits until the total size of the WAL files on mem is stable for
// five checks 10 ms apart, at most two seconds, and returns the wait in ms.
func settleWAL(mem *vfs.MemFS) int64 {
	size := func() int64 {
		names, err := mem.List(memDir)
		check(err)
		var total int64
		for _, name := range names {
			if strings.HasSuffix(name, ".log") {
				info, err := mem.Stat(filepath.Join(memDir, name))
				check(err)
				total += info.Size()
			}
		}
		return total
	}
	started := time.Now()
	last, stable := size(), 0
	for stable < 5 && time.Since(started) < 2*time.Second {
		time.Sleep(10 * time.Millisecond)
		now := size()
		if now == last {
			stable++
		} else {
			last, stable = now, 0
		}
	}
	return time.Since(started).Milliseconds()
}

func prefixed(db ethdb.KeyValueReader, iter ethdb.Iteratee, prefix string) map[string]string {
	out := map[string]string{}
	it := iter.NewIterator([]byte(prefix), nil)
	defer it.Release()
	for it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	check(it.Error())
	return out
}

// boundaryOf reads the boundary record, if any.
func boundaryOf(db ethdb.Database) map[string]any {
	for key, value := range prefixed(db, db, lifecyclePrefix) {
		if !strings.HasSuffix(key, "/boundary") {
			continue
		}
		var b lifecycle.Boundary
		check(json.Unmarshal([]byte(value), &b))
		return map[string]any{"number": b.Head.Number, "hash": plainHash(b.Head.Hash), "root": plainHash(b.Root)}
	}
	return nil
}

// nativeAt reads the native canonical hash and header state root at number.
func nativeAt(db ethdb.Database, number uint64) map[string]any {
	hash := rawdb.ReadCanonicalHash(db, number)
	header := rawdb.ReadHeader(db, hash, number)
	if header == nil {
		return nil
	}
	return map[string]any{"number": number, "hash": hex.EncodeToString(hash[:]),
		"root": hex.EncodeToString(header.Root[:])}
}

func has(records map[string]string, end string) bool {
	for key := range records {
		if strings.HasSuffix(key, end) {
			return true
		}
	}
	return false
}

// below reports whether sequence 0 of the run exists as the accepted
// packages read it: below the durable counter. A record beyond the counter is
// an orphan that the next step overwrites (admission and decision open load
// only sequences below the counter).
func below(records map[string]string) bool {
	for key, value := range records {
		if strings.HasSuffix(key, "/counter") {
			var counter struct {
				Next uint64 `json:"next"`
			}
			check(json.Unmarshal([]byte(value), &counter))
			return counter.Next > 0
		}
	}
	return false
}

// states reads the durable states of the fault block (sequence 0 of the run)
// from the records, before any attach, as the accepted packages read them.
func states(db ethdb.Database) map[string]any {
	adm := prefixed(db, db, admissionPrefix)
	dec := prefixed(db, db, decisionPrefix)
	if !below(adm) {
		adm = map[string]string{}
	}
	if !below(dec) {
		dec = map[string]string{}
	}
	admission := "none"
	switch {
	case has(adm, "/0000000000000000/decided"):
		admission = "decided"
	case has(adm, "/0000000000000000/exec-ready"):
		admission = "exec_ready"
	case has(adm, "/0000000000000000/admitted"):
		admission = "admitted"
	case has(adm, "/0000000000000000/preparing"):
		admission = "preparing"
	}
	pin := "none"
	if len(prefixed(db, db, pinPrefix)) > 0 {
		pin = "live"
	} else if admission == "admitted" || admission == "exec_ready" || admission == "decided" {
		pin = "released"
	}
	decision := "none"
	switch {
	case has(dec, "/0000000000000000/decided"):
		decision = "decided"
	case has(dec, "/0000000000000000/intent") && len(prefixed(db, db, markerPrefix)) > 0:
		decision = "intent_marker"
	case has(dec, "/0000000000000000/intent"):
		decision = "intent"
	}
	out := map[string]any{"admission": admission, "pin": pin, "decision": decision, "boundary": boundaryOf(db)}
	if b, ok := out["boundary"].(map[string]any); ok && b != nil {
		out["native_h"] = nativeAt(db, b["number"].(uint64))
	} else {
		out["boundary"] = nil
	}
	return out
}

// ---------------------------------------------------------------- one case

// guarded runs step and reports whether it stopped at the armed point.
func guarded(step func() error) (hit string, err error) {
	defer func() {
		if r := recover(); r != nil {
			if f, ok := r.(faultHit); ok {
				hit = f.point
				return
			}
			panic(r)
		}
	}()
	return "", step()
}

type caseResult map[string]any

func runCase(point, variant, work string, genesis *core.Genesis, blocks []*types.Block, cfg extractorgeth.Config,
	numbers map[string]uint64) caseResult {
	dir := filepath.Join(work, point+"-"+variant)
	check(os.MkdirAll(filepath.Join(dir, "geth"), 0o700))
	check(extractorgeth.Preflight(filepath.Join(dir, "geth"), cfg))
	mem := vfs.NewCrashableMem()
	check(mem.MkdirAll(memDir, 0o755))
	syncDirs(mem)
	result := caseResult{"point": point, "variant": variant}
	n, err := open(mem, dir, genesis, cfg, nil)
	check(err)
	isH := strings.HasPrefix(point, "h.")
	faultBlock := uint64(1)
	var hit string
	var stepErr error
	if isH {
		// BOOTSTRAP: block 1 is the initial sync; H is set at block 1.
		_, err = n.chain.InsertChain(types.Blocks{blocks[0]})
		check(err)
		if point == "h.after-golive-before-first-import" {
			_, stepErr = n.life.GoLive()
			if stepErr == nil {
				hit = point
			}
		} else {
			n.fs.armed = arms(point)
			hit, stepErr = guarded(func() error { _, e := n.life.GoLive(); return e })
		}
	} else {
		// H is the genesis; block 1 is the first live block and the fault block.
		_, err = n.life.GoLive()
		check(err)
		n.fs.armed = arms(point)
		hit, stepErr = guarded(func() error { _, e := n.chain.InsertChain(types.Blocks{blocks[0]}); return e })
	}
	result["fault"] = map[string]any{"hit": hit == point, "block": faultBlock}
	result["step_error"] = errText(stepErr)
	dropped := n.fs.pending
	// Abandon the node: no BlockChain.Stop and no store close, as at a crash.
	var clone *vfs.MemFS
	if variant == "power_loss" {
		clone = mem.CrashClone(vfs.CrashCloneCfg{UnsyncedDataPercent: 0})
	} else {
		// A process crash keeps what reached the file system. Pebble writes
		// WAL records asynchronously, so let the abandoned store's flusher
		// finish (no sync) before the copy.
		result["wal_settle_ms"] = settleWAL(mem)
		clone = mem.CrashClone(vfs.CrashCloneCfg{UnsyncedDataPercent: 100, RNG: rand.New(rand.NewPCG(1, 2))})
		dropped = 0
	}
	result["storage"] = map[string]any{"reset_to_synced": variant == "power_loss", "unsynced_dropped": dropped}

	// Restart: read the durable states, then attach and recover.
	pebble.ExtractorFS = clone
	kv, err := pebble.New(memDir, 16, 16, "", false)
	check(err)
	ancient, err := os.MkdirTemp(dir, "ancient-")
	check(err)
	raw, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: ancient})
	check(err)
	after := states(raw)
	check(raw.Close())
	// The gate's phase when it opens, before the shipped attach recovers
	// and goes live, from its "gate_opened" trace event.
	opened := ""
	trace := func(event string, fields map[string]any) {
		if event == "gate_opened" && opened == "" {
			opened, _ = fields["phase"].(string)
		}
	}
	r, err := open(clone, dir, genesis, cfg, trace)
	result["restart_error"] = errText(err)
	if err != nil {
		result["after_restart"] = after
		return result
	}
	after["mode"] = opened
	result["attach_phase"] = phaseText(r.life.Gate().Phase())
	result["after_restart"] = after
	_, liveErr := r.life.GoLive()
	result["golive_error"] = errText(liveErr)
	index, importErr := r.chain.InsertChain(types.Blocks{blocks[0], blocks[1]})
	result["import"] = map[string]any{"index": index, "error": errText(importErr)}
	final := map[string]any{"mode": phaseText(r.life.Gate().Phase()), "head": r.chain.CurrentBlock().Number.Uint64()}
	if b, ok := r.life.Gate().Boundary(); ok {
		boundary := map[string]any{"number": b.Head.Number, "hash": plainHash(b.Head.Hash), "root": plainHash(b.Root)}
		final["boundary"] = boundary
		final["native_h"] = nativeAt(r.db, b.Head.Number)
	} else {
		final["boundary"] = nil
	}
	records := prefixed(r.db, r.db, decisionPrefix)
	decided, undecided := decisions(records, numbers)
	exported, seen, duplicates := []uint64{}, map[uint64]bool{}, 0
	for _, row := range decided {
		// A recovered obligation may close as a CANONICAL activation of the
		// stored block; SIDE and FINALITY decisions deliver nothing.
		m := row.(map[string]any)
		if m["kind"] != "IMPORT" && m["kind"] != "CANONICAL" {
			continue
		}
		number := m["block"].(uint64)
		if seen[number] {
			duplicates++
			continue
		}
		seen[number] = true
		exported = append(exported, number)
	}
	sort.Slice(exported, func(i, j int) bool { return exported[i] < exported[j] })
	final["exported"], final["duplicates"] = exported, duplicates
	export := exportBlocks(r.chain, r.db, len(blocks))
	// Re-import of the same blocks: no new observation or decision.
	admBefore, decBefore := counted(prefixed(r.db, r.db, admissionPrefix)), counted(records)
	_, reErr := r.chain.InsertChain(types.Blocks{blocks[0], blocks[1]})
	admAfter, decAfter := counted(prefixed(r.db, r.db, admissionPrefix)), counted(prefixed(r.db, r.db, decisionPrefix))
	result["final"] = final
	result["observation"] = map[string]any{
		"schema_version": 1, "kind": "native-corpus-observation", "family": "eth",
		"chain_id": genesis.Config.ChainID.Int64(), "blocks": export["blocks"], "decisions": decided,
		"reimport": map[string]any{"new_observations": admAfter - admBefore, "new_decisions": decAfter - decBefore},
		"native": map[string]any{"reexecution": export["reexecution"], "undecided_intents": undecided,
			"reimport_error": errText(reErr)},
	}
	return result
}

// ---------------------------------------------------------------- run

func run(genesisPath, work, reportPath string) {
	started := time.Now()
	check(os.MkdirAll(work, 0o700))
	genesis, blocks := corpusInputs(genesisPath)
	numbers := map[string]uint64{}
	for _, block := range blocks {
		numbers[hex.EncodeToString(block.Hash().Bytes())] = block.NumberU64()
	}
	commit, err := extractorgeth.NativeIdentity()
	check(err)
	cases := []any{}
	for index, point := range points {
		for _, variant := range []string{"crash", "power_loss"} {
			cfg := extractorgeth.Config{StoreID: fmt.Sprintf("f%031x", index*2+len(variant)%2),
				ProducerID: producerID, ChainID: genesis.Config.ChainID.String(),
				GenesisHash: genesis.ToBlock().Hash(), NativeCommit: commit, Profile: profile}
			cases = append(cases, runCase(point, variant, work, genesis, blocks, cfg, numbers))
		}
	}
	report := map[string]any{"schema_version": 1, "kind": "native-faults-probe", "family": "eth",
		"storage_model": "pebble-v2-crashable-mem", "native_commit": commit,
		"genesis_hash": genesis.ToBlock().Hash().Hex(), "cases": cases,
		"elapsed_seconds": time.Since(started).Seconds()}
	report["handoff_runs"] = runHandoffs(work, genesis, blocks, commit)
	data, err := json.MarshalIndent(report, "", " ")
	check(err)
	check(os.WriteFile(reportPath, append(data, '\n'), 0o600))
	fmt.Println("Recorded the Geth fault probe report; the judgement is separate.")
}

func main() {
	if len(os.Args) == 5 && os.Args[1] == "run" {
		run(os.Args[2], os.Args[3], os.Args[4])
		return
	}
	fmt.Fprintln(os.Stderr, "usage: extractor-faults-geth run GENESIS WORKDIR REPORT")
	os.Exit(2)
}


// ---------------------------------------------------------------- export (from test/native_corpus/eth/probe.go)

func plain(address common.Address) string {
	return strings.ToLower(strings.TrimPrefix(address.Hex(), "0x"))
}

func accounts(statedb *state.StateDB) map[string]any {
	out := map[string]any{}
	for _, address := range listed {
		row := map[string]any{
			"balance": statedb.GetBalance(address).ToBig().String(),
			"nonce":   statedb.GetNonce(address),
			"code":    hex.EncodeToString(statedb.GetCode(address)),
		}
		if withSlots[address] {
			slots := map[string]string{}
			for slot := int64(0); slot < 4; slot++ {
				value := statedb.GetState(address, common.BigToHash(big.NewInt(slot)))
				slots[fmt.Sprint(slot)] = hex.EncodeToString(value[:])
			}
			row["storage"] = slots
		}
		out[plain(address)] = row
	}
	return out
}

// hexField turns a callTracer hex string ("0x..." or absent) into lowercase
// hex without 0x.
func hexField(value any) string {
	text, _ := value.(string)
	return strings.ToLower(strings.TrimPrefix(text, "0x"))
}

// frame converts one native callTracer frame to the observation format.
func frame(raw map[string]any) map[string]any {
	value := "0"
	if text, ok := raw["value"].(string); ok {
		number, ok := new(big.Int).SetString(strings.TrimPrefix(text, "0x"), 16)
		if !ok {
			panic("callTracer value is not hex: " + text)
		}
		value = number.String()
	}
	var errText any
	if text, ok := raw["error"].(string); ok {
		errText = text
	}
	calls := []any{}
	if list, ok := raw["calls"].([]any); ok {
		for _, call := range list {
			calls = append(calls, frame(call.(map[string]any)))
		}
	}
	return map[string]any{"type": raw["type"], "from": hexField(raw["from"]), "to": hexField(raw["to"]),
		"value": value, "input": hexField(raw["input"]), "output": hexField(raw["output"]), "error": errText,
		"calls": calls}
}

func logs(list []*types.Log) []any {
	out := []any{}
	for _, log := range list {
		topics := []string{}
		for _, topic := range log.Topics {
			topics = append(topics, hex.EncodeToString(topic[:]))
		}
		out = append(out, map[string]any{"address": plain(log.Address), "topics": topics,
			"data": hex.EncodeToString(log.Data)})
	}
	return out
}

func receiptView(receipt *types.Receipt) map[string]any {
	return map[string]any{"status": receipt.Status, "gas_used": receipt.GasUsed,
		"cumulative_gas_used": receipt.CumulativeGasUsed, "effective_gas_price": receipt.EffectiveGasPrice.String(),
		"logs": logs(receipt.Logs)}
}

func txView(tx *types.Transaction, index int, signer types.Signer) map[string]any {
	from, err := types.Sender(signer, tx)
	check(err)
	row := map[string]any{"index": index, "hash": hex.EncodeToString(tx.Hash().Bytes()), "type": int(tx.Type()),
		"from": plain(from), "nonce": tx.Nonce(), "to": plain(*tx.To()), "value": tx.Value().String(),
		"gas": tx.Gas(), "data": hex.EncodeToString(tx.Data())}
	if tx.Type() == types.DynamicFeeTxType {
		row["max_fee_per_gas"], row["max_priority_fee_per_gas"] = tx.GasFeeCap().String(), tx.GasTipCap().String()
	} else {
		row["gas_price"] = tx.GasPrice().String()
	}
	return row
}

// exportBlocks reads blocks 1..n of the canonical chain from the native
// database and re-executes each on its parent state with the native call
// tracer.
func exportBlocks(chain *core.BlockChain, db ethdb.Database, count int) map[string]any {
	config := chain.Config()
	out := []any{}
	checks := []any{}
	for number := uint64(1); number <= uint64(count); number++ {
		hash := rawdb.ReadCanonicalHash(db, number)
		block := rawdb.ReadBlock(db, hash, number)
		if block == nil {
			panic(fmt.Sprintf("block %d is not in the native database", number))
		}
		header := block.Header()
		parentHeader := chain.GetHeaderByHash(block.ParentHash())
		stored := rawdb.ReadReceipts(db, hash, number, block.Time(), config)
		if len(stored) != len(block.Transactions()) {
			panic(fmt.Sprintf("block %d has %d receipts for %d transactions", number, len(stored), len(block.Transactions())))
		}
		statedb, err := chain.StateAt(parentHeader.Root, parentHeader.Number, parentHeader.Time)
		check(err)
		signer := types.MakeSigner(config, header.Number, header.Time)
		pool := core.NewGasPool(header.GasLimit)
		var cumulative uint64
		transactions := []any{}
		reexecuted := []any{}
		for index, tx := range block.Transactions() {
			before := accounts(statedb)
			tracer, err := tracers.DefaultDirectory.New("callTracer", &tracers.Context{BlockHash: hash,
				BlockNumber: header.Number, TxIndex: index, TxHash: tx.Hash()}, nil, config)
			check(err)
			blockContext := core.NewEVMBlockContext(header, chain, nil)
			evm := vm.NewEVM(blockContext, state.NewHookedState(statedb, tracer.Hooks), config, vm.Config{Tracer: tracer.Hooks})
			message, err := core.TransactionToMessage(tx, signer, header.BaseFee)
			check(err)
			statedb.SetTxContext(tx.Hash(), index, uint32(index+1))
			receipt, _, err := core.ApplyTransactionWithEVM(context.Background(), message, pool, statedb, header.Number,
				hash, header.Time, tx, evm)
			check(err)
			evm.Release()
			cumulative = receipt.CumulativeGasUsed
			result, err := tracer.GetResult()
			check(err)
			var raw map[string]any
			check(json.Unmarshal(result, &raw))
			row := txView(tx, index, signer)
			row["receipt"] = receiptView(stored[index])
			row["frame"] = frame(raw)
			row["state_before"] = before
			row["state_after"] = accounts(statedb)
			transactions = append(transactions, row)
			// ApplyTransactionWithEVM leaves EffectiveGasPrice unset in this
			// Geth version; the re-execution computes it from the
			// transaction and the base fee (EIP-1559) and compares it with
			// the value ReadReceipts derives for the imported receipt.
			tip, err := tx.EffectiveGasTip(header.BaseFee)
			check(err)
			receipt.EffectiveGasPrice = new(big.Int).Add(tip, header.BaseFee)
			again, imported := receiptView(receipt), receiptView(stored[index])
			again["bloom"], imported["bloom"] = hex.EncodeToString(receipt.Bloom[:]), hex.EncodeToString(stored[index].Bloom[:])
			again["type"], imported["type"] = receipt.Type, stored[index].Type
			reexecuted = append(reexecuted, map[string]any{"index": index,
				"receipt_equal": fmt.Sprint(again) == fmt.Sprint(imported)})
		}
		// The block reward, outside any transaction, then the root.
		chain.Engine().Finalize(chain, header, statedb, block.Body())
		root := statedb.IntermediateRoot(config.Rules(header.Number, false, header.Time))
		atRoot, err := chain.StateAt(header.Root, header.Number, header.Time)
		check(err)
		var baseFee any
		if header.BaseFee != nil {
			baseFee = header.BaseFee.String()
		}
		out = append(out, map[string]any{
			"number": number, "hash": hex.EncodeToString(hash[:]), "state_root": hex.EncodeToString(header.Root[:]),
			"base_fee": baseFee, "gas_limit": header.GasLimit, "gas_used": header.GasUsed,
			"body_tx_count": len(block.Transactions()), "transactions": transactions,
			"system_transactions": []any{}, "state_sync": []any{}, "state_after_block": accounts(atRoot),
		})
		checks = append(checks, map[string]any{"number": number, "receipts": reexecuted,
			"cumulative_gas_used": cumulative, "header_gas_used": header.GasUsed,
			"root_equal": root == header.Root, "reexecuted_root": root.Hex(), "header_root": header.Root.Hex()})
	}
	return map[string]any{"blocks": out, "reexecution": checks}
}

type intentRow struct {
	Kind    string `json:"kind"`
	NewHead struct {
		Number uint64 `json:"number"`
		Hash   string `json:"hash"`
	} `json:"new_head"`
	Members []struct {
		Block string `json:"block"`
	} `json:"members"`
}

// decisions derives the NATIVE_DECIDED decisions from the retained decision
// records: one row per sequence with an intent and a decided record, naming
// the chain number of its single member (the written block; every decision
// of this corpus import has one member). It also returns the keys of
// intents without a decided record, which the driver refuses.
func decisions(records map[string]string, numbers map[string]uint64) ([]any, []string) {
	type row struct {
		key   string
		block uint64
		kind  string
	}
	rows := []row{}
	undecided := []string{}
	for key, value := range records {
		if !strings.HasSuffix(key, "/intent") {
			continue
		}
		base := strings.TrimSuffix(key, "/intent")
		if _, ok := records[base+"/decided"]; !ok {
			if _, rejected := records[base+"/rejected"]; !rejected {
				undecided = append(undecided, key)
			}
			continue
		}
		var intent intentRow
		check(json.Unmarshal([]byte(value), &intent))
		if len(intent.Members) != 1 {
			panic(fmt.Sprintf("decided intent %s has %d members, want 1", key, len(intent.Members)))
		}
		number, ok := numbers[strings.ToLower(strings.TrimPrefix(intent.Members[0].Block, "0x"))]
		if !ok {
			panic("decided intent of an unknown block: " + key)
		}
		rows = append(rows, row{key, number, intent.Kind})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].key < rows[j].key })
	out := []any{}
	for _, r := range rows {
		out = append(out, map[string]any{"block": r.block, "kind": r.kind})
	}
	sort.Strings(undecided)
	return out, undecided
}

// counted returns the sum of the "next" values of every counter record under
// a record set: the number of sequences ever started.
func counted(records map[string]string) int {
	total := 0
	for key, value := range records {
		if strings.HasSuffix(key, "/counter") {
			var counter struct {
				Next int `json:"next"`
			}
			check(json.Unmarshal([]byte(value), &counter))
			total += counter.Next
		}
	}
	return total
}
