package eth

import (
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// Balance action errors.
var (
	// ErrActionShape reports a structurally invalid action observation.
	ErrActionShape = errors.New("eth: invalid balance action observation")
)

// ActionIndex is the tx-local balance action identity counter (1-based). It
// is its own identity, not a chronology shared with frames or execution
// actions.
type ActionIndex struct{ next uint64 }

// Next returns the next action index.
func (a *ActionIndex) Next() uint64 {
	a.next++
	return a.next
}

// ProtocolIndex is the tx-local counter of observed transaction-protocol
// invocations (0-based, no gaps). It is neither execution_order nor the
// native body position.
type ProtocolIndex struct{ next uint64 }

// Next returns the next protocol operation index.
func (p *ProtocolIndex) Next() uint64 {
	v := p.next
	p.next++
	return v
}

// FeeObservation is the observed native fee flow of one body transaction:
// the payer, the gas and blob charges, the fee recipient credit and the
// observed balance points of each fee mutation. ETH fee flow is EIP-1559:
// the payer is debited the upfront purchase (gas limit at the effective
// gas price plus the blob charge), the unused part returns after
// execution, and the recipient is credited the tip (effective gas price
// minus base fee times gas used). The base-fee burn is determined by fee
// rules and verified separately; no extra payer debit is invented on top
// of the charged gas (D62).
type FeeObservation struct {
	// Payer is the fee payer.
	Payer Address20
	// GasLimit is the transaction gas limit.
	GasLimit uint64
	// GasUsed is the actual gas used.
	GasUsed uint64
	// EffectiveGasPrice is the effective gas price of the execution.
	EffectiveGasPrice UInt256
	// BlobGasUsed and BlobGasPrice are the actual blob charge inputs.
	BlobGasUsed  Optional[uint64]
	BlobGasPrice Optional[UInt256]
	// Recipient is the native fee recipient (the block beneficiary).
	Recipient Address20
	// BaseFee is the block base fee used to split tip from burn.
	BaseFee UInt256

	// Observed balance points. PayerBalanceBefore is the payer point
	// before the purchase; PayerBalanceAtReturn is the point before the
	// return, required when the leftover is nonzero; RecipientBalanceBefore
	// is the recipient point before the credit. They are observations, not
	// derivations: native moved other funds in between.
	PayerBalanceBefore     UInt256
	PayerBalanceAtReturn   Optional[UInt256]
	RecipientBalanceBefore UInt256
}

// purchaseAmount computes the upfront debit: gas limit times the effective
// gas price plus the blob charge, with checked arithmetic.
func (f *FeeObservation) purchaseAmount() (UInt256, error) {
	amount, ok := f.EffectiveGasPrice.MulUint64(f.GasLimit)
	if !ok {
		return UInt256{}, fmt.Errorf("%w: gas purchase overflow", ErrActionShape)
	}
	if blobGas, hasBlob := f.BlobGasUsed.Get(); hasBlob {
		price, hasPrice := f.BlobGasPrice.Get()
		if !hasPrice {
			return UInt256{}, fmt.Errorf("%w: blob gas used without a blob gas price", ErrActionShape)
		}
		blob, ok := price.MulUint64(blobGas)
		if !ok {
			return UInt256{}, fmt.Errorf("%w: blob charge overflow", ErrActionShape)
		}
		sum, err := amount.AddChecked(blob)
		if err != nil {
			return UInt256{}, fmt.Errorf("%w: %v", ErrActionShape, err)
		}
		amount = sum
	} else if _, hasPrice := f.BlobGasPrice.Get(); hasPrice {
		return UInt256{}, fmt.Errorf("%w: blob gas price without blob gas used", ErrActionShape)
	}
	return amount, nil
}

// returnAmount computes the monetary return of unused gas.
func (f *FeeObservation) returnAmount() (UInt256, error) {
	if f.GasUsed > f.GasLimit {
		return UInt256{}, fmt.Errorf("%w: gas used %d above the limit %d",
			ErrActionShape, f.GasUsed, f.GasLimit)
	}
	leftover := f.GasLimit - f.GasUsed
	amount, ok := f.EffectiveGasPrice.MulUint64(leftover)
	if !ok {
		return UInt256{}, fmt.Errorf("%w: gas return overflow", ErrActionShape)
	}
	return amount, nil
}

// tipAmount computes the recipient credit: (effective gas price minus base
// fee) times gas used.
func (f *FeeObservation) tipAmount() (UInt256, error) {
	if f.EffectiveGasPrice.Cmp(f.BaseFee) < 0 {
		return UInt256{}, fmt.Errorf("%w: effective gas price below the base fee", ErrActionShape)
	}
	tipPrice, err := f.EffectiveGasPrice.SubChecked(f.BaseFee)
	if err != nil {
		return UInt256{}, fmt.Errorf("%w: %v", ErrActionShape, err)
	}
	tip, ok := tipPrice.MulUint64(f.GasUsed)
	if !ok {
		return UInt256{}, fmt.Errorf("%w: tip overflow", ErrActionShape)
	}
	return tip, nil
}

// BuildGasPurchaseAction normalizes the upfront gas purchase: one payer
// debit at the transaction boundary scope 0, with one payer effect. The
// purchase runs before any frame entry; its order is allocated here from
// the shared counter. Fee actions are actual movements and stay committed
// even when the root execution reverted (D74 rule 7).
//
// The smoke corpus fee rows are the fixtures: transaction 0 of block 1
// (test/native_corpus/expected/eth.json) purchases 42000000000000 wei,
// returns nothing (all 21000 gas used) and credits the coinbase tip of
// 23625000000000 wei.
func BuildGasPurchaseAction(order *Order, actions *ActionIndex, protocols *ProtocolIndex, fee FeeObservation) (balance.BalanceAction, error) {
	purchase, err := fee.purchaseAmount()
	if err != nil {
		return balance.BalanceAction{}, err
	}
	purchaseOrder, err := order.Allocate()
	if err != nil {
		return balance.BalanceAction{}, err
	}
	purchaseScope := uint64(0)
	payer := fee.Payer
	afterPurchase, err := fee.PayerBalanceBefore.SubChecked(purchase)
	if err != nil {
		return balance.BalanceAction{}, fmt.Errorf("%w: %v", ErrActionShape, err)
	}
	return balance.BalanceAction{
		ActionIndex:      actions.Next(),
		ExecutionOrder:   purchaseOrder,
		Cause:            balance.ProtocolCause(balance.OpGasPurchase, protocols.Next()),
		Engine:           balance.EngineEVM,
		Kind:             balance.KindFee,
		From:             &payer,
		Amount:           purchase,
		Asset:            balance.AssetNative,
		EffectsCommitted: true,
		RollbackScope:    &purchaseScope,
		Effects: []balance.BalanceEffect{{
			EffectIndex: 0,
			Address:     fee.Payer,
			Before:      fee.PayerBalanceBefore,
			After:       afterPurchase,
		}},
	}, nil
}

// BuildGasReturnAction normalizes the monetary return of unused gas. It
// runs after execution at its own position; EVM gas refund counter units
// are a different quantity and never appear here. It returns no action
// when the leftover is zero: a movement of nothing is not a native
// operation.
func BuildGasReturnAction(order *Order, actions *ActionIndex, protocols *ProtocolIndex, fee FeeObservation) (*balance.BalanceAction, error) {
	ret, err := fee.returnAmount()
	if err != nil {
		return nil, err
	}
	if ret.IsZero() {
		return nil, nil
	}
	atReturn, ok := fee.PayerBalanceAtReturn.Get()
	if !ok {
		return nil, fmt.Errorf("%w: nonzero gas return without the observed payer point", ErrActionShape)
	}
	returnOrder, err := order.Allocate()
	if err != nil {
		return nil, err
	}
	returnScope := uint64(0)
	payer := fee.Payer
	afterReturn, err := atReturn.AddChecked(ret)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrActionShape, err)
	}
	return &balance.BalanceAction{
		ActionIndex:      actions.Next(),
		ExecutionOrder:   returnOrder,
		Cause:            balance.ProtocolCause(balance.OpGasReturn, protocols.Next()),
		Engine:           balance.EngineEVM,
		Kind:             balance.KindRefund,
		To:               &payer,
		Amount:           ret,
		Asset:            balance.AssetNative,
		EffectsCommitted: true,
		RollbackScope:    &returnScope,
		Effects: []balance.BalanceEffect{{
			EffectIndex: 0,
			Address:     fee.Payer,
			Before:      atReturn,
			After:       afterReturn,
		}},
	}, nil
}

