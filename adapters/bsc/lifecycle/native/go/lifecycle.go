// Package bsclifecycle joins the shared lifecycle gate
// (E01-02.lifecycle_seams, extractor/lifecycle) to the BSC native callers,
// on top of the BSC admission seam (extractor/bsc) and the BSC decision
// bridge (extractor/bsc/decision): the gate passes of the mapped callers,
// the CANONICAL and FINALITY decisions without a new block, their recovery,
// the startup reconciliation, the live boundary H, the error-aware
// attestation read of the import decision, the capture profile check and
// the node wiring. The design is in adapters/bsc/lifecycle/native/README.md.
package bsclifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	bscdecision "github.com/ethereum/go-ethereum/extractor/bsc/decision"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

// Domain tags of the BSC lifecycle evidence digests.
const (
	tagCanonical = "extractor-bsc-lifecycle-canonical-v1"
	tagClear     = "extractor-bsc-lifecycle-clear-v1"
	tagNotDone   = "extractor-bsc-lifecycle-not-applied-v1"
)

// viewPrefix starts the key of the lifecycle view record. The key is longer
// than 33 bytes and starts with "e", so the native offline pruner keeps it.
const viewPrefix = "extractor-lifecycle-bsc-v1/"

// The BSC caller IDs of the lifecycle map that this package uses.
const (
	callerRecovery  = "bsc.recovery"
	callerReconcile = "bsc.startup-reconcile"
	callerSetHead   = "bsc.set-head"
	callerShutdown  = "bsc.shutdown"
	callerKnown     = "bsc.known-block"
)

var (
	// ErrCorpus is the known-branch source error: a stored block that would
	// become the head without an execution has no retained corpus. BSC
	// retains none (D104), so the activation stops before the head moves.
	ErrCorpus = fmt.Errorf("%w: a branch block has no retained EXEC_READY observation (D104)", lifecycle.ErrInvariant)
	// ErrLifecycle stops a lifecycle decision that native data cannot
	// settle.
	ErrLifecycle = fmt.Errorf("%w: native data does not settle the lifecycle decision", lifecycle.ErrInvariant)
	// ErrConfig refuses a lifecycle configuration.
	ErrConfig = errors.New("invalid BSC lifecycle configuration")
)

// Options are optional parts of the lifecycle. The shipped node passes the
// zero value.
type Options struct {
	// SkipRecovery stops Attach after the gate is open, for probes.
	SkipRecovery bool
	// Point is called at the named points of a lifecycle decision
	// ("intended" after its intent, "rewound" after the native rewind of a
	// SetHead and before its marker batch, "settled" after its decision),
	// for probes that crash there. Nil in the shipped node.
	Point func(name string)
	// AttestationReader, when set, replaces the reader of the attestation
	// read of an import decision, for probes that make the native snapshot
	// lookup fail. Nil in the shipped node.
	AttestationReader func(evidence consensus.ChainHeaderReader, block *types.Header) consensus.ChainHeaderReader
}

// Lifecycle is the lifecycle gate of one BSC node. It implements
// core.ExtractorHook (over the decision hook) and core.ExtractorLifecycle.
type Lifecycle struct {
	hook    *bscdecision.Hook
	chain   *core.BlockChain
	db      ethdb.Database
	store   admission.DeleteStore
	storeID string
	mirror  decision.Mirror
	roots   *bscdecision.Roots
	gate    *lifecycle.Gate
	opts    Options
	emit    func(bsc.Event)
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
	// head is the native head when the intent was recorded; a native error
	// with this head unchanged means the mutation did not happen. Zero when
	// the native change happened before the intent.
	head common.Hash
}

// rewindState is a SetHead rewind in progress.
type rewindState struct {
	target common.Hash
}

// viewRecord is the lifecycle view: the native head and finalized block
// that the decided native state holds. It is written in the batch of every
// decision marker that moves the head or the finalized block, and at the
// live boundary.
type viewRecord struct {
	Head      decision.Head  `json:"head"`
	Finalized *decision.Head `json:"finalized"`
}

// attached maps each chain to its lifecycle, for probes (Of).
var attached sync.Map

// Of returns the lifecycle attached to chain, or nil.
func Of(chain *core.BlockChain) *Lifecycle {
	l, _ := attached.Load(chain)
	lc, _ := l.(*Lifecycle)
	return lc
}

