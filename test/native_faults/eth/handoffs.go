// Native scenarios for the Geth early-fault handoffs. The Python driver
// judges the recorded facts; this probe does not assign verdicts.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	extractorgeth "github.com/ethereum/go-ethereum/extractor/admission/native"
)

// handoffStore intercepts native head writes. Batch writes use the same
// hook, and Reset clears their retained keys.
type handoffStore struct {
	ethdb.KeyValueStore
	before func([]string)
}

func (s *handoffStore) Put(k, v []byte) error {
	s.before([]string{string(k)})
	return s.KeyValueStore.Put(k, v)
}
func (s *handoffStore) Delete(k []byte) error {
	s.before([]string{string(k)})
	return s.KeyValueStore.Delete(k)
}
func (s *handoffStore) NewBatch() ethdb.Batch {
	return &handoffBatch{Batch: s.KeyValueStore.NewBatch(), store: s}
}
func (s *handoffStore) NewBatchWithSize(n int) ethdb.Batch {
	return &handoffBatch{Batch: s.KeyValueStore.NewBatchWithSize(n), store: s}
}

type handoffBatch struct {
	ethdb.Batch
	store *handoffStore
	keys  []string
}

func (b *handoffBatch) Put(k, v []byte) error {
	b.keys = append(b.keys, string(k))
	return b.Batch.Put(k, v)
}
func (b *handoffBatch) Delete(k []byte) error {
	b.keys = append(b.keys, string(k))
	return b.Batch.Delete(k)
}
func (b *handoffBatch) Reset()       { b.keys = nil; b.Batch.Reset() }
func (b *handoffBatch) Write() error { b.store.before(b.keys); return b.Batch.Write() }

func headWrite(keys []string) bool {
	for _, k := range keys {
		if k == "LastBlock" {
			return true
		}
	}
	return false
}

func handoffSetup(work, id, commit string, genesis *core.Genesis, index int) (string, *vfs.MemFS, extractorgeth.Config) {
	dir := filepath.Join(work, id)
	check(os.MkdirAll(filepath.Join(dir, "geth"), 0700))
	cfg := extractorgeth.Config{StoreID: fmt.Sprintf("b%031x", index), ProducerID: producerID,
		ChainID: genesis.Config.ChainID.String(), GenesisHash: genesis.ToBlock().Hash(), NativeCommit: commit, Profile: profile}
	check(extractorgeth.Preflight(filepath.Join(dir, "geth"), cfg))
	mem := vfs.NewCrashableMem()
	check(mem.MkdirAll(memDir, 0755))
	syncDirs(mem)
	return dir, mem, cfg
}

func awaitResult(ch <-chan error) error {
	select {
	case e := <-ch:
		return e
	case <-time.After(10 * time.Second):
		panic("handoff operation timed out")
	}
}

func passAtGoLive(work string, genesis *core.Genesis, blocks []*types.Block, commit string) caseResult {
	dir, mem, cfg := handoffSetup(work, "geth.pass-at-golive", commit, genesis, 100)
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	active := false
	n, err := openHandoff(mem, dir, genesis, cfg, nil, extractorgeth.LifecycleOptions{}, func(kv ethdb.KeyValueStore) ethdb.KeyValueStore {
		return &handoffStore{KeyValueStore: kv, before: func(keys []string) {
			if active && headWrite(keys) {
				once.Do(func() { close(held); <-release })
			}
		}}
	})
	check(err)
	active = true
	imported := make(chan error, 1)
	go func() { _, e := n.chain.InsertChain(types.Blocks{blocks[0]}); imported <- e }()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		panic("BOOTSTRAP import never reached head write")
	}
	lived := make(chan error, 1)
	go func() { _, e := n.life.GoLive(); lived <- e }()
	row := caseResult{"held": true, "phase_during_pass": phaseText(n.life.Gate().Phase())}
	select {
	case e := <-lived:
		row["returned_during_pass"] = true
		row["during_error"] = errText(e)
	case <-time.After(200 * time.Millisecond):
		row["returned_during_pass"] = false
	}
	row["boundary_during_pass"] = boundaryOf(n.db)
	close(release)
	row["import_error"] = errText(awaitResult(imported))
	if row["returned_during_pass"] == false {
		row["golive_error"] = errText(awaitResult(lived))
	} else {
		_, e := n.life.GoLive()
		row["golive_error"] = errText(e)
	}
	row["boundary"] = boundaryOf(n.db)
	row["block1"] = map[string]any{"hash": plainHash(blocks[0].Hash().Hex()), "root": plainHash(blocks[0].Root().Hex())}
	row["observations_before_block2"] = counted(prefixed(n.db, n.db, admissionPrefix))
	row["decisions_before_block2"] = counted(prefixed(n.db, n.db, decisionPrefix))
	_, err = n.chain.InsertChain(types.Blocks{blocks[1]})
	row["block2_error"] = errText(err)
	row["head"] = n.chain.CurrentBlock().Number.Uint64()
	row["decisions"] = prefixed(n.db, n.db, decisionPrefix)
	return row
}

