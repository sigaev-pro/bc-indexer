// Package borlifecycle joins the shared lifecycle gate (extractor/lifecycle)
// to the native Bor callers of the capture node: the gate and its bor.recovery
// pass inside the decision recovery driver, the guard token that a chain-lock
// holder lends to its nested calls, the write choke points, the DECISION
// callers' intents and markers, the startup reconcile, and the refusals that
// happen outside the gate. See adapters/bor/lifecycle/native/README.md.
//
// Extractor patch (E01-02.lifecycle_seams, Bor block).
package borlifecycle

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/downloader/whitelist"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/lifecycle"
)

// Client is the lifecycle client of this package.
const Client = "bor"

// evidencePrefix starts the key of every evidence record; the key is longer
// than 33 bytes and starts with "e", so the native offline pruner keeps it.
const evidencePrefix = "extractor-lifecycle-bor-v1/"

// Domain tags of the evidence digests, each followed by one zero byte.
const (
	tagEvidence  = "extractor-bor-lifecycle-evidence-v1"
	tagRejection = "extractor-bor-lifecycle-rejection-v1"
)

var (
	// ErrCorpus refuses a known-branch transition whose block has no
	// retained EXEC_READY observation (the known-branch corpus rule): the
	// transition is a source error, never a silent activation.
	ErrCorpus = errors.New("known-branch block has no retained EXEC_READY observation")
	// ErrHead reports a native write that did not end at the intent's head
	// or finality value; the intent stays unresolved.
	ErrHead = errors.New("native result differs from the lifecycle intent")
)

var (
	enabled atomic.Bool
	nodes   sync.Map // *core.BlockChain -> *Node
)

// Enable makes the next decision recovery driver open the lifecycle gate (the
// capture flag of the node wiring). Without it a node keeps the behavior of
// the admission and decision blocks.
func Enable() { enabled.Store(true) }

// Disable undoes Enable.
func Disable() { enabled.Store(false) }

// Of returns the lifecycle of a capture chain, or nil.
func Of(chain *core.BlockChain) *Node {
	if value, ok := nodes.Load(chain); ok {
		return value.(*Node)
	}
	return nil
}

// Inputs are the native and bridge objects of one capture node.
type Inputs struct {
	Chain     *core.BlockChain
	DB        ethdb.Database
	Guard     *admission.Guard
	Admission *admission.Admission
	Bridge    *decision.Bridge
	StoreID   string
	Roots     decision.Roots
}

// Settled is one unresolved lifecycle intent that the startup reconcile
// settled from native evidence.
type Settled struct {
	Sequence uint64 `json:"sequence"`
	Caller   string `json:"caller"`
	Outcome  string `json:"outcome"`
	// Completed reports that the native mutation was interrupted between
	// its old and new head and was completed before the decision.
	Completed bool `json:"completed"`
}

// Report is what the startup reconcile did.
type Report struct {
	Phase    string              `json:"phase"`
	Settled  []Settled           `json:"settled"`
	Repaired *decision.Head      `json:"repaired"`
	Boundary *lifecycle.Boundary `json:"boundary"`
}

// Node is the lifecycle of one capture chain. It implements
// core.ExtractorLifecycle.
type Node struct {
	in   Inputs
	gate *lifecycle.Gate
	m    lifecycle.Map

	mu sync.Mutex
	// held is the guard token that a pass opened by a chain-lock holder
	// lends to the calls nested in it. It is set and read only with chainmu
	// held, so only the holder's goroutine can see it.
	held     *admission.Token
	stack    []*pass
	outside  map[string]uint64
	recovery *pass
	report   Report
	// Step, if set, runs at the named steps of a lifecycle decision (the
	// probe's crash points; test only): "write" before each choke-point
	// check of a native write, "marker" after the native mutation and before
	// the marker batch, "resolve" after the synced marker and before Resolve.
	Step func(step, caller string)
}

