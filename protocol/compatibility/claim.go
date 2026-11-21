package compatibility

import "fmt"

// ClaimType names which preflight version family of DELIVERY_STATE_MACHINE
// ("Errors, ENOSPC, совместимость": store_format, plan_format, wire_schema,
// validator_profile, native_profile are checked before any writable
// mutation) a transition decision belongs to. The distinction is
// normative, not cosmetic:
//
//   - ClaimWirePlan: an incompatible upgrade becomes allowed once the old
//     build is DRAINED. A drain closes every saved obligation, so after a
//     completed, sealed drain no old-format obligation remains and the new
//     format's inability to read the old one stops mattering for new
//     writes.
//   - ClaimStoreFormat: an incompatible persistent store format change is
//     never allowed within the same store, whatever the drain state. The
//     store keeps identity and lifecycle metadata for its whole life,
//     there are no automatic migrations, and the refusal holds even for a
//     completed drain with empty payload (UPGRADE-01).
//
// Both families share the read and replay claims; only the transition
// rules differ. The claim type is stated by the caller of a transition
// decision, never defaulted: the two rules give different answers to the
// same version pair.
type ClaimType uint8

const (
	// ClaimWirePlan is the wire/plan family: the transport wrapper, the
	// record format, the transport layout, the immutable headers, the
	// control protocol and the READY manifest versions of the v2 contract
	// (the pinned versions registered in this package). An incompatible
	// upgrade in this family is a timing question: it requires DRAINED by
	// the old build, enforced by the drain guard, not a capability
	// rejection.
	ClaimWirePlan ClaimType = iota + 1

	// ClaimStoreFormat is the persistent store format family: the
	// store_format preflight version of the durable store itself. No
	// concrete version of this family is registered in this package; the
	// store format is owned outside the wire contract. An incompatible
	// change in this family is a capability rejection before any write,
	// regardless of drain state.
	ClaimStoreFormat
)

// valid reports whether c is one of the defined claim types.
func (c ClaimType) valid() bool {
	return c == ClaimWirePlan || c == ClaimStoreFormat
}

func (c ClaimType) String() string {
	switch c {
	case ClaimWirePlan:
		return "wire/plan"
	case ClaimStoreFormat:
		return "store-format"
	default:
		return fmt.Sprintf("ClaimType(%d)", uint8(c))
	}
}

// ErrInvalidClaim marks a transition decision made with an unset or
// unknown claim type. The wire/plan and store-format rules answer the
// same pair differently, so the family is never defaulted from a zero
// value.
type ErrInvalidClaim struct{ Claim ClaimType }

func (e *ErrInvalidClaim) Error() string {
	return fmt.Sprintf("compatibility: claim type %s is unknown; wire/plan and store-format transitions follow different rules, so the family must be stated", e.Claim)
}