// branchInputs forks after block 2, leaving four old-branch blocks. The
// new branch has five blocks, so a stop before its final head batch leaves
// a head strictly inside the new branch.
func branchInputs(genesis *core.Genesis) ([]*types.Block, []*types.Block) {
	makeBranch := func(n int, extra byte) []*types.Block {
		_, blocks, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), n, func(i int, b *core.BlockGen) {
			b.SetCoinbase(coinbase)
			if i >= 2 {
				b.SetExtra([]byte{extra})
			}
		})
		return blocks
	}
	return makeBranch(6, 1), makeBranch(7, 2)
}

func interrupted(work, id, commit string, genesis *core.Genesis, index int) caseResult {
	dir, mem, cfg := handoffSetup(work, id, commit, genesis, index)
	old, next := branchInputs(genesis)
	armed := false
	var n *node
	var err error
	headWrites := 0
	var wrap func(ethdb.KeyValueStore) ethdb.KeyValueStore
	if id == "geth.sethead-interrupted" {
		wrap = func(store ethdb.KeyValueStore) ethdb.KeyValueStore {
			return &handoffStore{KeyValueStore: store, before: func(keys []string) {
				if !armed || !headWrite(keys) {
					return
				}
				headWrites++
				if headWrites == 2 {
					// The first native marker batch persisted head 5. Stop
					// before the second batch can persist head 4; target is 3.
					check(n.db.SyncKeyValue())
					panic(faultHit{id})
				}
			}}
		}
	}
	n, err = openHandoff(mem, dir, genesis, cfg, nil, extractorgeth.LifecycleOptions{Point: func(p string) {
		if armed && id == "geth.mid-reorg-long-branch" && p == "final" {
			check(n.db.SyncKeyValue())
			panic(faultHit{id})
		}
	}}, wrap)
	check(err)
	_, err = n.chain.InsertChain(types.Blocks{old[0]})
	check(err)
	_, err = n.life.GoLive()
	check(err)
	_, err = n.chain.InsertChain(types.Blocks(old[1:]))
	check(err)
	if id == "geth.mid-reorg-long-branch" {
		for _, b := range next[2:] {
			_, err = n.chain.InsertBlockWithoutSetHead(context.Background(), b, false)
			check(err)
		}
	}
	armed = true
	hit, stepErr := guarded(func() error {
		if id == "geth.sethead-interrupted" {
			return n.chain.SetHead(3)
		}
		_, e := n.chain.SetCanonical(next[6])
		return e
	})
	row := caseResult{"hit": hit, "step_error": errText(stepErr), "old_head": plainHash(old[5].Hash().Hex()),
		"records_before": prefixed(n.db, n.db, decisionPrefix), "head_before_restart": plainHash(rawdb.ReadHeadBlockHash(n.db).Hex())}
	if id == "geth.sethead-interrupted" {
		row["head_write_attempts"] = headWrites
		row["intermediate"] = map[string]any{"number": old[4].NumberU64(), "hash": plainHash(old[4].Hash().Hex()), "root": plainHash(old[4].Root().Hex())}
	}
	target := old[2]
	if id == "geth.mid-reorg-long-branch" {
		target = next[6]
	}
	row["target"] = map[string]any{"number": target.NumberU64(), "hash": plainHash(target.Hash().Hex()), "root": plainHash(target.Root().Hex())}
	clone := mem.CrashClone(vfs.CrashCloneCfg{UnsyncedDataPercent: 0})
	events := []map[string]any{}
	r, restartErr := open(clone, dir, genesis, cfg, func(event string, fields map[string]any) {
		if event == "settling" {
			m := map[string]any{"event": event}
			for k, v := range fields {
				m[k] = v
			}
			events = append(events, m)
		}
	})
	row["restart_error"] = errText(restartErr)
	row["settled"] = events
	if restartErr == nil {
		row["phase"] = phaseText(r.life.Gate().Phase())
		row["head"] = nativeAt(r.db, r.chain.CurrentBlock().Number.Uint64())
		row["boundary"] = boundaryOf(r.db)
		row["records_after"] = prefixed(r.db, r.db, decisionPrefix)
		_, e := r.chain.StateAt(target.Root(), target.Number(), target.Time())
		row["state_error"] = errText(e)
	}
	row["old_branch_length"] = len(old) - 2
	return row
}

func runHandoffs(work string, genesis *core.Genesis, blocks []*types.Block, commit string) map[string]any {
	return map[string]any{
		"geth.pass-at-golive":        passAtGoLive(work, genesis, blocks, commit),
		"geth.sethead-interrupted":   interrupted(work, "geth.sethead-interrupted", commit, genesis, 101),
		"geth.mid-reorg-long-branch": interrupted(work, "geth.mid-reorg-long-branch", commit, genesis, 102),
	}
}