// Open opens the lifecycle gate of a capture chain inside the decision
// recovery driver: the caller holds the guard token and chainmu. It opens the
// bor.recovery pass, installs the choke-point hooks, and runs the startup
// reconcile. Without Enable it returns nil, nil.
func Open(in Inputs, token *admission.Token) (*Node, error) {
	if !enabled.Load() {
		return nil, nil
	}
	m, err := lifecycle.Callers()
	if err != nil {
		return nil, err
	}
	gate, err := lifecycle.Open(token, in.DB, in.Admission, in.StoreID, Client, false)
	if err != nil {
		return nil, err
	}
	n := &Node{in: in, gate: gate, m: m, outside: map[string]uint64{}}
	gp, err := gate.Enter(token, "bor.recovery")
	if err != nil {
		return nil, err
	}
	n.recovery = &pass{node: n, id: "bor.recovery", gp: gp, token: token}
	n.stack = []*pass{n.recovery}
	n.held = token
	n.install()
	n.report = Report{Phase: string(gate.Phase()), Settled: []Settled{}}
	if err := n.settleAll(token); err != nil {
		return nil, errors.Join(err, n.Abort())
	}
	return n, nil
}

func (n *Node) install() {
	n.in.Chain.SetExtractorLifecycle(n)
	rawdb.ExtractorRequire = n.Require
	rawdb.ExtractorFinality = n.finality
	whitelist.ExtractorEnter = n.whitelist
	nodes.Store(n.in.Chain, n)
}

// Close removes the hooks of the node. The node must be stopped.
func (n *Node) Close() {
	if n == nil {
		return
	}
	n.in.Chain.SetExtractorLifecycle(nil)
	rawdb.ExtractorRequire, rawdb.ExtractorFinality, whitelist.ExtractorEnter = nil, nil, nil
	nodes.Delete(n.in.Chain)
}

// Abort closes the recovery pass after a failed recovery and removes the
// hooks.
func (n *Node) Abort() error {
	if n == nil {
		return nil
	}
	var err error
	if n.recovery != nil {
		err = n.recovery.gp.Exit()
		n.recovery = nil
	}
	n.mu.Lock()
	n.stack, n.held = nil, nil
	n.mu.Unlock()
	n.Close()
	return err
}

// GoLive runs the repair part of the startup reconcile, closes the
// bor.recovery pass and switches the gate to LIVE with the native head read
// under the guard as H (a restart keeps the stored H). The decision recovery
// driver has switched the bridge to LIVE in the same guard hold, so no caller
// can run between the bridge and the boundary.
func (n *Node) GoLive(token *admission.Token) error {
	if n == nil {
		return nil
	}
	if err := n.repair(token); err != nil {
		return err
	}
	if err := n.recovery.gp.Exit(); err != nil {
		return err
	}
	n.mu.Lock()
	n.stack, n.held, n.recovery = nil, nil, nil
	n.mu.Unlock()
	head := n.in.Chain.CurrentBlock()
	b, err := n.gate.GoLive(token, headOf(head), hexOf(head.Root), n.in.Roots, n.in.Bridge)
	if err != nil {
		return err
	}
	n.report.Boundary = &b
	return nil
}

// Report returns what the startup reconcile did and the boundary.
func (n *Node) Report() Report { return n.report }

// Phase returns the gate phase.
func (n *Node) Phase() lifecycle.Phase { return n.gate.Phase() }

func hexOf(hash common.Hash) string { return hex.EncodeToString(hash[:]) }

func headOf(h *types.Header) decision.Head {
	return decision.Head{Number: h.Number.Uint64(), Hash: hexOf(h.Hash())}
}

// ---------------------------------------------------------------------------
// Passes.

// pass is one open pass of a mapped caller, or a call covered by an outer
// decision.
type pass struct {
	node    *Node
	id      string
	gp      *lifecycle.Pass
	token   *admission.Token
	own     bool
	covered bool
	// open is the intent of this pass that waits for its marker.
	open *decision.IntentRecord
	// check verifies the native result of the open intent before its marker.
	check func() error
}

func (n *Node) caller(id string) (lifecycle.Caller, bool) {
	for _, c := range n.m.Callers {
		if c.Client == Client && c.ID == id {
			return c, true
		}
	}
	return lifecycle.Caller{}, false
}

// Enter implements core.ExtractorLifecycle.
func (n *Node) Enter(id string, chain bool) (core.ExtractorPass, error) {
	return n.enter(id, chain)
}

