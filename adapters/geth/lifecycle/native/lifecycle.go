// This file is Geth-only. The E01-02.lifecycle_seams Geth preparation copies
// it from adapters/geth/lifecycle/native into the Geth tree as
// extractor/admission/native/lifecycle.go, next to geth.go and decision.go,
// so it shares the package of the Geth admission wiring and the Geth
// decision bridge.
//
// It joins the shared lifecycle gate (extractor/lifecycle) to the Geth
// native callers: the gate passes of the mapped callers, the write choke
// points, the CANONICAL and FINALITY decisions without a new block, the
// recovery of their intents, the startup reconciliation, the live boundary
// H, the capture profile check and the native-commit identity of the binary.
// The design is in adapters/geth/lifecycle/native/README.md.

package native

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
	"github.com/ethereum/go-ethereum/internal/version"
	"github.com/ethereum/go-ethereum/log"
)

// BinaryCommit is the source commit of the native binary. The extractor
// build stamps it with -ldflags "-X
// github.com/ethereum/go-ethereum/extractor/admission/native.BinaryCommit=<commit>".
var BinaryCommit string

// Domain tags of the Geth lifecycle evidence digests.
const (
	tagCanonical = "extractor-geth-lifecycle-canonical-v1"
	tagClear     = "extractor-geth-lifecycle-clear-v1"
	tagNotDone   = "extractor-geth-lifecycle-not-applied-v1"
)

// viewPrefix starts the key of the lifecycle view record. The key is longer
// than 33 bytes and starts with "e", so the native offline pruner keeps it.
const viewPrefix = "extractor-lifecycle-geth-v1/"

// The Geth caller IDs of the lifecycle map that this file uses.
const (
	callerRecovery  = "geth.recovery"
	callerReconcile = "geth.startup-reconcile"
	callerSetHead   = "geth.set-head"
)

var (
	// ErrProfile refuses a node configuration that the capture profile
	// forbids.
	ErrProfile = errors.New("the capture profile refuses this node configuration")
	// ErrIdentity refuses a config whose native commit is not the commit of
	// the binary.
	ErrIdentity = errors.New("native commit identity")
	// ErrCorpus is the known-branch source error: a block of a branch that
	// becomes canonical has no retained corpus.
	ErrCorpus = errors.New("source invariant error: a branch block has no retained EXEC_READY observation")
	// ErrLifecycle stops a lifecycle decision that native data cannot
	// settle.
	ErrLifecycle = errors.New("source invariant error: native data does not settle the lifecycle decision")
)

// NativeIdentity returns the source commit of the running binary: the
// extractor stamp, or else the VCS revision that the Go toolchain recorded.
// A binary with neither, or with a modified VCS tree, has no identity.
func NativeIdentity() (string, error) {
	if BinaryCommit != "" {
		if !commitPattern.MatchString(BinaryCommit) {
			return "", fmt.Errorf("%w: stamp %q", ErrIdentity, BinaryCommit)
		}
		return BinaryCommit, nil
	}
	if vcs, ok := version.VCS(); ok && commitPattern.MatchString(vcs.Commit) && !vcs.Dirty {
		return vcs.Commit, nil
	}
	return "", fmt.Errorf("%w: the binary has no native commit", ErrIdentity)
}

// CheckSyncMode is the sync-mode part of the capture profile check: only
// full sync executes every block. eth.New calls it on a capture node.
func CheckSyncMode(mode string) error {
	if mode != "full" {
		return fmt.Errorf("%w: sync mode %q (geth.snap-sync)", ErrProfile, mode)
	}
	return nil
}

// CheckFlags is the flag part of the capture profile check, before the
// node is made: on a capture node (ConfigEnv set) the developer beacon
// (geth.dev-sealing), the sync target with its synthesized finality
// (geth.sync-target) and any sync mode but full (geth.snap-sync) are
// refused.
func CheckFlags(developer, syncTarget bool, syncMode string) error {
	if os.Getenv(ConfigEnv) == "" {
		return nil
	}
	switch {
	case developer:
		return fmt.Errorf("%w: --dev (geth.dev-sealing)", ErrProfile)
	case syncTarget:
		return fmt.Errorf("%w: --synctarget (geth.sync-target)", ErrProfile)
	}
	return CheckSyncMode(syncMode)
}

