// Extractor patch (E01-02.lifecycle_seams, Bor block): the decision bridge's
// side of the lifecycle gate. The preparation adds this file to the installed
// extractor/bordecision package; see adapters/bor/lifecycle/native/README.md.

package bordecision

import (
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/extractor/admission"
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
		Admission: n.Driver.Admission, Bridge: bridge, StoreID: n.StoreID(), Roots: roots{chain: chain}}, token)
}
