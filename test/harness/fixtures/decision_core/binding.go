// Package decisionbinding adapts the source inputs core (E01-02.inputs) to
// the InputPurger interface of the admission core, so that closing an
// observation deletes its capsule and dependency log (D100).
//
// This is test-only glue for block 0 of E01-02.decision_bridge. `python3
// test/harness/decision.py assemble DIR` installs it with source_inputs,
// admission and decision under extractor/ in a module named
// github.com/ethereum/go-ethereum, the path at which the client blocks
// install them in the accepted native trees.
package decisionbinding

import sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"

// Purger deletes the inputs of a closed observation from the native store.
type Purger struct {
	DB sourceinputs.Deleter
}

// Purge deletes the capsule and dependency log of the observation.
func (c Purger) Purge(observation string) error {
	return sourceinputs.Purge(c.DB, observation)
}
