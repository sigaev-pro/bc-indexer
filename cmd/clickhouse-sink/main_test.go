package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// fakeMsg is a minimal jetstream.Msg for messageKey and handler tests.
type fakeMsg struct {
	subject string
	data    []byte
	meta    *jetstream.MsgMetadata
	metaErr error
	acked   int
	termed  int
}

func (f *fakeMsg) Metadata() (*jetstream.MsgMetadata, error) { return f.meta, f.metaErr }
func (f *fakeMsg) Data() []byte                              { return f.data }
func (f *fakeMsg) Headers() nats.Header                      { return nil }
func (f *fakeMsg) Subject() string                           { return f.subject }
func (f *fakeMsg) Reply() string                             { return "" }
func (f *fakeMsg) Ack() error                                { f.acked++; return nil }
func (f *fakeMsg) DoubleAck(context.Context) error           { f.acked++; return nil }
func (f *fakeMsg) Nak() error                                { return nil }
func (f *fakeMsg) NakWithDelay(time.Duration) error          { return nil }
func (f *fakeMsg) InProgress() error                         { return nil }
func (f *fakeMsg) Term() error                               { f.termed++; return nil }
func (f *fakeMsg) TermWithReason(string) error               { f.termed++; return nil }

// testSink builds a sink around a subject filter without any server.
func testSink(t *testing.T, filter string) *sink {
	t.Helper()
	p, err := newSubjectParser(filter)
	if err != nil {
		t.Fatalf("newSubjectParser(%q): %v", filter, err)
	}
	return &sink{cfg: config{NatsSubject: filter, BatchSize: 100}, subjects: p, pendingKeys: make(map[string]struct{})}
}

func TestNewSubjectParser(t *testing.T) {
	cases := []struct {
		filter string
		prefix []string
		ok     bool
	}{
		{"extractor.>", []string{"extractor"}, true},
		{"evm.v3.>", []string{"evm", "v3"}, true},
		{"a.b.c.d.>", []string{"a", "b", "c", "d"}, true},
		{"extractor.block", nil, false}, // no > tail
		{"extractor.*.>", nil, false},   // wildcard in the prefix
		{"extractor..>", nil, false},    // empty token
		{">", nil, false},               // no literal tokens
		{"", nil, false},
	}
	for _, c := range cases {
		p, err := newSubjectParser(c.filter)
		if c.ok && err != nil {
			t.Errorf("newSubjectParser(%q): unexpected error %v", c.filter, err)
			continue
		}
		if !c.ok {
			if err == nil {
				t.Errorf("newSubjectParser(%q): expected error, got prefix %v", c.filter, p.prefix)
			}
			continue
		}
		if len(p.prefix) != len(c.prefix) {
			t.Errorf("newSubjectParser(%q): prefix %v, want %v", c.filter, p.prefix, c.prefix)
			continue
		}
		for i := range c.prefix {
			if p.prefix[i] != c.prefix[i] {
				t.Errorf("newSubjectParser(%q): prefix %v, want %v", c.filter, p.prefix, c.prefix)
				break
			}
		}
	}
}

func TestSubjectParserSplitSubject(t *testing.T) {
	cases := []struct {
		filter, subject string
		network, etype  string
		ok              bool
	}{
		// Default envelope filter: extractor.<network>.<type>
		{"extractor.>", "extractor.eth-sepolia.block", "eth-sepolia", "block", true},
		{"extractor.>", "extractor.eth-sepolia.transaction", "eth-sepolia", "transaction", true},
		{"extractor.>", "extractor.bsc-testnet.block", "bsc-testnet", "block", true},
		{"extractor.>", "extractor.eth-sepolia.block.extra", "eth-sepolia", "block", true}, // extra tokens tolerated
		{"extractor.>", "extractor.eth-sepolia", "", "", false},                            // no type token
		{"extractor.>", "extractor..block", "", "", false},                                 // empty network
		{"extractor.>", "other.eth-sepolia.block", "", "", false},                          // wrong prefix
		{"extractor.>", "eth-sepolia.block", "", "", false},                                // no prefix
		// Production stack filter: evm.v3.<network>.<family>.<kind>
		{"evm.v3.>", "evm.v3.eth.tx.applied", "eth", "tx", true},
		{"evm.v3.>", "evm.v3.bsc.block.removed", "bsc", "block", true},
		{"evm.v3.>", "evm.v3.eth.tx", "eth", "tx", true},
		{"evm.v3.>", "evm.eth.tx.applied", "", "", false}, // prefix mismatch
		// Structurally valid but semantically garbage: the parser has no
		// opinion on token values, the family branch decides the mapping.
		{"evm.v3.>", "evm.v3.tx.applied", "tx", "applied", true},
	}
	for _, c := range cases {
		p, err := newSubjectParser(c.filter)
		if err != nil {
			t.Fatalf("newSubjectParser(%q): %v", c.filter, err)
		}
		network, etype, ok := p.splitSubject(c.subject)
		if ok != c.ok || (ok && (network != c.network || etype != c.etype)) {
			t.Errorf("split(%q over %q) = (%q, %q, %v), want (%q, %q, %v)",
				c.subject, c.filter, network, etype, ok, c.network, c.etype, c.ok)
		}
	}
}

