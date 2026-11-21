package compatibility

import (
	"errors"
	"strings"
	"testing"
)

func TestPendingRewriteUnderDifferentVersionRejected(t *testing.T) {
	rec := PendingRecord{ID: "batch/7/event/3", Version: idOld}
	err := RewritePending(rec, idCur)
	if err == nil {
		t.Fatal("rewriting a durable pending record under a different version must be rejected")
	}
	var imm *ErrPendingImmutable
	if !errors.As(err, &imm) {
		t.Fatalf("must be ErrPendingImmutable, got %T", err)
	}
	if imm.Record != "batch/7/event/3" || imm.Original != idOld || imm.Attempted != idCur {
		t.Fatalf("error must name the record and both versions, got %+v", imm)
	}
	for _, want := range []string{"batch/7/event/3", string(idOld), string(idCur)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message must contain %q: %v", want, err)
		}
	}
	// The original record keeps its durable version after the rejected
	// attempt.
	if rec.Version != idOld {
		t.Fatalf("the durable record must keep version %q, got %q", idOld, rec.Version)
	}
}

func TestPendingRewriteSameVersionAllowed(t *testing.T) {
	rec := PendingRecord{ID: "batch/7/event/3", Version: idOld}
	if err := RewritePending(rec, idOld); err != nil {
		t.Fatalf("a rewrite under the original version is the chunk-retry path: %v", err)
	}
}

func TestPendingImmutalityDoesNotDependOnRegistration(t *testing.T) {
	// The immutability rule holds for any different version, including
	// one the running build has never registered; it is not a capability
	// question.
	rec := PendingRecord{ID: "batch/9", Version: idOld}
	err := RewritePending(rec, "record-never-registered")
	var imm *ErrPendingImmutable
	if !errors.As(err, &imm) || imm.Attempted != "record-never-registered" {
		t.Fatalf("must be ErrPendingImmutable naming the attempted version, got %v", err)
	}
}
