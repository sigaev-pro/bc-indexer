// Native facts for the Bor early-fault handoffs. Each handoff runs in its
// own child process; the Python driver derives verdicts from these facts.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/borlifecycle"
)

var handoffWrap func(ethdb.KeyValueStore) ethdb.KeyValueStore

// holdStore stops one native head batch before it reaches the database.
// The underlying fault store still sees every write and sync.
type holdStore struct {
	ethdb.KeyValueStore
	once          sync.Once
	held, release chan struct{}
}

func (s *holdStore) NewBatch() ethdb.Batch {
	return &holdBatch{Batch: s.KeyValueStore.NewBatch(), store: s}
}

func (s *holdStore) NewBatchWithSize(size int) ethdb.Batch {
	return &holdBatch{Batch: s.KeyValueStore.NewBatchWithSize(size), store: s}
}

type holdBatch struct {
	ethdb.Batch
	store *holdStore
	head  bool
}

func (b *holdBatch) Put(key, value []byte) error {
	b.head = b.head || string(key) == "LastBlock"
	return b.Batch.Put(key, value)
}

func (b *holdBatch) Reset() { b.head = false; b.Batch.Reset() }

func (b *holdBatch) Write() error {
	if b.head && b.store.held != nil {
		b.store.once.Do(func() { close(b.store.held); <-b.store.release })
	}
	return b.Batch.Write()
}

func nativeDecided(n *instance, numbers map[string]uint64) []uint64 {
	rows, _ := decisions(prefixed(n.db, n.db, decisionPrefix), numbers)
	result := []uint64{}
	for _, row := range rows {
		m := row.(map[string]any)
		if m["kind"] == "IMPORT" || m["kind"] == "CANONICAL" {
			result = append(result, m["block"].(uint64))
		}
	}
	slices.Sort(result)
	return result
}