// LifecycleOptions are optional parts of the lifecycle. The shipped binary
// passes the zero value.
type LifecycleOptions struct {
	// Point is called at the named points of a lifecycle decision
	// ("intended" after its intent, "rewound" after the native rewind of a
	// SetHead and before its marker batch, "settled" after its decision),
	// for probes that crash there. Nil in the shipped binary.
	Point func(name string)
}

// Lifecycle is the lifecycle gate of one Geth node. It implements
// core.ExtractorAdmission (over the Geth admission) and
// core.ExtractorLifecycle.
type Lifecycle struct {
	d     *Decisions
	a     *Admission
	chain *core.BlockChain
	db    ethdb.Database
	gate  *lifecycle.Gate
	opts  LifecycleOptions
	// The fields below are used with the guard held.
	stack      []*lifecycle.Pass
	recovering bool
	pending    *pending
	rewind     *rewindState
	exitErrs   []string
}

// pending is the lifecycle decision between its intent and its decision.
type pending struct {
	intent decision.IntentRecord
	marker [][]byte
	// target is the head whose head batch carries the marker; zero for a
	// decision with a batch of its own.
	target  common.Hash
	written bool
	// head is the native head when the intent was recorded.
	head common.Hash
}

// rewindState is a SetHead rewind in progress.
type rewindState struct {
	target common.Hash
}

// viewRecord is the lifecycle view: the native head and finality references
// that the decided native state holds. It is written in the batch of every
// decision marker that changes them, and at the live boundary.
type viewRecord struct {
	Head      decision.Head  `json:"head"`
	Finalized *decision.Head `json:"finalized"`
	Safe      *decision.Head `json:"safe"`
}

// attached maps each chain to its lifecycle, for probes (LifecycleOf).
var attached sync.Map

// LifecycleOf returns the lifecycle attached to chain, or nil.
func LifecycleOf(chain *core.BlockChain) *Lifecycle {
	l, _ := attached.Load(chain)
	lc, _ := l.(*Lifecycle)
	return lc
}

// AttachLifecycle is AttachDecisions with the lifecycle gate. It checks the
// native-commit identity of the binary, binds the store, opens the
// admission with input cleanup, installs the admission (through the gate),
// the decision seam and the lifecycle seam into the chain, opens the gate
// and runs the recovery driver inside the gate's recovery passes: the
// admission obligations, the saved intent (a lifecycle intent first), the
// native-only replay, the startup reconciliation, and, on a restart, the
// switch to live at the stored boundary H. On a first start the gate stays
// in BOOTSTRAP until GoLive. With opts.SkipRecovery it stops after the gate
// is open, for probes.
func AttachLifecycle(chain *core.BlockChain, db ethdb.Database, cfg Config, opts Options, dopts DecisionOptions,
	lopts LifecycleOptions) (*Lifecycle, error) {
	commit, err := NativeIdentity()
	if err != nil {
		return nil, err
	}
	if cfg.NativeCommit != commit {
		return nil, fmt.Errorf("%w: the binary is %s, the config names %s", ErrIdentity, commit, cfg.NativeCommit)
	}
	if chain.TrieDB().Scheme() != rawdb.HashScheme {
		return nil, fmt.Errorf("%w: the lifecycle needs the hash state scheme", ErrConfig)
	}
	a, err := attach(chain, db, cfg, opts, true)
	if err != nil {
		return nil, err
	}
	if dopts.Mirror == nil {
		dopts.Mirror = noMirror{}
	}
	d := &Decisions{a: a, opts: dopts}
	chain.SetExtractorDecision(d)
	l := &Lifecycle{d: d, a: a, chain: chain, db: db, opts: lopts}
	chain.SetExtractorAdmission(l)
	chain.SetExtractorLifecycle(l)
	attached.Store(chain, l)
	token := a.guard.Acquire()
	l.gate, err = lifecycle.Open(token, a.store, a.core, cfg.StoreID, "geth", false)
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return nil, err
	}
	a.trace("gate_opened", map[string]any{"phase": string(l.gate.Phase())})
	if opts.SkipRecovery {
		return l, nil
	}
	if err := l.recover(); err != nil {
		return nil, err
	}
	return l, nil
}

