package compatibility

import (
	"strings"
	"testing"
)

func TestErrorMessagesNameTheirInputs(t *testing.T) {
	cases := []struct {
		err  error
		want []string
	}{
		{&ErrInvalidVersion{Reason: "version identifier is empty"}, []string{"version identifier is empty"}},
		{&ErrDuplicateVersion{ID: idOld}, []string{string(idOld)}},
		{&ErrUnknownVersion{ID: idUnread}, []string{string(idUnread)}},
		{&ErrReadUnsupported{From: idOld, To: idCur}, []string{string(idOld), string(idCur)}},
		{&ErrReplayUnsupported{From: idOld, To: idCur}, []string{string(idOld), string(idCur)}},
		{&ErrWriteTargetNotCurrent{Target: idNext, Current: idCur}, []string{string(idNext), string(idCur)}},
		{&ErrVersionNotWritable{ID: idOld}, []string{string(idOld)}},
		{&ErrStoreTransitionRejected{From: idUnread, To: idCur}, []string{string(idUnread), string(idCur)}},
		{&ErrPendingImmutable{Record: "batch/7", Original: idOld, Attempted: idCur}, []string{"batch/7", string(idOld), string(idCur)}},
		{&ErrTransitionBlocked{Reason: "drain is in progress", Snapshot: DrainSnapshot{Mode: ModeDrain, Pending: 1, MaxDelivery: StateSealed}}, []string{"drain is in progress", "DRAIN", "1 pending", "SEALED"}},
		{&ErrInvalidSnapshot{Reason: "pending count is negative"}, []string{"pending count is negative"}},
		{&ErrInvalidClaim{Claim: ClaimType(0)}, []string{"ClaimType(0)"}},
		{&ErrInvalidClaim{Claim: ClaimType(7)}, []string{"ClaimType(7)"}},
	}
	for _, tc := range cases {
		msg := tc.err.Error()
		if msg == "" {
			t.Fatalf("%T: error message must not be empty", tc.err)
		}
		for _, want := range tc.want {
			if !strings.Contains(msg, want) {
				t.Fatalf("%T: message %q must contain %q", tc.err, msg, want)
			}
		}
	}
}

func TestEnumStringsRoundTrip(t *testing.T) {
	states := map[DeliveryState]string{
		StateNone: "NONE", StateIntent: "INTENT", StateStaging: "STAGING",
		StateReady: "READY", StateSealed: "SEALED", StateAckedCleanup: "ACKED_CLEANUP",
		StateOutbox: "OUTBOX", DeliveryState(9): "DeliveryState(9)",
	}
	for st, want := range states {
		if got := st.String(); got != want {
			t.Fatalf("DeliveryState(%d).String() = %q, want %q", uint8(st), got, want)
		}
	}
	modes := map[Mode]string{
		ModeUnset: "UNSET", ModeLive: "LIVE", ModeDrain: "DRAIN",
		ModeDrained: "DRAINED", ModeBlocked: "BLOCKED", Mode(9): "Mode(9)",
	}
	for m, want := range modes {
		if got := m.String(); got != want {
			t.Fatalf("Mode(%d).String() = %q, want %q", uint8(m), got, want)
		}
	}
	if !StateSealed.SealedOrLater() || !StateOutbox.SealedOrLater() {
		t.Fatal("SEALED and later must report SealedOrLater")
	}
	if StateReady.SealedOrLater() || StateNone.SealedOrLater() {
		t.Fatal("pre-SEALED and none must not report SealedOrLater")
	}
	claims := map[ClaimType]string{
		ClaimWirePlan: "wire/plan", ClaimStoreFormat: "store-format", ClaimType(9): "ClaimType(9)",
	}
	for c, want := range claims {
		if got := c.String(); got != want {
			t.Fatalf("ClaimType(%d).String() = %q, want %q", uint8(c), got, want)
		}
	}
}

func TestPolicyExposesMatrix(t *testing.T) {
	m := newTestMatrix(t)
	p := NewPolicy(m)
	if p.Matrix() != m {
		t.Fatal("Policy must expose the matrix it consults")
	}
	if p.Matrix().Current() != idCur {
		t.Fatalf("matrix current must be %q", idCur)
	}
}
