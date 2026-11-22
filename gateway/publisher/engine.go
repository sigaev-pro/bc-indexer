// The publishing engine: one manifest/plan/destination from a CLOSED
// batch to durable NATS_ACKED and the IngestAck, with per-frame PubAck
// accounting, partial-set refusal, immutable retry bytes and the
// publisher-generation fence against the Release and SealGeneration
// barriers.
package publisher

import (
	"errors"
	"fmt"
	"sync"

	"github.com/sigaev-pro/bc-indexer/core/delivery"
	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
)

// ErrIncomplete marks a publish run that ended before every frame carried
// a PubAck: the batch stays not-NATS_ACKED and the run is retried with
// identical bytes. It wraps the causing error.
type ErrIncomplete struct {
	Missing int
	Cause   error
}

func (e *ErrIncomplete) Error() string {
	return fmt.Sprintf("publisher: %d frames unacked: %v", e.Missing, e.Cause)
}

// Unwrap returns the causing error.
func (e *ErrIncomplete) Unwrap() error { return e.Cause }

// ErrBindingMismatch marks a batch whose binding digest differs from the
// verified target: acknowledgments of different destinations never
// compose one NATS_ACKED.
var ErrBindingMismatch = errors.New("publisher: batch binding differs from the verified target")

// ErrRouteOutsideTarget marks a frame whose subject the verified target
// does not admit.
var ErrRouteOutsideTarget = errors.New("publisher: frame subject outside the verified target")

// Frame is one ready-to-publish transport frame with its immutable
// identity: the accounting key, the concrete subject, the exact NATS
// message id, the exact header bytes and the canonical wrapper payload.
// The engine treats all five as immutable across attempts.
type Frame struct {
	Key       delivery.FrameKey
	Subject   string
	MessageID string
	Header    []byte
	Payload   []byte
}

// Batch is one CLOSED batch ready to publish: its immutable identity and
// its frames in layout order.
type Batch struct {
	Identity delivery.BatchIdentity
	Frames   []Frame
}

// Engine publishes batches over the Conn seam and accounts every PubAck in
// the gateway store. One engine is one internal publisher worker with its
// own publisher generation; the Barrier of a Release or SealGeneration
// invalidates in-flight work of the previous generation.
type Engine struct {
	conn  Conn
	store delivery.GatewayStore

	mu  sync.Mutex
	gen uint64
}

// New returns an engine over the connection and store.
func New(conn Conn, store delivery.GatewayStore) (*Engine, error) {
	if conn == nil {
		return nil, fmt.Errorf("publisher: connection is nil")
	}
	if store == nil {
		return nil, fmt.Errorf("publisher: store is nil")
	}
	return &Engine{conn: conn, store: store}, nil
}

// Barrier invalidates the current publisher generation. A PubAck that
// arrives for work of an older generation creates no state: the callback
// is bound to batch, plan digest and generation (DELIVERY_STATE_MACHINE,
// "Fencing").
func (e *Engine) Barrier() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gen++
	return e.gen
}

// generation returns the current publisher generation.
func (e *Engine) generation() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.gen
}

