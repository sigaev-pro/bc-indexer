package compatibility

import "fmt"

// PendingRecord is the version fact of one durable pending obligation:
// its opaque record identity and the encoding version it was made
// durable under. Once durable, that version never changes.
type PendingRecord struct {
	ID      string
	Version VersionID
}

// ErrPendingImmutable is the immutable-pending rejection. A pending
// record's version is fixed once the record is durable; any operation
// that would rewrite it under a different version is rejected, and the
// error names the record and both versions. The original record is left
// unchanged; fixing or diagnosing it is a separate explicit action, never
// an automatic re-encode (DELIVERY_STATE_MACHINE: bytes under an existing
// ID are never changed; CONFIG_BLOCKED/invalid pending does not mutate or
// delete the batch).
type ErrPendingImmutable struct {
	Record    string
	Original  VersionID
	Attempted VersionID
}

func (e *ErrPendingImmutable) Error() string {
	return fmt.Sprintf("compatibility: pending record %q keeps version %q; rewriting it as %q is rejected", e.Record, e.Original, e.Attempted)
}

// RewritePending decides one attempt to rewrite a durable pending record.
// Rewriting under the original version is the ordinary chunk-retry path
// (same bytes, same version) and is not a compatibility decision.
// Rewriting under any other version is rejected with ErrPendingImmutable;
// the record value itself is never modified, so the caller's durable copy
// keeps its original version.
func RewritePending(r PendingRecord, attempted VersionID) error {
	if r.Version != attempted {
		return &ErrPendingImmutable{
			Record:    r.ID,
			Original:  r.Version,
			Attempted: attempted,
		}
	}
	return nil
}
