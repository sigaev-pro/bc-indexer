// This test-only probe runs the E01-04.early_faults catalog on the accepted
// patched Bor tree (E01-02.admission, E01-02.decision_bridge and
// E01-02.lifecycle_seams Bor patch sets, plus the E01-04.early_faults Bor
// patch set, which lets ethdb/pebble open on a Pebble (v1) in-memory file
// system and lets the node wrap its key-value store). Preparation copies it
// to cmd/extractor-faults-bor and the Heimdall stub to its heimdallstub
// subdirectory.
//
// Each case starts a capture node as the Bor lifecycle probe does (node
// stack, the native backend in the sequential capture profile, Heimdall from
// the in-process stub, wire.Start), with the chaindata store on a new
// vfs.NewStrictMem file system and the key-value store wrapped in a fault
// store. It runs one scenario on the POLY smoke corpus blocks (E01-03.smoke_corpus
// POLY inputs: block 1 with the corpus transactions, empty blocks 2 and 3,
// and block 4, the first sprint start with a state sync). The fault store
// stops the scenario at the catalog point by panicking inside the store
// call. The probe abandons the node (no stop, no store close), copies the
// chaindata files into a new strict file system (after a reset to the synced
// state for a power loss), reads the durable states, starts a new node on a
// new OS data directory and the copy, imports the corpus blocks, imports them
// again and exports the native output in the corpus format. It writes no
// expected value; faults_poly.py judges the report.
//
// Usage:
//
//	extractor-faults-bor run -genesis G -config C -bodies DIR -blocks RLP -commit SHA -accounts LIST -work DIR -report FILE
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/vfs"
	"github.com/ethereum/go-ethereum/cmd/extractor-faults-bor/heimdallstub"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/downloader/whitelist"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/tracers"
	_ "github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/extractor/borimport"
	"github.com/ethereum/go-ethereum/extractor/borlifecycle/wire"
	_ "github.com/ethereum/go-ethereum/extractor/borwire"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	producerID           = "bor"
	runID                = "run-1"
	normalizationProfile = "test-only-probe-v1"
	instanceName         = "bor"
	corpusBlocks         = 4
	chainID              = 15001

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

// ---------------------------------------------------------------- inputs (from test/native_corpus/poly/main.go)

func readBounded(path string, maximum int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, maximum)
	}
	return data, nil
}

type inputs struct {
	genesisBytes []byte
	configBytes  []byte
	genesis      *core.Genesis
}

func loadInputs(genesisPath, configPath string) (inputs, error) {
	genesisBytes, err := readBounded(genesisPath, 4*1024*1024)
	if err != nil {
		return inputs{}, err
	}
	configBytes, err := readBounded(configPath, 1024*1024)
	if err != nil {
		return inputs{}, err
	}
	var genesis core.Genesis
	if err = json.Unmarshal(genesisBytes, &genesis); err != nil {
		return inputs{}, err
	}
	var config params.ChainConfig
	if err = json.Unmarshal(configBytes, &config); err != nil {
		return inputs{}, err
	}
	if config.Bor == nil || config.ChainID.Cmp(big.NewInt(chainID)) != 0 {
		return inputs{}, errors.New("expected the private Bor genesis 15001")
	}
	genesis.Config = &config
	return inputs{genesisBytes: genesisBytes, configBytes: configBytes, genesis: &genesis}, nil
}

// stubTransport answers every Heimdall request from the stub files.
type stubTransport struct{ stub heimdallstub.Stub }

func (s stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	if req.URL.Host != heimdallstub.Host {
		return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
	}
	body, status := s.stub.Serve(req.URL)
	return &http.Response{Status: http.StatusText(status), StatusCode: status, Proto: "HTTP/1.1", ProtoMajor: 1,
		ProtoMinor: 1, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)), Request: req}, nil
}