// Hook returns the decision hook.
func (l *Lifecycle) Hook() *bscdecision.Hook { return l.hook }

// Gate returns the lifecycle gate.
func (l *Lifecycle) Gate() *lifecycle.Gate { return l.gate }

// ExitErrors lists the pass exits that the gate refused (always empty when
// the passes nest correctly).
func (l *Lifecycle) ExitErrors() []string {
	release := l.hook.Seam().Acquire()
	defer release()
	return append([]string{}, l.exitErrs...)
}

func (l *Lifecycle) point(name string) {
	if l.opts.Point != nil {
		l.opts.Point(name)
	}
}

func (l *Lifecycle) token() *admission.Token {
	_, token := l.hook.Seam().Admission()
	return token
}

func (l *Lifecycle) producer() admission.Producer {
	adm, _ := l.hook.Seam().Admission()
	return adm.Producer()
}

// ---------------------------------------------------------------- import hook

// Acquire implements core.ExtractorHook: it takes the observation guard.
// While the recovery driver runs, the guarded call is nested in a pass of
// the recovery caller, so that the gate admits it in RECOVERY.
func (l *Lifecycle) Acquire() func() {
	release := l.hook.Acquire()
	if !l.recovering {
		return release
	}
	p, err := l.gate.Enter(l.token(), callerRecovery)
	if err != nil {
		// The nested call then fails to enter the gate (ErrPhase).
		l.emit(bsc.Event{"event": "recovery-pass-refused", "error": err.Error()})
		return release
	}
	l.stack = append(l.stack, p)
	return func() {
		l.pop(p)
		release()
	}
}

// Begin implements core.ExtractorHook. During the initial native sync
// (BOOTSTRAP) the pass does not observe: the block is imported natively and
// uncaptured. Otherwise the decision hook observes it, and its decision step
// first reads the attestation evidence without losing errors.
func (l *Lifecycle) Begin(chain *core.BlockChain, block *types.Block) (core.ExtractorObservation, error) {
	if !l.Observing() {
		return nil, nil
	}
	inner, err := l.hook.Begin(chain, block)
	if err != nil || inner == nil {
		return inner, err
	}
	return &observation{ExtractorObservation: inner, l: l}, nil
}

// observation adds the error-aware attestation read to the decision step.
type observation struct {
	core.ExtractorObservation
	l *Lifecycle
}

// Intend implements core.ExtractorDecider: the attestation read, then the
// decision step of the decision hook. A lookup error stops the import
// before any write; the observation stays EXEC_READY without a decision.
func (o *observation) Intend(chain *core.BlockChain, block *types.Block, head *types.Header,
	canonical bool) (core.ExtractorDecision, error) {
	decider, ok := o.ExtractorObservation.(core.ExtractorDecider)
	if !ok {
		return nil, fmt.Errorf("%w: block %d has no decision step", core.ErrExtractorDecision, block.NumberU64())
	}
	if _, err := o.l.attestation(chain, block); err != nil {
		return nil, err
	}
	return decider.Intend(chain, block, head, canonical)
}

// ---------------------------------------------------------------- gate seam

// Enter opens the pass of a mapped caller with the held guard.
func (l *Lifecycle) Enter(id string) (func(), error) {
	token := l.token()
	if token == nil {
		return nil, bsc.ErrGuard
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
		l.emit(bsc.Event{"event": "pass-exit-refused", "caller": p.Caller().ID, "error": err.Error()})
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
		l.emit(bsc.Event{"event": "choke-point-refused", "effect": effect, "error": err.Error()})
	}
	return err
}

// Refuse takes the guard and refuses a REFUSE caller.
func (l *Lifecycle) Refuse(id string) error {
	release := l.hook.Seam().Acquire()
	defer release()
	_, err := l.gate.Enter(l.token(), id)
	if err == nil {
		return fmt.Errorf("%w: %s entered as a refused caller", lifecycle.ErrInvalid, id)
	}
	l.emit(bsc.Event{"event": "refused", "caller": id, "error": err.Error()})
	return err
}

// ---------------------------------------------------------------- evidence

func hexHash(h common.Hash) string { return hex.EncodeToString(h.Bytes()) }

