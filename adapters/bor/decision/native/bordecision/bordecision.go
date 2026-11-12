// Package bordecision binds the decision bridge (E01-02.decision_bridge) to
// the Bor product import path: the child-root barrier over the hash trie
// database, the live decider of the import path, and the recovery driver
// that settles saved intents and completes the native import of EXEC_READY
// observations in RECOVERY_ONLY before GoLive. The design is in
// adapters/bor/decision/native/README.md.
//
// The preparation installs it at extractor/bordecision in the accepted Bor
// tree. The decision rules live in the stdlib-only package
// extractor/decision/native/decider.
package bordecision

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/admission/native/obligation"
	"github.com/ethereum/go-ethereum/extractor/borimport"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/extractor/decision/native/decider"
)

// ErrCapsule reports a capsule that is not the saved candidate.
var ErrCapsule = errors.New("capsule is not the saved candidate")

func hexOf(hash common.Hash) string { return hex.EncodeToString(hash[:]) }

func headOf(h *types.Header) decision.Head {
	return decision.Head{Number: h.Number.Uint64(), Hash: hexOf(h.Hash())}
}

func hashOf(value string) (common.Hash, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != common.HashLength {
		return common.Hash{}, fmt.Errorf("%w: hash %q", decision.ErrInvalid, value)
	}
	return common.BytesToHash(raw), nil
}

// NoMirror is the mirror until E05-01.registry exists: it has applied no
// decision, so every NATIVE_DECIDED BATCH decision keeps its obligation and
// its marker.
type NoMirror struct{}

// Applied implements decision.Mirror.
func (NoMirror) Applied(uint64, string) (bool, error) { return false, nil }

// roots is the child-root barrier check over the native store.
type roots struct{ chain *core.BlockChain }

func (r roots) Durable(root string) (bool, error) {
	hash, err := hashOf(root)
	if err != nil {
		return false, err
	}
	return r.chain.ExtractorRootDurable(hash)
}

// Node is the decision bridge of one running Bor node.
type Node struct {
	Import  *borimport.Node
	Bridge  *decision.Bridge
	Decider *decider.Decider
	// Report is what the decision recovery driver did at the start.
	Report decider.Report
	live   liveDecider
}

// Start runs the recovery driver of a node that borimport.Start opened in
// RECOVERY_ONLY, with the native chain lock held, then the observation
// guard: the admission recovery (INPUT_PREPARING abandoned, ADMITTED
// re-executed to EXEC_READY or rejected), decision.Open, the decision
// recovery (decider.Recover: the saved intent settled, the native import of
// every EXEC_READY observation without a decision completed from its saved
// inputs) and GoLive. Only then does it install the decider and the
// observer. mirror nil is NoMirror. On an error the node stays in
// RECOVERY_ONLY without a decider or observer; the returned node then only
// carries what the decision recovery reported before the error.
func Start(n *borimport.Node, mirror decision.Mirror) (*Node, obligation.Report, error) {
	if mirror == nil {
		mirror = NoMirror{}
	}
	chain := n.Chain()
	node := &Node{Import: n}
	var report obligation.Report
	err := chain.ExtractorRecover(func(rec *core.ExtractorRecovery) error {
		var err error
		report, err = n.RecoverThen(func(token *admission.Token) error {
			bridge, err := decision.Open(token, n.DB(), n.Driver.Admission, n.StoreID(), roots{chain: chain}, mirror)
			if err != nil {
				return err
			}
			d := &decider.Decider{Admission: n.Driver.Admission, Bridge: bridge}
			d.Native = recovery{node: n, rec: rec, d: d}
			node.Bridge, node.Decider = bridge, d
			if node.Report, err = d.Recover(token); err != nil {
				return err
			}
			d.Native = live{chain: chain}
			node.live = liveDecider{node: n, d: d}
			chain.SetExtractorDecider(node.live)
			return nil
		})
		return err
	})
	if err != nil {
		chain.SetExtractorDecider(nil)
	}
	return node, report, err
}

// Detach removes the decider and the observer.
func (n *Node) Detach() {
	n.Import.Chain().SetExtractorDecider(nil)
	n.Import.Detach()
}

// Attach installs the decider again with observer (the node's own observer,
// or a wrapper of it) after a Detach.
func (n *Node) Attach(observer core.ExtractorObserver) {
	n.Import.Chain().SetExtractorDecider(n.live)
	n.Import.Chain().SetExtractorObserver(observer)
}

func transition(t core.ExtractorTransition) decider.Transition {
	tr := decider.Transition{Kind: decision.Kind(t.Kind), Block: headOf(t.Block.Header()), ChildRoot: hexOf(t.Block.Root()),
		OldHead: headOf(t.OldHead), NewHead: headOf(t.NewHead), LocalTD: t.LocalTD.String(), ExternTD: t.ExternTD.String()}
	for _, h := range t.Branch {
		tr.Branch = append(tr.Branch, decider.BranchBlock{Block: headOf(h), Root: hexOf(h.Root)})
	}
	return tr
}

// intent implements core.ExtractorIntent over a recorded intent.
type intent struct {
	d       *decider.Decider
	token   *admission.Token
	rec     decision.IntentRecord
	block   decision.Head
	key     []byte
	value   []byte
	release func() error
	// rejected, if set, is set when the native rejection was recorded.
	rejected *bool
}

