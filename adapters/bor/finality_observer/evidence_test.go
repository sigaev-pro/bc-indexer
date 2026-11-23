package finalityobserver

import (
	"encoding/json"
	"testing"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

// validSourceRef builds one valid source observation ref.
func validSourceRef() SourceObservationRef {
	return SourceObservationRef{
		SourceStoreIdentity: mustID("heimdall-store"),
		SourcesBinding:      mustHash("sources-s1"),
		Heimdall:            testHeimdallIdentity,
		ObservationID:       "poly-obs-00000001",
		RequestSequence:     4,
		Request:             Request{Operation: OpCheckpointLatest},
		ResponseDigest:      mustHash("response"),
	}
}

// validWhitelist builds one accepted milestone whitelist with its saved
// full source response.
func validWhitelist() Whitelist {
	w := Whitelist{
		Kind:           WhitelistMilestone,
		Anchor:         block.AnchorRef{Height: 30, Hash: mustHash("whitelist-anchor")},
		Decision:       testDecision(2),
		SourceEvidence: block.Some(validSourceRef()),
	}
	w.AncestryBindingDigest = WhitelistAncestryDigest(w.Kind, w.Anchor, w.Decision)
	return w
}

// validL1Checkpoint builds one verified L1 checkpoint with consistent
// digests.
func validL1Checkpoint() L1Checkpoint {
	ev := L1Checkpoint{
		L1Network:          testL1Network,
		SupportingNodeID:   mustID("l1-node"),
		SourcesBinding:     mustHash("sources-s1"),
		RootChainAddress:   testRootChain,
		CheckpointID:       testCheckpointID,
		HeaderBlockID:      block.UInt256(mustHash("header-block-id")),
		ChildBlockInterval: testInterval,
		BorStart:           testBorStart,
		BorEnd:             testBorEnd,
		CheckpointRoot:     testCheckpointRt,
		BorEndRef:          block.AnchorRef{Height: testBorEnd, Hash: mustHash("bor-end-block")},
		FinalizedL1Anchor:  block.AnchorRef{Height: 500, Hash: mustHash("l1-finalized-500")},
	}
	ev.BorRangeDigest = BorRangeDigest(ev.BorStart, ev.BorEnd, ev.BorEndRef)
	ev.Verification = FinalizedContractState{
		CallBlockHash:         ev.FinalizedL1Anchor.Hash,
		RootChainParamsDigest: ParamsDigest(ev.RootChainAddress, ev.ChildBlockInterval),
		CalldataDigest:        DigestBytes(HeaderBlocksCalldata(ev.HeaderBlockID)),
		ResultDigest:          DigestBytes([]byte("result")),
		ResultCreatedAt:       block.UInt256(mustHash("created-at")),
		ResultProposer:        testProposer,
	}
	return ev
}

func TestEvidenceEnumsRoundTripAndRejectUnknown(t *testing.T) {
	operations := []Operation{OpMilestoneLatest, OpCheckpointLatest, OpCheckpointBuffer, OpChainParams,
		OpCheckpointParams, OpMilestoneByNumber, OpCheckpointByNumber, OpMilestoneEvent}
	for _, op := range operations {
		raw, err := json.Marshal(op)
		if err != nil {
			t.Fatal(err)
		}
		var back Operation
		if err := json.Unmarshal(raw, &back); err != nil || back != op {
			t.Fatalf("operation %s round trip: %v %v", op, back, err)
		}
	}
	if err := json.Unmarshal([]byte(`"Nope"`), new(Operation)); err == nil {
		t.Fatal("unknown operation must be rejected")
	}
	var invalid Operation
	if _, err := invalid.MarshalJSON(); err == nil {
		t.Fatal("operation 0 must not marshal")
	}

	outcomes := []MilestoneOutcome{MilestoneObserved, MilestoneFutureQueued, MilestoneLocallyVerified, MilestoneRejected}
	for _, m := range outcomes {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var back MilestoneOutcome
		if err := json.Unmarshal(raw, &back); err != nil || back != m {
			t.Fatalf("outcome %s round trip", m)
		}
	}
	states := []CheckpointState{CheckpointBuffered, CheckpointStoredAcknowledged}
	for _, c := range states {
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var back CheckpointState
		if err := json.Unmarshal(raw, &back); err != nil || back != c {
			t.Fatalf("state %s round trip", c)
		}
	}
	kinds := []WhitelistKind{WhitelistMilestone, WhitelistCheckpoint}
	for _, k := range kinds {
		raw, err := json.Marshal(k)
		if err != nil {
			t.Fatal(err)
		}
		var back WhitelistKind
		if err := json.Unmarshal(raw, &back); err != nil || back != k {
			t.Fatalf("kind %s round trip", k)
		}
	}
	bases := []NativeFinalizedBasis{BasisMilestoneWhitelist, BasisCheckpointWhitelist}
	for _, b := range bases {
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		var back NativeFinalizedBasis
		if err := json.Unmarshal(raw, &back); err != nil || back != b {
			t.Fatalf("basis %s round trip", b)
		}
	}
	if NativeCoverage(0).String() != "INVALID" || L1Coverage(0).String() != "INVALID" {
		t.Fatal("zero coverage states are INVALID")
	}
	if NativeMilestoneCovered.String() != "NATIVE_MILESTONE_COVERED" || L1NotVerified.String() != "L1_NOT_VERIFIED" {
		t.Fatal("coverage names must be the contract names")
	}
}

func TestRequestSelectorMatrix(t *testing.T) {
	cases := []struct {
		request Request
		valid   bool
	}{
		{Request{Operation: OpMilestoneLatest}, true},
		{Request{Operation: OpCheckpointLatest}, true},
		{Request{Operation: OpCheckpointBuffer}, true},
		{Request{Operation: OpChainParams}, true},
		{Request{Operation: OpCheckpointParams}, true},
		{Request{Operation: OpMilestoneByNumber, Number: block.Some(uint64(7))}, true},
		{Request{Operation: OpCheckpointByNumber, Number: block.Some(uint64(7))}, true},
		{Request{Operation: OpMilestoneEvent, Height: block.Some(uint64(7)), EventOrdinal: block.Some(uint64(2))}, true},
		{Request{Operation: OpMilestoneLatest, Number: block.Some(uint64(7))}, false},
		{Request{Operation: OpMilestoneByNumber}, false},
		{Request{Operation: OpMilestoneByNumber, Number: block.Some(uint64(0))}, false},
		{Request{Operation: OpMilestoneEvent, Height: block.Some(uint64(7))}, false},
		{Request{Operation: OpMilestoneEvent, Number: block.Some(uint64(7))}, false},
		{Request{Operation: OpCheckpointLatest, Height: block.Some(uint64(7)), EventOrdinal: block.Some(uint64(1))}, false},
		{Request{}, false},
	}
	for _, tc := range cases {
		err := tc.request.Validate()
		if tc.valid && err != nil {
			t.Fatalf("request %s: unexpected error %v", tc.request.Operation, err)
		}
		if !tc.valid && err == nil {
			t.Fatalf("request %s with the wrong selector must be rejected", tc.request.Operation)
		}
	}
}

func TestSourceObservationRefValidate(t *testing.T) {
	if err := validSourceRef().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		mut  func(*SourceObservationRef)
	}{
		{"zero store identity", func(r *SourceObservationRef) { r.SourceStoreIdentity = block.Id16{} }},
		{"absent binding digest", func(r *SourceObservationRef) { r.SourcesBinding = block.Hash32{} }},
		{"incomplete heimdall identity", func(r *SourceObservationRef) { r.Heimdall = HeimdallIdentity{} }},
		{"empty observation id", func(r *SourceObservationRef) { r.ObservationID = "" }},
		{"invalid selector", func(r *SourceObservationRef) { r.Request = Request{Operation: OpMilestoneEvent} }},
		{"zero response digest", func(r *SourceObservationRef) { r.ResponseDigest = block.Hash32{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := validSourceRef()
			tc.mut(&ref)
			if err := ref.Validate(); err == nil {
				t.Fatal("the broken ref must be rejected")
			}
		})
	}
	// The D80 digest accessor returns exactly the pinned digest.
	ref := validSourceRef()
	if ref.BindingDigest() != profiles.SourcesBindingDigest(ref.SourcesBinding) {
		t.Fatal("BindingDigest exposes the mandatory digest")
	}
}

func TestWhitelistValidate(t *testing.T) {
	w := validWhitelist()
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
	if w.Narrower() {
		t.Fatal("a whitelist with its saved response is the full form")
	}
	narrower := w
	narrower.SourceEvidence = block.None[SourceObservationRef]()
	if !narrower.Narrower() {
		t.Fatal("without the saved response the whitelist is the narrower form")
	}
	if err := narrower.Validate(); err != nil {
		t.Fatalf("the narrower form is valid without an invented digest: %v", err)
	}
	for _, tc := range []struct {
		name string
		mut  func(*Whitelist)
	}{
		{"kind zero", func(x *Whitelist) { x.Kind = 0 }},
		{"zero anchor", func(x *Whitelist) { x.Anchor = block.AnchorRef{Height: 30} }},
		{"decision zero", func(x *Whitelist) { x.Decision = block.NativeDecisionRef{} }},
		{"digest mismatch", func(x *Whitelist) { x.AncestryBindingDigest = mustHash("other") }},
		{"invalid source evidence", func(x *Whitelist) {
			x.SourceEvidence = block.Some(SourceObservationRef{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := validWhitelist()
			tc.mut(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatal("the broken whitelist must be rejected")
			}
		})
	}
}

func TestMilestoneAndCheckpointValidate(t *testing.T) {
	m := Milestone{
		Source: validSourceRef(), MilestoneID: "ms-1", Proposer: testProposer,
		Start: 1, End: 2, Timestamp: 3, TotalDifficulty: 4,
		EndHash: mustHash("ms-end"), Outcome: MilestoneObserved,
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.Anchor() != (block.AnchorRef{Height: 2, Hash: mustHash("ms-end")}) {
		t.Fatal("the milestone anchor is its end block")
	}
	bad := m
	bad.End = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("an inverted range must be rejected")
	}
	bad = m
	bad.EndHash = block.Hash32{}
	if err := bad.Validate(); err == nil {
		t.Fatal("a zero end hash must be rejected")
	}

	cp := Checkpoint{
		Source: validSourceRef(), CheckpointID: block.Some(uint64(3)), Proposer: testProposer,
		Start: 1, End: 2, Timestamp: 3, RootHash: testCheckpointRt, State: CheckpointStoredAcknowledged,
	}
	if err := cp.Validate(); err != nil {
		t.Fatal(err)
	}
	badCp := cp
	badCp.LocalVerifiedEnd = block.Some(block.AnchorRef{Height: 2, Hash: testCheckpointRt})
	if err := badCp.Validate(); err == nil {
		t.Fatal("the checkpoint root is never the verified end block hash")
	}
	badCp = cp
	badCp.State = 0
	if err := badCp.Validate(); err == nil {
		t.Fatal("state zero must be rejected")
	}
}

func TestL1CheckpointValidate(t *testing.T) {
	ev := validL1Checkpoint()
	if err := ev.Validate(); err != nil {
		t.Fatal(err)
	}
	if ev.BindingDigest() != profiles.SourcesBindingDigest(ev.SourcesBinding) {
		t.Fatal("BindingDigest exposes the mandatory D80 digest")
	}
	for _, tc := range []struct {
		name string
		mut  func(*L1Checkpoint)
	}{
		{"absent binding digest", func(x *L1Checkpoint) { x.SourcesBinding = block.Hash32{} }},
		{"bor end height mismatch", func(x *L1Checkpoint) { x.BorEndRef = block.AnchorRef{Height: 1, Hash: mustHash("bor-end-block")} }},
		{"root as end block hash", func(x *L1Checkpoint) {
			x.BorEndRef = block.AnchorRef{Height: x.BorEnd, Hash: x.CheckpointRoot}
			x.BorRangeDigest = BorRangeDigest(x.BorStart, x.BorEnd, x.BorEndRef)
		}},
		{"range digest mismatch", func(x *L1Checkpoint) { x.BorRangeDigest = mustHash("other") }},
		{"finalized anchor off the call", func(x *L1Checkpoint) {
			x.FinalizedL1Anchor = block.AnchorRef{Height: 9, Hash: mustHash("other-l1")}
		}},
		{"zero interval", func(x *L1Checkpoint) { x.ChildBlockInterval = 0 }},
		{"inverted bor range", func(x *L1Checkpoint) { x.BorStart = x.BorEnd + 1 }},
		{"zero verification digest", func(x *L1Checkpoint) { x.Verification.CalldataDigest = block.Hash32{} }},
		{"zero node id", func(x *L1Checkpoint) { x.SupportingNodeID = block.Id16{} }},
		{"zero checkpoint id", func(x *L1Checkpoint) { x.CheckpointID = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := validL1Checkpoint()
			tc.mut(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatal("the broken evidence must be rejected")
			}
		})
	}
}

func TestEvidenceJSONKinds(t *testing.T) {
	// Known round trip with the origin.
	known := KnownOf(validWhitelist(), block.OriginNativeLive)
	raw, err := json.Marshal(known)
	if err != nil {
		t.Fatal(err)
	}
	var back Evidence[Whitelist]
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Kind != EvidenceKnown || !back.Equal(known) {
		t.Fatal("the known evidence must round trip")
	}

	// NotObserved carries no fields.
	raw, err = json.Marshal(NotObservedOf[Whitelist]())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"kind":"NOT_OBSERVED"}` {
		t.Fatalf("not-observed encoding %s", raw)
	}

	// Unavailable carries exactly its reason.
	unavailable := UnavailableOf[L1Checkpoint](block.UnavailableSourceSyncing)
	raw, err = json.Marshal(unavailable)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"kind":"UNAVAILABLE","reason":"SOURCE_SYNCING"}` {
		t.Fatalf("unavailable encoding %s", raw)
	}
	var backUnavailable Evidence[L1Checkpoint]
	if err := json.Unmarshal(raw, &backUnavailable); err != nil {
		t.Fatal(err)
	}
	if backUnavailable.Kind != EvidenceUnavailable || backUnavailable.Reason != block.UnavailableSourceSyncing {
		t.Fatal("the unavailable evidence must round trip")
	}

	// The malformed shapes are rejected.
	for _, bad := range []string{
		`{"kind":"KNOWN","value":{}}`,
		`{"kind":"KNOWN","origin":"NATIVE_LIVE"}`,
		`{"kind":"NOT_OBSERVED","reason":"SOURCE_OFFLINE"}`,
		`{"kind":"UNAVAILABLE"}`,
		`{"kind":"UNAVAILABLE","reason":"MAYBE"}`,
		`{"kind":"WHAT"}`,
	} {
		var ev Evidence[Whitelist]
		if err := json.Unmarshal([]byte(bad), &ev); err == nil {
			t.Fatalf("malformed evidence %s must be rejected", bad)
		}
	}

	// The full evidence set validates its fields.
	full := PolygonEvidence{
		Milestone:           KnownOf(Milestone{Source: validSourceRef(), MilestoneID: "m", Proposer: testProposer, Start: 1, End: 2, EndHash: mustHash("e"), Outcome: MilestoneObserved}, block.OriginNativeLive),
		Checkpoint:          NotObservedOf[Checkpoint](),
		MilestoneWhitelist:  NotObservedOf[Whitelist](),
		CheckpointWhitelist: NotObservedOf[Whitelist](),
		NativeFinalized:     NotObservedOf[NativeFinalized](),
		L1Checkpoint:        KnownOf(validL1Checkpoint(), block.OriginNativeLive),
	}
	if err := full.Validate(); err != nil {
		t.Fatal(err)
	}
	brokenSet := full
	brokenSet.L1Checkpoint = KnownOf(L1Checkpoint{}, block.OriginNativeLive)
	if err := brokenSet.Validate(); err == nil {
		t.Fatal("an invalid field must fail the whole set")
	}
}

// Equal reports evidence equality through the canonical encoding.
func (e Evidence[T]) Equal(other Evidence[T]) bool {
	a, errA := json.Marshal(e)
	b, errB := json.Marshal(other)
	return errA == nil && errB == nil && string(a) == string(b)
}
