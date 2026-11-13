package bsclifecycle

// The recovery driver inside the gate, the startup reconciliation and the
// live boundary H.
//
// Every start runs, after the native DB recovery of NewBlockChain:
//
//  1. The settlement of an unresolved lifecycle intent without a marker,
//     from native evidence (the decision recovery driver settles only
//     block-write intents).
//  2. The decision recovery driver (bscdecision.Hook.Recover): admission
//     obligations, decision.Open, the saved write intent, the replay of
//     undecided observations. Its imports run nested in recovery passes.
//  3. On a restart (RECOVERY), the startup reconciliation, then GoLive at
//     the stored H. On a first start the gate stays in BOOTSTRAP until
//     GoLive after the initial native sync.

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
	"github.com/ethereum/go-ethereum/log"
)

func (l *Lifecycle) recover() error {
	l.recovering = true
	defer func() { l.recovering = false }()
	if err := l.settleLifecycleIntent(); err != nil {
		return err
	}
	if err := l.hook.Recover(l.chain); err != nil {
		return err
	}
	if l.gate.Phase() == lifecycle.Recovery {
		if err := l.reconcile(); err != nil {
			return err
		}
	}
	l.recovering = false
	if l.gate.Phase() == lifecycle.Recovery {
		if _, err := l.GoLive(); err != nil {
			return err
		}
	}
	return nil
}

// lifecycleIntent reports whether an intent is a lifecycle decision: BSC
// block writes make only IMPORT and SIDE intents.
func lifecycleIntent(kind decision.Kind) bool {
	return kind == decision.Canonical || kind == decision.Finality
}

// settleLifecycleIntent settles an unresolved lifecycle intent without a
// marker, before the decision recovery driver, which would refuse it. It
// opens the bridge once to read and settle it; the driver opens it again.
//
// A FINALITY marker is in the same synced batch as the finalized write, so
// without the marker the setter did not take durable effect: the intent is
// rejected. A CANONICAL intent is completed (marker and view written, then
// resolved) when the native head is its new head, rejected when the native
// head is still its old head, and otherwise stops startup.
func (l *Lifecycle) settleLifecycleIntent() error {
	release := l.hook.Seam().Acquire()
	defer release()
	adm, token := l.hook.Seam().Admission()
	bridge, err := decision.Open(token, l.store, adm, l.storeID, l.roots, l.mirror)
	if err != nil {
		return err
	}
	decisions, err := bridge.Decisions(token)
	if err != nil {
		return err
	}
	var saved *decision.Decision
	for i := range decisions {
		if decisions[i].State == decision.Intended {
			saved = &decisions[i]
		}
	}
	if saved == nil || saved.Marker || !lifecycleIntent(saved.Intent.Kind) {
		return nil
	}
	head := l.chain.CurrentBlock()
	reject := func(evidence string) error {
		rejected, err := bridge.RejectIntent(token, saved.Sequence, NotAppliedEvidence(saved.Intent.Digest, head.Hash()))
		if err == nil {
			l.emit(bsc.Event{"event": "lifecycle-settle", "sequence": saved.Sequence, "outcome": "rejected",
				"evidence": evidence, "digest": rejected.Digest})
		}
		return err
	}
	switch {
	case saved.Intent.Kind == decision.Finality:
		return reject("finality-not-durable")
	case hexHash(head.Hash()) == saved.Intent.NewHead.Hash:
		value, err := decision.MarkerValue(saved.Intent)
		if err != nil {
			return err
		}
		records := append(l.View(head, rawdb.ReadFinalizedBlockHash(l.db)),
			decision.MarkerKey(adm.Producer(), saved.Sequence), value)
		if err := l.writeRecords(records); err != nil {
			return err
		}
		decided, err := bridge.Resolve(token, saved.Sequence)
		if err != nil {
			return err
		}
		l.emit(bsc.Event{"event": "lifecycle-settle", "sequence": saved.Sequence, "outcome": "completed",
			"evidence": "native-head-is-new-head", "digest": decided.Digest})
		return nil
	case hexHash(head.Hash()) == saved.Intent.OldHead.Hash:
		return reject("native-head-is-old-head")
	}
	return fmt.Errorf("%w: native head %d %s is neither the old nor the new head of intent %d", ErrLifecycle,
		head.Number.Uint64(), head.Hash(), saved.Sequence)
}

// isAncestor reports whether a is b or an ancestor of b in the stored
// headers.
func (l *Lifecycle) isAncestor(a *types.Header, b decision.Head) bool {
	header := l.chain.GetHeaderByHash(common.HexToHash(b.Hash))
	for header != nil && header.Number.Uint64() > a.Number.Uint64() {
		header = l.chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	}
	return header != nil && header.Hash() == a.Hash()
}

