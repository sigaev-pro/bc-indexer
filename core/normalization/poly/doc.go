// Package poly is the Polygon (Bor) family transaction normalization of
// E04 (#41..#49): it turns captured native observations of one Bor block and
// its body transactions into the typed records of the message contract and
// validates them against the closed protocol models.
//
// The package is a pure in-memory library. It performs no I/O, imports no
// client type, and every exported function is deterministic in its inputs:
// the same capture always yields the same records, logical IDs, orders and
// scopes (the capture-recovery stability that E04-04.import_pressure
// requires). Native execution, Heimdall fetches and capture hooks belong to
// the Bor adapter; this package never invents native results.
//
// # Areas
//
// Each ETH area has a Polygon counterpart here, plus the Polygon-specific
// StateSync semantics:
//
//   - calls: the CallFrame tree with the shared D74 execution_order, the
//     synthetic NATIVE_PROTOCOL root frame of a body StateSync transaction,
//     D81 attempted-versus-created addresses and the committed flag after
//     ancestor rollback.
//   - receipts: the typed Receipt with the D75 source encoding (kinds 1 and
//     2, native_type 127 for StateSync), the BorReceiptDetails variant and
//     the fee summary as an aggregate, never a second movement.
//   - authorization: the closed empty profile. EIP-7702 is not active in the
//     current Polygon profile, so any source authorization tuple is an
//     explicit normalization error, never a silently dropped field.
//   - block record: block.BlockMetadata with BorBlockDetails (sprint and
//     side-chain context, StateSync count and mode) and the finality marker
//     with Heimdall checkpoint references.
//   - state diff: transaction-boundary AccountChange values, storage reset
//     and existence flags, reconciled with the committed balance effects.
//   - selfdestruct: the SelfDestructAction execution action with fork
//     eligibility, its single monetary projection and the separate
//     finalization burn.
//   - balance actions: gas purchase, gas return, the two Polygon fee
//     credits (tip to the signer and base-fee burn to the zero address),
//     frame transfers and BOR_STATE_SYNC_EXECUTION protocol changes.
//   - import pressure: record equality and replay-prefix verification that
//     keep IDs, orders and scopes stable across capture recovery.
//   - optional scope: the default-OFF declaration of the optional legacy
//     Bor synthetic receipt; enabled records exist only with declared typed
//     fixtures.
//
// # StateSync (post-Madhugiri body type 0x7f)
//
// After Madhugiri the StateSync transaction is a real native body
// transaction: wire 0x7f || RLP(items), items {ID, Contract, Data, TxHash},
// executed by Bor Finalize with the system caller 0xff..fe. It occupies the
// final body position, pays no gas, and its receipt carries
// BorReceiptDetails.source_log_scopes: every source receipt log exactly
// once, by transaction_log_index in source order, with the native
// instrumentation scope SYSTEM_PRE, TX_STATE_SYNC or SYSTEM_POST.
//
// # Legacy synthetic receipt (pre-Madhugiri, optional)
//
// Before Madhugiri the same system work produced a separate native storage
// receipt, never a body transaction. When the optional scope is enabled it
// is published as a separate auxiliary record with raw storage RLP
// (D75 kind 3, no native_type) and the derived identity
// keccak256("matic-bor-receipt-" || u64be(number) || block_hash). Its log
// provenance semantics differ from the body source_log_scopes and are kept
// as an explicit auxiliary attribution.
//
// # Normative anchors
//
//   - .project/docs/MESSAGE_CONTRACT.md, sections 3, 3.1, 3.2 and 4
//   - .project/docs/CURRENT_SEMANTICS.md, sections D62, D63, D74 and the
//     Polygon evidence section
//   - .project/docs/research/POLYGON_LEGACY_CAPTURE.md (E00-15)
//   - protocol/balance, protocol/block and protocol/profiles (E02 models)
//   - E01 evidence: test/type_paths/poly (crossing and Madhugiri fixtures),
//     test/native_corpus/poly and .project/evidence/E01-05.type_paths.poly*.md
package poly
