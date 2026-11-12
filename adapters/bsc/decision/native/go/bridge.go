// Package bscdecision wires the shared decision core (E01-02.decision_bridge)
// into the BSC import path, on top of the BSC admission seam (package bsc).
//
// The driver test/harness/bsc_decision.py installs this package at
// extractor/bsc/decision of the accepted BSC source tree, next to
// extractor/bsc and the shared packages extractor/admission,
// extractor/decision, extractor/source_inputs and extractor/state_provider,
// and applies the admission patches and ../patches. The design is in
// ../README.md.
package bscdecision

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	"github.com/ethereum/go-ethereum/extractor/decision"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
)

var (
	// ErrDeferred refuses the decision of a block while the bridge is not
	// open: during the admission part of recovery, an ADMITTED obligation is
	// re-executed to EXEC_READY and decided after the bridge opens.
	ErrDeferred = errors.New("decision bridge is not open yet")
	// ErrTransition reports an unresolved intent that the import cannot
	// complete as saved: another block, or a fork choice, finality or member
	// that differs from the saved intent.
	ErrTransition = errors.New("saved transition differs")
)

// Purger is the admission InputPurger over the source inputs core: closing
// an observation deletes its capsule and dependency log (D100).
type Purger struct {
	DB sourceinputs.Deleter
}

// Purge deletes the capsule and dependency log of the observation.
func (p Purger) Purge(observation string) error { return sourceinputs.Purge(p.DB, observation) }

// Roots is the child-root barrier of the hash trie database. A root is
// durable when the barrier of this process synced it and its node is in the
// store. A root node that a store holds after a restart does not prove that
// its subtrie is complete (the trie flush writes several batches), so a
// restart replays every undecided observation, whose barrier then syncs it.
type Roots struct {
	db     ethdb.KeyValueReader
	mu     sync.Mutex
	synced map[common.Hash]bool
}

// NewRoots returns the barrier record of a native store.
func NewRoots(db ethdb.KeyValueReader) *Roots {
	return &Roots{db: db, synced: map[common.Hash]bool{}}
}

// Synced records a root whose barrier (commit, trie flush, sync) returned.
func (r *Roots) Synced(root common.Hash) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.synced[root] = true
}

// Durable implements decision.Roots.
func (r *Roots) Durable(root string) (bool, error) {
	raw, err := hex.DecodeString(root)
	if err != nil || len(raw) != common.HashLength {
		return false, fmt.Errorf("%w: root %q", decision.ErrInvalid, root)
	}
	hash := common.BytesToHash(raw)
	r.mu.Lock()
	synced := r.synced[hash]
	r.mu.Unlock()
	return synced && rawdb.HasLegacyTrieNode(r.db, hash), nil
}

// Config configures a Hook.
type Config struct {
	// Seam is the configuration of the admission seam. Start sets its
	// Cleanup to a Purger over the store.
	Seam bsc.Config
	// Mirror is the decision mirror (E05-01.registry).
	Mirror decision.Mirror
}

// Hook is the core.ExtractorHook of a node with the decision bridge. It
// wraps the admission seam: every observation it returns also implements
// core.ExtractorDecider. Create it with Start, install it on the chain, and
// run Recover before live import.
type Hook struct {
	seam    *bsc.Seam
	db      ethdb.Database
	store   admission.DeleteStore
	storeID string
	roots   *Roots
	mirror  decision.Mirror
	emit    func(bsc.Event)
	bridge  *decision.Bridge
}

// Start opens the admission seam with the input cleanup of D100 (closing an
// observation deletes its capsule and log). The bridge opens in Recover,
// after the admission obligations are resolved.
func Start(guard *admission.Guard, db ethdb.Database, chain *core.BlockChain, cfg Config) (*Hook, error) {
	if cfg.Mirror == nil {
		return nil, fmt.Errorf("%w: missing mirror", decision.ErrInvalid)
	}
	var store admission.DeleteStore = db
	if cfg.Seam.Store != nil {
		deleter, ok := cfg.Seam.Store.(admission.DeleteStore)
		if !ok {
			return nil, fmt.Errorf("%w: the store cannot delete records", decision.ErrInvalid)
		}
		store = deleter
	}
	seamConfig := cfg.Seam
	seamConfig.Store, seamConfig.Cleanup = store, Purger{DB: store}
	seam, err := bsc.Start(guard, db, chain, seamConfig)
	if err != nil {
		return nil, err
	}
	emit := cfg.Seam.Emit
	if emit == nil {
		emit = func(bsc.Event) {}
	}
	return &Hook{seam: seam, db: db, store: store, storeID: cfg.Seam.Identity.StoreID, roots: NewRoots(db),
		mirror: cfg.Mirror, emit: emit}, nil
}

