// The durable identities of the delivery protocol: the batch id, the
// immutable publication identity an IngestAck carries, the ReleaseAck of
// the ordered cleanup handshake, the gateway batch states, the frame key of
// one published transport frame and the session fence of a mutating step
// (CONTROL_WIRE_CONTRACT, "Базовые кодирования и идентичности";
// DELIVERY_STATE_MACHINE, "Таблица переходов").
package delivery

import (
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/compatibility"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// ProducerState is the producer-side delivery state of one batch. The
// states and their meanings are owned by the compatibility version policy
// (E02-04), which fixes INTENT/STAGING/READY/SEALED/ACKED_CLEANUP/OUTBOX;
// a batch leaves the state machine through the producer retired prefix,
// not through one more state.
type ProducerState = compatibility.DeliveryState

// ProducerMode is the producer mode LIVE/DRAIN/DRAINED/BLOCKED, also owned
// by the compatibility policy.
type ProducerMode = compatibility.Mode

// The producer delivery states, re-exported from the compatibility policy.
const (
	StateIntent       = compatibility.StateIntent
	StateStaging      = compatibility.StateStaging
	StateReady        = compatibility.StateReady
	StateSealed       = compatibility.StateSealed
	StateAckedCleanup = compatibility.StateAckedCleanup
	StateOutbox       = compatibility.StateOutbox
)

// The producer modes of DELIVERY_STATE_MACHINE.
const (
	ModeLive    = compatibility.ModeLive
	ModeDrain   = compatibility.ModeDrain
	ModeDrained = compatibility.ModeDrained
	ModeBlocked = compatibility.ModeBlocked
)

// BatchID identifies one delivery batch:
// batch_id=(producer_id,run_id,batch_seq). batch_seq is allocated
// atomically with the durable delivery intent and starts at 1; zero is the
// empty frontier only.
type BatchID struct {
	Producer proto.ID16
	Run      proto.ID16
	Seq      uint64
}

// Validate rejects an unset producer or run identity or a zero sequence.
func (b BatchID) Validate() error {
	if b.Producer.IsZero() {
		return fmt.Errorf("%w: batch producer is zero", ErrInvalidValue)
	}
	if b.Run.IsZero() {
		return fmt.Errorf("%w: batch run is zero", ErrInvalidValue)
	}
	if b.Seq == 0 {
		return fmt.Errorf("%w: batch sequence 0, counters start at 1", ErrInvalidValue)
	}
	return nil
}

// Less orders two batch ids of one run by sequence; ids of different
// producers or runs never compare.
func (b BatchID) Less(other BatchID) bool {
	if b.Producer != other.Producer || b.Run != other.Run {
		return false
	}
	return b.Seq < other.Seq
}

// BatchIdentity is the immutable publication identity one batch carries
// from READY onward: the manifest, plan and layout digests, the destination
// binding digest and the issuing gateway store epoch. It is the exact
// content of an IngestAck and the exact content the gateway compares on
// every retry: a different digest after READY is a conflict, never a
// replacement.
type BatchIdentity struct {
	Batch                BatchID
	ManifestDigest       proto.Hash32
	PlanDigest           proto.Hash32
	LayoutManifestDigest proto.Hash32
	BindingDigest        proto.Hash32
	IssuerStoreEpoch     proto.ID16
}

// Validate checks the batch and the issuer epoch; the digests are opaque
// 32-byte values and may legitimately be any bit pattern.
func (i BatchIdentity) Validate() error {
	if err := i.Batch.Validate(); err != nil {
		return err
	}
	if i.IssuerStoreEpoch.IsZero() {
		return fmt.Errorf("%w: issuer store epoch is zero", ErrInvalidValue)
	}
	return nil
}

// SameIdentity reports whether both records name the same batch and carry
// identical digests and issuer epoch.
func (i BatchIdentity) SameIdentity(other BatchIdentity) bool {
	return i == other
}

// IngestAck is the full durable acknowledgment of one completely delivered
// batch: {batch_id, manifest_digest, plan_digest, layout_manifest_digest,
// binding_digest, issuer_store_epoch}. A partial PubAck set, a single
// transport ACK or a successful transport write never composes one. The
// producer keeps the whole IngestAck and the release outbox until a
// verified ReleaseAck advances the retired prefix.
type IngestAck struct {
	Identity BatchIdentity
}

// Validate checks the carried identity.
func (a IngestAck) Validate() error { return a.Identity.Validate() }

// ReleaseAck answers an ordered Release or a RecoveryRelease:
// {batch_id, retired_prefix}. It is sent only after the releasing side's
// final atomic commit, so a ReleaseAck always proves a durably advanced
// prefix.
type ReleaseAck struct {
	Batch         BatchID
	RetiredPrefix uint64
}

// Validate checks the batch id.
func (a ReleaseAck) Validate() error { return a.Batch.Validate() }

// GatewayBatchState is the gateway-side state of one batch
// (DELIVERY_STATE_MACHINE, gateway prefixes): PREPARING/PREPARED/
// RECEIVING precede CLOSED and are never published; CLOSED allows
// publication; NATS_ACKED follows the full PubAck set of the batch and
// precedes IngestAck; RELEASING is the bounded cleanup with its cursor;
// a retired batch is gone, only the retired prefix remains.
type GatewayBatchState uint8

// The gateway batch states.
const (
	// StateNone means the gateway holds no record of the batch.
	StateNone GatewayBatchState = iota
	StatePreparing
	StatePrepared
	StateReceiving
	StateClosed
	StateNATSACKed
	StateReleasing
)

// Published reports whether the batch state allows publication: only a
// CLOSED batch may be published, and only before the release barrier.
func (s GatewayBatchState) Published() bool { return s == StateClosed }

// Retired reports whether the batch record itself is gone and only the
// retired prefix remembers it. A batch is retired exactly when its
// sequence is at or below the retired prefix of its run.
func (s GatewayBatchState) Retired() bool { return s == StateNone }

// String returns the state name.
func (s GatewayBatchState) String() string {
	switch s {
	case StateNone:
		return "NONE"
	case StatePreparing:
		return "PREPARING"
	case StatePrepared:
		return "PREPARED"
	case StateReceiving:
		return "RECEIVING"
	case StateClosed:
		return "CLOSED"
	case StateNATSACKed:
		return "NATS_ACKED"
	case StateReleasing:
		return "RELEASING"
	}
	return fmt.Sprintf("GatewayBatchState(%d)", uint8(s))
}

// FrameKey identifies one published transport frame of a batch: the event
// ordinal, the fragment index, the form and, for a PART frame, the part
// index. It is the unit of PubAck accounting; COMPLETE carries part 0.
type FrameKey struct {
	Ordinal  uint64
	Fragment uint64
	Part     uint64
	Complete bool
}

// String returns a stable printable form for logs and tests.
func (k FrameKey) String() string {
	form := "part"
	if k.Complete {
		form = "complete"
	}
	return fmt.Sprintf("%d/%d/%s/%d", k.Ordinal, k.Fragment, form, k.Part)
}

// SessionKind distinguishes the ordinary data session from the
// MAINTENANCE session that finishes a saved transition intent. A
// MAINTENANCE session admits exactly the saved switch/resume identity and
// never accepts new Prepare/Publish/Seal.
type SessionKind uint8

// The session kinds.
const (
	SessionNormal SessionKind = iota
	SessionMaintenance
)

// String returns the kind name.
func (k SessionKind) String() string {
	if k == SessionMaintenance {
		return "MAINTENANCE"
	}
	return "NORMAL"
}

// Fence is the authenticated session fence of a mutating step: the
// unpredictable session token, the authenticated producer and run, the
// monotonic connection and admission generations and the binding digest
// the session was opened for. The token check is serialized with the
// commit, not only with the RPC read; a stale fence never creates state
// (DELIVERY_STATE_MACHINE, "Fencing"; CONTROL_WIRE_CONTRACT, "Служебные
// запросы и ответы").
type Fence struct {
	SessionToken         []byte
	Producer             proto.ID16
	Run                  proto.ID16
	ConnectionGeneration uint64
	AdmissionGeneration  uint64
	BindingDigest        proto.Hash32
	Kind                 SessionKind
}

// SameSession reports whether both fences carry the same session identity
// (token, owner, generations, binding and kind).
func (f Fence) SameSession(other Fence) bool {
	return string(f.SessionToken) == string(other.SessionToken) &&
		f.Producer == other.Producer && f.Run == other.Run &&
		f.ConnectionGeneration == other.ConnectionGeneration &&
		f.AdmissionGeneration == other.AdmissionGeneration &&
		f.BindingDigest == other.BindingDigest && f.Kind == other.Kind
}

// ErrNilFence marks a mutating call without a fence.
var ErrNilFence = fmt.Errorf("%w: mutating step carries no session fence", ErrFenced)
