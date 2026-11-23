package poly

import (
	"fmt"
	"sort"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// normalizeBalanceActions maps the captured monetary operations onto the
// closed D62 action registry. One captured invocation becomes exactly one
// action that groups its effects; the normalizer never splits or merges
// invocations by amount proximity, never invents an OTHER fallback for an
// unknown native path, and keeps net-zero pairs as separate actions.
func normalizeBalanceActions(tx *TxCapture, orders *txOrders, scopeOfRow []uint64) ([]balance.BalanceAction, error) {
	out := make([]balance.BalanceAction, len(tx.Operations))
	for i, op := range tx.Operations {
		action := balance.BalanceAction{
			ExecutionOrder:   orders.ops[i],
			Amount:           op.Amount,
			Asset:            balance.AssetNative,
			EffectsCommitted: op.Committed,
			From:             op.From,
			To:               op.To,
		}
		for j, eff := range op.Effects {
			action.Effects = append(action.Effects, balance.BalanceEffect{
				EffectIndex:      uint64(j),
				Address:          eff.Address,
				Before:           eff.Before,
				After:            eff.After,
				NativeReasonCode: eff.ReasonCode,
			})
		}
		scope, err := scopeID(op.Snapshot, scopeOfRow)
		if err != nil {
			return nil, fmt.Errorf("op %d: %w", i, err)
		}
		actionScope := scope
		action.RollbackScope = &actionScope

		switch op.Cause {
		case OpCauseFrame:
			if op.Frame < 0 || op.Frame >= len(tx.Frames) {
				return nil, fmt.Errorf("op %d references unknown frame %d: %w", i, op.Frame, ErrCapture)
			}
			action.Cause = balance.FrameCause(uint64(op.Frame))
			action.Engine = balance.EngineEVM
			action.Kind = balance.KindTransfer
		case OpCauseSelfDestruct:
			if op.SelfDestruct < 0 || op.SelfDestruct >= len(tx.SelfDestructs) {
				return nil, fmt.Errorf("op %d references unknown selfdestruct %d: %w", i, op.SelfDestruct, ErrCapture)
			}
			id := uint64(op.SelfDestruct) + 1
			action.Cause = balance.ExecutionActionCause(id)
			action.Engine = balance.EngineEVM
			action.Kind = balance.KindSelfDestructTransfer
		case OpCauseProtocol:
			action.Cause = balance.ProtocolCause(op.Operation, orders.protocolIndex[i])
			switch op.Operation {
			case balance.OpGasPurchase:
				action.Engine = balance.EngineEVM
				action.Kind = balance.KindFee
			case balance.OpGasReturn:
				action.Engine = balance.EngineEVM
				action.Kind = balance.KindRefund
			case balance.OpFeeRecipientCredit:
				action.Engine = balance.EngineEVM
				action.Kind = balance.KindFee
			case balance.OpSelfDestructFinalizationBurn:
				action.Engine = balance.EngineEVM
				action.Kind = balance.KindBurn
				for _, ref := range op.Related {
					if ref < 0 || ref >= len(tx.SelfDestructs) {
						return nil, fmt.Errorf("op %d references unknown related selfdestruct %d: %w", i, ref, ErrCapture)
					}
					action.Cause.RelatedExecutionActions = append(action.Cause.RelatedExecutionActions, uint64(ref)+1)
				}
				if len(action.Cause.RelatedExecutionActions) == 0 {
					return nil, fmt.Errorf("op %d finalization burn without related actions: %w", i, ErrCapture)
				}
			case balance.OpBorStateSyncExecution:
				// The actual effects of the registered Bor state-sync
				// execution path are kept as they were observed; no
				// economic transfer shape is invented for them.
				action.Engine = balance.EngineNativeProtocol
				action.Kind = balance.KindNativeProtocolChange
			default:
				return nil, fmt.Errorf("op %d operation %s is not in the closed Polygon registry: %w",
					i, op.Operation, ErrCapture)
			}
		default:
			return nil, fmt.Errorf("op %d cause is unspecified: %w", i, ErrCapture)
		}
		out[i] = action
	}

	// The stored list is sorted by execution order with the action_index
	// identity assigned in that order.
	sort.SliceStable(out, func(a, b int) bool { return out[a].ExecutionOrder < out[b].ExecutionOrder })
	for i := range out {
		out[i].ActionIndex = uint64(i) + 1
	}
	for i := 1; i < len(out); i++ {
		if out[i].ExecutionOrder == out[i-1].ExecutionOrder {
			return nil, fmt.Errorf("ops %d and %d share order %d: %w",
				i-1, i, out[i].ExecutionOrder, ErrOrder)
		}
	}
	return out, nil
}

// buildFeeSummary aggregates the fee view of the transaction from its fee
// actions (CURRENT_SEMANTICS D62). The summary is never a second monetary
// movement: every field equals the exact aggregate of the corresponding
// committed fee actions, and a zero-fee system transaction stores applicable
// explicit zeros with an absent payer.
func buildFeeSummary(actions []balance.BalanceAction) (*balance.FeeSummary, error) {
	summary := &balance.FeeSummary{RecipientCredits: nil}
	var purchase, ret UInt256
	for i := range actions {
		a := &actions[i]
		if a.Cause.Kind != balance.CauseTransactionProtocol {
			continue
		}
		switch a.Cause.Operation {
		case balance.OpGasPurchase:
			sum, err := purchase.AddChecked(a.Amount)
			if err != nil {
				return nil, fmt.Errorf("fee summary: %w", err)
			}
			purchase = sum
			if summary.Payer == nil {
				summary.Payer = a.From
			}
		case balance.OpGasReturn:
			sum, err := ret.AddChecked(a.Amount)
			if err != nil {
				return nil, fmt.Errorf("fee summary: %w", err)
			}
			ret = sum
		case balance.OpFeeRecipientCredit:
			if a.To == nil {
				return nil, fmt.Errorf("fee credit without a recipient: %w", ErrCapture)
			}
			summary.RecipientCredits = append(summary.RecipientCredits, balance.FeeCredit{
				Address: *a.To,
				Amount:  a.Amount,
			})
		}
	}
	net, err := purchase.SubChecked(ret)
	if err != nil {
		return nil, fmt.Errorf("fee summary: %w", err)
	}
	summary.GasPurchaseDebit = purchase
	summary.GasReturnCredit = ret
	summary.NetCharge = net
	return summary, nil
}
