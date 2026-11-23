package bsc

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/calls"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/receipt"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/selfdestruct"
	"github.com/sigaev-pro/bc-indexer/core/normalization/bsc/state"
	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Sentinel errors of the record validation.
var (
	ErrRecordShape    = errors.New("bsc: the record shape violates the family contract")
	ErrSystemEngine   = errors.New("bsc: a ParliaSystem root frame is a NATIVE_PROTOCOL frame; an Ordinary root frame is EVM")
	ErrSystemGasPrice = errors.New("bsc: a ParliaSystem transaction carries an explicit zero effective gas price")
	ErrSystemBoundary = errors.New("bsc: a system transition expectation lacks its boundary points")
)

// Record is the normalized view of one included BSC body transaction.
type Record struct {
	// Classification is Ordinary or ParliaSystem over the actual wire
	// type; the wire type, signature and payload stay unchanged in
	// Envelope.
	Classification Classification
	// Position is the native body position of the transaction.
	Position uint64
	// TransactionHash is the native transaction hash.
	TransactionHash [32]byte

	// Envelope keeps the typed effective projection with provenance and
	// the raw wire facts.
	Envelope EnvelopeFacts

	// Calls is the full call tree, including failed and reverted frames.
	Calls calls.Tree
	// Scopes is the ordered rollback-scope list, scope 0 first.
	Scopes []balance.RollbackScope
	// Actions is the causal monetary action list in execution order.
	Actions []balance.BalanceAction
	// ExecutionActions is the SELFDESTRUCT action list in execution
	// order.
	ExecutionActions []selfdestruct.Action
	// Receipt is the typed receipt with the source consensus encoding and
	// the mandatory, explicitly empty BscReceiptDetails variant.
	Receipt receipt.Receipt
	// StateDiff is the committed transaction-boundary diff.
	StateDiff state.Diff
	// SystemCallKind is the registered kind of a ParliaSystem call;
	// SystemCallInvalid for Ordinary transactions.
	SystemCallKind SystemCallKind
	// Coinbase is the block coinbase observed with the execution.
	Coinbase balance.Address20
}

// Validate checks the whole record against the family contract: the call
// tree, the rollback-scope tree, the balance view (through the shared
// phase-1 validator), the receipt, the state diff, and the BSC
// classification consistency of the root frame.
func (r *Record) Validate() error {
	var errs []error
	fail := func(err error) { errs = append(errs, fmt.Errorf("%w: %v", ErrRecordShape, err)) }

	if err := r.Calls.Validate(); err != nil {
		fail(err)
	}
	root := r.Calls.Root()
	if root != nil {
		switch r.Classification {
		case ClassificationParliaSystem:
			if root.Engine != balance.EngineNativeProtocol || root.Kind != calls.KindNativeProtocol {
				fail(fmt.Errorf("root engine %s kind %s: %w", root.Engine, root.Kind, ErrSystemEngine))
			}
			if !r.Envelope.EffectiveGasPrice.IsZero() {
				fail(fmt.Errorf("price %s: %w", r.Envelope.EffectiveGasPrice.Decimal(), ErrSystemGasPrice))
			}
		case ClassificationOrdinary:
			if root.Engine != balance.EngineEVM || root.Kind == calls.KindNativeProtocol {
				fail(fmt.Errorf("root engine %s kind %s: %w", root.Engine, root.Kind, ErrSystemEngine))
			}
		default:
			fail(fmt.Errorf("classification %s", r.Classification))
		}
	}
	if err := r.Receipt.Validate(); err != nil {
		fail(err)
	}
	if err := r.StateDiff.Validate(); err != nil {
		fail(err)
	}
	for _, a := range r.ExecutionActions {
		if err := a.Validate(); err != nil {
			fail(err)
		}
	}
	view := r.Balances()
	if err := balance.Validate(view); err != nil {
		fail(err)
	}
	return errors.Join(errs...)
}

// Balances builds the shared phase-1 balance view of the record: the
// scopes, the actions, the frame presence view, the execution-action
// presence view and the fee summary aggregate.
func (r *Record) Balances() balance.TxBalances {
	frames := r.Calls.Refs()
	execRefs := make([]balance.ExecutionActionRef, 0, len(r.ExecutionActions))
	for _, a := range r.ExecutionActions {
		execRefs = append(execRefs, a.Ref())
	}
	summary := r.Receipt.FeeSummary
	return balance.TxBalances{
		Scopes:           r.Scopes,
		Actions:          r.Actions,
		Frames:           frames,
		ExecutionActions: execRefs,
		FeeSummary:       &summary,
	}
}