// reconcile is the startup reconciliation (bsc.startup-reconcile). Native
// startup repair runs before the gate opens, so the driver compares the
// native head and finalized block with the lifecycle view of the last
// decided state. A head that differs and is an ancestor of the view head
// (a native startup rewind) becomes one CANONICAL decision; a head below H,
// or one that is not an ancestor, is a source invariant error. A changed
// finalized block becomes one FINALITY decision.
func (l *Lifecycle) reconcile() error {
	view, ok, err := l.readView()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: a boundary without a lifecycle view", lifecycle.ErrCorrupt)
	}
	boundary, _ := l.gate.Boundary()
	release := l.Acquire()
	defer release()
	exit, err := l.Enter(callerReconcile)
	if err != nil {
		return err
	}
	defer exit()
	p, _ := l.top()
	head := l.chain.CurrentBlock()
	if hexHash(head.Hash()) != view.Head.Hash {
		if head.Number.Uint64() < boundary.Head.Number {
			return fmt.Errorf("%w: native head %d is below the boundary H %d", lifecycle.ErrInvariant,
				head.Number.Uint64(), boundary.Head.Number)
		}
		if !l.isAncestor(head, view.Head) {
			return fmt.Errorf("%w: native head %d %s is not an ancestor of the decided head %d %s", ErrLifecycle,
				head.Number.Uint64(), head.Hash(), view.Head.Number, view.Head.Hash)
		}
		in := decision.Intent{Kind: decision.Canonical, OldHead: view.Head, NewHead: headOf(head),
			Members: []decision.Member{}, Events: true}
		in.EvidenceDigest = CanonicalEvidence(callerReconcile, in.OldHead, in.NewHead)
		// The native change happened before the intent, so a failed batch is
		// never a native rejection: the intent stays unresolved and the next
		// start completes it.
		if err := l.intend(p, in, common.Hash{}); err != nil {
			return err
		}
		view.Head = headOf(head)
		if err := l.Settle(l.writeRecords(append(l.viewRecords(view), l.pending.marker...))); err != nil {
			return err
		}
	}
	native := l.finalizedHead(rawdb.ReadFinalizedBlockHash(l.db))
	old := decision.Head{Hash: lifecycle.ZeroHash}
	if view.Finalized != nil {
		old = *view.Finalized
	}
	if native != nil && old.Hash == native.Hash || native == nil && view.Finalized == nil {
		return nil
	}
	in := decision.Intent{Kind: decision.Finality, OldHead: headOf(head), NewHead: headOf(head), Events: true}
	if native == nil {
		in.EvidenceDigest = ClearEvidence(lifecycle.Finalized, callerReconcile, old)
	} else {
		evidence, err := lifecycle.FinalityDigest(lifecycle.FinalityChange{Field: lifecycle.Finalized,
			Caller: callerReconcile, Old: old, New: *native})
		if err != nil {
			return err
		}
		in.EvidenceDigest = evidence
	}
	if err := l.intend(p, in, common.Hash{}); err != nil {
		return err
	}
	view.Finalized = native
	return l.Settle(l.writeRecords(append(l.viewRecords(view), l.pending.marker...)))
}

// ---------------------------------------------------------------- live boundary

// GoLive switches the gate to LIVE. On a first start it reads the native
// head under the guard, flushes the state root of that head through the
// hash trie database and syncs it, writes the lifecycle view of H, and lets
// the gate record the boundary H before the decision bridge goes live. On a
// restart the gate keeps the stored H.
func (l *Lifecycle) GoLive() (lifecycle.Boundary, error) {
	release := l.hook.Seam().Acquire()
	defer release()
	token := l.token()
	bridge := l.hook.Bridge()
	if bridge == nil {
		return lifecycle.Boundary{}, fmt.Errorf("%w: the decision bridge is not open", lifecycle.ErrPhase)
	}
	head := l.chain.CurrentBlock()
	if l.gate.Phase() == lifecycle.Bootstrap {
		if err := l.chain.TrieDB().Commit(head.Root, false); err != nil {
			return lifecycle.Boundary{}, err
		}
		if err := l.db.SyncKeyValue(); err != nil {
			return lifecycle.Boundary{}, err
		}
		l.roots.Synced(head.Root)
		if err := l.writeRecords(l.View(head, rawdb.ReadFinalizedBlockHash(l.db))); err != nil {
			return lifecycle.Boundary{}, err
		}
	}
	b, err := l.gate.GoLive(token, headOf(head), hexHash(head.Root), l.roots, bridge)
	if err != nil {
		return lifecycle.Boundary{}, err
	}
	l.emit(bsc.Event{"event": "lifecycle-live", "boundary": b.Head, "root": b.Root})
	return b, nil
}

// LiveService is the node service that goes live after the initial native
// sync of a first start.
type LiveService struct {
	l      *Lifecycle
	synced func() bool
	every  time.Duration
	quit   chan struct{}
	done   chan struct{}
}

// Live returns the node service (node.Lifecycle) that calls GoLive once
// synced reports the end of the initial native sync. On a restart the gate
// is already live and the service does nothing.
func (l *Lifecycle) Live(synced func() bool) *LiveService {
	return &LiveService{l: l, synced: synced, every: 5 * time.Second, quit: make(chan struct{}),
		done: make(chan struct{})}
}

// Start starts the service.
func (s *LiveService) Start() error {
	if s.l.gate.Phase() != lifecycle.Bootstrap {
		close(s.done)
		return nil
	}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.every)
		defer ticker.Stop()
		for {
			select {
			case <-s.quit:
				return
			case <-ticker.C:
				if s.l.gate.Phase() != lifecycle.Bootstrap {
					return
				}
				if !s.synced() {
					continue
				}
				if b, err := s.l.GoLive(); err != nil {
					// Fail closed: a node that cannot set H must not keep
					// importing uncaptured.
					log.Crit("Extractor lifecycle could not go live", "err", err)
				} else {
					log.Info("Extractor lifecycle is live", "boundary", b.Head.Number, "hash", b.Head.Hash)
				}
				return
			}
		}
	}()
	return nil
}

// Stop stops the service.
func (s *LiveService) Stop() error {
	close(s.quit)
	<-s.done
	return nil
}
