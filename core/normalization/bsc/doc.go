// Package bsc is the root of the BSC (Parlia) normalization module of E04:
// it owns the Parlia transaction classification, the Parlia system
// transaction semantics, the capture-observation input model and the
// normalizer that folds one observed included transaction into the full
// TransactionRecord view — call tree, rollback scopes, balance actions,
// receipt, state diff — validated against the shared phase-1 models.
//
// The nine areas of the module live in their sub-packages (calls, receipt,
// authorization, block, state, selfdestruct, balance, importpath,
// optionalscope); this package binds them into one record and adds what is
// BSC-specific: the Ordinary | ParliaSystem classification over the actual
// wire type, the native system sender, zero gas price and known system
// contract calls with independently expected nonce, balance and storage
// transitions, and the explicitly empty BscReceiptDetails variant of D75.
//
// The module is a pure normalization layer: it consumes typed observed
// facts (the seam the E01 capture spool maps onto), performs no I/O and
// never re-executes anything. Normative anchors are named per file; the
// umbrella contract is .project/docs/MESSAGE_CONTRACT.md section 3 with
// .project/docs/CURRENT_SEMANTICS.md.
package bsc