// Seam returns the admission seam.
func (h *Hook) Seam() *bsc.Seam { return h.seam }

// Bridge returns the decision bridge, or nil before Recover opens it.
func (h *Hook) Bridge() *decision.Bridge { return h.bridge }

// Acquire implements core.ExtractorHook.
func (h *Hook) Acquire() func() { return h.seam.Acquire() }

// withGuard runs f with the observation guard held.
func (h *Hook) withGuard(f func(token *admission.Token) error) error {
	release := h.seam.Acquire()
	defer release()
	_, token := h.seam.Admission()
	return f(token)
}

// unresolved returns the unresolved intent, if any. The guard is held.
func (h *Hook) unresolved(token *admission.Token) (*decision.Decision, error) {
	if h.bridge == nil {
		return nil, nil
	}
	decisions, err := h.bridge.Decisions(token)
	if err != nil {
		return nil, err
	}
	for i := range decisions {
		if decisions[i].State == decision.Intended {
			return &decisions[i], nil
		}
	}
	return nil, nil
}

// execReady returns the open EXEC_READY observation of a block, if any.
func execReady(adm *admission.Admission, token *admission.Token, hash common.Hash) (*admission.Observation, error) {
	observations, err := adm.Observations(token)
	if err != nil {
		return nil, err
	}
	want := hex.EncodeToString(hash.Bytes())
	for i := range observations {
		if observations[i].State == admission.ExecReady && observations[i].Candidate.Hash == want {
			return &observations[i], nil
		}
	}
	return nil, nil
}

// Begin implements core.ExtractorHook. While an intent is unresolved, only
// its member block is imported, which completes it. A block with an open
// EXEC_READY observation (one without a decision) is replayed natively
// (Seam.Replay) instead of observed again; any other block goes to the
// admission seam.
func (h *Hook) Begin(chain *core.BlockChain, block *types.Block) (core.ExtractorObservation, error) {
	adm, token := h.seam.Admission()
	if token == nil {
		return nil, bsc.ErrGuard
	}
	pending, err := h.unresolved(token)
	if err != nil {
		return nil, err
	}
	if pending != nil && !names(pending.Intent, block.Hash()) {
		return nil, fmt.Errorf("%w: intent %d is unresolved", decision.ErrPending, pending.Sequence)
	}
	ready, err := execReady(adm, token, block.Hash())
	if err != nil {
		return nil, err
	}
	var inner core.ExtractorObservation
	if ready != nil && h.bridge == nil {
		// Recovery replays it after the bridge opens.
		return nil, fmt.Errorf("%w: %s is EXEC_READY", ErrDeferred, ready.Key)
	}
	if ready != nil {
		inner, err = h.seam.Replay(chain, block)
	} else {
		inner, err = h.seam.Begin(chain, block)
	}
	if err != nil || inner == nil {
		return nil, err
	}
	return &observation{ExtractorObservation: inner, hook: h}, nil
}

func names(intent decision.IntentRecord, hash common.Hash) bool {
	want := hex.EncodeToString(hash.Bytes())
	for _, m := range intent.Members {
		if m.Block == want {
			return true
		}
	}
	return false
}

// observation adds the decision step to an admission observation.
type observation struct {
	core.ExtractorObservation
	hook *Hook
}

