package eth

import (
	"errors"
	"fmt"
)

// ErrOrderExhausted reports that the tx-local execution_order counter would
// overflow. Overflow is an explicit normalization error before READY; the
// counter never wraps and never reuses a number (CURRENT_SEMANTICS D74).
var ErrOrderExhausted = errors.New("eth: execution_order counter exhausted")

// Order is the single tx-local execution_order counter of one transaction
// (CURRENT_SEMANTICS D74). The number is allocated at the synchronous
// native boundary of an operation represented by a CallFrame, an
// ExecutionAction or a BalanceAction. A separate representation of the same
// operation reuses the number already allocated for it; every distinct
// operation gets the next number. Values start at 1.
//
// The counter is not an opcode PC, a timestamp, a source sequence or a chain
// sequence, and unused VM opcodes, receipt logs, final AccountChange rows,
// snapshot bookkeeping, byte chunks, worker scheduling and transport retries
// never advance it.
type Order struct {
	next uint64
}

// NewOrder returns a counter whose first allocation is 1.
func NewOrder() *Order { return &Order{next: 0} }

// Allocate reserves and returns the next number.
func (o *Order) Allocate() (uint64, error) {
	if o.next == ^uint64(0) {
		return 0, fmt.Errorf("%w: reached the u64 limit", ErrOrderExhausted)
	}
	o.next++
	return o.next, nil
}

// Last returns the highest allocated number, or 0 when nothing is allocated.
func (o *Order) Last() uint64 { return o.next }

// Reuse returns the highest allocated number for a second representation of
// the operation that just allocated it. It allocates nothing.
func (o *Order) Reuse() uint64 { return o.next }
