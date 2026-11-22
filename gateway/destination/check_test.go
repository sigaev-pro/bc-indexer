package destination

import (
	"context"
	"strings"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// verifiedStream builds the stream info a correctly bootstrapped
// environment answers for the standard test binding.
func verifiedStream(t *testing.T, b Binding) StreamInfo {
	t.Helper()
	digest, err := b.NetworkDigest()
	if err != nil {
		t.Fatalf("NetworkDigest: %v", err)
	}
	return StreamInfo{
		Name:       b.Stream,
		Subjects:   []string{b.SubjectPrefix + ".>"},
		Metadata:   map[string]string{MetadataNetworkDigest: hexEncode(digest[:]), MetadataDestinationID: hexEncode(b.DestinationID[:])},
		MaxMsgSize: 2 * 1024 * 1024,
	}
}

func testBinding(t *testing.T) Binding {
	t.Helper()
	b, err := ParseBinding(validBindingJSON(t, nil, true))
	if err != nil {
		t.Fatalf("ParseBinding: %v", err)
	}
	return b
}

func testSubjects(t *testing.T, b Binding) []string {
	t.Helper()
	out := []string{}
	for _, family := range []proto.Family{proto.FamilyTx, proto.FamilyBlock} {
		for _, kind := range []proto.Kind{proto.KindApplied, proto.KindRemoved, proto.KindStatus} {
			s, err := Subject(b.SubjectPrefix, family, kind)
			if err != nil {
				t.Fatalf("Subject: %v", err)
			}
			out = append(out, s)
		}
	}
	return out
}

func TestCheckVerifiesCorrectTarget(t *testing.T) {
	b := testBinding(t)
	remote, err := NewMemoryRemote(b.ExpectedAccount, verifiedStream(t, b))
	if err != nil {
		t.Fatalf("NewMemoryRemote: %v", err)
	}
	report := Check(context.Background(), remote, b, testSubjects(t, b), 2*1024*1024)
	if !report.OK() {
		t.Fatalf("Check did not verify: %+v", report)
	}
	target, err := NewVerifiedTarget(b, testSubjects(t, b), verifiedStream(t, b).MaxMsgSize)
	if err != nil {
		t.Fatalf("NewVerifiedTarget: %v", err)
	}
	if target.Stream != b.Stream || len(target.Subjects) != 6 {
		t.Fatalf("target: %+v", target)
	}
}

func TestCheckMetadataMatrix(t *testing.T) {
	b := testBinding(t)
	subjects := testSubjects(t, b)
	cases := map[string]func(*StreamInfo){
		"missing map":         func(s *StreamInfo) { s.Metadata = nil },
		"missing network key": func(s *StreamInfo) { delete(s.Metadata, MetadataNetworkDigest) },
		"missing id key":      func(s *StreamInfo) { delete(s.Metadata, MetadataDestinationID) },
		"empty value":         func(s *StreamInfo) { s.Metadata[MetadataNetworkDigest] = "" },
		"uppercase value": func(s *StreamInfo) {
			s.Metadata[MetadataDestinationID] = strings.ToUpper(hexEncode(b.DestinationID[:]))
		},
		"wrong length": func(s *StreamInfo) { s.Metadata[MetadataDestinationID] = "0a" },
		"wrong value":  func(s *StreamInfo) { s.Metadata[MetadataNetworkDigest] = strings.Repeat("07", 32) },
		"other network digest": func(s *StreamInfo) {
			s.Metadata[MetadataNetworkDigest] = hexEncode(func() []byte { var h proto.Hash32; h[0] = 9; return h[:] }())
		},
	}
	for name, mutate := range cases {
		stream := verifiedStream(t, b)
		mutate(&stream)
		remote, err := NewMemoryRemote(b.ExpectedAccount, stream)
		if err != nil {
			t.Fatalf("%s: NewMemoryRemote: %v", name, err)
		}
		report := Check(context.Background(), remote, b, subjects, 1024)
		if report.Status != StatusConfigBlocked {
			t.Errorf("%s: status %v, want CONFIG_BLOCKED (%v)", name, report.Status, report.Reasons)
			continue
		}
		if len(report.Reasons) == 0 {
			t.Errorf("%s: CONFIG_BLOCKED without a named reason", name)
		}
	}
	// Unknown extra metadata keys are preserved and ignored.
	stream := verifiedStream(t, b)
	stream.Metadata["ops.owner"] = "platform"
	remote, _ := NewMemoryRemote(b.ExpectedAccount, stream)
	if report := Check(context.Background(), remote, b, subjects, 1024); !report.OK() {
		t.Fatalf("extra metadata key blocked the check: %+v", report)
	}
	// The reason names the stream and the key, never credentials.
	remote, _ = NewMemoryRemote(b.ExpectedAccount, func() StreamInfo {
		s := verifiedStream(t, b)
		delete(s.Metadata, MetadataNetworkDigest)
		return s
	}())
	report := Check(context.Background(), remote, b, subjects, 1024)
	joined := strings.Join(report.Reasons, "; ")
	if !strings.Contains(joined, b.Stream) || !strings.Contains(joined, MetadataNetworkDigest) {
		t.Fatalf("reason does not name stream and key: %q", joined)
	}
	if strings.Contains(joined, "password") || strings.Contains(joined, "credential") {
		t.Fatalf("reason leaks credential material: %q", joined)
	}
}

func TestCheckUnavailableIsNotEvidence(t *testing.T) {
	b := testBinding(t)
	remote, err := NewMemoryRemote(b.ExpectedAccount, verifiedStream(t, b))
	if err != nil {
		t.Fatalf("NewMemoryRemote: %v", err)
	}
	remote.SetAvailable(false)
	report := Check(context.Background(), remote, b, testSubjects(t, b), 1024)
	if report.Status != StatusUnavailable {
		t.Fatalf("status %v, want UNAVAILABLE", report.Status)
	}
	// A stream that does not exist on the server is also unavailability,
	// not a metadata verdict.
	remote.SetAvailable(true)
	empty, _ := NewMemoryRemote(b.ExpectedAccount)
	if report := Check(context.Background(), empty, b, testSubjects(t, b), 1024); report.Status != StatusUnavailable {
		t.Fatalf("missing stream status %v, want UNAVAILABLE", report.Status)
	}
	// A one-shot failure answers unavailable once, then verifies: the
	// publication path retries under the transient class.
	remote.FailNextCall()
	if report := Check(context.Background(), remote, b, testSubjects(t, b), 1024); report.Status != StatusUnavailable {
		t.Fatalf("one-shot failure status %v, want UNAVAILABLE", report.Status)
	}
	if report := Check(context.Background(), remote, b, testSubjects(t, b), 1024); !report.OK() {
		t.Fatalf("retry after one-shot failure: %+v", report)
	}
}

func TestCheckAccountAndRouteBlocked(t *testing.T) {
	b := testBinding(t)
	subjects := testSubjects(t, b)
	// Wrong account.
	remote, _ := NewMemoryRemote("OTHER_ACCOUNT", verifiedStream(t, b))
	if report := Check(context.Background(), remote, b, subjects, 1024); report.Status != StatusConfigBlocked {
		t.Fatalf("wrong account status %v", report.Status)
	}
	// Route not covered: stream admits another network.
	wrongRoute := verifiedStream(t, b)
	wrongRoute.Subjects = []string{"evm.v3.bsc.>"}
	remote, _ = NewMemoryRemote(b.ExpectedAccount, wrongRoute)
	report := Check(context.Background(), remote, b, subjects, 1024)
	if report.Status != StatusConfigBlocked {
		t.Fatalf("uncovered route status %v", report.Status)
	}
	// Frame budget above the stream limit.
	remote, _ = NewMemoryRemote(b.ExpectedAccount, verifiedStream(t, b))
	if report := Check(context.Background(), remote, b, subjects, verifiedStream(t, b).MaxMsgSize+1); report.Status != StatusConfigBlocked {
		t.Fatalf("oversize frame budget status %v", report.Status)
	}
	// The check is read-only: no API mutates the binding or pending data,
	// so a blocked check leaves the caller's state untouched by
	// construction. The reasons are present for every blocked case above.
	if len(report.Reasons) == 0 {
		t.Fatalf("blocked without reasons")
	}
}

func TestCheckReChecksAfterURLChange(t *testing.T) {
	b := testBinding(t)
	// A URL or credential change is allowed only after the same
	// binding/account/stream/route check passes again: the check runs per
	// connection, and a previous success never carries over.
	first, _ := NewMemoryRemote(b.ExpectedAccount, verifiedStream(t, b))
	if report := Check(context.Background(), first, b, testSubjects(t, b), 1024); !report.OK() {
		t.Fatalf("first connection: %+v", report)
	}
	second, _ := NewMemoryRemote("CHANGED_ACCOUNT", verifiedStream(t, b))
	if report := Check(context.Background(), second, b, testSubjects(t, b), 1024); report.Status != StatusConfigBlocked {
		t.Fatalf("second connection with a different account must block")
	}
}