func (n *Node) enter(id string, chain bool) (*pass, error) {
	c, _ := n.caller(id)
	n.mu.Lock()
	token := (*admission.Token)(nil)
	if chain && n.held != nil {
		token = n.held
		// The decision of the lender covers a DECISION caller nested in it:
		// the nested native calls of one reorganization are its writes.
		if top := n.stack[len(n.stack)-1]; c.Route == lifecycle.Decision && top.open != nil &&
			top.open.Kind == decision.Canonical {
			n.mu.Unlock()
			return &pass{node: n, id: id, covered: true}, nil
		}
	}
	n.mu.Unlock()
	own := token == nil
	if own {
		token = n.in.Guard.Acquire()
	}
	gp, err := n.gate.Enter(token, id)
	if err != nil {
		if own {
			err = errors.Join(err, token.Release())
		}
		return nil, err
	}
	p := &pass{node: n, id: id, gp: gp, token: token, own: own}
	n.mu.Lock()
	n.stack = append(n.stack, p)
	if own && chain {
		n.held = token
	}
	n.mu.Unlock()
	return p, nil
}

// Covered implements core.ExtractorPass.
func (p *pass) Covered() bool { return p.covered }

// Exit implements core.ExtractorPass.
func (p *pass) Exit() error {
	if p.covered {
		return nil
	}
	n := p.node
	err := p.gp.Exit()
	n.mu.Lock()
	if len(n.stack) > 0 && n.stack[len(n.stack)-1] == p {
		n.stack = n.stack[:len(n.stack)-1]
	}
	if p.own && n.held == p.token {
		n.held = nil
	}
	n.mu.Unlock()
	if p.own {
		err = errors.Join(err, p.token.Release())
	}
	return err
}

// Held implements core.ExtractorLifecycle.
func (n *Node) Held() any {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.held == nil {
		return nil
	}
	return n.held
}

// Floor implements core.ExtractorLifecycle: the number of the boundary H, if
// the run has one. No rewind may go below it.
func (n *Node) Floor() (uint64, bool) {
	b, ok := n.gate.Boundary()
	return b.Head.Number, ok
}

// Require implements core.ExtractorLifecycle.
func (n *Node) Require(effect string) error {
	if n.Step != nil {
		if p := n.innermost(); p != nil {
			n.Step("write", p.id)
		}
	}
	return n.gate.Require(lifecycle.Effect(effect))
}

// Refuse implements core.ExtractorLifecycle.
func (n *Node) Refuse(id string, chain bool) error {
	n.mu.Lock()
	token := (*admission.Token)(nil)
	if chain && n.held != nil {
		token = n.held
	}
	n.mu.Unlock()
	own := token == nil
	if own {
		token = n.in.Guard.Acquire()
	}
	gp, err := n.gate.Enter(token, id)
	if err == nil {
		err = fmt.Errorf("%w: %s is not a REFUSE caller", lifecycle.ErrInvariant, id)
		err = errors.Join(err, gp.Exit())
	}
	if own {
		err = errors.Join(err, token.Release())
	}
	return fmt.Errorf("%w: %w", core.ErrExtractorRefused, err)
}

// RefuseOutside implements core.ExtractorLifecycle.
func (n *Node) RefuseOutside(id string) error {
	if err := n.AddRefusal(id); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", core.ErrExtractorRefused, id)
}

