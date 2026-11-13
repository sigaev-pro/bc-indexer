// Extractor patch (E01-02.lifecycle_seams, Bor block): the decision bridge's
// side of the lifecycle gate. The preparation adds this file to the installed
// extractor/bordecision package; see adapters/bor/lifecycle/native/README.md.

package bordecision

import (
	"errors"
	"sync"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/admission/native/obligation"
	"github.com/ethereum/go-ethereum/extractor/borimport"
	"github.com/ethereum/go-ethereum/extractor/borlifecycle"
	"github.com/ethereum/go-ethereum/extractor/decision"
)

// extractorAcquire returns the guard token of the live decider: the token
// that the import pass of the lifecycle gate lends (the import path holds
// chainmu), or a newly acquired one. release frees only an acquired token.
func extractorAcquire(n *borimport.Node) (*admission.Token, func() error) {
	if held, ok := n.Chain().ExtractorHeld().(*admission.Token); ok && held != nil {
		return held, func() error { return nil }
	}
	token := n.Driver.Guard.Acquire()
	return token, token.Release
}

// extractorLifecycleOpen opens the lifecycle gate inside the recovery driver,
// with the guard held and chainmu held: the gate, the bor.recovery pass, the
// choke-point hooks and the startup reconcile. Without borlifecycle.Enable it
// returns nil and the node keeps the decision block's behavior.
func extractorLifecycleOpen(n *borimport.Node, token *admission.Token, bridge *decision.Bridge,
	chain *core.BlockChain) (*borlifecycle.Node, error) {
	return borlifecycle.Open(borlifecycle.Inputs{Chain: chain, DB: n.DB(), Guard: n.Driver.Guard,
		Admission: n.Driver.Admission, Bridge: bridge, StoreID: n.StoreID(), Roots: roots{chain: chain},
		Observer: n}, token)
}

// ErrNotBootstrapping refuses ExtractorGoLive on a node that is not in
// BOOTSTRAP.
var ErrNotBootstrapping = errors.New("decision bridge: the node is not in BOOTSTRAP")

// extractorDeferred holds the mirror of a node in BOOTSTRAP until its
// ExtractorGoLive.
var extractorDeferred sync.Map

// Start starts the decision bridge of a node that borimport.Start opened.
// On a capture node with the lifecycle gate (borlifecycle.Enable) whose store
// has no live boundary H yet, the node is in BOOTSTRAP (D105): the initial
// native sync runs natively and uncaptured, with no decider, observer or
// choke-point hook, and the recovery driver, H and GoLive wait for
// ExtractorGoLive. Otherwise the recovery driver and GoLive run at once.
func Start(n *borimport.Node, mirror decision.Mirror) (*Node, obligation.Report, error) {
	boot, err := borlifecycle.Bootstrapping(n.DB(), n.Driver.Guard, n.Driver.Admission, n.StoreID())
	if err != nil {
		return &Node{Import: n}, obligation.Report{}, err
	}
	if !boot {
		return extractorStart(n, mirror)
	}
	node := &Node{Import: n}
	if mirror == nil {
		mirror = NoMirror{}
	}
	extractorDeferred.Store(node, mirror)
	return node, obligation.Report{}, nil
}

// Bootstrapping reports whether the node waits in BOOTSTRAP.
func (n *Node) Bootstrapping() bool {
	_, ok := extractorDeferred.Load(n)
	return ok
}

// ExtractorGoLive ends BOOTSTRAP after the native initial sync: the recovery
// driver, the lifecycle gate with H read under chainmu and the guard, GoLive,
// and the decider and observer installed in the same lock, so no import runs
// uncaptured after H.
func (n *Node) ExtractorGoLive() (obligation.Report, error) {
	value, ok := extractorDeferred.Load(n)
	if !ok {
		return obligation.Report{}, ErrNotBootstrapping
	}
	started, report, err := extractorStart(n.Import, value.(decision.Mirror))
	if err != nil {
		return report, err
	}
	extractorDeferred.Delete(n)
	*n = *started
	return report, nil
}