// Intend implements core.ExtractorDecider. The barrier synced the child
// root just before; Intend records it, computes the decision evidence (fork
// choice and finality), and records the transition intent, or checks that
// the unresolved intent saved before a restart is exactly this transition.
func (o *observation) Intend(chain *core.BlockChain, block *types.Block, head *types.Header,
	canonical bool) (core.ExtractorDecision, error) {
	h := o.hook
	if h.bridge == nil {
		return nil, ErrDeferred
	}
	adm, token := h.seam.Admission()
	h.roots.Synced(block.Root())
	h.emit(bsc.Event{"event": "barrier", "number": block.NumberU64(), "root": hex.EncodeToString(block.Root().Bytes())})
	ready, err := execReady(adm, token, block.Hash())
	if err != nil {
		return nil, err
	}
	if ready == nil {
		return nil, fmt.Errorf("%w: block %d has no EXEC_READY observation", decision.ErrBinding, block.NumberU64())
	}
	evidence, err := DecisionEvidence(chain, h.seam, ready.Key, block, head, canonical)
	if err != nil {
		h.emit(bsc.Event{"event": "evidence-refused", "observation": ready.Key, "error": err.Error()})
		return nil, err
	}
	intent := decision.Intent{Kind: evidence.Kind, OldHead: evidence.OldHead, NewHead: evidence.NewHead,
		Members: []decision.Member{{Observation: ready.Key, ExecReadyDigest: ready.ExecReady.Digest,
			ChildRoot: hex.EncodeToString(block.Root().Bytes())}},
		EvidenceDigest: evidence.Digest, Events: evidence.Kind == decision.Import}
	h.emit(evidence.event(ready.Key))
	pending, err := h.unresolved(token)
	if err != nil {
		return nil, err
	}
	var record decision.IntentRecord
	if pending != nil {
		if !sameTransition(pending.Intent, intent, hex.EncodeToString(block.Hash().Bytes())) {
			return nil, fmt.Errorf("%w: intent %d", ErrTransition, pending.Sequence)
		}
		record = pending.Intent
		h.emit(bsc.Event{"event": "intent-resumed", "observation": ready.Key, "sequence": record.Sequence,
			"digest": record.Digest})
	} else {
		if record, err = h.bridge.Intend(token, intent); err != nil {
			return nil, err
		}
		h.emit(bsc.Event{"event": "intent", "observation": ready.Key, "sequence": record.Sequence,
			"kind": string(record.Kind), "digest": record.Digest})
	}
	marker, err := decision.MarkerValue(record)
	if err != nil {
		return nil, err
	}
	return &step{hook: h, record: record, observation: ready.Key, finalized: evidence.finalized,
		key: decision.MarkerKey(adm.Producer(), record.Sequence), value: marker}, nil
}

// sameTransition reports whether a saved intent is exactly the transition
// that the import proposes now.
func sameTransition(saved decision.IntentRecord, in decision.Intent, block string) bool {
	if saved.Kind != in.Kind || saved.OldHead != in.OldHead || saved.NewHead != in.NewHead ||
		saved.EvidenceDigest != in.EvidenceDigest || saved.Events != in.Events || len(saved.Members) != 1 ||
		len(in.Members) != 1 {
		return false
	}
	m, want := saved.Members[0], in.Members[0]
	return m.Observation == want.Observation && m.Block == block && m.ExecReadyDigest == want.ExecReadyDigest &&
		m.ChildRoot == want.ChildRoot
}

// step is one transition intent of an observed block.
type step struct {
	hook        *Hook
	record      decision.IntentRecord
	observation string
	finalized   *types.Header
	key, value  []byte
}

// Marker implements core.ExtractorDecision.
func (s *step) Marker() (key, value []byte) { return s.key, s.value }

// Finalized implements core.ExtractorDecision.
func (s *step) Finalized() *types.Header { return s.finalized }

// Written implements core.ExtractorDecision.
func (s *step) Written(stage string) {
	s.hook.emit(bsc.Event{"event": "native-" + stage, "observation": s.observation, "sequence": s.record.Sequence})
}

// Resolve implements core.ExtractorDecision: it records NATIVE_DECIDED,
// which closes the member observation, then removes the markers that no
// longer serve (Cleanup).
func (s *step) Resolve() error {
	_, token := s.hook.seam.Admission()
	decided, err := s.hook.bridge.Resolve(token, s.record.Sequence)
	if err != nil {
		return err
	}
	s.hook.emit(bsc.Event{"event": "decided", "observation": s.observation, "sequence": decided.Sequence,
		"delivery": string(decided.Delivery), "digest": decided.Digest})
	return s.hook.bridge.Cleanup(token)
}