// Publish drives one batch to a durable NATS_ACKED and returns the
// IngestAck. The run:
//
//   - admits only a CLOSED (or already NATS_ACKED, idempotently) batch of
//     the fence's run under the same plan identity;
//   - verifies the batch binding digest against the verified target and
//     every frame subject against the target route;
//   - publishes only frames whose key carries no PubAck yet, with the
//     exact saved id, bytes and subject;
//   - verifies every PubAck stream against the plan stream;
//   - accounts each PubAck durably under the captured publisher
//     generation, so a barrier mid-run fences the rest;
//   - syncs NATS_ACKED only when every expected frame key of the store
//     record is acked, and answers the IngestAck.
//
// A transient failure mid-run returns *ErrIncomplete; a config failure
// returns the typed error; neither mutates the batch identity.
func (e *Engine) Publish(fence delivery.Fence, target destination.VerifiedTarget, batch Batch) (delivery.IngestAck, error) {
	if fence.Kind != delivery.SessionNormal {
		return delivery.IngestAck{}, fmt.Errorf("%w: a MAINTENANCE session never publishes",
			delivery.ErrFenced)
	}
	if err := batch.Identity.Validate(); err != nil {
		return delivery.IngestAck{}, err
	}
	if err := target.Binding.Validate(); err != nil {
		return delivery.IngestAck{}, fmt.Errorf("publisher: target binding: %w", err)
	}
	if batch.Identity.BindingDigest != target.Binding.DescriptorDigest {
		return delivery.IngestAck{}, fmt.Errorf("%w: batch %d carries %x, target carries %x",
			ErrBindingMismatch, batch.Identity.Batch.Seq,
			batch.Identity.BindingDigest, target.Binding.DescriptorDigest)
	}
	allowed := make(map[string]struct{}, len(target.Subjects))
	for _, s := range target.Subjects {
		allowed[s] = struct{}{}
	}
	for i := range batch.Frames {
		if batch.Frames[i].Subject == "" || batch.Frames[i].MessageID == "" ||
			len(batch.Frames[i].Header) == 0 || len(batch.Frames[i].Payload) == 0 {
			return delivery.IngestAck{}, fmt.Errorf("publisher: frame %d of batch %d lacks its immutable identity",
				i, batch.Identity.Batch.Seq)
		}
		if _, ok := allowed[batch.Frames[i].Subject]; !ok {
			return delivery.IngestAck{}, fmt.Errorf("%w: %q of frame %s",
				ErrRouteOutsideTarget, batch.Frames[i].Subject, batch.Frames[i].Key)
		}
	}
	// ALREADY_RETIRED guard first: a retired sequence creates no keys.
	if err := e.store.PrepareGuard(fence, batch.Identity.Batch); err != nil {
		return delivery.IngestAck{}, err
	}
	gen := e.generation()
	for i := range batch.Frames {
		frame := &batch.Frames[i]
		record, ok := e.store.Batch(fence, batch.Identity.Batch)
		if !ok {
			return delivery.IngestAck{}, fmt.Errorf("%w: batch %d is not registered",
				delivery.ErrNotFound, batch.Identity.Batch.Seq)
		}
		if record.Identity.PlanDigest != batch.Identity.PlanDigest {
			return delivery.IngestAck{}, fmt.Errorf("%w: batch %d is registered under another plan",
				delivery.ErrImmutableConflict, batch.Identity.Batch.Seq)
		}
		if record.State != delivery.StateClosed && record.State != delivery.StateNATSACKed {
			return delivery.IngestAck{}, fmt.Errorf("%w: batch %d is %s, only CLOSED publishes",
				delivery.ErrState, batch.Identity.Batch.Seq, record.State)
		}
		if _, acked := record.Acked[frame.Key]; acked {
			continue // a retry skips already-acked frames
		}
		ack, err := e.conn.Publish(frame.Subject, frame.Header, frame.Payload)
		if e.generation() != gen {
			// A Release or SealGeneration barrier passed while the frame
			// was in flight: the answer creates no state.
			return delivery.IngestAck{}, fmt.Errorf("%w: publisher generation barrier passed mid-run",
				delivery.ErrFenced)
		}
		if err != nil {
			return delivery.IngestAck{}, &ErrIncomplete{Missing: e.missingKeys(record, batch), Cause: err}
		}
		if ack.Stream != target.Binding.Stream {
			return delivery.IngestAck{}, fmt.Errorf("%w: PubAck names %q, plan names %q",
				ErrUnexpectedStream, ack.Stream, target.Binding.Stream)
		}
		if err := e.store.RecordPubAck(fence, batch.Identity.Batch, batch.Identity.PlanDigest, frame.Key); err != nil {
			return delivery.IngestAck{}, err
		}
	}
	// Every frame accounted: sync NATS_ACKED with the manifest and plan
	// identity. A partial set still fails here with ErrState.
	return e.store.MarkNATSACKed(fence, batch.Identity)
}

// missingKeys counts the frames of the batch without a PubAck.
func (e *Engine) missingKeys(record delivery.GatewayRecord, batch Batch) int {
	missing := 0
	for i := range batch.Frames {
		if _, ok := record.Acked[batch.Frames[i].Key]; !ok {
			missing++
		}
	}
	return missing
}

// FramesForPlan maps one layout plan and the exact wrapper bytes of its
// entries to publisher frames, deriving each frame's accounting key from
// the layout entry and part index. The caller owns the bytes; this helper
// only pairs them with keys, so the immutable bytes never pass through a
// transformation here.
func FramesForPlan(identity delivery.BatchIdentity, entries []proto.LayoutManifestEntry, emitted func(i int) (subject, msgID string, header, payload []byte)) ([]Frame, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	frames := make([]Frame, 0, len(entries))
	for i, entry := range entries {
		subject, msgID, header, payload := emitted(i)
		key := delivery.FrameKey{Ordinal: entry.Ordinal, Fragment: entry.FragmentIndex,
			Part: 0, Complete: entry.Form == proto.FormComplete}
		if entry.Form == proto.FormPart {
			key.Part = partIndexOf(i, entries)
		}
		frames = append(frames, Frame{Key: key, Subject: subject, MessageID: msgID,
			Header: header, Payload: payload})
	}
	return frames, nil
}

// partIndexOf returns the part index of entry i: entries of one (ordinal,
// fragment) pair are consecutive parts from 0 when the form is PART.
func partIndexOf(i int, entries []proto.LayoutManifestEntry) uint64 {
	part := uint64(0)
	for j := i - 1; j >= 0; j-- {
		if entries[j].Ordinal == entries[i].Ordinal && entries[j].FragmentIndex == entries[i].FragmentIndex {
			part++
			continue
		}
		break
	}
	return part
}