// checkSystemTransition runs the independent Parlia system-transition
// expectation for a registered ParliaSystem call, from the committed
// boundary points of the record: the sender nonce and balance rules, the
// initializer no-move rule, the deposit distribution and the election
// dusk transfer. The checked kinds and fractions come from the retained
// E01 evidence; an unregistered call keeps its real effects and only the
// generic sender rules apply.
func (r *Record) checkSystemTransition(obs *Observation) error {
	if r.Classification != ClassificationParliaSystem {
		return nil
	}
	before := make(map[balance.Address20]AccountPoint)
	after := make(map[balance.Address20]AccountPoint)
	for i := range r.StateDiff.Accounts {
		a := &r.StateDiff.Accounts[i]
		before[a.Address] = AccountPoint{Balance: a.BalanceBefore, Nonce: a.NonceBefore, Exists: a.ExistedBefore}
		after[a.Address] = AccountPoint{Balance: a.BalanceAfter, Nonce: a.NonceAfter, Exists: a.ExistsAfter}
	}
	sender := r.Envelope.Sender
	if _, ok := before[sender]; !ok {
		return fmt.Errorf("%w: sender %x missing", ErrSystemBoundary, sender)
	}
	if _, ok := after[sender]; !ok {
		return fmt.Errorf("%w: sender %x after point missing", ErrSystemBoundary, sender)
	}
	distribution := obs.Distribution
	if distribution.Denominator == 0 {
		distribution = DefaultDistribution()
	}
	return ExpectSystemTransition(r.SystemCallKind, sender, r.Envelope.Value, obs.SystemAccounts, distribution, before, after)
}

// Digest returns the logical digest of the record: a stable function of
// the classification, the position, the tx hash, the frame orders, the
// scope tree, the action identities and the execution-action identities.
// Capture recovery compares these digests prefix-wise; equal digests mean
// equal logical IDs, orders and scopes for the same corpus.
func (r *Record) Digest() [32]byte {
	h := sha256.New()
	writeU64 := func(v uint64) {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], v)
		h.Write(buf[:])
	}
	writeTag := func(tag string) { h.Write([]byte(tag)) }

	writeTag("bsc.record.v1")
	writeU64(uint64(r.Classification))
	writeU64(r.Position)
	h.Write(r.TransactionHash[:])
	for _, f := range r.Calls.Frames {
		writeTag("f")
		writeU64(f.FrameID)
		writeU64(f.ExecutionOrder)
		writeU64(uint64(f.Engine))
		writeU64(uint64(f.Kind))
		writeU64(uint64(f.Outcome))
		if f.ParentID != nil {
			writeU64(*f.ParentID)
		} else {
			writeTag("root")
		}
	}
	for _, s := range r.Scopes {
		writeTag("s")
		writeU64(s.ScopeID)
		if s.ParentScopeID != nil {
			writeU64(*s.ParentScopeID)
		} else {
			writeTag("root")
		}
		writeU64(boolWord(s.RolledBack))
	}
	for _, a := range r.Actions {
		writeTag("a")
		writeU64(a.ActionIndex)
		writeU64(a.ExecutionOrder)
		writeU64(uint64(a.Cause.Kind))
		writeU64(a.Cause.OperationIndex)
		writeU64(uint64(a.Kind))
		writeU64(uint64(a.Cause.Operation))
		if a.From != nil {
			h.Write(a.From[:])
		}
		if a.To != nil {
			h.Write(a.To[:])
		}
		h.Write(a.Amount[:])
		writeU64(boolWord(a.EffectsCommitted))
	}
	for _, a := range r.ExecutionActions {
		writeTag("e")
		writeU64(a.ExecutionActionID)
		writeU64(a.ExecutionOrder)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func boolWord(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// ReconcileBoundary runs the fixture oracle over the record: the sum of
// the committed address effects must agree with the transaction-boundary
// points of every touched account. This is a test oracle, not a
// production balance completeness verifier.
func (r *Record) ReconcileBoundary() error {
	return balance.ReconcileBoundary(r.Scopes, r.Actions, r.StateDiff.BeforePoints(), r.StateDiff.AfterPoints())
}