func newStack(dataDir string) (*node.Node, error) {
	return node.New(&node.Config{Name: instanceName, DataDir: dataDir,
		P2P: p2p.Config{NoDiscovery: true, MaxPeers: 0, ListenAddr: "127.0.0.1:0"}})
}

// backend starts the native Bor backend in the sequential capture profile
// (hash scheme archive, full sync, no parallel, pipelined, stateless or
// preconf paths), Heimdall behind the process transport.
func backend(stack *node.Node, genesis *core.Genesis) (*eth.Ethereum, error) {
	cfg := ethconfig.Defaults
	cfg.Genesis, cfg.NetworkId, cfg.SyncMode = genesis, chainID, downloader.FullSync
	cfg.NoPruning, cfg.StateScheme = true, "hash"
	cfg.DatabaseCache, cfg.DatabaseHandles = 64, 64
	cfg.TrieCleanCache, cfg.TrieDirtyCache, cfg.SnapshotCache = 32, 32, 0
	cfg.WithoutHeimdall, cfg.DevFakeAuthor = false, false
	cfg.HeimdallURL, cfg.HeimdallgRPCAddress, cfg.HeimdallWSAddress = heimdallstub.URL, "", ""
	cfg.HeimdallTimeout = 5 * time.Second
	cfg.ParallelEVM.Enable, cfg.EnablePipelinedImportSRC = false, false
	cfg.EnableParallelStatelessImport, cfg.EnablePreconfs, cfg.AcceptPreconfTx = false, false, false
	backend, err := eth.New(stack, &cfg)
	if err != nil {
		return nil, fmt.Errorf("create native Bor backend: %w", err)
	}
	if _, ok := backend.Engine().(*bor.Bor); !ok {
		return nil, errors.New("native engine is not Bor")
	}
	return backend, nil
}