// Decisions returns the decision bridge of the lifecycle.
func (l *Lifecycle) Decisions() *Decisions { return l.d }

// Gate returns the lifecycle gate.
func (l *Lifecycle) Gate() *lifecycle.Gate { return l.gate }

// ExitErrors lists the pass exits that the gate refused (always empty when
// the passes nest correctly).
func (l *Lifecycle) ExitErrors() []string {
	release := l.a.Lock()
	defer release()
	return append([]string{}, l.exitErrs...)
}

func (l *Lifecycle) point(name string) {
	if l.opts.Point != nil {
		l.opts.Point(name)
	}
}

// ---------------------------------------------------------------- admission seam

// Lock takes the observation guard for one native call. While the recovery
// driver runs, the call is nested in a pass of the recovery caller, so that
// the gate admits it in RECOVERY.
func (l *Lifecycle) Lock() func() {
	release := l.a.Lock()
	if !l.recovering {
		return release
	}
	p, err := l.gate.Enter(l.a.token, callerRecovery)
	if err != nil {
		// The nested call then fails to enter the gate (ErrPhase).
		l.a.trace("recovery_pass_refused", map[string]any{"error": err.Error()})
		return release
	}
	l.stack = append(l.stack, p)
	return func() {
		l.pop(p)
		release()
	}
}

// Begin starts the admission observation of an observed import.
func (l *Lifecycle) Begin(block *types.Block, parentRoot common.Hash) (core.ExtractorObservation, error) {
	return l.a.Begin(block, parentRoot)
}

// ---------------------------------------------------------------- lifecycle seam

// Enter opens the pass of a mapped caller with the held guard.
func (l *Lifecycle) Enter(id string) (func(), error) {
	token := l.a.token
	if token == nil {
		return nil, ErrGuardMissing
	}
	p, err := l.gate.Enter(token, id)
	if err != nil {
		return nil, err
	}
	l.stack = append(l.stack, p)
	return func() { l.pop(p) }, nil
}

func (l *Lifecycle) pop(p *lifecycle.Pass) {
	if n := len(l.stack); n > 0 && l.stack[n-1] == p {
		l.stack = l.stack[:n-1]
	}
	if err := p.Exit(); err != nil {
		l.exitErrs = append(l.exitErrs, err.Error())
		l.a.trace("pass_exit_refused", map[string]any{"caller": p.Caller().ID, "error": err.Error()})
	}
}

func (l *Lifecycle) top() (*lifecycle.Pass, error) {
	if len(l.stack) == 0 {
		return nil, fmt.Errorf("%w: no open pass", lifecycle.ErrOrder)
	}
	return l.stack[len(l.stack)-1], nil
}

// Observing reports whether the innermost pass captures.
func (l *Lifecycle) Observing() bool {
	p, err := l.top()
	return err != nil || p.Observe()
}

// Require is the check of a native write choke point.
func (l *Lifecycle) Require(effect string) error {
	err := l.gate.Require(lifecycle.Effect(effect))
	if err != nil {
		l.a.trace("choke_point_refused", map[string]any{"effect": effect, "error": err.Error()})
	}
	return err
}

// Refused records the refusal of a REFUSE caller where its mechanism runs,
// with the guard held.
func (l *Lifecycle) Refused(id string) {
	if _, err := l.gate.Enter(l.a.token, id); !errors.Is(err, lifecycle.ErrRefused) {
		l.a.trace("refusal_not_recorded", map[string]any{"caller": id, "error": errorText(err)})
	}
}

// Refuse takes the guard and refuses a REFUSE caller.
func (l *Lifecycle) Refuse(id string) error {
	release := l.a.Lock()
	defer release()
	_, err := l.gate.Enter(l.a.token, id)
	if err == nil {
		return fmt.Errorf("%w: %s entered as a refused caller", lifecycle.ErrInvalid, id)
	}
	l.a.trace("refused", map[string]any{"caller": id, "error": err.Error()})
	return err
}

