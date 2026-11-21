package block

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func fixtureBatch(t *testing.T, rec BlockRecord) DeliveryBatchRef {
	t.Helper()
	return DeliveryBatchRef{
		Batch:       BatchID{ProducerID: rec.Activation.ProducerID, RunID: rec.Activation.RunID, Sequence: 1},
		Activations: []BlockActivationRef{rec.Activation},
		Frontier:    FrontierWalSaved,
	}
}

func TestDeliveryFrontierAdvance(t *testing.T) {
	next, err := AdvanceFrontier(FrontierWalSaved, FrontierNatsAcked)
	if err != nil {
		t.Fatal(err)
	}
	if next != FrontierNatsAcked {
		t.Fatalf("frontier must advance to NATS_ACKED, got %s", next)
	}

	same, err := AdvanceFrontier(FrontierNatsAcked, FrontierNatsAcked)
	if err != nil || same != FrontierNatsAcked {
		t.Fatalf("idempotent frontier advance failed: %v", err)
	}

	if _, err := AdvanceFrontier(FrontierNatsAcked, FrontierWalSaved); err == nil {
		t.Fatal("frontier must never move back")
	}
	if _, err := AdvanceFrontier(0, FrontierNatsAcked); err == nil {
		t.Fatal("zero frontier must be invalid")
	}
	var zero DeliveryFrontier
	if err := zero.Validate(); err == nil {
		t.Fatal("the zero frontier must not be a valid value")
	}
}

func TestDeliveryBatchRefValidation(t *testing.T) {
	rec := ethBlockRecord(t, 5)
	batch := fixtureBatch(t, rec)
	if err := batch.Validate(); err != nil {
		t.Fatalf("valid batch rejected: %v", err)
	}

	noActivation := batch
	noActivation.Activations = nil
	if err := noActivation.Validate(); err == nil {
		t.Fatal("batch without activations must be rejected")
	}

	duplicate := batch
	duplicate.Activations = []BlockActivationRef{rec.Activation, rec.Activation}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("duplicate activation references must be rejected")
	}

	zeroSequence := batch
	zeroSequence.Batch.Sequence = 0
	if err := zeroSequence.Validate(); err == nil {
		t.Fatal("batch counter starts at 1; zero must be rejected")
	}

	acked := batch
	if err := acked.Ack(); err != nil {
		t.Fatal(err)
	}
	if acked.Frontier != FrontierNatsAcked {
		t.Fatalf("ack must reach NATS_ACKED, got %s", acked.Frontier)
	}
	if err := acked.Ack(); err != nil {
		t.Fatalf("repeated ack must stay idempotent: %v", err)
	}
}

func TestDeliveryBatchRefRoundTrip(t *testing.T) {
	rec := ethBlockRecord(t, 5)
	batch := fixtureBatch(t, rec)
	data, err := Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{"complete", "coverage", "inventory", "final"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("delivery batch encoding must not carry %q: %s", forbidden, data)
		}
	}
	var back DeliveryBatchRef
	if err := Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batch, back) {
		t.Fatal("delivery batch round trip changed the reference")
	}
}

func TestBlockCompleteForbidden(t *testing.T) {
	for _, ok := range []string{"applied", "removed", "status"} {
		kind, err := ParseBlockEventKind(ok)
		if err != nil {
			t.Fatalf("kind %q must parse: %v", ok, err)
		}
		if err := kind.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, forbidden := range []string{"BLOCK_COMPLETE", "block_complete", "COVERAGE_UPDATED", "coverage_updated"} {
		_, err := ParseBlockEventKind(forbidden)
		if !errors.Is(err, ErrBlockCompleteForbidden) {
			t.Fatalf("kind %q must be rejected as the excluded D25 marker, got %v", forbidden, err)
		}
	}
	if _, err := ParseBlockEventKind("finalized_block"); !errors.Is(err, ErrInvalidValue) {
		t.Fatal("ordinary unknown kinds are invalid values, not forbidden markers")
	}

	// The forbidden name is rejected in the local encoding too.
	var kind BlockEventKind
	if err := Unmarshal([]byte(`"BLOCK_COMPLETE"`), &kind); !errors.Is(err, ErrBlockCompleteForbidden) {
		t.Fatalf("encoded BLOCK_COMPLETE must be rejected, got %v", err)
	}
	if err := Unmarshal([]byte(`"applied"`), &kind); err != nil {
		t.Fatal(err)
	}
	if kind != BlockEventApplied {
		t.Fatal("applied kind mismatch")
	}

	// The delivery reference type itself has no completeness member: the
	// field set of DeliveryBatchRef is exactly batch, activations and
	// frontier.
	batchType := reflect.TypeOf(DeliveryBatchRef{})
	want := map[string]bool{"Batch": false, "Activations": false, "Frontier": false}
	for i := 0; i < batchType.NumField(); i++ {
		name := batchType.Field(i).Name
		seen, known := want[name]
		if !known {
			t.Fatalf("unexpected delivery batch field %q", name)
		}
		if seen {
			t.Fatalf("duplicate field %q", name)
		}
		want[name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing delivery batch field %q", name)
		}
	}
}