// AddRefusal counts a refusal of a REFUSE caller that happened outside the
// gate: the profile check, a refused command, a path refused without the
// guard.
func (n *Node) AddRefusal(id string) error {
	c, ok := n.caller(id)
	if !ok || c.Route != lifecycle.Refuse {
		return fmt.Errorf("%w: %q is not a REFUSE caller of %s", lifecycle.ErrInvalid, id, Client)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.outside[id]++
	return nil
}

// Coverage returns the gate coverage with the refusals outside the gate.
func (n *Node) Coverage() map[string]lifecycle.Reach {
	out := n.gate.Coverage()
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, count := range n.outside {
		r := out[id]
		r.Refusals += count
		out[id] = r
	}
	return out
}

// ---------------------------------------------------------------------------
// Decisions without a new execution.

func field(value string) []byte {
	return append(binary.BigEndian.AppendUint64(nil, uint64(len(value))), value...)
}

// Digest returns the SHA-256 of tag, one zero byte, then each value as its
// 8-byte big-endian length and its bytes.
func Digest(tag string, values ...string) string {
	h := sha256.New()
	h.Write(append([]byte(tag), 0))
	for _, value := range values {
		h.Write(field(value))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func headFields(h decision.Head) []string { return []string{strconv.FormatUint(h.Number, 10), h.Hash} }

// Evidence is the evidence record of one lifecycle intent, synced under
// evidence/<digest> before the intent and deleted once the intent is
// resolved or rejected. The startup reconcile settles an unresolved intent
// from it and the native state. A record left by a loss after the resolve
// names a settled intent and is never read again.
type Evidence struct {
	Caller    string        `json:"caller"`
	Kind      decision.Kind `json:"kind"`
	Operation string        `json:"operation"`
	OldHead   decision.Head `json:"old_head"`
	NewHead   decision.Head `json:"new_head"`
	// Key, Old and New name a finality write: the native key, and the
	// SHA-256 of its value before and after ("" for no value).
	Key string `json:"key"`
	Old string `json:"old"`
	New string `json:"new"`
}

// EvidenceDigest is the evidence digest of a lifecycle intent.
func EvidenceDigest(e Evidence) string {
	values := []string{e.Caller, string(e.Kind), e.Operation}
	values = append(values, headFields(e.OldHead)...)
	values = append(values, headFields(e.NewHead)...)
	return Digest(tagEvidence, append(values, e.Key, e.Old, e.New)...)
}

func (n *Node) evidenceKey(digest string) []byte {
	p := n.in.Admission.Producer()
	return []byte(evidencePrefix + p.ProducerID + "." + p.RunID + "/evidence/" + digest)
}

func valueDigest(value []byte, present bool) string {
	if !present {
		return ""
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// intend syncs the evidence record and records the intent of p.
func (p *pass) intend(e Evidence, members []decision.Member, events bool) error {
	n := p.node
	digest := EvidenceDigest(e)
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := n.in.DB.Put(n.evidenceKey(digest), data); err != nil {
		return err
	}
	if err := n.in.DB.SyncKeyValue(); err != nil {
		return err
	}
	rec, err := p.gp.Intend(n.in.Bridge, decision.Intent{Kind: e.Kind, OldHead: e.OldHead, NewHead: e.NewHead,
		Members: members, EvidenceDigest: digest, Events: events})
	if err != nil {
		// No intent was recorded: the evidence record names nothing.
		return errors.Join(err, n.in.DB.Delete(n.evidenceKey(digest)))
	}
	p.open = &rec
	return nil
}

// members binds the blocks of the new branch above the common ancestor of
// old and next, ascending, to their retained EXEC_READY observations.
func (n *Node) members(token *admission.Token, old, next *types.Header) ([]decision.Member, error) {
	chain := n.in.Chain
	onOld := func(h *types.Header) bool {
		for o := old; o != nil; o = chain.GetHeader(o.ParentHash, o.Number.Uint64()-1) {
			if o.Number.Uint64() == h.Number.Uint64() {
				return o.Hash() == h.Hash()
			}
			if o.Number.Uint64() < h.Number.Uint64() || o.Number.Uint64() == 0 {
				return false
			}
		}
		return false
	}
	var branch []*types.Header
	for h := next; h == nil || !onOld(h); h = chain.GetHeader(h.ParentHash, h.Number.Uint64()-1) {
		if h == nil || h.Number.Uint64() == 0 {
			return nil, fmt.Errorf("%w: no common ancestor of %d and %d", lifecycle.ErrInvariant,
				old.Number.Uint64(), next.Number.Uint64())
		}
		branch = append([]*types.Header{h}, branch...)
	}
	observations, err := n.in.Admission.Observations(token)
	if err != nil {
		return nil, err
	}
	members := []decision.Member{}
	for _, h := range branch {
		var found *admission.Observation
		for i := range observations {
			obs := &observations[i]
			if obs.Candidate.Hash == hexOf(h.Hash()) && obs.State == admission.ExecReady &&
				(found == nil || obs.Sequence < found.Sequence) {
				found = obs
			}
		}
		if found == nil {
			return nil, fmt.Errorf("%w: block %d %s", ErrCorpus, h.Number.Uint64(), hexOf(h.Hash()))
		}
		members = append(members, decision.Member{Observation: found.Key, Block: found.Candidate.Hash,
			ExecReadyDigest: found.ExecReady.Digest, ChildRoot: hexOf(h.Root)})
	}
	return members, nil
}

// Canonical implements core.ExtractorPass: the CANONICAL intent of a head
// change from old to next, with the new branch's blocks as members. In
// BOOTSTRAP the pass does not observe and no intent is made.
func (p *pass) Canonical(operation string, old, next *types.Header) error {
	if p.covered || !p.gp.Observe() {
		return nil
	}
	n := p.node
	members, err := n.members(p.token, old, next)
	if err != nil {
		return err
	}
	want := next.Hash()
	p.check = func() error {
		if got := n.in.Chain.CurrentBlock().Hash(); got != want {
			return fmt.Errorf("%w: %s ended at head %x, not %x", ErrHead, p.id, got, want)
		}
		return nil
	}
	return p.intend(Evidence{Caller: p.id, Kind: decision.Canonical, Operation: operation, OldHead: headOf(old),
		NewHead: headOf(next)}, members, true)
}

// Decide implements core.ExtractorPass.
func (p *pass) Decide() error {
	if p.covered || p.open == nil {
		return nil
	}
	n := p.node
	if p.check != nil {
		if err := p.check(); err != nil {
			return err
		}
	}
	if n.Step != nil {
		n.Step("marker", p.id)
	}
	rec := *p.open
	value, err := decision.MarkerValue(rec)
	if err != nil {
		return err
	}
	batch := n.in.DB.NewBatch()
	if err := batch.Put(decision.MarkerKey(n.in.Admission.Producer(), rec.Sequence), value); err != nil {
		return err
	}
	if err := batch.Write(); err != nil {
		return err
	}
	if err := n.in.DB.SyncKeyValue(); err != nil {
		return err
	}
	if n.Step != nil {
		n.Step("resolve", p.id)
	}
	if _, err := n.in.Bridge.Resolve(p.token, rec.Sequence); err != nil {
		return err
	}
	p.open, p.check = nil, nil
	if err := n.in.DB.Delete(n.evidenceKey(rec.EvidenceDigest)); err != nil {
		return err
	}
	return n.in.Bridge.Cleanup(p.token)
}

// innermost returns the innermost open pass.
func (n *Node) innermost() *pass {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.stack) == 0 {
		return nil
	}
	return n.stack[len(n.stack)-1]
}

// finality is the rawdb finality hook: one FINALITY decision per native
// finality write, in the innermost pass, which holds the guard.
func (n *Node) finality(name string, key, value []byte, deleted bool) (func() error, error) {
	p := n.innermost()
	if p == nil || !p.gp.Observe() {
		// No pass: Require fails closed (ErrBypass). BOOTSTRAP: uncaptured.
		if err := n.gate.Require(lifecycle.Finality); err != nil {
			return nil, err
		}
		return func() error { return nil }, nil
	}
	if p.open != nil {
		// A covered set-head inside a milestone reorganization clears a
		// finality reference while the outer CANONICAL intent is open: the
		// clear cannot be its own decision, so it fails closed.
		return nil, fmt.Errorf("%w: %s %s under the open %s intent %d of %s", lifecycle.ErrOrder, name,
			hex.EncodeToString(key), p.open.Kind, p.open.Sequence, p.id)
	}
	present, err := n.in.DB.Has(key)
	if err != nil {
		return nil, err
	}
	var old []byte
	if present {
		if old, err = n.in.DB.Get(key); err != nil {
			return nil, err
		}
	}
	head := headOf(n.in.Chain.CurrentBlock())
	next := valueDigest(value, !deleted)
	e := Evidence{Caller: p.id, Kind: decision.Finality, Operation: name, OldHead: head, NewHead: head,
		Key: hex.EncodeToString(key), Old: valueDigest(old, present), New: next}
	if err := p.intend(e, nil, false); err != nil {
		return nil, err
	}
	p.check = func() error {
		if got, err := n.readValue(key); err != nil || got != next {
			return fmt.Errorf("%w: %s %s is %q, not %q (%v)", ErrHead, p.id, name, got, next, err)
		}
		return nil
	}
	if err := n.gate.Require(lifecycle.Finality); err != nil {
		return nil, err
	}
	return p.Decide, nil
}

func (n *Node) readValue(key []byte) (string, error) {
	present, err := n.in.DB.Has(key)
	if err != nil || !present {
		return "", err
	}
	value, err := n.in.DB.Get(key)
	if err != nil {
		return "", err
	}
	return valueDigest(value, true), nil
}

// whitelist is the whitelist hook: the pass of a whitelist caller, taken
// before the whitelist finality lock and never under chainmu.
func (n *Node) whitelist(id string) (func() error, error) {
	p, err := n.enter(id, false)
	if err != nil {
		return nil, err
	}
	return p.Exit, nil
}

// ---------------------------------------------------------------------------
// Startup reconcile (bor.startup-reconcile).

// settleAll is the first part of the startup reconcile (bor.startup-reconcile).
// It runs inside the bor.recovery pass, before the decision recovery driver,
// and settles every unresolved lifecycle intent without a marker from native
// evidence. An intent with a marker is left to the decision recovery driver,
// which resolves it; an intent without an evidence record belongs to the
// import path and is left to it too.
func (n *Node) settleAll(token *admission.Token) error {
	decisions, err := n.in.Bridge.Decisions(token)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if d.State == decision.Intended && !d.Marker {
			if err := n.settle(token, d); err != nil {
				return err
			}
		}
	}
	return nil
}

// repair is the second part of the startup reconcile. It runs after the
// decision recovery driver, still inside the bor.recovery pass, when every
// intent is settled: a native head that differs from the last decided head
// is native startup repair made before the gate opened, and becomes a
// CANONICAL decision of bor.startup-reconcile. A native head below H, or an
// intent that is still unresolved, is a source invariant error.
func (n *Node) repair(token *admission.Token) error {
	decisions, err := n.in.Bridge.Decisions(token)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if d.State == decision.Intended {
			return fmt.Errorf("%w: intent %d is unresolved after the decision recovery", lifecycle.ErrInvariant,
				d.Sequence)
		}
	}
	// Every intent is resolved now. The decision recovery driver resolved
	// the intents it found with their markers (and may have cleaned up a
	// FINALITY one already), so the evidence records left are deleted here,
	// as Decide would have deleted them.
	if err := n.deleteEvidence(); err != nil {
		return err
	}
	b, live := n.gate.Boundary()
	if !live {
		return nil
	}
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].Sequence < decisions[j].Sequence })
	last := b.Head
	for _, d := range decisions {
		switch d.State {
		case decision.Decided:
			last = d.Intent.NewHead
		case decision.Intended:
			return fmt.Errorf("%w: intent %d is unresolved after the decision recovery", lifecycle.ErrInvariant,
				d.Sequence)
		}
	}
	native := n.in.Chain.CurrentBlock()
	if native.Number.Uint64() < b.Head.Number {
		return fmt.Errorf("%w: native head %d is below H %d", lifecycle.ErrInvariant, native.Number.Uint64(),
			b.Head.Number)
	}
	if hexOf(native.Hash()) == last.Hash {
		return nil
	}
	old := n.in.Chain.GetHeaderByHash(common.HexToHash(last.Hash))
	if old == nil {
		return fmt.Errorf("%w: the last decided head %d %s is not stored", lifecycle.ErrInvariant, last.Number,
			last.Hash)
	}
	p, err := n.enter("bor.startup-reconcile", true)
	if err != nil {
		return err
	}
	if err := p.Canonical("startup-repair", old, native); err != nil {
		return errors.Join(err, p.Exit())
	}
	if err := p.Decide(); err != nil {
		return errors.Join(err, p.Exit())
	}
	repaired := headOf(native)
	n.report.Repaired = &repaired
	return p.Exit()
}

