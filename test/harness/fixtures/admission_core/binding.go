// Package admissionbinding adapts the source inputs core (E01-02.inputs) to
// the Inputs interface of the admission core (E01-02.admission).
//
// This is test-only glue for block 0 of E01-02.admission. `python3
// test/harness/admission.py assemble DIR` installs it with both packages
// under extractor/ in a module named github.com/ethereum/go-ethereum, the
// path at which the client blocks install them in the accepted native trees.
// The client blocks wire the native pin ledger themselves.
package admissionbinding

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/extractor/admission"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
)

// ErrNotClosed reports a dependency log without a closed consumed manifest.
var ErrNotClosed = errors.New("dependency log is not closed")

// Inputs reads capsules and closed consumed manifests from the native store.
type Inputs struct {
	DB sourceinputs.Store
}

// CapsuleDigest returns the digest of the sealed and checked capsule.
func (i Inputs) CapsuleDigest(observation string) (string, error) {
	capsule, err := sourceinputs.ReadCapsule(i.DB, observation)
	if err != nil {
		return "", err
	}
	return capsule.Digest, nil
}

// Consumed opens the log, which checks the chain of every durable entry and
// the closed manifest against it, and returns the closed manifest.
func (i Inputs) Consumed(observation string) (admission.Consumed, error) {
	log, err := sourceinputs.OpenLog(i.DB, observation)
	if err != nil {
		return admission.Consumed{}, err
	}
	closed, ok := log.Closed()
	if !ok {
		return admission.Consumed{}, fmt.Errorf("%w: %s", ErrNotClosed, observation)
	}
	return admission.Consumed{Observation: closed.Observation, CapsuleDigest: closed.CapsuleDigest,
		Count: closed.Count, Chain: closed.Chain, Digest: closed.Digest}, nil
}