func runHandoff(c config, id string) caseResult {
	ci := load(c)
	dir := filepath.Join(c.work, id)
	check(os.MkdirAll(dir, 0o700))
	first := filepath.Join(dir, "node-1")
	var holder *holdStore
	if id == "bor.pass-at-golive" {
		handoffWrap = func(kv ethdb.KeyValueStore) ethdb.KeyValueStore {
			holder = &holdStore{KeyValueStore: kv}
			return holder
		}
	}
	mem := freshFS(chaindata(first))
	n, err := start(c, ci.in, fmt.Sprintf("%032x", 101), first, mem)
	check(err)
	r := caseResult{"id": id, "phase_at_start": n.phase()}
	switch id {
	case "bor.power-loss-model":
		check(n.run.GoLive())
		_, _, err = n.insert(ci.blocks)
		check(err)
		r["boundary_before"] = boundaryOf(n.db)
		r["head_before"] = n.chain.CurrentBlock().Number.Uint64()
		r["decided_before"] = nativeDecided(n, ci.numbers)
		root := ci.blocks[3].Root()
		previous := ci.blocks[2]
		r["predecessor"] = map[string]any{"number": previous.NumberU64(), "hash": plainHash(previous.Hash().Hex()), "root": plainHash(previous.Root().Hex())}
		r["roots_distinct"] = root != previous.Root()
		r["predecessor_state_before"] = n.chain.HasState(previous.Root())
		r["root_present_before_fixture"] = rawdb.HasLegacyTrieNode(n.db, root)
		// Match the accepted lifecycle probe's explicit missing-state
		// fixture. This deletion is not attributed to power loss: the
		// capture barrier makes the imported roots durable.
		rawdb.DeleteLegacyTrieNode(n.db, root)
		check(n.db.SyncKeyValue())
		r["fixture_root"] = root.Hex()
		r["fixture_deletion_synced"] = true
		const sentinel = "extractor-faults-unsynced-sentinel"
		check(n.db.Put([]byte(sentinel), []byte("discard")))
		r["sentinel_before_reset"], err = n.db.Has([]byte(sentinel))
		check(err)
		mem.SetIgnoreSyncs(true)
		mem.ResetToSyncedState()
		second := filepath.Join(dir, "node-2")
		clone := copyFS(mem, chaindata(first), chaindata(second))
		restarted, restartErr := start(c, ci.in, fmt.Sprintf("%032x", 101), second, clone)
		r["reset_to_synced"] = true
		r["restart_error"] = errText(restartErr)
		if restartErr != nil {
			return r
		}
		r["sentinel_after_reset"], err = restarted.db.Has([]byte(sentinel))
		check(err)
		r["fixture_root_after_reset"] = rawdb.HasLegacyTrieNode(restarted.db, root)
		r["predecessor_state_after"] = restarted.chain.HasState(previous.Root())
		r["boundary_after"] = boundaryOf(restarted.db)
		r["head_after"] = restarted.chain.CurrentBlock().Number.Uint64()
		r["phase_after"] = restarted.phase()
		r["recovery"] = restarted.run.Lifecycle.Report()
		_, undecided := decisions(prefixed(restarted.db, restarted.db, decisionPrefix), ci.numbers)
		r["undecided"] = undecided
		r["decisions_after"] = prefixed(restarted.db, restarted.db, decisionPrefix)
		n = restarted
	case "bor.below-h-rewind":
		_, err = n.chain.InsertChain(types.Blocks{ci.blocks[0]}, false)
		check(err)
		check(n.run.GoLive())
		_, err = n.chain.InsertChain(types.Blocks{ci.blocks[1]}, false)
		check(err)
		r["boundary_before"] = boundaryOf(n.db)
		r["head_before"] = n.chain.CurrentBlock().Hash().Hex()
		r["decisions_before"] = prefixed(n.db, n.db, decisionPrefix)
		r["rewind_error"] = errText(n.chain.SetHead(0))
		r["head_after"] = n.chain.CurrentBlock().Hash().Hex()
		r["boundary_after"] = boundaryOf(n.db)
		r["decisions_after"] = prefixed(n.db, n.db, decisionPrefix)
	case "bor.bootstrap-vote-pass", "bor.golive-order":
		_, err = n.chain.InsertChain(types.Blocks{ci.blocks[0]}, false)
		check(err)
		life := borlifecycle.Of(n.chain)
		d := n.eth.Downloader()
		r["coverage_before"] = life.Coverage()
		r["vote_locked"] = d.LockMutex(11)
		r["coverage_held"] = life.Coverage()
		r["boundary_held"] = boundaryOf(n.db)
		r["decisions_held"] = prefixed(n.db, n.db, decisionPrefix)
		// The hook reports a real failed TryLock on the same guard held
		// by the native vote. Only that fact releases the vote; callbacks
		// never manufacture a wait, and no sleep is used as evidence.
		contended := make(chan struct{})
		var traceMu sync.Mutex
		trace := []string{}
		add := func(step string) { traceMu.Lock(); trace = append(trace, step); traceMu.Unlock() }
		core.ExtractorFaultRecoveryStep = func(step string) {
			if step == "chain-locked" {
				add("chain-locked")
			}
		}
		sameGuard := true
		admission.ExtractorFaultGuardAcquire = func(guard *admission.Guard, step string) {
			sameGuard = sameGuard && guard == n.run.Import.Driver.Guard
			add("guard-" + step)
			if step == "contended" {
				close(contended)
			}
		}
		done := make(chan error, 1)
		go func() { done <- n.run.GoLive() }()
		<-contended
		r["boundary_before_vote_release"] = boundaryOf(n.db)
		r["decisions_before_golive"] = prefixed(n.db, n.db, decisionPrefix)
		add("vote-release")
		d.UnlockMutex(false, "", 11, common.Hash{})
		r["coverage_released"] = life.Coverage()
		r["golive_error"] = errText(<-done)
		core.ExtractorFaultRecoveryStep = nil
		admission.ExtractorFaultGuardAcquire = nil
		r["guard_matches_vote"] = sameGuard
		r["order"] = trace
		r["boundary"] = boundaryOf(n.db)
		r["phase"] = n.phase()
	case "bor.pass-at-golive":
		holder.held, holder.release = make(chan struct{}), make(chan struct{})
		imported := make(chan error, 1)
		go func() { _, e := n.chain.InsertChain(types.Blocks{ci.blocks[0]}, false); imported <- e }()
		<-holder.held
		r["native_head_write_held"] = true
		attempted := make(chan struct{})
		core.ExtractorFaultRecoveryStep = func(step string) {
			if step == "before-chain-lock" {
				close(attempted)
			}
		}
		live := make(chan error, 1)
		go func() { live <- n.run.GoLive() }()
		<-attempted
		r["golive_attempted"] = true
		r["boundary_during_import"] = boundaryOf(n.db)
		close(holder.release)
		r["import_error"] = errText(<-imported)
		r["golive_error"] = errText(<-live)
		core.ExtractorFaultRecoveryStep = nil
		r["boundary"] = boundaryOf(n.db)
		r["decided_before_block2"] = nativeDecided(n, ci.numbers)
		r["observations_before_block2"] = counted(prefixed(n.db, n.db, admissionPrefix))
		_, err = n.chain.InsertChain(types.Blocks{ci.blocks[1]}, false)
		r["block2_error"] = errText(err)
		r["decided"] = nativeDecided(n, ci.numbers)
	default:
		r["unsupported"] = true
	}
	r["block1"] = map[string]any{"hash": plainHash(ci.blocks[0].Hash().Hex()), "root": plainHash(ci.blocks[0].Root().Hex())}
	r["head"] = n.chain.CurrentBlock().Number.Uint64()
	return r
}