// deleteEvidence deletes every evidence record of the producer run.
func (n *Node) deleteEvidence() error {
	p := n.in.Admission.Producer()
	it := n.in.DB.NewIterator([]byte(evidencePrefix+p.ProducerID+"."+p.RunID+"/evidence/"), nil)
	keys := [][]byte{}
	for it.Next() {
		keys = append(keys, append([]byte{}, it.Key()...))
	}
	err := it.Error()
	it.Release()
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := n.in.DB.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

// ancestor reports whether a is b or one of b's ancestors.
func (n *Node) ancestor(a, b *types.Header) bool {
	for h := b; h != nil; h = n.in.Chain.GetHeader(h.ParentHash, h.Number.Uint64()-1) {
		if h.Number.Uint64() <= a.Number.Uint64() {
			return h.Hash() == a.Hash()
		}
	}
	return false
}

// complete finishes a CANONICAL native mutation that a process loss
// interrupted: native rewinds and reorganizations move the head in steps, so
// the native head can lie on the path between the intent's old and new head
// (a rewind: on the old chain above the new head; a reorganization: on the
// new branch below the new head). The mutation is completed natively inside
// the recovery pass, with the saved intent covering the nested callers, and
// the intent is then decided. A native head off that path is left to the
// caller, which reports a source invariant error.
func (n *Node) complete(d decision.Decision, e Evidence) (bool, error) {
	native := n.in.Chain.CurrentBlock()
	next := n.in.Chain.GetHeaderByHash(common.HexToHash(e.NewHead.Hash))
	if next == nil || n.recovery == nil {
		return false, nil
	}
	rewind := n.ancestor(next, native) && native.Number.Uint64() < e.OldHead.Number
	reorg := n.ancestor(native, next)
	if !rewind && !reorg {
		return false, nil
	}
	intent := d.Intent
	n.mu.Lock()
	n.recovery.open = &intent
	n.mu.Unlock()
	err := n.in.Chain.ExtractorCompleteCanonical(next)
	n.mu.Lock()
	n.recovery.open = nil
	n.mu.Unlock()
	return err == nil, err
}

// settle settles one unresolved intent without a marker whose evidence
// record names a lifecycle caller; an intent without one belongs to the
// import path and stays for the decision recovery driver.
func (n *Node) settle(token *admission.Token, d decision.Decision) error {
	key := n.evidenceKey(d.Intent.EvidenceDigest)
	present, err := n.in.DB.Has(key)
	if err != nil || !present {
		return err
	}
	data, err := n.in.DB.Get(key)
	if err != nil {
		return err
	}
	var e Evidence
	if err := json.Unmarshal(data, &e); err != nil || EvidenceDigest(e) != d.Intent.EvidenceDigest {
		return fmt.Errorf("%w: evidence record of intent %d", lifecycle.ErrCorrupt, d.Sequence)
	}
	var now, next, old string
	completed := false
	switch e.Kind {
	case decision.Canonical:
		now, next, old = hexOf(n.in.Chain.CurrentBlock().Hash()), e.NewHead.Hash, e.OldHead.Hash
		if now != next && now != old {
			if completed, err = n.complete(d, e); err != nil {
				return err
			}
			now = hexOf(n.in.Chain.CurrentBlock().Hash())
		}
	case decision.Finality:
		raw, err := hex.DecodeString(e.Key)
		if err != nil {
			return fmt.Errorf("%w: evidence key of intent %d", lifecycle.ErrCorrupt, d.Sequence)
		}
		if now, err = n.readValue(raw); err != nil {
			return err
		}
		next, old = e.New, e.Old
	default:
		return fmt.Errorf("%w: lifecycle intent %d of kind %s", lifecycle.ErrInvariant, d.Sequence, e.Kind)
	}
	outcome := ""
	switch now {
	case next:
		value, err := decision.MarkerValue(d.Intent)
		if err != nil {
			return err
		}
		if err := n.in.DB.Put(decision.MarkerKey(n.in.Admission.Producer(), d.Sequence), value); err != nil {
			return err
		}
		if err := n.in.DB.SyncKeyValue(); err != nil {
			return err
		}
		if _, err := n.in.Bridge.Resolve(token, d.Sequence); err != nil {
			return err
		}
		outcome = string(decision.Decided)
	case old:
		if _, err := n.in.Bridge.RejectIntent(token, d.Sequence, Digest(tagRejection, d.Intent.Digest, now)); err != nil {
			return err
		}
		outcome = string(decision.Rejected)
	default:
		return fmt.Errorf("%w: native state of intent %d (%s) is neither its old nor its new value",
			lifecycle.ErrInvariant, d.Sequence, e.Caller)
	}
	n.report.Settled = append(n.report.Settled, Settled{Sequence: d.Sequence, Caller: e.Caller, Outcome: outcome,
		Completed: completed})
	if err := n.in.DB.Delete(key); err != nil {
		return err
	}
	return n.in.Bridge.Cleanup(token)
}