// BuildFeeCreditAction normalizes the fee recipient credit: the tip credit
// to the native recipient, with no from field. An absent from is not a
// mint and not the zero address.
func BuildFeeCreditAction(order *Order, actions *ActionIndex, protocols *ProtocolIndex, fee FeeObservation) (balance.BalanceAction, error) {
	tip, err := fee.tipAmount()
	if err != nil {
		return balance.BalanceAction{}, err
	}
	creditOrder, err := order.Allocate()
	if err != nil {
		return balance.BalanceAction{}, err
	}
	creditScope := uint64(0)
	recipient := fee.Recipient
	afterTip, err := fee.RecipientBalanceBefore.AddChecked(tip)
	if err != nil {
		return balance.BalanceAction{}, fmt.Errorf("%w: %v", ErrActionShape, err)
	}
	return balance.BalanceAction{
		ActionIndex:      actions.Next(),
		ExecutionOrder:   creditOrder,
		Cause:            balance.ProtocolCause(balance.OpFeeRecipientCredit, protocols.Next()),
		Engine:           balance.EngineEVM,
		Kind:             balance.KindFee,
		To:               &recipient,
		Amount:           tip,
		Asset:            balance.AssetNative,
		EffectsCommitted: true,
		RollbackScope:    &creditScope,
		Effects: []balance.BalanceEffect{{
			EffectIndex: 0,
			Address:     fee.Recipient,
			Before:      fee.RecipientBalanceBefore,
			After:       afterTip,
		}},
	}, nil
}

