package poly

import "testing"

// TestSourceLogScopesOrdinaryEmpty: an ordinary body transaction carries
// the present empty provenance list, and any scope row is rejected.
func TestSourceLogScopesOrdinaryEmpty(t *testing.T) {
	logs := []LogCapture{}
	got, err := validateSourceLogScopes(logs, nil, false)
	if err != nil {
		t.Fatalf("ordinary receipt: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("ordinary provenance must be the present empty list")
	}

	_, err = validateSourceLogScopes(logs, []LogScopeCapture{{0, LogScopeTXStateSync}}, false)
	if err == nil {
		t.Fatalf("scope row on an ordinary receipt accepted")
	}
}

// TestSourceLogScopesStateSyncExactOnce: the StateSync provenance describes
// every source log exactly once, in order; duplicates, gaps, out-of-range
// indices and unknown scopes are rejected.
func TestSourceLogScopesStateSyncExactOnce(t *testing.T) {
	logs := []LogCapture{fixtureLog(fixtureTarget, 1), fixtureLog(fixtureRevert, 2)}

	full := []LogScopeCapture{
		{0, LogScopeSystemPre},
		{1, LogScopeTXStateSync},
	}
	got, err := validateSourceLogScopes(logs, full, true)
	if err != nil {
		t.Fatalf("full provenance: %v", err)
	}
	if len(got) != 2 || got[0].Scope != LogScopeSystemPre || got[1].Scope != LogScopeTXStateSync {
		t.Fatalf("provenance rows differ")
	}

	// A gap in the indices.
	if _, err := validateSourceLogScopes(logs, []LogScopeCapture{{0, LogScopeTXStateSync}, {2, LogScopeSystemPost}}, true); err == nil {
		t.Fatalf("index gap accepted")
	}
	// A repeated index.
	if _, err := validateSourceLogScopes(logs, []LogScopeCapture{{0, LogScopeTXStateSync}, {0, LogScopeSystemPost}}, true); err == nil {
		t.Fatalf("repeated index accepted")
	}
	// A missing row.
	if _, err := validateSourceLogScopes(logs, []LogScopeCapture{{0, LogScopeTXStateSync}}, true); err == nil {
		t.Fatalf("missing row accepted")
	}
	// An unspecified scope value.
	if _, err := validateSourceLogScopes(logs, []LogScopeCapture{{0, LogScopeInvalid}, {1, LogScopeTXStateSync}}, true); err == nil {
		t.Fatalf("invalid scope accepted")
	}
	// No logs: the present empty list is the correct provenance.
	if got, err := validateSourceLogScopes(nil, nil, true); err != nil || len(got) != 0 {
		t.Fatalf("log-free provenance: %v", err)
	}
}

// TestSystemScopeLogsAreNotStateSyncActions: the provenance never turns a
// SYSTEM_PRE or SYSTEM_POST log into an action of the StateSync
// transaction; the receipt keeps its bytes and order.
func TestSystemScopeLogsAreNotStateSyncActions(t *testing.T) {
	tx := crossingStateSyncTx(t)
	systemLog := fixtureLog(fixtureReceiver, 7)
	tx.Receipt.Logs = append(tx.Receipt.Logs, systemLog)
	tx.Receipt.LogScopes = append(tx.Receipt.LogScopes, LogScopeCapture{1, LogScopeSystemPost})
	tx.Receipt.LogsBloom = fixtureBloom(tx.Receipt.Logs)
	typeByte := StateSyncTxType
	tx.Receipt.Consensus = fixtureReceiptWire(t, &typeByte, 21000, tx.Receipt.LogsBloom, tx.Receipt.Logs)
	record, err := NormalizeTransaction(&tx)
	if err != nil {
		t.Fatalf("system log provenance: %v", err)
	}
	if len(record.Receipt.Logs) != 2 {
		t.Fatalf("logs were filtered or reordered")
	}
	if len(record.Receipt.NetworkDetails.SourceLogScopes) != 2 ||
		record.Receipt.NetworkDetails.SourceLogScopes[1].Scope != LogScopeSystemPost {
		t.Fatalf("system post provenance missing")
	}
	if len(record.Actions) != 0 {
		t.Fatalf("a system log became an action of the state sync transaction")
	}
}
