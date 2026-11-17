// The BSC handoffs of the E01-04.early_faults catalog (part B), run by
// TestBSCFaults after the 30 catalog cases and written to the probe report as
// handoff_runs. Each scenario records native facts only; faults_bsc.py
// derives the handoff verdicts.
//
//   - bsc.pass-at-golive: a canonical import held open in BOOTSTRAP while
//     GoLive runs.
//   - bsc.skip-block: a crash after the block batch of a decision and before
//     its head batch, so recovery imports a stored block again on a chain
//     without snapshots, where native skipBlock would skip it.
//   - bsc.store-write-errors: a store error, not a crash, at five places of
//     the decision step.
//
// Installed next to faults_test.go as extractor/bsc/faults/handoffs_test.go.
package bscfaults_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/vfs"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

// errInjected is the store error of the store-write-errors scenario.
var errInjected = errors.New("extractor-faults: injected store error")

// headKey is the native head block key (rawdb headBlockKey).
const headKey = "LastBlock"

// actionStore sits between the fault store and the database. Its hooks see
// every key write and sync before they reach the store: a write hook may
// block, return an error (the write is not applied) or panic; a sync hook may
// return an error (the sync does not run).
type actionStore struct {
	ethdb.KeyValueStore
	mu      sync.Mutex
	onWrite func(keys []string) error
	onSync  func() error
}

func (s *actionStore) write(keys []string) error {
	s.mu.Lock()
	hook := s.onWrite
	s.mu.Unlock()
	if hook == nil {
		return nil
	}
	return hook(keys)
}

func (s *actionStore) Put(key, value []byte) error {
	if err := s.write([]string{string(key)}); err != nil {
		return err
	}
	return s.KeyValueStore.Put(key, value)
}

func (s *actionStore) Delete(key []byte) error {
	if err := s.write([]string{string(key)}); err != nil {
		return err
	}
	return s.KeyValueStore.Delete(key)
}

func (s *actionStore) SyncKeyValue() error {
	s.mu.Lock()
	hook := s.onSync
	s.mu.Unlock()
	if hook != nil {
		if err := hook(); err != nil {
			return err
		}
	}
	return s.KeyValueStore.SyncKeyValue()
}

func (s *actionStore) NewBatch() ethdb.Batch {
	return &actionBatch{Batch: s.KeyValueStore.NewBatch(), store: s}
}

func (s *actionStore) NewBatchWithSize(size int) ethdb.Batch {
	return &actionBatch{Batch: s.KeyValueStore.NewBatchWithSize(size), store: s}
}

type actionBatch struct {
	ethdb.Batch
	store *actionStore
	keys  []string
}

func (b *actionBatch) Put(key, value []byte) error {
	b.keys = append(b.keys, string(key))
	return b.Batch.Put(key, value)
}

func (b *actionBatch) Delete(key []byte) error {
	b.keys = append(b.keys, string(key))
	return b.Batch.Delete(key)
}

func (b *actionBatch) Reset() {
	b.keys = nil
	b.Batch.Reset()
}

func (b *actionBatch) Write() error {
	if err := b.store.write(b.keys); err != nil {
		return err
	}
	return b.Batch.Write()
}

func anyKey(keys []string, match func(string) bool) bool {
	for _, key := range keys {
		if match(key) {
			return true
		}
	}
	return false
}

func native(key string) bool { return !strings.HasPrefix(key, "extractor-") }

// trieNode reports a hash-scheme trie node key: a bare 32-byte hash.
func trieNode(key string) bool { return len(key) == common.HashLength }

// within runs step and waits at most timeout for it.
func within(timeout time.Duration, step func() error) (done bool, err error) {
	result := make(chan error, 1)
	go func() { result <- step() }()
	select {
	case err := <-result:
		return true, err
	case <-time.After(timeout):
		return false, nil
	}
}

// decidedBlocks lists the blocks of the decided IMPORT or CANONICAL members.
func decidedBlocks(db ethdb.Database, numbers map[string]uint64) []uint64 {
	rows, _ := decisions(prefixed(db, db, decisionPrefix), numbers)
	out := []uint64{}
	for _, row := range rows {
		m := row.(map[string]any)
		if m["kind"] == "IMPORT" || m["kind"] == "CANONICAL" {
			out = append(out, m["block"].(uint64))
		}
	}
	return out
}

