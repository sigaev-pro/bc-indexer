// The maintenance identities of the drain/cutover protocol: the binding
// view both owners exchange, the seal intent and ack, the switch intents
// and receipts, and the resume intent and ack
// (CONTROL_WIRE_CONTRACT, "Управляемый restart, drain, cutover";
// DELIVERY_STATE_MACHINE, "Drain и cutover D54/D57/D59"). These are
// control-wire identities like IngestAck and ReleaseAck; the maintenance
// engine of core/delivery/maintenance drives them over the store seams.
package delivery

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// BindingView is the neutral view of one DestinationBinding descriptor
// both owners exchange. The gateway/destination package owns the strict
// descriptor form and its digests; this view carries the verified content
// across the module boundary without importing it.
type BindingView struct {
	DestinationID    proto.ID16
	Generation       uint64
	Network          proto.NetworkIdentity
	ExpectedAccount  string
	Stream           string
	SubjectPrefix    string
	DescriptorDigest proto.Hash32
}

// SealIntent is the SealGeneration request: the binding and admission
// generations being sealed, the drain identity and the batch highwater
// every obligation below is finished for.
type SealIntent struct {
	BindingGeneration   uint64
	AdmissionGeneration uint64
	DrainID             proto.ID16
	Highwater           uint64
	BindingDigest       proto.Hash32
}

// Validate checks the counters and the drain identity.
func (i SealIntent) Validate() error {
	if i.BindingGeneration == 0 || i.AdmissionGeneration == 0 {
		return fmt.Errorf("%w: seal generation counters are zero", ErrInvalidValue)
	}
	if i.DrainID.IsZero() {
		return fmt.Errorf("%w: seal drain id is zero", ErrInvalidValue)
	}
	if i.Highwater == 0 {
		return fmt.Errorf("%w: seal highwater is zero (the empty frontier is 0 only before any batch)", ErrInvalidValue)
	}
	return nil
}

// SealAck is the durable answer of a SealGeneration: the whole intent
// bound together — one matching number alone is never enough.
type SealAck struct {
	DrainID             proto.ID16
	BindingDigest       proto.Hash32
	AdmissionGeneration uint64
	Highwater           uint64
	IssuerStoreEpoch    proto.ID16
}

// SameSeal reports whether both acks bind the same whole intent.
func (a SealAck) SameSeal(other SealAck) bool { return a == other }

// SwitchPrepared is the receipt of a successful PrepareSwitch: the switch
// identity with the old and new binding digests, the drain it continues,
// the admission generation and the issuing gateway store epoch.
type SwitchPrepared struct {
	SwitchID            proto.ID16
	OldDigest           proto.Hash32
	NewDigest           proto.Hash32
	DrainID             proto.ID16
	AdmissionGeneration uint64
	IssuerStoreEpoch    proto.ID16
}

// SameSwitch reports whether both receipts bind the same switch identity.
func (r SwitchPrepared) SameSwitch(other SwitchPrepared) bool { return r == other }

// SwitchCommitted is the receipt of a durable CommitSwitch: the same
// identity the SwitchPrepared carried.
type SwitchCommitted struct {
	Prepared SwitchPrepared
}

// ResumeIntent is the durable producer intent to open the next admission
// generation after a seal, under the active binding.
type ResumeIntent struct {
	ResumeID                proto.ID16
	OldSeal                 SealAck
	BindingDigest           proto.Hash32
	NextAdmissionGeneration uint64
}

// Validate checks the resume identity and counters.
func (i ResumeIntent) Validate() error {
	if i.ResumeID.IsZero() {
		return fmt.Errorf("%w: resume id is zero", ErrInvalidValue)
	}
	if i.BindingDigest == (proto.Hash32{}) {
		return fmt.Errorf("%w: resume binding digest is zero", ErrInvalidValue)
	}
	if i.NextAdmissionGeneration == 0 {
		return fmt.Errorf("%w: next admission generation is zero", ErrInvalidValue)
	}
	return nil
}

// ResumeAck is the durable answer of a Resume: the resume identity with
// the old and new admission generations and the drain it continues.
type ResumeAck struct {
	ResumeID               proto.ID16
	BindingDigest          proto.Hash32
	OldAdmissionGeneration uint64
	NewAdmissionGeneration uint64
	DrainID                proto.ID16
	IssuerStoreEpoch       proto.ID16
}

// SameResume reports whether both acks bind the same resume identity.
func (a ResumeAck) SameResume(other ResumeAck) bool { return a == other }

// SwitchPhase is the gateway state of a destination switch.
type SwitchPhase uint8

// The switch phases.
const (
	// SwitchNone means the active binding matches the desired one and no
	// transition is open.
	SwitchNone SwitchPhase = iota

	// SwitchAwait means the gateway opened with a desired binding that
	// differs from its active one: the old binding stays active and only
	// recovery and maintenance handshakes run; publication to the desired
	// route is forbidden.
	SwitchAwait

	// SwitchPreparedPhase means PrepareSwitch succeeded: the new target
	// verified and the transition intent is durable.
	SwitchPreparedPhase

	// SwitchCommittedPhase means CommitSwitch durably activated the new
	// binding.
	SwitchCommittedPhase
)

// String returns the phase name.
func (p SwitchPhase) String() string {
	switch p {
	case SwitchNone:
		return "NONE"
	case SwitchAwait:
		return "AWAIT_SWITCH"
	case SwitchPreparedPhase:
		return "PREPARED"
	case SwitchCommittedPhase:
		return "COMMITTED"
	}
	return fmt.Sprintf("SwitchPhase(%d)", uint8(p))
}
