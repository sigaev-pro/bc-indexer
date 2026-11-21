package balance

// FeeCredit is one fee recipient credit of the receipt fee summary, in native
// order.
type FeeCredit struct {
	// Address is the native recipient of the credit.
	Address Address20
	// Amount is the exact credited amount.
	Amount UInt256
}

// FeeSummary is the receipt fee summary (CURRENT_SEMANTICS D62):
//
//	Receipt.fee_summary={payer?, gas_purchase_debit, gas_return_credit,
//	  net_charge, recipient_credits[]}
//
// The summary is an aggregate view over the fee actions of the transaction.
// It is never a second monetary movement: there is no balance action for the
// summary itself, and adding summary amounts to action effects double-counts.
// The validator enforces this by requiring every summary field to equal the
// exact aggregate of the corresponding committed fee actions.
//
// For an applicable normal transaction net_charge = gas_purchase_debit -
// gas_return_credit and covers the actual native gas and blob charges from
// those operations. An implicit burn determined by fee rules is verified
// separately by the profile; the summary never invents an extra payer debit
// on top of the charged gas. A zero-fee system transaction stores applicable
// explicit zeros; an inapplicable payer is absent.
type FeeSummary struct {
	// Payer is the fee payer, when applicable.
	Payer *Address20
	// GasPurchaseDebit aggregates the gas purchase actions.
	GasPurchaseDebit UInt256
	// GasReturnCredit aggregates the gas return actions.
	GasReturnCredit UInt256
	// NetCharge is GasPurchaseDebit - GasReturnCredit.
	NetCharge UInt256
	// RecipientCredits lists the fee recipient credits in native order, one
	// entry per committed credit action.
	RecipientCredits []FeeCredit
}