func headOf(h *types.Header) decision.Head {
	if h == nil {
		return decision.Head{Hash: lifecycle.ZeroHash}
	}
	return decision.Head{Number: h.Number.Uint64(), Hash: hexHash(h.Hash())}
}

func optionalHead(h *types.Header) *decision.Head {
	if h == nil {
		return nil
	}
	head := headOf(h)
	return &head
}

func headBytes(h decision.Head) []byte {
	return append(binary.BigEndian.AppendUint64(nil, h.Number), common.HexToHash(h.Hash).Bytes()...)
}

func digest(tag string, parts ...[]byte) string {
	h := sha256.New()
	h.Write(append([]byte(tag), 0))
	for _, part := range parts {
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func lengthPrefixed(value string) []byte {
	return append(binary.BigEndian.AppendUint64(nil, uint64(len(value))), value...)
}

// CanonicalEvidence is the evidence digest of a CANONICAL decision without
// a new execution: H(tag || caller || old head || new head || u64(count) ||
// each member observation), with lengths before strings and numbers before
// hashes.
func CanonicalEvidence(caller string, old, next decision.Head, members []decision.Member) string {
	parts := [][]byte{lengthPrefixed(caller), headBytes(old), headBytes(next),
		binary.BigEndian.AppendUint64(nil, uint64(len(members)))}
	for _, m := range members {
		parts = append(parts, lengthPrefixed(m.Observation))
	}
	return digest(tagCanonical, parts...)
}

// ClearEvidence is the evidence digest of a finality clear (a native setter
// call with no block): H(tag || field || caller || old reference).
func ClearEvidence(field lifecycle.Field, caller string, old decision.Head) string {
	return digest(tagClear, lengthPrefixed(string(field)), lengthPrefixed(caller), headBytes(old))
}

// NotAppliedEvidence is the evidence digest of the rejection of a lifecycle
// intent whose native mutation did not happen: H(tag || intent digest ||
// native head hash).
func NotAppliedEvidence(intentDigest string, head common.Hash) string {
	return digest(tagNotDone, lengthPrefixed(intentDigest), head.Bytes())
}

func (l *Lifecycle) viewKey() []byte {
	p := l.a.producer
	return []byte(viewPrefix + p.ProducerID + "." + p.RunID + "/view")
}

// View returns the view record of the head and finality references.
func (l *Lifecycle) View(head, finalized, safe *types.Header) [][]byte {
	data, err := json.Marshal(viewRecord{Head: headOf(head), Finalized: optionalHead(finalized), Safe: optionalHead(safe)})
	if err != nil {
		panic(err)
	}
	return [][]byte{l.viewKey(), data}
}

func (l *Lifecycle) readView() (viewRecord, bool, error) {
	var v viewRecord
	ok, err := l.db.Has(l.viewKey())
	if err != nil || !ok {
		return v, false, err
	}
	data, err := l.db.Get(l.viewKey())
	if err != nil {
		return v, false, err
	}
	if err := strictDecode(data, &v); err != nil {
		return v, false, fmt.Errorf("%w: lifecycle view: %v", lifecycle.ErrCorrupt, err)
	}
	return v, true, nil
}

func (l *Lifecycle) writeRecords(records [][]byte) error {
	batch := l.db.NewBatch()
	defer batch.Close()
	for i := 0; i+1 < len(records); i += 2 {
		if err := batch.Put(records[i], records[i+1]); err != nil {
			return err
		}
	}
	if err := batch.Write(); err != nil {
		return err
	}
	return l.db.SyncKeyValue()
}

// retainedBranch returns the members for a head change to head: the SIDE
// members retained for every block of the branch of head above the
// canonical chain, head included. Every such block must have one (the
// known-branch corpus rule); a block without one is ErrCorpus.
func (l *Lifecycle) retainedBranch(head *types.Header) ([]decision.Member, error) {
	token := l.a.token
	branch := map[string]uint64{}
	for header := head; header != nil && l.chain.GetCanonicalHash(header.Number.Uint64()) != header.Hash(); header =
		l.chain.GetHeader(header.ParentHash, header.Number.Uint64()-1) {
		branch[hexHash(header.Hash())] = header.Number.Uint64()
		if header.Number.Uint64() == 0 {
			break
		}
	}
	if len(branch) == 0 {
		return []decision.Member{}, nil
	}
	decisions, err := l.d.bridge.Decisions(token)
	if err != nil {
		return nil, err
	}
	observations, err := l.a.core.Observations(token)
	if err != nil {
		return nil, err
	}
	open := map[string]admission.Observation{}
	for _, obs := range observations {
		open[obs.Key] = obs
	}
	members, covered := []decision.Member{}, map[string]bool{}
	for _, dec := range decisions {
		if dec.State != decision.Decided || dec.Intent.Kind != decision.Side {
			continue
		}
		for _, m := range dec.Intent.Members {
			if obs, ok := open[m.Observation]; ok && obs.State == admission.ExecReady && !covered[m.Block] {
				if _, onBranch := branch[m.Block]; onBranch {
					covered[m.Block] = true
					members = append(members, decision.Member{Observation: m.Observation,
						ExecReadyDigest: m.ExecReadyDigest, ChildRoot: m.ChildRoot})
				}
			}
		}
	}
	for hash, number := range branch {
		if !covered[hash] {
			return nil, fmt.Errorf("%w: block %d %s", ErrCorpus, number, hash)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Observation < members[j].Observation })
	return members, nil
}

func (l *Lifecycle) intend(p *lifecycle.Pass, in decision.Intent, target, head common.Hash) error {
	if l.pending != nil {
		return fmt.Errorf("%w: lifecycle intent %d is pending", decision.ErrPending, l.pending.intent.Sequence)
	}
	rec, err := p.Intend(l.d.bridge, in)
	if err != nil {
		return err
	}
	value, err := decision.MarkerValue(rec)
	if err != nil {
		return err
	}
	l.pending = &pending{intent: rec, marker: [][]byte{decision.MarkerKey(l.a.producer, rec.Sequence), value},
		target: target, head: head}
	l.a.trace("lifecycle_intent", map[string]any{"caller": p.Caller().ID, "sequence": rec.Sequence,
		"kind": string(rec.Kind), "old": rec.OldHead, "new": rec.NewHead, "members": len(rec.Members),
		"digest": rec.Digest})
	l.point("intended")
	return nil
}

// Canonical records the CANONICAL intent of a head change to head without a
// new execution. A pass that does not observe (BOOTSTRAP) records nothing.
func (l *Lifecycle) Canonical(old, head *types.Header) error {
	p, err := l.top()
	if err != nil {
		return err
	}
	if !p.Observe() {
		return nil
	}
	members, err := l.retainedBranch(head)
	if err != nil {
		return err
	}
	in := decision.Intent{Kind: decision.Canonical, OldHead: headOf(old), NewHead: headOf(head), Members: members,
		Events: true}
	in.EvidenceDigest = CanonicalEvidence(p.Caller().ID, in.OldHead, in.NewHead, members)
	return l.intend(p, in, head.Hash(), old.Hash())
}

// HeadMarker returns the marker of the pending decision whose head batch is
// the batch of head, once.
func (l *Lifecycle) HeadMarker(head *types.Header) [][]byte {
	pd := l.pending
	if pd == nil || pd.written || pd.target == (common.Hash{}) || pd.target != head.Hash() {
		return nil
	}
	pd.written = true
	return pd.marker
}

// Finality records the FINALITY intent of one setter call.
func (l *Lifecycle) Finality(field string, old, next, head, finalized, safe *types.Header) ([][]byte, error) {
	p, err := l.top()
	if err != nil {
		return nil, err
	}
	if old == nil && next == nil {
		return nil, core.ErrExtractorNoop
	}
	if !p.Observe() {
		return nil, nil
	}
	return l.finality(p, lifecycle.Field(field), headOf(old), next, head, finalized, safe)
}

func (l *Lifecycle) finality(p *lifecycle.Pass, field lifecycle.Field, old decision.Head, next, head, finalized,
	safe *types.Header) ([][]byte, error) {
	caller := p.Caller().ID
	in := decision.Intent{Kind: decision.Finality, OldHead: headOf(head), NewHead: headOf(head), Events: true}
	if next == nil {
		// A clear is a real native write, not the Engine API no-op of a
		// zero hash: its own intent and evidence.
		in.EvidenceDigest = ClearEvidence(field, caller, old)
	} else {
		change := lifecycle.FinalityChange{Field: field, Caller: caller, Old: old, New: headOf(next)}
		evidence, err := lifecycle.FinalityDigest(change)
		if errors.Is(err, lifecycle.ErrNoop) {
			// An unchanged reference: the pass may rewrite it once, without
			// an intent or a marker.
			if _, err := p.Finality(l.d.bridge, headOf(head), change, true); !errors.Is(err, lifecycle.ErrNoop) {
				return nil, fmt.Errorf("%w: unchanged finality reference: %v", ErrLifecycle, err)
			}
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		in.EvidenceDigest = evidence
	}
	if field == lifecycle.Safe {
		safe = next
	} else {
		finalized = next
	}
	if err := l.intend(p, in, common.Hash{}, head.Hash()); err != nil {
		return nil, err
	}
	return append(l.View(head, finalized, safe), l.pending.marker...), nil
}

// Rewind records the CANONICAL intent of a SetHead rewind from old to
// target.
func (l *Lifecycle) Rewind(old, target *types.Header) error {
	p, err := l.top()
	if err != nil {
		return err
	}
	if target == nil {
		return fmt.Errorf("%w: the rewind has no target", ErrLifecycle)
	}
	l.rewind = &rewindState{target: target.Hash()}
	if !p.Observe() {
		return nil
	}
	in := decision.Intent{Kind: decision.Canonical, OldHead: headOf(old), NewHead: headOf(target),
		Members: []decision.Member{}, Events: true}
	in.EvidenceDigest = CanonicalEvidence(p.Caller().ID, in.OldHead, in.NewHead, nil)
	if err := l.intend(p, in, common.Hash{}, old.Hash()); err != nil {
		l.rewind = nil
		return err
	}
	return nil
}

// Rewinding reports whether a SetHead rewind is in progress.
func (l *Lifecycle) Rewinding() bool { return l.rewind != nil }

// Rewound checks the rewound head against the intent and returns the
// records of the rewind batch.
func (l *Lifecycle) Rewound(head, finalized, safe *types.Header) ([][]byte, error) {
	rw := l.rewind
	l.rewind = nil
	if rw == nil {
		return nil, fmt.Errorf("%w: no rewind in progress", ErrLifecycle)
	}
	if head.Hash() != rw.target {
		return nil, fmt.Errorf("%w: the rewind kept head %d %s, the intent names %s", ErrLifecycle, head.Number.Uint64(),
			head.Hash(), rw.target)
	}
	if l.pending == nil {
		return nil, nil
	}
	l.point("rewound")
	return append(l.View(head, finalized, safe), l.pending.marker...), nil
}

// Settle resolves the pending decision after its batch is durable, or
// rejects an intent whose native mutation did not happen.
func (l *Lifecycle) Settle(err error) error {
	if err != nil {
		l.rewind = nil
	}
	pd := l.pending
	if pd == nil {
		return err
	}
	token := l.a.token
	if err == nil && (pd.written || pd.target == (common.Hash{})) {
		l.pending = nil
		record, rerr := l.d.bridge.Resolve(token, pd.intent.Sequence)
		if rerr == nil {
			l.a.trace("lifecycle_decided", map[string]any{"sequence": record.Sequence,
				"delivery": string(record.Delivery), "digest": record.Digest})
			rerr = l.d.bridge.Cleanup(token)
		}
		l.point("settled")
		return rerr
	}
	head := l.chain.CurrentBlock().Hash()
	if !pd.written && head == pd.head {
		// The marker batch was never written and the head did not move: the
		// native side did not apply the transition.
		l.pending = nil
		_, rerr := l.d.bridge.RejectIntent(token, pd.intent.Sequence, NotAppliedEvidence(pd.intent.Digest, head))
		l.a.trace("lifecycle_rejected", map[string]any{"sequence": pd.intent.Sequence, "cause": errorText(err)})
		return errors.Join(err, rerr)
	}
	return errors.Join(err, fmt.Errorf("%w: lifecycle intent %d stays unresolved", ErrLifecycle, pd.intent.Sequence))
}

// ---------------------------------------------------------------- recovery

// recover is the recovery driver inside the gate: the steps of
// Decisions.Recover, with the settlement of a lifecycle intent before the
// write intents, the startup reconciliation, and GoLive at the stored H
// instead of decision.Bridge.GoLive.
func (l *Lifecycle) recover() error {
	l.recovering = true
	defer func() { l.recovering = false }()
	d := l.d
	if err := d.a.RecoverObligations(); err != nil {
		return err
	}
	store, ok := d.a.store.(admission.DeleteStore)
	if !ok {
		return fmt.Errorf("%w: the decision store cannot delete", ErrConfig)
	}
	token := d.a.guard.Acquire()
	bridge, err := decision.Open(token, store, d.a.core, d.a.cfg.StoreID, Roots{DB: d.a.db}, d.opts.Mirror)
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return err
	}
	d.bridge = bridge
	d.a.trace("bridge_opened", nil)
	if err := l.settleLifecycleIntent(); err != nil {
		return err
	}
	if err := d.settleIntent(); err != nil {
		return err
	}
	if err := d.replayUndecided(); err != nil {
		return err
	}
	if l.gate.Phase() == lifecycle.Recovery {
		if err := l.reconcile(); err != nil {
			return err
		}
	}
	token = d.a.guard.Acquire()
	err = d.bridge.Cleanup(token)
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return err
	}
	l.recovering = false
	if l.gate.Phase() == lifecycle.Recovery {
		_, err = l.GoLive()
	}
	return err
}

// writeIntent reports whether an intent is the intent of a block write of
// the decision bridge (its first member is the written block with the
// write evidence); the decision recovery driver settles those.
func (l *Lifecycle) writeIntent(intent decision.IntentRecord) bool {
	switch intent.Kind {
	case decision.Import, decision.Side:
		return true
	case decision.Finality:
		return false
	}
	if len(intent.Members) == 0 {
		return false
	}
	block, err := l.a.savedBlock(intent.Members[0].Observation)
	if err != nil {
		return false
	}
	return intent.EvidenceDigest == IntentEvidence(intent.Kind, block, common.HexToHash(intent.Members[0].ChildRoot))
}

// settleLifecycleIntent settles an unresolved lifecycle intent without a
// marker from native evidence. A FINALITY marker is in the same synced
// batch as the finalized write (and safe lives in memory), so without the
// marker the setter did not take durable effect: the intent is rejected. A
// CANONICAL intent is completed (its marker and view written) when the
// native head is its new head, rejected when the native head is still its
// old head, and otherwise stops startup.
func (l *Lifecycle) settleLifecycleIntent() error {
	release := l.Lock()
	defer release()
	token := l.a.token
	saved, err := l.d.unresolved(token)
	if err != nil || saved == nil || saved.Marker || l.writeIntent(saved.Intent) {
		return err
	}
	head := l.chain.CurrentBlock()
	reject := func(evidence string) error {
		_, err := l.d.bridge.RejectIntent(token, saved.Sequence, NotAppliedEvidence(saved.Intent.Digest, head.Hash()))
		l.a.trace("settling", map[string]any{"sequence": saved.Sequence, "evidence": evidence})
		return err
	}
	switch {
	case saved.Intent.Kind == decision.Finality:
		return reject("finality_not_durable")
	case hexHash(head.Hash()) == saved.Intent.NewHead.Hash:
		value, err := decision.MarkerValue(saved.Intent)
		if err != nil {
			return err
		}
		records := append(l.View(head, l.chain.CurrentFinalBlock(), l.chain.CurrentSafeBlock()),
			decision.MarkerKey(l.a.producer, saved.Sequence), value)
		if err := l.writeRecords(records); err != nil {
			return err
		}
		if _, err := l.d.bridge.Resolve(token, saved.Sequence); err != nil {
			return err
		}
		l.a.trace("settling", map[string]any{"sequence": saved.Sequence, "evidence": "native_head_is_new_head"})
		return nil
	case hexHash(head.Hash()) == saved.Intent.OldHead.Hash:
		return reject("native_head_is_old_head")
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

// reconcile is the startup reconciliation (geth.startup-reconcile): native
// startup repair runs before the gate opens, so the driver compares the
// native head and finality references with the lifecycle view of the last
// decided state. A head below the stored view head that is its ancestor
// (a native startup rewind) becomes one CANONICAL decision; a head below H,
// or a head that is not an ancestor of the view head, is a source invariant
// error. Each changed finality reference (for example safe, which a restart
// sets to finalized) becomes one FINALITY decision.
func (l *Lifecycle) reconcile() error {
	view, ok, err := l.readView()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: a boundary without a lifecycle view", lifecycle.ErrCorrupt)
	}
	boundary, _ := l.gate.Boundary()
	release := l.Lock()
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
		in.EvidenceDigest = CanonicalEvidence(callerReconcile, in.OldHead, in.NewHead, nil)
		if err := l.intend(p, in, common.Hash{}, head.Hash()); err != nil {
			return err
		}
		records := append(l.View(head, l.chain.CurrentFinalBlock(), l.chain.CurrentSafeBlock()), l.pending.marker...)
		if err := l.Settle(l.writeRecords(records)); err != nil {
			return err
		}
	}
	for _, field := range []lifecycle.Field{lifecycle.Finalized, lifecycle.Safe} {
		stored, native := view.Finalized, l.chain.CurrentFinalBlock()
		if field == lifecycle.Safe {
			stored, native = view.Safe, l.chain.CurrentSafeBlock()
		}
		old := decision.Head{Hash: lifecycle.ZeroHash}
		if stored != nil {
			old = *stored
		}
		if old.Hash == headOf(native).Hash {
			continue
		}
		records, err := l.finality(p, field, old, native, head, l.chain.CurrentFinalBlock(), l.chain.CurrentSafeBlock())
		if err != nil {
			return err
		}
		if err := l.Settle(l.writeRecords(records)); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- live boundary

// GoLive switches the gate to LIVE. On a first start it reads the native
// head under the guard, flushes the state root of that head through the
// hash trie database and syncs it, writes the lifecycle view of H, and lets
// the gate record the boundary H before the decision bridge goes live. On a
// restart the gate keeps the stored H.
func (l *Lifecycle) GoLive() (lifecycle.Boundary, error) {
	release := l.a.Lock()
	defer release()
	token := l.a.token
	head := l.chain.CurrentBlock()
	if l.gate.Phase() == lifecycle.Bootstrap {
		if err := l.chain.TrieDB().Commit(head.Root, false); err != nil {
			return lifecycle.Boundary{}, err
		}
		if err := l.db.SyncKeyValue(); err != nil {
			return lifecycle.Boundary{}, err
		}
		if err := l.writeRecords(l.View(head, l.chain.CurrentFinalBlock(), l.chain.CurrentSafeBlock())); err != nil {
			return lifecycle.Boundary{}, err
		}
	}
	b, err := l.gate.GoLive(token, headOf(head), hexHash(head.Root), Roots{DB: l.db}, l.d.bridge)
	if err != nil {
		return lifecycle.Boundary{}, err
	}
	l.a.trace("lifecycle_live", map[string]any{"boundary": b.Head, "root": b.Root})
	return b, nil
}

// liveService is the node service that goes live after the initial native
// sync of a first start.
type liveService struct {
	l      *Lifecycle
	synced func() bool
	every  time.Duration
	quit   chan struct{}
	done   chan struct{}
}

// Live returns the node service (node.Lifecycle) that calls GoLive once
// synced reports the end of the initial native sync. On a restart the gate
// is already live and the service does nothing.
func (l *Lifecycle) Live(synced func() bool) *liveService {
	return &liveService{l: l, synced: synced, every: 5 * time.Second, quit: make(chan struct{}),
		done: make(chan struct{})}
}

// Start starts the service.
func (s *liveService) Start() error {
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
				if !s.synced() {
					continue
				}
				if b, err := s.l.GoLive(); err != nil {
					log.Error("Extractor lifecycle could not go live", "err", err)
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
func (s *liveService) Stop() error {
	close(s.quit)
	<-s.done
	return nil
}