// restartAndExport abandons the node, copies its store as a crash leaves it,
// reads the durable states and restarts the node, then finishes as a catalog
// case does.
func restartAndExport(t *testing.T, mem *vfs.MemFS, dir, storeID string, in inputs, numbers map[string]uint64,
	result caseResult) {
	result["wal_settle_ms"] = settleWAL(mem)
	clone := copyFS(mem)
	pebble.ExtractorFS = clone
	kv, err := pebble.New(memDir, 64, 64, "", false)
	check(err)
	ancient, err := os.MkdirTemp(dir, "ancient-")
	check(err)
	raw, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: ancient})
	check(err)
	after := states(raw)
	if len(in.blocks) > 0 {
		block := in.blocks[0]
		after["block1_stored"] = rawdb.ReadHeader(raw, block.Hash(), block.NumberU64()) != nil
		after["block1_state"] = rawdb.HasLegacyTrieNode(raw, block.Root())
		after["head"] = hexOf(rawdb.ReadHeadBlockHash(raw).Bytes())
	}
	check(raw.Close())
	opened := ""
	emit := func(event bsc.Event) {
		if event["event"] == "gate-opened" && opened == "" {
			opened, _ = event["phase"].(string)
		}
	}
	r, err := open(clone, dir, storeID, in, emit)
	result["restart_error"] = errText(err)
	result["after_restart"] = after
	if err != nil {
		return
	}
	after["mode"] = opened
	result["snapshots"] = r.chain.Snapshots() != nil
	result["decided_after_recovery"] = decidedBlocks(r.db, numbers)
	recoverAndExport(t, r, in, numbers, result)
}

// passAtGoLive holds the BOOTSTRAP import of block 1 at its native head
// write while GoLive runs, then releases it.
func passAtGoLive(t *testing.T, work string, in inputs, numbers map[string]uint64) caseResult {
	dir := filepath.Join(work, "bsc.pass-at-golive")
	check(os.MkdirAll(dir, 0o700))
	result := caseResult{"id": "bsc.pass-at-golive"}
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var store *actionStore
	wrap := func(kv ethdb.KeyValueStore) ethdb.KeyValueStore {
		store = &actionStore{KeyValueStore: kv}
		return store
	}
	n, err := openWrapped(freshFS(), dir, fmt.Sprintf("%032x", 101), in, nil, wrap)
	check(err)
	result["phase_at_start"] = string(n.life.Gate().Phase())
	store.mu.Lock()
	store.onWrite = func(keys []string) error {
		if anyKey(keys, func(key string) bool { return key == headKey }) {
			once.Do(func() {
				close(held)
				<-release
			})
		}
		return nil
	}
	store.mu.Unlock()
	imported := make(chan error, 1)
	go func() {
		_, err := n.chain.InsertChain(types.Blocks{in.blocks[0]})
		imported <- err
	}()
	select {
	case <-held:
		result["held"] = true
	case err := <-imported:
		result["held"] = false
		result["import_error"] = errText(err)
		return result
	case <-time.After(60 * time.Second):
		result["held"] = false
		return result
	}
	// GoLive while the import is held: it must not set H before the import
	// ends. It is either refused or waits for the import.
	lived := make(chan error, 1)
	go func() {
		_, err := n.life.GoLive()
		lived <- err
	}()
	during := map[string]any{"returned": false}
	select {
	case err := <-lived:
		during = map[string]any{"returned": true, "error": errText(err), "order": errors.Is(err, lifecycle.ErrOrder)}
	case <-time.After(3 * time.Second):
	}
	result["golive_during_pass"] = during
	result["phase_during_pass"] = string(n.life.Gate().Phase())
	close(release)
	result["import_error"] = errText(<-imported)
	if during["returned"] == false {
		result["golive_error"] = errText(<-lived)
	} else {
		_, err := n.life.GoLive()
		result["golive_error"] = errText(err)
	}
	store.mu.Lock()
	store.onWrite = nil
	store.mu.Unlock()
	if b, ok := n.life.Gate().Boundary(); ok {
		result["boundary"] = map[string]any{"number": b.Head.Number, "hash": plainHash(b.Head.Hash), "root": plainHash(b.Root)}
	}
	result["block1"] = map[string]any{"hash": hexOf(in.blocks[0].Hash().Bytes()), "root": hexOf(in.blocks[0].Root().Bytes())}
	result["observations_before_block2"] = counted(prefixed(n.db, n.db, admissionPrefix))
	result["decided_before_block2"] = decidedBlocks(n.db, numbers)
	_, err = n.chain.InsertChain(types.Blocks{in.blocks[1]})
	result["block2_error"] = errText(err)
	result["decided"] = decidedBlocks(n.db, numbers)
	result["head"] = n.chain.CurrentBlock().Number.Uint64()
	return result
}

