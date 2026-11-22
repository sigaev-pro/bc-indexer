// The minimal journal seams of the delivery engine. The producer and
// gateway stores of E03-01 (built against these interfaces in a parallel
// block) expose exactly the durable steps the release, maintenance and
// publisher engines drive; every mutating method is one atomic sync
// commit, and every read observes only committed state. Chunk transport
// (PrepareChunk/PublishChunk) is E03-02.rpc_streaming; what survives it is
// a CLOSED batch registered through ImportClosed.
package delivery

import "github.com/sigaev-pro/bc-indexer/protocol/proto"

// SealedPlan is the producer's durable transport plan of one SEALED batch:
// the immutable publication identity, the reservation sequence base, the
// stream of the binding the plan was sealed for and the frame keys in
// layout order. After Gateway store loss the exact issuer plan is imported
// from this record through IMPORT_SEALED: the issuer epoch, the layout and
// the frame identities never change and no new sequence is allocated.
type SealedPlan struct {
	Identity     BatchIdentity
	SequenceBase uint64
	Stream       string
	FrameKeys    []FrameKey
}

// ProducerRecord is the durable producer state of one batch: its delivery
// state, its immutable identity once READY, the payload presence and, from
// ACKED_CLEANUP on, the release outbox entry with the received IngestAck.
type ProducerRecord struct {
	State    ProducerState
	Identity BatchIdentity
	// HasIdentity reports whether the closing commit already fixed the
	// identity (READY and later).
	HasIdentity bool
	// HasPayload reports whether the producer still holds the batch
	// payload. Payload cleanup after IngestAck never removes the outbox
	// entry or the IngestAck.
	HasPayload bool
	// Outbox is the release outbox entry: set atomically with
	// ACKED_CLEANUP, removed atomically with the retired prefix advance.
	Outbox bool
	// Ack is the full received IngestAck of the outbox entry.
	Ack IngestAck
}

// GatewayRecord is the durable gateway state of one batch.
type GatewayRecord struct {
	State    GatewayBatchState
	Identity BatchIdentity
	// Acked is the durable PubAck set of the batch by frame key. A lost
	// bitmap is recoverable: re-publishing an acked frame is a harmless
	// duplicate.
	Acked map[FrameKey]struct{}
	// FrameKeys is the expected full frame set of the batch in layout
	// order; NATS_ACKED requires a PubAck of every key.
	FrameKeys []FrameKey
	// Ack is the IngestAck the producer was answered, once durable.
	Ack IngestAck
	// HasAck reports whether the durable IngestAck exists.
	HasAck bool
	// ReleasingCursor counts the bounded cleanup steps already synced
	// while the batch is RELEASING.
	ReleasingCursor uint64
}

// ProducerStore is the producer-side journal seam. One serialized writer
// per store; all methods are safe for concurrent use.
type ProducerStore interface {
	// Identity returns the persistent producer and run ids.
	Identity() (producer, run proto.ID16)

	// Mode returns the durable producer mode.
	Mode() ProducerMode

	// SetMode durably records the mode. A DRAINED store never returns to
	// LIVE without the explicit Resume commit.
	SetMode(mode ProducerMode) error

	// RetiredPrefix returns the producer's retired prefix: every batch
	// sequence at or below it is fully released and gone.
	RetiredPrefix() uint64

	// Batch reads one batch record; ok is false when the store holds no
	// record for the sequence. A retired batch reads as gone.
	Batch(seq uint64) (record ProducerRecord, ok bool)

	// SealedPlan reads the durable sealed plan of a SEALED batch for
	// IMPORT_SEALED recovery.
	SealedPlan(seq uint64) (plan SealedPlan, ok bool)

	// AllocateIntent atomically allocates the next batch sequence with
	// the durable delivery intent. The decision-to-batch binding is part
	// of the same commit in the real store; here the sequence alone
	// carries it.
	AllocateIntent() (seq uint64, err error)

	// CommitReady closes STAGING into READY: one atomic commit of the
	// manifest identity and the capture checkpoint. Staged RAM chunks the
	// commit did not consume are lost by definition.
	CommitReady(seq uint64, identity BatchIdentity) error

	// CommitSealed closes READY into SEALED with the exact transport
	// plan. Attempt regeneration before this point keeps the batch id and
	// changes only the attempt generation.
	CommitSealed(seq uint64, plan SealedPlan) error

	// SaveAckOutbox atomically records ACKED_CLEANUP plus the release
	// outbox entry with the verified IngestAck. It requires SEALED with
	// the same identity and is idempotent for the same ack.
	SaveAckOutbox(seq uint64, ack IngestAck) error

	// CleanupPayload deletes the batch payload in bounded steps. It never
	// deletes the outbox entry or the IngestAck, which RecoveryRelease
	// needs after a Gateway store loss. Each call is one durable bounded
	// step; done reports whether payload remains.
	CleanupPayload(seq uint64) (remains bool, err error)

	// Retire atomically advances the retired prefix to seq and deletes
	// the outbox entry, after a verified ReleaseAck. It is an error for a
	// batch above the current prefix plus one or for a batch without an
	// outbox entry.
	Retire(seq uint64, ack IngestAck) error
}

