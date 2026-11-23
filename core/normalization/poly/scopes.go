package poly

import (
	"fmt"
)

// LogScope is the closed D75 source log scope of the BorReceiptDetails
// variant: the native instrumentation phase that produced one source
// receipt log.
type LogScope uint8

const (
	// LogScopeInvalid is the invalid zero value; zero is never a published
	// scope.
	LogScopeInvalid LogScope = iota
	// LogScopeSystemPre attributes a log to native system work before the
	// state-sync commit of the block (for example span updates).
	LogScopeSystemPre
	// LogScopeTXStateSync attributes a log to the state-sync receiver
	// execution itself.
	LogScopeTXStateSync
	// LogScopeSystemPost attributes a log to native system work after the
	// state-sync commit (for example block-alloc code upgrades).
	LogScopeSystemPost
)

// String returns the contract name of the scope.
func (s LogScope) String() string {
	switch s {
	case LogScopeSystemPre:
		return "SYSTEM_PRE"
	case LogScopeTXStateSync:
		return "TX_STATE_SYNC"
	case LogScopeSystemPost:
		return "SYSTEM_POST"
	default:
		return "LOG_SCOPE_INVALID"
	}
}

// SourceLogScope is one D75 provenance row: the transaction_log_index of
// one source receipt log and its native scope. The provenance never
// filters, reorders or rewrites the logs, and a SYSTEM_PRE or SYSTEM_POST
// log never becomes an action of the StateSync transaction.
type SourceLogScope struct {
	TransactionLogIndex uint64
	Scope               LogScope
}

// validateSourceLogScopes checks the D75 rules:
//
//   - an ordinary body transaction has a present, empty list: its receipt
//     logs belong to the transaction itself;
//   - a StateSync receipt describes every source log exactly once, by
//     transaction_log_index, in source order: duplicates, gaps, indices
//     outside the receipt and unknown scopes are rejected;
//   - a StateSync receipt without logs carries the present empty list.
func validateSourceLogScopes(logs []LogCapture, scopes []LogScopeCapture, stateSync bool) ([]SourceLogScope, error) {
	if !stateSync {
		if len(scopes) != 0 {
			return nil, fmt.Errorf("ordinary receipt carries %d source log scopes: %w", len(scopes), ErrLogScope)
		}
		return []SourceLogScope{}, nil
	}
	if len(scopes) != len(logs) {
		return nil, fmt.Errorf("state-sync receipt has %d logs but %d source log scopes: %w",
			len(logs), len(scopes), ErrLogScope)
	}
	out := make([]SourceLogScope, 0, len(scopes))
	for i, s := range scopes {
		if s.TransactionLogIndex != uint64(i) {
			return nil, fmt.Errorf("source log scope %d has index %d, want %d in source order: %w",
				i, s.TransactionLogIndex, i, ErrLogScope)
		}
		if s.Scope == LogScopeInvalid {
			return nil, fmt.Errorf("source log scope %d is unspecified: %w", i, ErrLogScope)
		}
		if s.TransactionLogIndex >= uint64(len(logs)) {
			return nil, fmt.Errorf("source log scope index %d is outside the receipt: %w",
				s.TransactionLogIndex, ErrLogScope)
		}
		out = append(out, SourceLogScope{TransactionLogIndex: s.TransactionLogIndex, Scope: s.Scope})
	}
	return out, nil
}