// skipBlock stops the decision of block 1 after its block batch. BSC writes
// the head batch in a goroutine of writeHeadBlock, and the import waits for
// it; so the fault syncs the store (the outcome in which the block batch
// reached the disk) and then stops that goroutine for good before the head
// batch is applied. The probe abandons the node, as at a crash.
func skipBlock(t *testing.T, work string, in inputs, numbers map[string]uint64) caseResult {
	const id = "bsc.skip-block"
	dir := filepath.Join(work, id)
	check(os.MkdirAll(dir, 0o700))
	result := caseResult{"id": id}
	storeID := fmt.Sprintf("%032x", 102)
	var store *actionStore
	wrap := func(kv ethdb.KeyValueStore) ethdb.KeyValueStore {
		store = &actionStore{KeyValueStore: kv}
		return store
	}
	mem := freshFS()
	n, err := openWrapped(mem, dir, storeID, in, nil, wrap)
	check(err)
	_, err = n.life.GoLive()
	check(err)
	stopped := make(chan error, 1)
	var once sync.Once
	store.mu.Lock()
	store.onWrite = func(keys []string) error {
		if anyKey(keys, func(key string) bool { return key == headKey }) {
			once.Do(func() { stopped <- store.KeyValueStore.SyncKeyValue() })
			select {} // the head batch is never applied
		}
		return nil
	}
	store.mu.Unlock()
	imported := make(chan error, 1)
	go func() {
		_, err := n.chain.InsertChain(types.Blocks{in.blocks[0]})
		imported <- err
	}()
	select {
	case err := <-stopped:
		result["fault"] = map[string]any{"hit": err == nil}
	case err := <-imported:
		result["fault"] = map[string]any{"hit": false}
		result["step_error"] = errText(err)
	case <-time.After(60 * time.Second):
		result["fault"] = map[string]any{"hit": false}
	}
	restartAndExport(t, mem, dir, storeID, in, numbers, result)
	return result
}

// storeErrors are the store errors of the decision step of block 1, after
// GoLive at the genesis.
var storeErrors = []string{"barrier-sync", "intent-sync", "block-batch", "final-sync", "decided-write"}

// storeErrorHooks arms one store error. Only the importing goroutine writes
// to the store while the hooks are set.
func storeErrorHooks(point string, fired *bool) (func([]string) error, func() error) {
	var execReady, execSynced, trie, intent, intentSynced, marker bool
	record := func(prefix, end string) func(string) bool {
		return func(key string) bool { return strings.HasPrefix(key, prefix) && strings.HasSuffix(key, end) }
	}
	onWrite := func(keys []string) error {
		if *fired {
			return nil
		}
		execReady = execReady || anyKey(keys, record(admissionPrefix, "/exec-ready"))
		trie = trie || (execSynced && anyKey(keys, trieNode))
		intent = intent || anyKey(keys, record(decisionPrefix, "/intent"))
		marker = marker || anyKey(keys, record(markerPrefix, ""))
		if (point == "block-batch" && intentSynced && anyKey(keys, native)) ||
			(point == "decided-write" && anyKey(keys, record(decisionPrefix, "/decided"))) {
			*fired = true
			return errInjected
		}
		return nil
	}
	onSync := func() error {
		if *fired {
			return nil
		}
		if (point == "barrier-sync" && trie) || (point == "intent-sync" && intent) ||
			(point == "final-sync" && marker) {
			*fired = true
			return errInjected
		}
		execSynced = execSynced || execReady
		intentSynced = intentSynced || intent
		return nil
	}
	return onWrite, onSync
}

func storeWriteError(t *testing.T, work, point string, index int, in inputs, numbers map[string]uint64) caseResult {
	dir := filepath.Join(work, "bsc.store-write-errors-"+point)
	check(os.MkdirAll(dir, 0o700))
	result := caseResult{"point": point}
	storeID := fmt.Sprintf("%032x", 110+index)
	var store *actionStore
	wrap := func(kv ethdb.KeyValueStore) ethdb.KeyValueStore {
		store = &actionStore{KeyValueStore: kv}
		return store
	}
	mem := freshFS()
	n, err := openWrapped(mem, dir, storeID, in, nil, wrap)
	check(err)
	_, err = n.life.GoLive()
	check(err)
	fired := false
	onWrite, onSync := storeErrorHooks(point, &fired)
	store.mu.Lock()
	store.onWrite, store.onSync = onWrite, onSync
	store.mu.Unlock()
	_, stepErr := n.chain.InsertChain(types.Blocks{in.blocks[0]})
	store.mu.Lock()
	store.onWrite, store.onSync = nil, nil
	store.mu.Unlock()
	result["injected"] = fired
	result["step_error"] = errText(stepErr)
	// The live node after the error: block 2 must not be decided.
	done, nextErr := within(30*time.Second, func() error {
		_, e := n.chain.InsertChain(types.Blocks{in.blocks[1]})
		return e
	})
	result["block2_live"] = map[string]any{"returned": done, "error": errText(nextErr)}
	result["decided_before_restart"] = decidedBlocks(n.db, numbers)
	restartAndExport(t, mem, dir, storeID, in, numbers, result)
	return result
}

// runHandoffs runs the BSC handoff scenarios.
func runHandoffs(t *testing.T, work string, in inputs, numbers map[string]uint64) map[string]any {
	errorsRun := []any{}
	for index, point := range storeErrors {
		errorsRun = append(errorsRun, storeWriteError(t, work, point, index, in, numbers))
	}
	return map[string]any{
		"bsc.pass-at-golive":     passAtGoLive(t, work, in, numbers),
		"bsc.skip-block":         skipBlock(t, work, in, numbers),
		"bsc.store-write-errors": errorsRun,
	}
}