func newIntent(d *decider.Decider, token *admission.Token, rec decision.IntentRecord, block decision.Head,
	release func() error) (*intent, error) {
	value, err := decision.MarkerValue(rec)
	if err != nil {
		return nil, err
	}
	return &intent{d: d, token: token, rec: rec, block: block, key: decision.MarkerKey(d.Admission.Producer(), rec.Sequence),
		value: value, release: release}, nil
}

func (i *intent) Marker() ([]byte, []byte) { return i.key, i.value }

func (i *intent) done(err error) error {
	if i.release == nil {
		return err
	}
	return errors.Join(err, i.release())
}

func (i *intent) Resolve() error { return i.done(i.d.Resolve(i.token, i.rec.Sequence)) }

func (i *intent) Reject(nativeErr error) error {
	err := i.d.Reject(i.token, i.rec, i.block, nativeErr)
	if err == nil && i.rejected != nil {
		*i.rejected = true
	}
	return i.done(err)
}

func (i *intent) Release() error { return i.done(nil) }

// liveDecider is the decider of the import path. It takes the observation
// guard with the native chain lock already held and keeps it until the
// intent is resolved, rejected or released.
type liveDecider struct {
	node *borimport.Node
	d    *decider.Decider
}

func (l liveDecider) Intend(t core.ExtractorTransition) (core.ExtractorIntent, error) {
	token := l.node.Driver.Guard.Acquire()
	tr := transition(t)
	rec, err := l.d.Intend(token, tr, false)
	if err == nil {
		var in *intent
		if in, err = newIntent(l.d, token, rec, tr.Block, token.Release); err == nil {
			return in, nil
		}
	}
	return nil, errors.Join(err, token.Release())
}

// recoveryDecider is the decider of a recovery completion: it runs under the
// recovery driver's token and returns the saved intent of the same
// transition.
type recoveryDecider struct {
	d        *decider.Decider
	token    *admission.Token
	rejected *bool
}

func (r recoveryDecider) Intend(t core.ExtractorTransition) (core.ExtractorIntent, error) {
	tr := transition(t)
	rec, err := r.d.Intend(r.token, tr, true)
	if err != nil {
		return nil, err
	}
	in, err := newIntent(r.d, r.token, rec, tr.Block, nil)
	if err == nil {
		in.rejected = r.rejected
	}
	return in, err
}

// live is the native side of the import path.
type live struct{ chain *core.BlockChain }

func (l live) Stored(block decision.Head) (bool, error) {
	hash, err := hashOf(block.Hash)
	if err != nil {
		return false, err
	}
	return l.chain.HasBlock(hash, block.Number), nil
}

func (live) Complete(*admission.Token, admission.Observation) error {
	return fmt.Errorf("%w: completion outside recovery", decision.ErrState)
}

// recovery is the native side of the recovery driver.
type recovery struct {
	node *borimport.Node
	rec  *core.ExtractorRecovery
	d    *decider.Decider
}

func (r recovery) Stored(block decision.Head) (bool, error) {
	hash, err := hashOf(block.Hash)
	if err != nil {
		return false, err
	}
	return r.rec.Stored(hash, block.Number), nil
}

// Complete replays obs from its capsule and closed log (obligation
// ReplayHeld: every counted read from the log, every saved entry consumed,
// the bound result digest) and completes the native import of its block
// with the execution of that replay.
func (r recovery) Complete(token *admission.Token, obs admission.Observation) error {
	run := &execution{node: r.node}
	if err := r.node.Driver.ReplayHeld(token, obs.Key, run); err != nil {
		return err
	}
	if run.block == nil {
		return fmt.Errorf("%w: %s was not executed", ErrCapsule, obs.Key)
	}
	// A native refusal returns the native error after the rejection was
	// recorded: the decision is settled, so recovery goes on.
	rejected := false
	err := r.rec.Import(recoveryDecider{d: r.d, token: token, rejected: &rejected}, run.block, run.receipts, run.logs,
		run.statedb)
	if err != nil && rejected {
		return nil
	}
	return err
}

// execution is the native-only replay of one observation that keeps the
// native result for the write.
type execution struct {
	node     *borimport.Node
	block    *types.Block
	receipts types.Receipts
	logs     []*types.Log
	statedb  *state.StateDB
}

// Reexecute implements obligation.Reexecutor: native execution and
// validation of the capsule block on its pinned parent state.
func (e *execution) Reexecute(key string, cand admission.Candidate) (string, error, error) {
	c, err := borimport.ReadCapsule(e.node.DB(), key)
	if err != nil {
		return "", nil, err
	}
	if borimport.Candidate(c.Block, c.Parent) != cand {
		return "", nil, fmt.Errorf("%w: %s", ErrCapsule, key)
	}
	chain := e.node.Chain()
	if !chain.HasState(c.Parent.Root) {
		return "", nil, fmt.Errorf("pinned parent state %s of %s is unavailable", c.Parent.Root, key)
	}
	receipts, logs, usedGas, statedb, _, nativeErr := chain.ProcessBlock(c.Block, c.Parent, nil, nil, nil)
	if nativeErr != nil && core.ExtractorLocalStateUnavailable(nativeErr) {
		return "", nil, nativeErr
	}
	if nativeErr != nil {
		return "", nativeErr, nil
	}
	e.block, e.receipts, e.logs, e.statedb = c.Block, receipts, logs, statedb
	return borimport.ResultDigest(c.Block, usedGas), nil, nil
}