func headOf(h *types.Header) decision.Head {
	if h == nil {
		return decision.Head{Hash: lifecycle.ZeroHash}
	}
	return decision.Head{Number: h.Number.Uint64(), Hash: hexHash(h.Hash())}
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

// CanonicalEvidence is the evidence digest of a CANONICAL decision without a
// new execution: H(tag || str(caller) || old head || new head), each head as
// u64(number) || hash.
func CanonicalEvidence(caller string, old, next decision.Head) string {
	return digest(tagCanonical, lengthPrefixed(caller), headBytes(old), headBytes(next))
}

// ClearEvidence is the evidence digest of a finalized clear (a native
// setter call with no block): H(tag || str(field) || str(caller) || old).
func ClearEvidence(field lifecycle.Field, caller string, old decision.Head) string {
	return digest(tagClear, lengthPrefixed(string(field)), lengthPrefixed(caller), headBytes(old))
}

// NotAppliedEvidence is the evidence digest of the rejection of a lifecycle
// intent whose native mutation did not happen: H(tag || str(intent digest)
// || native head hash).
func NotAppliedEvidence(intentDigest string, head common.Hash) string {
	return digest(tagNotDone, lengthPrefixed(intentDigest), head.Bytes())
}

// ---------------------------------------------------------------- view

func (l *Lifecycle) viewKey() []byte {
	p := l.producer()
	return []byte(viewPrefix + p.ProducerID + "." + p.RunID + "/view")
}

// finalizedHead returns the head of a finalized block hash; a zero hash is
// no finalized block.
func (l *Lifecycle) finalizedHead(hash common.Hash) *decision.Head {
	if hash == (common.Hash{}) {
		return nil
	}
	head := decision.Head{Hash: hexHash(hash)}
	if header := l.chain.GetHeaderByHash(hash); header != nil {
		head.Number = header.Number.Uint64()
	}
	return &head
}

// View returns the view record of head and the finalized block hash.
func (l *Lifecycle) View(head *types.Header, finalized common.Hash) [][]byte {
	return l.viewRecords(viewRecord{Head: headOf(head), Finalized: l.finalizedHead(finalized)})
}

func (l *Lifecycle) viewRecords(v viewRecord) [][]byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return [][]byte{l.viewKey(), data}
}

// ReadView returns the stored lifecycle view, if any.
func (l *Lifecycle) ReadView() (decision.Head, *decision.Head, bool, error) {
	v, ok, err := l.readView()
	return v.Head, v.Finalized, ok, err
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

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data")
	}
	return nil
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

// ---------------------------------------------------------------- decisions

func (l *Lifecycle) intend(p *lifecycle.Pass, in decision.Intent, head common.Hash) error {
	if l.pending != nil {
		return fmt.Errorf("%w: lifecycle intent %d is pending", decision.ErrPending, l.pending.intent.Sequence)
	}
	bridge := l.hook.Bridge()
	if bridge == nil {
		return fmt.Errorf("%w: the decision bridge is not open", lifecycle.ErrPhase)
	}
	rec, err := p.Intend(bridge, in)
	if err != nil {
		return err
	}
	value, err := decision.MarkerValue(rec)
	if err != nil {
		return err
	}
	l.pending = &pending{intent: rec, marker: [][]byte{decision.MarkerKey(l.producer(), rec.Sequence), value},
		head: head}
	l.emit(bsc.Event{"event": "lifecycle-intent", "caller": p.Caller().ID, "sequence": rec.Sequence,
		"kind": string(rec.Kind), "old_head": rec.OldHead, "new_head": rec.NewHead, "evidence": rec.EvidenceDigest,
		"digest": rec.Digest})
	l.point("intended")
	return nil
}

// Canonical records the CANONICAL intent of a stored block that becomes the
// head without an execution (bsc.known-block). The known-branch corpus rule
// needs a retained EXEC_READY observation of every block of the branch; the
// BSC import path retains none (the admission seam observes only blocks on
// top of the head, and a decision closes its observation and purges its
// inputs), so an observing pass stops here with ErrCorpus before any
// mutation (D104). A pass that does not observe (BOOTSTRAP) records nothing.
func (l *Lifecycle) Canonical(old, head *types.Header) error {
	p, err := l.top()
	if err != nil {
		return err
	}
	if !p.Observe() {
		return nil
	}
	err = fmt.Errorf("%w: block %d %s, head %d %s", ErrCorpus, head.Number.Uint64(), head.Hash(),
		old.Number.Uint64(), old.Hash())
	l.emit(bsc.Event{"event": "known-block-refused", "caller": p.Caller().ID, "block": headOf(head),
		"head": headOf(old), "error": err.Error()})
	return err
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
	in.EvidenceDigest = CanonicalEvidence(p.Caller().ID, in.OldHead, in.NewHead)
	if err := l.intend(p, in, old.Hash()); err != nil {
		l.rewind = nil
		return err
	}
	return nil
}

