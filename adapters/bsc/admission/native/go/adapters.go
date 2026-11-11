package bsc

// Adapters from the native pin ledger (E01-02.pins) and the source inputs
// core (E01-02.inputs) to the Pins and Inputs interfaces of the admission
// core.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
)

// PinRecordPrefix is the key prefix of a pin ledger record in the pinned
// state_provider source (pinPrefix there, unexported). The pin adapter test
// checks that a record under this key is the one that Pins lists.
const PinRecordPrefix = "extractor-state-provider-pin-v1-"

var (
	// ErrPinMismatch reports a pin whose ledger record names another parent
	// root, or whose record is missing.
	ErrPinMismatch = errors.New("pin ledger record does not match")
	// ErrNotClosed reports a dependency log without a closed manifest.
	ErrNotClosed = errors.New("dependency log is not closed")
)

// LedgerPins is the admission Pins interface over the native pin ledger.
type LedgerPins struct {
	DB       ethdb.Database
	Provider *stateprovider.Provider
}

// Verify checks that the pin exists, holds parentRoot and that every native
// reference it names is readable, and returns the SHA-256 of its exact ledger
// record bytes.
func (p LedgerPins) Verify(id, parentRoot string) (string, error) {
	record, err := p.DB.Get([]byte(PinRecordPrefix + id))
	if err != nil {
		return "", fmt.Errorf("%w: no ledger record for %s: %v", ErrPinMismatch, id, err)
	}
	pins, err := stateprovider.Pins(p.DB)
	if err != nil {
		return "", err
	}
	for _, pin := range pins {
		if pin.ID != id {
			continue
		}
		if hex.EncodeToString(pin.ParentRoot.Bytes()) != parentRoot {
			return "", fmt.Errorf("%w: pin %s holds root %s", ErrPinMismatch, id, pin.ParentRoot)
		}
		if err := stateprovider.Verify(p.DB, pin); err != nil {
			return "", err
		}
		digest := sha256.Sum256(record)
		return hex.EncodeToString(digest[:]), nil
	}
	return "", fmt.Errorf("%w: pin %s is not listed", ErrPinMismatch, id)
}

// Release removes the pin. An absent pin is already released.
func (p LedgerPins) Release(id string) error {
	if err := p.Provider.Release(id); err != nil && !errors.Is(err, stateprovider.ErrUnknownPin) {
		return err
	}
	return nil
}

// Inputs is the admission Inputs interface over the source inputs core.
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

func hexHash(hash common.Hash) string { return hex.EncodeToString(hash.Bytes()) }