// GatewayStore is the gateway-side journal seam. One serialized writer per
// store; all methods are safe for concurrent use.
type GatewayStore interface {
	// StoreEpoch returns the persistent gateway store identity. A new
	// store has a new epoch.
	StoreEpoch() proto.ID16

	// OpenSession installs a session fence, replacing any previous one:
	// an old token never survives a new session on the same store.
	OpenSession(fence Fence) error

	// RetiredPrefix returns the gateway's retired prefix for the run.
	RetiredPrefix(producer, run proto.ID16) uint64

	// Batch reads one batch record under the fence's run; ok is false
	// when the record is gone (retired or never present).
	Batch(fence Fence, id BatchID) (record GatewayRecord, ok bool)

	// ImportClosed registers a CLOSED batch. It is the single exit of
	// both the ordinary chunked publish stream and IMPORT_SEALED; the
	// importer passes preserveEpoch=true to keep the original issuer
	// epoch and sequence base instead of allocating new ones. A second
	// import with the same identity is idempotent; a different identity
	// under the same batch id is an immutable conflict.
	ImportClosed(fence Fence, plan SealedPlan, preserveEpoch bool) error

	// PrepareGuard rejects a Prepare/Publish whose sequence is at or
	// below the retired prefix of the fence's run: ALREADY_RETIRED
	// without creating keys.
	PrepareGuard(fence Fence, id BatchID) error

	// RecordPubAck accounts one verified PubAck of a CLOSED batch.
	// Duplicate accounting is safe; a different plan digest under the
	// batch id is a conflict; a batch past the release barrier (not
	// publishable) rejects the accounting so a late callback cannot
	// resurrect state.
	RecordPubAck(fence Fence, id BatchID, planDigest proto.Hash32, key FrameKey) error

	// MarkNATSACKed atomically syncs NATS_ACKED with the manifest and
	// plan identity once every expected frame key carries a PubAck, and
	// returns the durable IngestAck. It is idempotent.
	MarkNATSACKed(fence Fence, identity BatchIdentity) (IngestAck, error)

	// BeginRelease syncs RELEASING with a zero cleanup cursor. It
	// requires NATS_ACKED and the sequence order rule (exactly the next
	// batch of the run) and is idempotent while RELEASING.
	BeginRelease(fence Fence, id BatchID) error

	// CleanupStep deletes the next bounded chunk of a RELEASING batch and
	// syncs the cursor. more reports whether chunks remain; a restart
	// resumes from the durable cursor.
	CleanupStep(fence Fence, id BatchID) (more bool, err error)

	// FinishRelease is the final atomic commit of a release: it advances
	// the retired prefix to the batch sequence, deletes the RELEASING
	// marker and the batch record. Only then does the caller answer
	// ReleaseAck.
	FinishRelease(fence Fence, id BatchID) (ReleaseAck, error)

	// AdoptBaseline installs the producer's durable retired prefix as the
	// baseline of a recovered store. It is admitted only from the current
	// fenced owner, only forward (a lower producer prefix never rolls
	// the gateway back), and never over an unconfirmed local batch.
	AdoptBaseline(fence Fence, producer, run proto.ID16, prefix uint64) error

	// RecordRecoveryRelease accepts a producer's durable ACKED/outbox
	// assertion for exactly the next sequence of the run, syncs it,
	// advances the prefix and returns the ReleaseAck — without the
	// payload, which the producer may already have deleted. A local
	// record for the sequence, a sequence above the prefix plus one or a
	// stale fence is an error and advances nothing.
	RecordRecoveryRelease(fence Fence, ack IngestAck) (ReleaseAck, error)
}
