// Package eth implements the full Ethereum transaction semantics of E04
// (#41-#49, ETH family) on top of the E02 protocol packages: the call tree
// with one shared execution_order counter and D81 create-address semantics,
// rollback scopes, native receipts with the D75 encoding registry and the
// D76 bounded byte-equivalent consensus writer, source authorization tuples
// of EIP-7702, the native BlockApplied block-record projection, the
// transaction-boundary state diff, SELFDESTRUCT with action, monetary
// movement and deletion kept separate, causal balance actions with fees and
// refunds, the bounded import path over the capture seam, and the optional
// outside-tx scope declaration (D17, default OFF).
//
// The package owns normalization only. Native execution, capture adapters
// and delivery belong to other modules; this package never runs a native
// client and performs no I/O outside the interfaces it is given. Every
// retained fixture value cites its E01 evidence file.
//
// Normative anchors:
//
//   - .project/docs/CURRENT_SEMANTICS.md, sections D74 (order and rollback
//     scopes), D62 (balance actions and fees), D81 (attempted create
//     address) and the block/calls/account-changes section
//   - .project/docs/MESSAGE_CONTRACT.md, sections 3, 3.1 (D75/D76) and 3.2
//   - .project/docs/BALANCE_MODEL.md, historical source evidence
//   - protocol/balance, protocol/block, protocol/profiles and
//     protocol/semantic of this repository
//
// Evidence anchors:
//
//   - test/native_corpus/eth and test/native_corpus/expected/eth.json (the
//     smoke corpus: transfer, dynamic fee, nested revert, empty block)
//   - test/native_faults/eth (crash-recovery behavior of the import path)
//   - test/type_paths/eth (transaction types 0-4 and EIP-7702 delegation)
package eth

import (
	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// Address20 is the native account address of the balance model.
type Address20 = balance.Address20

// UInt256 is the unsigned 256-bit integer of the balance model, with exact
// integer arithmetic.
type UInt256 = balance.UInt256

// Hash32 is the 32-byte native hash of the block model.
type Hash32 = block.Hash32

// Optional is the presence-explicit optional of the block model. A present
// zero stays a present value; absence never becomes a zero.
type Optional[T any] = block.Optional[T]

// Some returns v as a present optional.
func Some[T any](v T) Optional[T] { return block.Some(v) }

// None returns the absent optional of T.
func None[T any]() Optional[T] { return block.None[T]() }