func readBlocks(path string) ([]*types.Block, error) {
	data, err := readBounded(path, 64*1024*1024)
	if err != nil {
		return nil, err
	}
	stream := rlp.NewStream(bytes.NewReader(data), uint64(len(data)))
	var blocks []*types.Block
	for {
		block := new(types.Block)
		if err := stream.Decode(block); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	if len(blocks) != corpusBlocks {
		return nil, fmt.Errorf("expected %d corpus blocks, read %d", corpusBlocks, len(blocks))
	}
	return blocks, nil
}

// ---------------------------------------------------------------- fault store (from test/native_faults/eth/probe.go)

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

// ---------------------------------------------------------------- catalog points (from test/native_faults/eth/probe.go)

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

// ---------------------------------------------------------------- durable states (from test/native_faults/eth/probe.go)

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

// ---------------------------------------------------------------- stores

// node is one started capture node: the fault store the node opened, the
// stack, the backend, the chain and the capture run.
type instance struct {
	fs    *faultStore
	stack *node.Node
	eth   *eth.Ethereum
	chain *core.BlockChain
	run   *wire.Run
	db    ethdb.Database
}

// nodeRun is what exportChain reads.
type nodeRun struct {
	chain *core.BlockChain
	eth   *eth.Ethereum
}

// chaindata is the chaindata path of the node stack on dataDir.
func chaindata(dataDir string) string { return filepath.Join(dataDir, instanceName, "chaindata") }

// syncDirs syncs dir and every parent directory on mem.
func syncDirs(mem *vfs.MemFS, dir string) {
	for current := dir; ; current = filepath.Dir(current) {
		f, err := mem.OpenDir(current)
		check(err)
		check(f.Sync())
		check(f.Close())
		if current == "/" {
			return
		}
	}
}

func freshFS(dir string) *vfs.MemFS {
	mem := vfs.NewStrictMem()
	check(mem.MkdirAll(dir, 0o755))
	syncDirs(mem, dir)
	return mem
}

// copyFS copies every file of the directory from on src into the directory
// to of a new strict file system and syncs it. Pebble v1's MemFS has no crash
// clone, and the abandoned store is never closed, so the copy reads its files
// as they are. The new node runs on a new OS data directory, so the copy
// moves the files to that node's chaindata path.
func copyFS(src *vfs.MemFS, from, to string) *vfs.MemFS {
	dst := freshFS(to)
	names, err := src.List(from)
	check(err)
	sort.Strings(names)
	for _, name := range names {
		in, err := src.Open(filepath.Join(from, name))
		if err != nil {
			continue // removed by the abandoned store since the listing
		}
		data, err := io.ReadAll(in)
		check(err)
		check(in.Close())
		out, err := dst.Create(filepath.Join(to, name))
		check(err)
		_, err = out.Write(data)
		check(err)
		check(out.Sync())
		check(out.Close())
	}
	syncDirs(dst, to)
	return dst
}

// settleWAL waits until the total size of the WAL files in dir on mem is
// stable for five checks 10 ms apart, at most two seconds, and returns the
// wait in ms.
func settleWAL(mem *vfs.MemFS, dir string) int64 {
	size := func() int64 {
		names, err := mem.List(dir)
		check(err)
		var total int64
		for _, name := range names {
			if strings.HasSuffix(name, ".log") {
				if info, err := mem.Stat(filepath.Join(dir, name)); err == nil {
					total += info.Size()
				}
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

type config struct {
	genesis, config, bodies, blocks, commit, accounts, work, report string
}

// start starts a capture node on dataDir with its chaindata store on mem, as
// the Bor lifecycle probe does: the store identity preflight, the native
// backend, then wire.Start (admission, the decision bridge with the
// lifecycle gate, the live observer). A new store stays in BOOTSTRAP.
func start(c config, in inputs, storeID, dataDir string, mem *vfs.MemFS) (*instance, error) {
	// A new process starts with the lifecycle's process-global hooks unset;
	// the abandoned node of this process installed its own.
	rawdb.ExtractorRequire, rawdb.ExtractorFinality, whitelist.ExtractorEnter = nil, nil, nil
	n := &instance{}
	pebble.ExtractorFS = mem
	node.ExtractorWrap = func(kv ethdb.KeyValueStore) ethdb.KeyValueStore {
		n.fs = &faultStore{KeyValueStore: kv}
		return n.fs
	}
	defer func() { node.ExtractorWrap = nil }()
	stack, err := newStack(dataDir)
	if err != nil {
		return nil, err
	}
	n.stack = stack
	capture := wire.Capture{StoreID: storeID, Producer: producerID, Run: runID, Genesis: c.genesis,
		ChainConfig: c.config, Commit: c.commit, Normalization: normalizationProfile, HeimdallHost: heimdallstub.Host}
	cfg, err := capture.Config(stack.ResolvePath(""))
	if err != nil {
		return n, err
	}
	if err = borimport.Preflight(cfg, in.genesis.Config.ChainID.String(), in.genesis.ToBlock().Hash()); err != nil {
		return n, err
	}
	http.DefaultTransport = stubTransport{stub: heimdallstub.Stub{Bodies: c.bodies, Variant: "corpus"}}
	backend, err := backend(stack, in.genesis)
	if err != nil {
		return n, err
	}
	if n.fs == nil {
		return n, errors.New("the node did not open its store through the fault store")
	}
	n.eth, n.chain, n.db = backend, backend.BlockChain(), backend.ChainDb()
	n.run, err = wire.Start(stack, backend, capture)
	return n, err
}

// phase is the gate phase of a started node: BOOTSTRAP while it waits.
func (n *instance) phase() string {
	if n.run.Bootstrapping() {
		return string(lifecycle.Bootstrap)
	}
	return string(n.run.Lifecycle.Phase())
}

// insert imports the blocks one by one through the native import path and
// returns the index of the first failed block, the state sync data the
// native engine recorded for each processed block, and the error.
func (n *instance) insert(blocks []*types.Block) (int, []map[string]any, error) {
	events := []map[string]any{}
	for i, block := range blocks {
		if _, err := n.chain.InsertChain([]*types.Block{block}, false); err != nil {
			return i, events, err
		}
		for _, data := range n.chain.HeaderChain().GetStateSync() {
			events = append(events, map[string]any{"block": block.NumberU64(), "id": data.ID,
				"contract": hex.EncodeToString(data.Contract[:]), "data": hex.EncodeToString(data.Data)})
		}
	}
	return len(blocks), events, nil
}

// ---------------------------------------------------------------- one case

type caseResult map[string]any

func runCase(c config, in inputs, index int, point, variant string, blocks []*types.Block,
	addresses []common.Address, numbers map[string]uint64) caseResult {
	dir := filepath.Join(c.work, point+"-"+variant)
	check(os.MkdirAll(dir, 0o700))
	storeID := fmt.Sprintf("%032x", index+1)
	first := filepath.Join(dir, "node-1")
	mem := freshFS(chaindata(first))
	result := caseResult{"point": point, "variant": variant}
	n, err := start(c, in, storeID, first, mem)
	check(err)
	if !n.run.Bootstrapping() {
		panic("a new store did not start in BOOTSTRAP")
	}
	var hit string
	var stepErr error
	if strings.HasPrefix(point, "h.") {
		// BOOTSTRAP: block 1 is the initial sync; H is set at block 1.
		_, err = n.chain.InsertChain(types.Blocks{blocks[0]}, false)
		check(err)
		if point == "h.after-golive-before-first-import" {
			if stepErr = n.run.GoLive(); stepErr == nil {
				hit = point
			}
		} else {
			n.fs.armed = arms(point)
			hit, stepErr = guarded(n.run.GoLive)
		}
	} else {
		// H is the genesis; block 1 is the first live block and the fault block.
		check(n.run.GoLive())
		n.fs.armed = arms(point)
		hit, stepErr = guarded(func() error { _, e := n.chain.InsertChain(types.Blocks{blocks[0]}, false); return e })
	}
	result["fault"] = map[string]any{"hit": hit == point, "block": uint64(1)}
	result["step_error"] = errText(stepErr)
	dropped := n.fs.pending
	// Abandon the node: no stop and no store close, as at a crash.
	if variant == "power_loss" {
		mem.SetIgnoreSyncs(true)
		mem.ResetToSyncedState()
	} else {
		// A process crash keeps what reached the file system; let the
		// abandoned store's WAL flusher finish (no sync) before the copy.
		result["wal_settle_ms"] = settleWAL(mem, chaindata(first))
		dropped = 0
	}
	second := filepath.Join(dir, "node-2")
	clone := copyFS(mem, chaindata(first), chaindata(second))
	result["storage"] = map[string]any{"reset_to_synced": variant == "power_loss", "unsynced_dropped": dropped}

	// Restart: read the durable states, then start the node and recover.
	pebble.ExtractorFS = clone
	kv, err := pebble.New(chaindata(second), 16, 16, "", false)
	check(err)
	raw := rawdb.NewDatabase(kv)
	after := states(raw)
	check(raw.Close())
	r, err := start(c, in, storeID, second, clone)
	result["restart_error"] = errText(err)
	if err != nil {
		result["after_restart"] = after
		return result
	}
	// The gate's phase when it opened: BOOTSTRAP while the node waits, else
	// the phase the startup reconcile reports from the opened gate.
	if r.run.Bootstrapping() {
		after["mode"] = string(lifecycle.Bootstrap)
	} else {
		after["mode"] = r.run.Lifecycle.Report().Phase
	}
	result["attach_phase"] = r.phase()
	result["after_restart"] = after
	var liveErr error
	if r.run.Bootstrapping() {
		liveErr = r.run.GoLive()
	}
	result["golive_error"] = errText(liveErr)
	imported, events, importErr := r.insert(blocks)
	result["import"] = map[string]any{"index": imported, "error": errText(importErr)}
	result["state_sync"] = events
	final := map[string]any{"mode": r.phase(), "head": r.chain.CurrentBlock().Number.Uint64()}
	if b := boundaryOf(r.db); b != nil {
		final["boundary"] = b
		final["native_h"] = nativeAt(r.db, b["number"].(uint64))
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
	// Re-import of the same blocks: no new observation or decision.
	admBefore, decBefore := counted(prefixed(r.db, r.db, admissionPrefix)), counted(records)
	_, reErr := r.chain.InsertChain(blocks, false)
	admAfter, decAfter := counted(prefixed(r.db, r.db, admissionPrefix)), counted(prefixed(r.db, r.db, decisionPrefix))
	result["final"] = final
	export := map[string]any{"decisions": decided, "undecided_intents": undecided,
		"reimport":       map[string]any{"new_observations": admAfter - admBefore, "new_decisions": decAfter - decBefore},
		"reimport_error": errText(reErr)}
	rows, exportErr := exportChain(&nodeRun{chain: r.chain, eth: r.eth}, blocks, addresses)
	if exportErr != nil {
		export["export_error"] = exportErr.Error()
	} else {
		export["blocks"] = rows
	}
	result["export"] = export
	return result
}

// ---------------------------------------------------------------- run

// caseInputs are the decoded inputs every case reads.
type caseInputs struct {
	in        inputs
	blocks    []*types.Block
	addresses []common.Address
	numbers   map[string]uint64
}

func load(c config) caseInputs {
	in, err := loadInputs(c.genesis, c.config)
	check(err)
	blocks, err := readBlocks(c.blocks)
	check(err)
	var addresses []common.Address
	for _, item := range strings.Split(c.accounts, ",") {
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(item) {
			panic(fmt.Sprintf("bad account %q", item))
		}
		addresses = append(addresses, common.HexToAddress(item))
	}
	numbers := map[string]uint64{}
	for _, block := range blocks {
		numbers[hex.EncodeToString(block.Hash().Bytes())] = block.NumberU64()
	}
	return caseInputs{in: in, blocks: blocks, addresses: addresses, numbers: numbers}
}

// caseCommand runs one case and writes its result. Each case runs in its own
// process: the abandoned nodes of a process are never freed.
func caseCommand(c config, index int, point, variant, out string) {
	ci := load(c)
	result := runCase(c, ci.in, index, point, variant, ci.blocks, ci.addresses, ci.numbers)
	data, err := json.Marshal(result)
	check(err)
	check(os.WriteFile(out, append(data, '\n'), 0o600))
}

// run runs every catalog point in both variants, one child process per case,
// and writes the probe report. A child that fails is recorded with the end
// of its error output.
func run(c config) {
	started := time.Now()
	check(os.MkdirAll(c.work, 0o700))
	ci := load(c)
	hashes := map[string]any{}
	for _, block := range ci.blocks {
		hashes[fmt.Sprint(block.NumberU64())] = hex.EncodeToString(block.Hash().Bytes())
	}
	self, err := os.Executable()
	check(err)
	cases := []any{}
	for index, point := range points {
		for v, variant := range []string{"crash", "power_loss"} {
			out := filepath.Join(c.work, fmt.Sprintf("case-%02d.json", index*2+v))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			cmd := exec.CommandContext(ctx, self, append(c.args(), "-index", fmt.Sprint(index*2+v), "-point", point,
				"-variant", variant, "-out", out)...)
			cmd.Args[1] = "case"
			var stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = io.Discard, &stderr
			err := cmd.Run()
			cancel()
			if err != nil {
				tail := stderr.String()
				if len(tail) > 2000 {
					tail = tail[len(tail)-2000:]
				}
				cases = append(cases, map[string]any{"point": point, "variant": variant,
					"child_error": fmt.Sprintf("%v: %s", err, tail)})
				continue
			}
			data, err := os.ReadFile(out)
			check(err)
			var result map[string]any
			check(json.Unmarshal(data, &result))
			cases = append(cases, result)
		}
	}
	report := map[string]any{"schema_version": 1, "kind": "native-faults-probe", "family": "poly",
		"storage_model": "pebble-v1-strict-mem", "native_commit": c.commit,
		"genesis_hash": ci.in.genesis.ToBlock().Hash().Hex(), "cases": cases, "blocks": hashes,
		"elapsed_seconds": time.Since(started).Seconds()}
	data, err := json.MarshalIndent(report, "", " ")
	check(err)
	check(os.WriteFile(c.report, append(data, '\n'), 0o600))
	fmt.Println("Recorded the Bor fault probe report; the judgement is separate.")
}

// args gives the common flags of c, after a placeholder for the command.
func (c config) args() []string {
	return []string{"", "-genesis", c.genesis, "-config", c.config, "-bodies", c.bodies, "-blocks", c.blocks,
		"-commit", c.commit, "-accounts", c.accounts, "-work", c.work, "-report", c.report}
}

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "run" && os.Args[1] != "case") {
		fmt.Fprintln(os.Stderr, "usage: extractor-faults-bor run -genesis G -config C -bodies DIR -blocks RLP "+
			"-commit SHA -accounts LIST -work DIR -report FILE")
		os.Exit(2)
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	var c config
	targets := map[string]*string{"genesis": &c.genesis, "config": &c.config, "bodies": &c.bodies,
		"blocks": &c.blocks, "commit": &c.commit, "accounts": &c.accounts, "work": &c.work, "report": &c.report}
	for name, target := range targets {
		flags.StringVar(target, name, "", name)
	}
	index := flags.Int("index", -1, "case index (case)")
	point := flags.String("point", "", "catalog point (case)")
	variant := flags.String("variant", "", "crash or power_loss (case)")
	out := flags.String("out", "", "case result file (case)")
	check(flags.Parse(os.Args[2:]))
	for name, target := range targets {
		if *target == "" {
			fmt.Fprintf(os.Stderr, "missing -%s\n", name)
			os.Exit(2)
		}
	}
	if os.Args[1] == "case" {
		caseCommand(c, *index, *point, *variant, *out)
		return
	}
	run(c)
}

// ---------------------------------------------------------------- export (from test/native_corpus/poly/main.go)

// preTransaction applies the system calls that the native processor
// (core.StateProcessor.Process) applies before the transactions of a block:
// the beacon root (none on Bor) and, from Prague on, the parent block hash.
func preTransaction(evm *vm.EVM, config *params.ChainConfig, header *types.Header) {
	if header.ParentBeaconRoot != nil {
		core.ProcessBeaconBlockRoot(*header.ParentBeaconRoot, evm)
	}
	if config.IsPrague(header.Number) || config.IsVerkle(header.Number) {
		core.ProcessParentBlockHash(header.ParentHash, evm)
	}
}

// Account is one native state read.
type Account struct {
	Balance string            `json:"balance"`
	Nonce   uint64            `json:"nonce"`
	Code    string            `json:"code"`
	Storage map[string]string `json:"storage"`
}

func readAccounts(statedb *state.StateDB, addresses []common.Address) map[string]Account {
	out := map[string]Account{}
	for _, address := range addresses {
		storage := map[string]string{}
		for slot := 0; slot < 4; slot++ {
			value := statedb.GetState(address, common.BigToHash(big.NewInt(int64(slot))))
			storage[fmt.Sprint(slot)] = hex.EncodeToString(value[:])
		}
		out[hex.EncodeToString(address[:])] = Account{Balance: statedb.GetBalance(address).ToBig().String(),
			Nonce: statedb.GetNonce(address), Code: hex.EncodeToString(statedb.GetCode(address)), Storage: storage}
	}
	return out
}

// LogRow is one native log.
type LogRow struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

func logRows(logs []*types.Log) []LogRow {
	out := []LogRow{}
	for _, l := range logs {
		row := LogRow{Address: hex.EncodeToString(l.Address[:]), Topics: []string{}, Data: hex.EncodeToString(l.Data)}
		for _, t := range l.Topics {
			row.Topics = append(row.Topics, hex.EncodeToString(t[:]))
		}
		out = append(out, row)
	}
	return out
}

// TxRow is the native output of one transaction.
type TxRow struct {
	Index       int                `json:"index"`
	Hash        string             `json:"hash"`
	Type        uint8              `json:"type"`
	From        string             `json:"from"`
	Nonce       uint64             `json:"nonce"`
	To          string             `json:"to"`
	Value       string             `json:"value"`
	Gas         uint64             `json:"gas"`
	GasPrice    string             `json:"gas_price,omitempty"`
	MaxFee      string             `json:"max_fee_per_gas,omitempty"`
	MaxPriority string             `json:"max_priority_fee_per_gas,omitempty"`
	Data        string             `json:"data"`
	Receipt     map[string]any     `json:"receipt"`
	Trace       json.RawMessage    `json:"call_tracer"`
	Before      map[string]Account `json:"state_before"`
	After       map[string]Account `json:"state_after"`
}

// BlockRow is the native output of one block.
type BlockRow struct {
	Number        uint64             `json:"number"`
	Hash          string             `json:"hash"`
	StateRoot     string             `json:"state_root"`
	BaseFee       *string            `json:"base_fee"`
	GasLimit      uint64             `json:"gas_limit"`
	GasUsed       uint64             `json:"gas_used"`
	BodyTxCount   int                `json:"body_tx_count"`
	Transactions  []TxRow            `json:"transactions"`
	BorReceipt    []LogRow           `json:"state_sync_receipt_logs"`
	HasBorReceipt bool               `json:"state_sync_receipt"`
	LastStateID   string             `json:"state_receiver_last_state_id"`
	StateAfter    map[string]Account `json:"state_after_block"`
}

func exportChain(run *nodeRun, blocks []*types.Block, addresses []common.Address) ([]BlockRow, error) {
	chain, config := run.chain, run.chain.Config()
	engine := run.eth.Engine().(*bor.Bor)
	tracer := "callTracer"
	api := tracers.NewAPI(run.eth.APIBackend)
	out := []BlockRow{}
	for _, want := range blocks {
		number := want.NumberU64()
		block := chain.GetBlockByNumber(number)
		if block == nil || block.Hash() != want.Hash() {
			return nil, fmt.Errorf("block %d is not the canonical corpus block", number)
		}
		header := block.Header()
		row := BlockRow{Number: number, Hash: hex.EncodeToString(block.Hash().Bytes()),
			StateRoot: hex.EncodeToString(block.Root().Bytes()), GasLimit: header.GasLimit, GasUsed: header.GasUsed,
			BodyTxCount: len(block.Transactions()), Transactions: []TxRow{}, BorReceipt: []LogRow{}}
		if header.BaseFee != nil {
			value := header.BaseFee.String()
			row.BaseFee = &value
		}
		receipts := chain.GetReceiptsByHash(block.Hash())
		if len(receipts) != len(block.Transactions()) {
			return nil, fmt.Errorf("block %d: %d receipts for %d transactions", number, len(receipts), len(block.Transactions()))
		}
		parentHeader := chain.GetHeader(header.ParentHash, number-1)
		statedb, err := chain.StateAt(parentHeader.Root)
		if err != nil {
			return nil, err
		}
		// Native re-execution of the block prefix on the parent state, as the
		// native processor applies it (author nil: the engine's author, the
		// recovered signer).
		evm := vm.NewEVM(core.NewEVMBlockContext(header, chain, nil), statedb, config, vm.Config{})
		preTransaction(evm, config, header)
		pool := new(core.GasPool).AddGas(header.GasLimit)
		used := uint64(0)
		signer := types.MakeSigner(config, header.Number, header.Time)
		for i, tx := range block.Transactions() {
			from, err := types.Sender(signer, tx)
			if err != nil {
				return nil, err
			}
			txRow := TxRow{Index: i, Hash: hex.EncodeToString(tx.Hash().Bytes()), Type: tx.Type(),
				From: hex.EncodeToString(from[:]), Nonce: tx.Nonce(), Value: tx.Value().String(), Gas: tx.Gas(),
				Data: hex.EncodeToString(tx.Data())}
			if tx.To() != nil {
				txRow.To = hex.EncodeToString(tx.To()[:])
			}
			if tx.Type() == types.DynamicFeeTxType {
				txRow.MaxFee, txRow.MaxPriority = tx.GasFeeCap().String(), tx.GasTipCap().String()
			} else {
				txRow.GasPrice = tx.GasPrice().String()
			}
			stored := receipts[i]
			txRow.Receipt = map[string]any{"status": stored.Status, "gas_used": stored.GasUsed,
				"cumulative_gas_used": stored.CumulativeGasUsed, "effective_gas_price": stored.EffectiveGasPrice.String(),
				"logs": logRows(stored.Logs)}
			trace, err := api.TraceTransaction(context.Background(), tx.Hash(), &tracers.TraceConfig{Tracer: &tracer})
			if err != nil {
				return nil, fmt.Errorf("trace block %d transaction %d: %w", number, i, err)
			}
			if txRow.Trace, err = json.Marshal(trace); err != nil {
				return nil, err
			}
			txRow.Before = readAccounts(statedb, addresses)
			statedb.SetTxContext(tx.Hash(), i)
			receipt, err := core.ApplyTransaction(evm, pool, statedb, header, tx, &used)
			if err != nil {
				return nil, fmt.Errorf("re-execute block %d transaction %d: %w", number, i, err)
			}
			// The re-execution must reproduce the stored receipt (the
			// effective gas price is derived from the transaction and the base
			// fee, not executed, so it is not compared).
			if receipt.Status != stored.Status || receipt.GasUsed != stored.GasUsed ||
				receipt.CumulativeGasUsed != stored.CumulativeGasUsed || len(receipt.Logs) != len(stored.Logs) {
				return nil, fmt.Errorf("block %d transaction %d: the re-execution differs from the stored receipt", number, i)
			}
			for j := range receipt.Logs {
				a, b := receipt.Logs[j], stored.Logs[j]
				if a.Address != b.Address || !bytes.Equal(a.Data, b.Data) || !slices.Equal(a.Topics, b.Topics) {
					return nil, fmt.Errorf("block %d transaction %d log %d: the re-execution differs", number, i, j)
				}
			}
			statedb.Finalise(true)
			txRow.After = readAccounts(statedb, addresses)
			row.Transactions = append(row.Transactions, txRow)
		}
		after, err := chain.StateAt(block.Root())
		if err != nil {
			return nil, err
		}
		row.StateAfter = readAccounts(after, addresses)
		if borReceipt := rawdb.ReadBorReceipt(run.eth.ChainDb(), block.Hash(), number, config); borReceipt != nil {
			row.HasBorReceipt = true
			row.BorReceipt = logRows(borReceipt.Logs)
		}
		last, err := engine.GenesisContractsClient.LastStateId(nil, number, block.Hash())
		if err != nil {
			return nil, fmt.Errorf("lastStateId at block %d: %w", number, err)
		}
		row.LastStateID = last.String()
		out = append(out, row)
	}
	return out, nil
}

// ---------------------------------------------------------------- decisions (from test/native_faults/bsc/faults_test.go)

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
		// As bsc_corpus.observation_of: one row per member; a decision
		// without members (a FINALITY change of the import) gives none.
		for _, member := range intent.Members {
			number, ok := numbers[strings.ToLower(strings.TrimPrefix(member.Block, "0x"))]
			if !ok {
				panic("decided intent of an unknown block: " + key)
			}
			rows = append(rows, row{key, number, intent.Kind})
		}
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