// TransferObservation is one observed native value transfer invocation of
// a frame entry: the frame that caused it, the two sides and the observed
// effects. One action groups the debit and the credit of the same
// invocation, including the self-transfer case; two callbacks of one
// invocation never become two transfers.
type TransferObservation struct {
	// FrameID is the frame whose entry carried the transfer. cause=Frame
	// names the causing frame; the transfer itself is its own native
	// operation with its own execution_order after the frame entry.
	FrameID uint64
	From    Address20
	To      Address20
	Amount  UInt256
	// Scope is the rollback scope of the transfer.
	Scope uint64
	// Committed is the final applicability of the transfer.
	Committed bool
	// Effects are the observed native mutations in native order.
	Effects []balance.BalanceEffect
}

// BuildTransferAction normalizes one observed transfer invocation.
func BuildTransferAction(order *Order, actions *ActionIndex, obs TransferObservation) (balance.BalanceAction, error) {
	entry, err := order.Allocate()
	if err != nil {
		return balance.BalanceAction{}, err
	}
	from, to := obs.From, obs.To
	scope := obs.Scope
	return balance.BalanceAction{
		ActionIndex:      actions.Next(),
		ExecutionOrder:   entry,
		Cause:            balance.FrameCause(obs.FrameID),
		Engine:           balance.EngineEVM,
		Kind:             balance.KindTransfer,
		From:             &from,
		To:               &to,
		Amount:           obs.Amount,
		Asset:            balance.AssetNative,
		EffectsCommitted: obs.Committed,
		RollbackScope:    &scope,
		Effects:          obs.Effects,
	}, nil
}

// FeeSummaryOf aggregates the fee actions into the receipt fee summary:
// payer, purchase debit, return credit, net charge and the recipient
// credits in native order. The summary is an aggregate view, never a
// second monetary event: adding summary amounts to action effects counts
// twice (D62).
func FeeSummaryOf(actions []balance.BalanceAction) (*balance.FeeSummary, error) {
	s := &balance.FeeSummary{}
	var purchase, ret UInt256
	var payer *Address20
	for i := range actions {
		a := &actions[i]
		if a.Cause.Kind != balance.CauseTransactionProtocol {
			continue
		}
		switch a.Cause.Operation {
		case balance.OpGasPurchase:
			sum, err := purchase.AddChecked(a.Amount)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrActionShape, err)
			}
			purchase = sum
			if a.From != nil {
				p := *a.From
				payer = &p
			}
		case balance.OpGasReturn:
			sum, err := ret.AddChecked(a.Amount)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrActionShape, err)
			}
			ret = sum
		case balance.OpFeeRecipientCredit:
			if a.To == nil {
				return nil, fmt.Errorf("%w: credit action without a recipient", ErrActionShape)
			}
			s.RecipientCredits = append(s.RecipientCredits, balance.FeeCredit{Address: *a.To, Amount: a.Amount})
		}
	}
	net, err := purchase.SubChecked(ret)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrActionShape, err)
	}
	s.Payer = payer
	s.GasPurchaseDebit = purchase
	s.GasReturnCredit = ret
	s.NetCharge = net
	return s, nil
}