// Rewinding reports whether a SetHead rewind is in progress.
func (l *Lifecycle) Rewinding() bool { return l.rewind != nil }

// Rewound checks the rewound head against the intent and returns the
// records of the rewind batch.
func (l *Lifecycle) Rewound(head *types.Header, finalized common.Hash) ([][]byte, error) {
	rw := l.rewind
	l.rewind = nil
	if rw == nil {
		return nil, fmt.Errorf("%w: no rewind in progress", ErrLifecycle)
	}
	if head.Hash() != rw.target {
		return nil, fmt.Errorf("%w: the rewind kept head %d %s, the intent names %s", ErrLifecycle,
			head.Number.Uint64(), head.Hash(), rw.target)
	}
	if l.pending == nil {
		return nil, nil
	}
	l.point("rewound")
	return append(l.View(head, finalized), l.pending.marker...), nil
}

// Clear records the FINALITY intent of the finalized clear of a rewind and
// returns the records of its batch.
func (l *Lifecycle) Clear(old, head *types.Header) ([][]byte, error) {
	p, err := l.top()
	if err != nil {
		return nil, err
	}
	if !p.Observe() {
		return nil, nil
	}
	in := decision.Intent{Kind: decision.Finality, OldHead: headOf(head), NewHead: headOf(head), Events: true,
		EvidenceDigest: ClearEvidence(lifecycle.Finalized, p.Caller().ID, headOf(old))}
	if err := l.intend(p, in, head.Hash()); err != nil {
		return nil, err
	}
	return append(l.View(head, common.Hash{}), l.pending.marker...), nil
}

// Shutdown records the CANONICAL intent of the head rewrite of Stop, from
// the head to itself, and returns the records of its batch.
func (l *Lifecycle) Shutdown(head *types.Header, finalized common.Hash) ([][]byte, error) {
	p, err := l.top()
	if err != nil {
		return nil, err
	}
	if !p.Observe() {
		return nil, nil
	}
	in := decision.Intent{Kind: decision.Canonical, OldHead: headOf(head), NewHead: headOf(head),
		Members: []decision.Member{}, Events: true}
	in.EvidenceDigest = CanonicalEvidence(p.Caller().ID, in.OldHead, in.NewHead)
	if err := l.intend(p, in, head.Hash()); err != nil {
		return nil, err
	}
	return append(l.View(head, finalized), l.pending.marker...), nil
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
	token := l.token()
	bridge := l.hook.Bridge()
	// The pending decision is cleared before the bridge call: a failed
	// Resolve or RejectIntent stops the bridge, and the intent stays
	// unresolved in the store for the next start.
	l.pending = nil
	if err == nil {
		record, rerr := bridge.Resolve(token, pd.intent.Sequence)
		if rerr == nil {
			l.emit(bsc.Event{"event": "lifecycle-decided", "sequence": record.Sequence,
				"delivery": string(record.Delivery), "digest": record.Digest})
			rerr = bridge.Cleanup(token)
		}
		l.point("settled")
		return rerr
	}
	head := l.chain.CurrentBlock().Hash()
	if pd.head != (common.Hash{}) && head == pd.head {
		// The marker batch was never written and the head did not move: the
		// native side did not apply the transition.
		rejected, rerr := bridge.RejectIntent(token, pd.intent.Sequence, NotAppliedEvidence(pd.intent.Digest, head))
		if rerr == nil {
			l.emit(bsc.Event{"event": "lifecycle-rejected", "sequence": pd.intent.Sequence,
				"digest": rejected.Digest, "cause": err.Error()})
		}
		return errors.Join(err, rerr)
	}
	return errors.Join(err, fmt.Errorf("%w: lifecycle intent %d stays unresolved", ErrLifecycle,
		pd.intent.Sequence))
}