func TestSubjectsIntersect(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"extractor.>", "extractor.>", true},
		{"extractor.>", "extractor.eth-sepolia.>", true},
		{"extractor.>", "extractor.eth.transaction", true}, // literal demo subject
		{"extractor.>", "evm.v3.>", false},                 // different literal prefix
		{"evm.v3.>", "evm.v3.eth.>", true},
		{"evm.v3.>", "evm.v3.eth.tx.applied", true},
		{"evm.v3.>", "extractor.eth-sepolia.>", false},
		{"extractor.>", "extractor", false}, // tail requires >= 2 tokens
		{"extractor", "extractor", true},    // exact literal match
		{"extractor", "extractor.>", false},
		{"extractor.*.block", "extractor.eth.block", true}, // * matches one token
		{"extractor.*.block", "extractor.eth-sepolia.block", true},
		{"extractor.*.block", "extractor.block", false}, // * needs a token
		{"extractor.eth.>", "extractor.bsc.>", false},
		{"other.>", "extractor.>", false},
		{"a.b.c", "a.b.>", true}, // the literal subject is inside the tail
		{"a.b.c", "a.b.c.d", false},
		{"a.>", "a.>", true},
	}
	for _, c := range cases {
		got := subjectsIntersect(strings.Split(c.a, "."), strings.Split(c.b, "."))
		if got != c.want {
			t.Errorf("subjectsIntersect(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestFlexNumber(t *testing.T) {
	decode := func(t *testing.T, in string) flexNumber {
		t.Helper()
		var f flexNumber
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		return f
	}
	if got := decode(t, `1732000000`); string(got) != "1732000000" {
		t.Errorf("number literal: got %q", got)
	}
	if got := decode(t, `"1000000000000000000"`); string(got) != "1000000000000000000" {
		t.Errorf("numeric string: got %q", got)
	}
	if got := decode(t, `null`); got != "" {
		t.Errorf("null: got %q, want empty", got)
	}
	var f flexNumber
	for _, bad := range []string{`"abc"`, `true`, `"1.5x"`} {
		if err := json.Unmarshal([]byte(bad), &f); err != nil {
			t.Logf("%s rejected at decode: %v (acceptable)", bad, err)
		}
	}
	if v, ok := decode(t, `42`).u64(); !ok || v != 42 {
		t.Errorf("u64(42): got (%d, %v)", v, ok)
	}
	if _, ok := flexNumber("").u64(); ok {
		t.Errorf("u64(empty): parsed, want absent")
	}
	if _, ok := flexNumber("abc").u64(); ok {
		t.Errorf("u64(abc): parsed, want absent")
	}
	if v, ok := flexNumber(" 7 ").u64(); !ok || v != 7 {
		t.Errorf("u64(whitespace): got (%d, %v)", v, ok)
	}
}

func TestFlexString(t *testing.T) {
	decode := func(t *testing.T, in string) flexString {
		t.Helper()
		var f flexString
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		return f
	}
	if got := decode(t, `"0xabc"`); got != "0xabc" {
		t.Errorf("string: got %q", got)
	}
	if got := decode(t, `21000`); got != "21000" {
		t.Errorf("number: got %q", got)
	}
	if got := decode(t, `null`); got != "" {
		t.Errorf("null: got %q, want empty", got)
	}
	var f flexString
	if err := json.Unmarshal([]byte(`{"x":1}`), &f); err == nil {
		t.Errorf("object: decoded into %q, want error", f)
	}
}

func TestTxStatus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{{"1", "success"}, {"0", "reverted"}, {"", ""}, {"weird", "weird"}}
	for _, c := range cases {
		if got := txStatus(flexNumber(c.in)); got != c.want {
			t.Errorf("txStatus(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLifecycle(t *testing.T) {
	cases := []struct {
		kind        string
		event, want string
	}{
		{"", "", ""},
		{"applied", "applied", "active"},
		{"removed", "removed", "removed"},
		{"status_changed", "status_changed", ""},
	}
	for _, c := range cases {
		event, status := lifecycle(flexString(c.kind))
		if event != c.event || status != c.want {
			t.Errorf("lifecycle(%q) = (%q, %q), want (%q, %q)", c.kind, event, status, c.event, c.want)
		}
	}
}

func TestChainID(t *testing.T) {
	cases := []struct {
		network string
		from    string
		want    uint64
	}{
		{"eth-mainnet", "", 1},
		{"eth-holesky", "", 17000},
		{"eth-sepolia", "", 11155111},
		{"bsc-mainnet", "", 56},
		{"bsc-testnet", "", 97},
		{"poly-mainnet", "", 137},
		{"poly-testnet", "", 80002},
		{"unknown-net", "", 0},
		{"eth-sepolia", "999", 999}, // explicit payload value wins
	}
	for _, c := range cases {
		if got := chainID(c.network, flexNumber(c.from)); got != c.want {
			t.Errorf("chainID(%q, %q) = %d, want %d", c.network, c.from, got, c.want)
		}
	}
}

func TestFirstHelpers(t *testing.T) {
	if got := firstU64(flexNumber(""), flexNumber("x"), flexNumber("7"), flexNumber("9")); got != 7 {
		t.Errorf("firstU64: got %d, want 7", got)
	}
	if got := firstU64(); got != 0 {
		t.Errorf("firstU64(): got %d, want 0", got)
	}
	if got := firstString(flexString(""), flexString("a"), flexString("b")); got != "a" {
		t.Errorf("firstString: got %q, want a", got)
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewEventID(t *testing.T) {
	seen := make(map[string]struct{}, 10000)
	for i := 0; i < 10000; i++ {
		id := newEventID()
		if !uuidRe.MatchString(id) {
			t.Fatalf("newEventID() = %q, want a v4 UUID shape", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("newEventID() repeated after %d calls: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

// liveBlockBody is a captured live block event of the adapter envelope
// (subject extractor.eth-sepolia.block).
const liveBlockBody = `{"number":12198499,"hash":"0xabc123def4567890abc123def4567890abc123def4567890abc123def4567890","parent_hash":"0xabc123def4567890abc123def4567890abc123def4567890abc123def4567999","timestamp":1732000000,"gas_used":100000,"tx_count":133,"schema":"extractor.rpc.v1","network":"eth-sepolia","family":"block","kind":"applied","decision_sequence":1,"entity_ordinal":0,"source_sequence":1,"descriptor_digest":"a1b2c3","record_digest":"d4e5f6"}`

func TestParseEventBlock(t *testing.T) {
	s := testSink(t, "extractor.>")
	now := time.Date(2026, 11, 24, 1, 2, 3, 456000000, time.UTC)
	row, err := s.parseEvent("extractor.eth-sepolia.block", []byte(liveBlockBody), now)
	if err != nil {
		t.Fatalf("parseEvent: %v", err)
	}
	wantTS := time.Unix(1732000000, 0).UTC()
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"network", row.Network, "eth-sepolia"},
		{"chain_id", row.ChainID, uint64(11155111)},
		{"event_type", row.EventType, "block"},
		{"family", row.Family, "block"},
		{"kind", row.Kind, "applied"},
		{"entity", row.Entity, "0xabc123def4567890abc123def4567890abc123def4567890abc123def4567890"},
		{"block_number", row.BlockNumber, uint64(12198499)},
		{"block_hash", row.BlockHash, "0xabc123def4567890abc123def4567890abc123def4567890abc123def4567890"},
		{"parent_hash", row.ParentHash, "0xabc123def4567890abc123def4567890abc123def4567890abc123def4567999"},
		{"block_gas_used", row.BlockGasUsed, uint64(100000)},
		{"block_tx_count", row.BlockTxCount, uint64(133)},
		{"tx_hash", row.TxHash, ""},
		{"lifecycle_event", row.LifecycleEvent, "applied"},
		{"lifecycle_status", row.LifecycleStatus, "active"},
		{"source_sequence", row.SourceSequence, uint64(1)},
		{"entity_ordinal", row.EntityOrdinal, uint64(0)},
		{"descriptor_digest", row.DescriptorDigest, "a1b2c3"},
		{"record_digest", row.RecordDigest, "d4e5f6"},
		{"captured_at", row.CapturedAt, now},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
	if row.BlockTimestamp == nil || !row.BlockTimestamp.Equal(wantTS) {
		t.Errorf("block_timestamp: got %v, want %v", row.BlockTimestamp, wantTS)
	}
	if !strings.Contains(row.Raw, `"number":12198499`) {
		t.Errorf("raw: body not preserved: %q", row.Raw)
	}
	if !uuidRe.MatchString(row.EventID) {
		t.Errorf("event_id: got %q, want a v4 UUID", row.EventID)
	}
}

func TestParseEventTransaction(t *testing.T) {
	s := testSink(t, "extractor.>")
	now := time.Date(2026, 11, 24, 1, 2, 3, 0, time.UTC)
	base := `{"block_number":100,"block_hash":"0xblk100","tx_index":0,"hash":"0xtx1","from":"0xfrom","to":%s,"value":"1000000000000000000","gas":21000,"status":%s,"schema":"extractor.rpc.v1","network":"eth-sepolia","family":"tx","kind":"applied","decision_sequence":3,"entity_ordinal":1,"source_sequence":19,"descriptor_digest":"dd","record_digest":"rd"}`

	// to present, status 1
	row, err := s.parseEvent("extractor.eth-sepolia.transaction", []byte(fmt.Sprintf(base, `"0xto"`, `1`)), now)
	if err != nil {
		t.Fatalf("parseEvent tx success: %v", err)
	}
	if row.TxTo != "0xto" || row.TxStatus != "success" || row.Entity != "0xtx1" || row.TxHash != "0xtx1" {
		t.Errorf("tx success mapping wrong: to=%q status=%q entity=%q", row.TxTo, row.TxStatus, row.Entity)
	}
	if row.BlockTimestamp != nil {
		t.Errorf("tx block_timestamp: got %v, want nil", row.BlockTimestamp)
	}
	if row.BlockNumber != 100 || row.BlockHash != "0xblk100" || row.TxGas != 21000 || row.TxValue != "1000000000000000000" {
		t.Errorf("tx context mapping wrong: %+v", row)
	}

	// status 0 -> reverted
	row, _ = s.parseEvent("extractor.eth-sepolia.transaction", []byte(fmt.Sprintf(base, `"0xto"`, `0`)), now)
	if row.TxStatus != "reverted" {
		t.Errorf("status 0: got %q, want reverted", row.TxStatus)
	}

	// to null (contract creation) and status null (receipt unknown)
	row, err = s.parseEvent("extractor.eth-sepolia.transaction", []byte(fmt.Sprintf(base, `null`, `null`)), now)
	if err != nil {
		t.Fatalf("parseEvent tx null: %v", err)
	}
	if row.TxTo != "" || row.TxStatus != "" {
		t.Errorf("null mapping: to=%q status=%q, want empty/empty", row.TxTo, row.TxStatus)
	}

	// status absent entirely
	noStatus := strings.Replace(fmt.Sprintf(base, `"0xto"`, `1`), `,"status":1`, ``, 1)
	row, err = s.parseEvent("extractor.eth-sepolia.transaction", []byte(noStatus), now)
	if err != nil {
		t.Fatalf("parseEvent tx no status: %v", err)
	}
	if row.TxStatus != "" {
		t.Errorf("absent status: got %q, want empty", row.TxStatus)
	}
}

func TestParseEventRejections(t *testing.T) {
	s := testSink(t, "extractor.>")
	now := time.Now().UTC()

	cases := []struct {
		name, subject, body string
	}{
		{"structural subject mismatch", "other.prefix.block", liveBlockBody},
		{"too few subject tokens", "extractor.eth-sepolia", liveBlockBody},
		{"undecodable body", "extractor.eth-sepolia.block", `{not json`},
		{"tx without hash", "extractor.eth-sepolia.transaction", `{"block_number":1,"from":"0xf","family":"tx"}`},
		{"tx without hash (mock aliases only)", "extractor.eth-sepolia.transaction", `{"height":1,"from_addr":"0xf"}`},
		{"huge block timestamp", "extractor.eth-sepolia.block", `{"number":1,"hash":"0xb","timestamp":99999999999999,"family":"block"}`},
		{"negative block timestamp", "extractor.eth-sepolia.block", `{"number":1,"hash":"0xb","timestamp":-5,"family":"block"}`},
		{"non-numeric block timestamp", "extractor.eth-sepolia.block", `{"number":1,"hash":"0xb","timestamp":"soon","family":"block"}`},
		{"pre-2000 block timestamp", "extractor.eth-sepolia.block", `{"number":1,"hash":"0xb","timestamp":100,"family":"block"}`},
		{"block without hash", "extractor.eth-sepolia.block", `{"number":1,"timestamp":1732000000,"family":"block"}`},
	}
	for _, c := range cases {
		if _, err := s.parseEvent(c.subject, []byte(c.body), now); err == nil {
			t.Errorf("%s: expected rejection, got a row", c.name)
		} else {
			t.Logf("%s rejected: %v", c.name, err)
		}
	}
}

func TestParseEventEvmSubjectAndAliases(t *testing.T) {
	// The production stack shape: evm.v3.<net>.<family>.<kind> with the
	// mock payload spellings (height/tx_hash/from_addr/to_addr).
	s := testSink(t, "evm.v3.>")
	now := time.Now().UTC()

	row, err := s.parseEvent("evm.v3.eth.block.applied",
		[]byte(`{"schema":"mock.v1","network":"eth","kind":"block.applied","height":42,"block_hash":"0xb42"}`), now)
	if err != nil {
		t.Fatalf("parseEvent evm block: %v", err)
	}
	if row.Network != "eth" || row.EventType != "block" || row.BlockNumber != 42 ||
		row.BlockHash != "0xb42" || row.Entity != "0xb42" || row.BlockTimestamp != nil {
		t.Errorf("evm block mapping wrong: %+v", row)
	}

	row, err = s.parseEvent("evm.v3.eth.tx.applied",
		[]byte(`{"schema":"mock.v1","kind":"tx.applied","height":42,"tx_index":3,"tx_hash":"0xt","from_addr":"0xf","to_addr":"0x2","value":"5","status":"applied"}`), now)
	if err != nil {
		t.Fatalf("parseEvent evm tx: %v", err)
	}
	if row.EventType != "transaction" || row.TxHash != "0xt" || row.TxFrom != "0xf" ||
		row.TxTo != "0x2" || row.BlockNumber != 42 || row.Entity != "0xt" {
		t.Errorf("evm tx mapping wrong (tx must normalize to transaction): %+v", row)
	}
}

func TestParseEventOtherFamilyEntity(t *testing.T) {
	// A balance-style event has no block context: entity falls back to
	// the record digest so distinct events never share a key.
	s := testSink(t, "extractor.>")
	now := time.Now().UTC()
	row, err := s.parseEvent("extractor.eth-sepolia.balance",
		[]byte(`{"address":"0xabc","value":"7","family":"balance","kind":"applied","record_digest":"rd1"}`), now)
	if err != nil {
		t.Fatalf("parseEvent balance: %v", err)
	}
	if row.Entity != "rd1" || row.EventType != "balance" || row.BlockNumber != 0 {
		t.Errorf("balance mapping wrong: %+v", row)
	}
}

func TestMessageKeyAndDuplicateDrop(t *testing.T) {
	s := testSink(t, "extractor.>")
	m := &fakeMsg{
		subject: "extractor.eth-sepolia.block",
		data:    []byte(liveBlockBody),
		meta:    &jetstream.MsgMetadata{Stream: "EXTRACTOR_ETH_SEPOLIA"},
	}
	m.meta.Sequence.Stream = 17
	if key := messageKey(m); key != "EXTRACTOR_ETH_SEPOLIA/17" {
		t.Fatalf("messageKey: got %q", key)
	}

	// A message without metadata keys as "" and bypasses dedup.
	if key := messageKey(&fakeMsg{metaErr: fmt.Errorf("no metadata")}); key != "" {
		t.Fatalf("messageKey without metadata: got %q, want empty", key)
	}

	// Deliver the same message twice: the second copy is dropped while
	// the first sits in the pending batch.
	s.handleMessage(m)
	s.handleMessage(m)
	if len(s.pending) != 1 {
		t.Fatalf("pending: got %d rows, want 1 (duplicate dropped)", len(s.pending))
	}
	if s.duplicates != 1 {
		t.Fatalf("duplicates: got %d, want 1", s.duplicates)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc..." {
		t.Errorf("truncate: got %q", got)
	}
	if got := truncate("ab", 3); got != "ab" {
		t.Errorf("truncate short: got %q", got)
	}
}
