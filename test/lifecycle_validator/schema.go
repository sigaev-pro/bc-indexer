// The schema half of the lifecycle validator: lifecycle events as schema
// messages. A SchemaEvent wraps one protocol/proto Envelope, the READY event
// descriptor v2 of its record and the canonical TransportFrame bytes that
// carry the record's fragments on the wire; Validate composes protocol/
// proto's envelope validation (family and kind consistency with the closed
// payload variant, fragment continuity, digest verification) and
// cross-checks the claimed descriptor and frames against the descriptors
// recomputed from the payload. The SchemaAutomaton feeds each verified
// SemanticDescriptor into the semantic-half state machine and additionally
// enforces the stream rules this half owns: per-activation event ordinal
// continuity and the writer validity of post-terminal StatusChanged events
// (README, open questions 3 and 4, resolved here).
package lifecycle

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// Named errors of the schema half. Every rejection carries one of these
// sentinels (or a protocol/proto sentinel passed through from the envelope
// validation) and a specific reason, so callers classify failures without
// matching free-form text.
var (
	// ErrOrdinalContinuity rejects an event whose event ordinal breaks the
	// per-activation continuity of the consumed stream: the ordinals of one
	// activation's events must arrive at consecutive steps, with no gap and
	// no duplicate.
	ErrOrdinalContinuity = errors.New("lifecycle: event ordinal continuity broken")

	// ErrWriterInvalid rejects an event the message contract forbids the
	// writer to emit (open question 3, resolved): a StatusChanged created
	// after its activation was terminalized. Its revision is above the
	// recorded Removed revision, and the revisions of one activation come
	// from one monotonic counter, so it cannot have been written before the
	// removal (MESSAGE_CONTRACT section 5: the writer creates no new Status
	// for a terminal activation).
	ErrWriterInvalid = errors.New("lifecycle: writer-invalid lifecycle event")

	// ErrDescriptorMismatch rejects a READY event descriptor that does not
	// equal the descriptors recomputed from the envelope payload.
	ErrDescriptorMismatch = errors.New("lifecycle: event descriptor does not match the envelope")

	// ErrFrameMismatch rejects a transport frame that disagrees with the
	// verified event: wrong form, identity, indices, counts, digests,
	// network, fragment bytes or chain sequence.
	ErrFrameMismatch = errors.New("lifecycle: transport frame does not match the event")
)

// SchemaEvent is one lifecycle event at the schema seam: the proto Envelope
// (the public event id, the network profile, the SemanticDescriptor and the
// payload fragments), the READY EventDescriptor v2 of the record and the
// canonical TransportFrame encodings that carry the record's fragments, one
// COMPLETE frame per fragment in index order. It is a value type: Validate
// works on a copy and never aliases the envelope's pointers into a returned
// value.
type SchemaEvent struct {
	Envelope   proto.Envelope
	Descriptor proto.EventDescriptor
	Frames     [][]byte
}

// NewSchemaEvent assembles one SchemaEvent the way the writer path does: it
// validates the envelope through the proto seam at the given batch ordinal,
// encodes one COMPLETE TransportFrame per payload fragment, and pins the
// shared record identity on every frame — event id, record digest, fragment
// count and digests from the READY descriptors, and the one chain sequence
// of the logical event, sequenceBase + ordinal (one sequence per logical
// event, shared by all its fragments; counters start at 1, so the base is
// 1 or above). It is the fixture path of the tests and the shape the
// delivery layer hands to a consumer.
func NewSchemaEvent(ordinal, sequenceBase uint64, env *proto.Envelope,
	net proto.NetworkIdentity, epoch proto.ID16) (SchemaEvent, error) {
	if sequenceBase == 0 {
		return SchemaEvent{}, fmt.Errorf("lifecycle: sequence base 0, counters start at 1")
	}
	chainSequence, err := addCounters(sequenceBase, ordinal)
	if err != nil {
		return SchemaEvent{}, err
	}
	event, frags, err := proto.ValidateEnvelope(ordinal, env)
	if err != nil {
		return SchemaEvent{}, err
	}
	if err := networkMatches(net, env.Semantic.Network); err != nil {
		return SchemaEvent{}, err
	}
	frames := make([][]byte, 0, len(env.Payload.Fragments))
	for i, fragment := range env.Payload.Fragments {
		frame := &proto.TransportFrame{
			TransportVersion:    proto.TransportVersion,
			Network:             net,
			EventID:             env.EventID,
			RecordDigest:        event.RecordDigest,
			PublicationEpoch:    epoch,
			ChainSequence:       chainSequence,
			Complete:            &proto.CompleteFragment{FragmentBytes: fragment},
			FragmentIndex:       uint64(i),
			FragmentCount:       event.FragmentCount,
			FragmentDigest:      frags[i].FragmentSHA256,
			RecordFormatVersion: proto.RecordFormatVersion,
		}
		encoded, err := frame.Encode()
		if err != nil {
			return SchemaEvent{}, err
		}
		frames = append(frames, encoded)
	}
	return SchemaEvent{Envelope: *env, Descriptor: event, Frames: frames}, nil
}

// Validate verifies one event end to end and returns the verified
// SemanticDescriptor. It composes, in order:
//
//   - protocol/proto's envelope validation: the phase-1 semantic identity,
//     the event id agreement, the closed family/kind/payload-variant
//     triangle admitted by the network profile, the typed payload views,
//     and the READY descriptor build over the payload fragments —
//     record_length, record_digest, the fragment digests and the fragment
//     manifest digest, with the fragment index continuity and the length
//     sum checked by ValidateFragments;
//   - the claimed event descriptor must equal the recomputed one, so a
//     lying manifest descriptor is a named error, never a silent
//     re-labeling;
//   - every frame decodes from its canonical bytes (the pinned v2 wrapper
//     table: versions, ascending tags, the complete/part oneof, bounds) and
//     agrees with the verified event: the COMPLETE form, fragment index
//     continuity 0..N-1, the shared event id, record digest, fragment
//     count, network and chain sequence (base >= 1, so the sequence stays
//     above the ordinal), and each frame's fragment bytes are exactly the
//     envelope's payload fragment and hash to the manifest digest pinned
//     for them.
//
// The same event always validates to the same descriptor.
func (e SchemaEvent) Validate() (semantic.SemanticDescriptor, error) {
	event, frags, err := proto.ValidateEnvelope(e.Descriptor.Ordinal, &e.Envelope)
	if err != nil {
		return semantic.SemanticDescriptor{}, err
	}
	if e.Descriptor != event {
		return semantic.SemanticDescriptor{}, fmt.Errorf("%w: %s",
			ErrDescriptorMismatch, describeDescriptorDiff(e.Descriptor, event))
	}
	if uint64(len(e.Frames)) != event.FragmentCount {
		return semantic.SemanticDescriptor{}, fmt.Errorf("%w: %d frames for fragment_count %d",
			ErrFrameMismatch, len(e.Frames), event.FragmentCount)
	}
	var chainSequence uint64
	for i, encoded := range e.Frames {
		frame, err := proto.DecodeTransportFrame(encoded)
		if err != nil {
			return semantic.SemanticDescriptor{}, err
		}
		if frame.Complete == nil {
			return semantic.SemanticDescriptor{}, fmt.Errorf("%w: frame %d is a PART, this seam carries whole fragments",
				ErrFrameMismatch, i)
		}
		if frame.FragmentIndex != uint64(i) {
			return semantic.SemanticDescriptor{}, fmt.Errorf("%w: frame fragment index %d at position %d breaks continuity",
				ErrFrameMismatch, frame.FragmentIndex, i)
		}
		if frame.FragmentCount != event.FragmentCount {
			return semantic.SemanticDescriptor{}, fmt.Errorf("%w: frame fragment count %d, want %d",
				ErrFrameMismatch, frame.FragmentCount, event.FragmentCount)
		}
		if frame.EventID != e.Envelope.EventID {
			return semantic.SemanticDescriptor{}, fmt.Errorf("%w: frame %d event id differs from the envelope event id",
				ErrFrameMismatch, i)
		}
		if frame.RecordDigest != event.RecordDigest {
			return semantic.SemanticDescriptor{}, fmt.Errorf("%w: frame %d record digest differs from the verified record digest",
				ErrFrameMismatch, i)
		}
		if err := networkMatches(frame.Network, e.Envelope.Semantic.Network); err != nil {
			return semantic.SemanticDescriptor{}, err
		}
		if i == 0 {
			// Counters start at 1 and chain_sequence = sequence_base + ordinal,
			// so the shared sequence of a real event stays strictly above the
			// batch ordinal.
			if frame.ChainSequence <= event.Ordinal {
				return semantic.SemanticDescriptor{}, fmt.Errorf("%w: chain sequence %d is not above the ordinal %d of a sequence base >= 1",
					ErrFrameMismatch, frame.ChainSequence, event.Ordinal)
			}
			chainSequence = frame.ChainSequence
		} else if frame.ChainSequence != chainSequence {
			return semantic.SemanticDescriptor{}, fmt.Errorf("%w: frame %d chain sequence %d differs from %d; one logical event carries one sequence",
				ErrFrameMismatch, i, frame.ChainSequence, chainSequence)
		}
		fragment := frame.Complete.FragmentBytes
		var digest proto.Hash32
		sum := sha256.Sum256(fragment)
		copy(digest[:], sum[:])
		if digest != frags[i].FragmentSHA256 {
			return semantic.SemanticDescriptor{}, fmt.Errorf("%w: frame %d fragment bytes do not hash to the manifest fragment digest",
				ErrFrameMismatch, i)
		}
		if !bytes.Equal(fragment, e.Envelope.Payload.Fragments[i]) {
			return semantic.SemanticDescriptor{}, fmt.Errorf("%w: frame %d carries other bytes than the envelope payload fragment",
				ErrFrameMismatch, i)
		}
	}
	return cloneDescriptor(e.Envelope.Semantic), nil
}

// SchemaAutomaton folds schema-level lifecycle events into activation
// records. It composes the semantic-half Automaton: every event is validated
// at the proto seam first, then the stream rules this half owns are
// enforced, then the verified descriptor is folded with the proto
// record_digest as the content discriminator. Like the inner automaton it is
// deterministic and single-threaded, and a rejected event changes nothing —
// not the records and not the ordinal ledger.
type SchemaAutomaton struct {
	inner *Automaton
	// ledger holds the next expected event ordinal per activation key.
	ledger map[ActivationKey]uint64
}

// NewSchema returns an empty schema automaton.
func NewSchema() *SchemaAutomaton {
	return &SchemaAutomaton{inner: New(), ledger: make(map[ActivationKey]uint64)}
}

// Inner returns the semantic-half automaton this one composes.
func (s *SchemaAutomaton) Inner() *Automaton { return s.inner }

// Len returns the number of activation records.
func (s *SchemaAutomaton) Len() int { return s.inner.Len() }

// Record returns a copy of the record of one activation key.
func (s *SchemaAutomaton) Record(key ActivationKey) (Record, bool) { return s.inner.Record(key) }

// Records returns deep copies of all activation records.
func (s *SchemaAutomaton) Records() map[ActivationKey]Record { return s.inner.Records() }

// Apply validates one schema event and folds it into the record state
// machine, returning the outcome of the fold. The checks run in a fixed
// order; a rejection is an error, never an outcome, and leaves the
// automaton untouched:
//
//  1. schema validation of the event (proto envelope validation plus the
//     descriptor and frame agreement); proto and schema rejections pass
//     through with their named sentinels;
//  2. writer validity (open question 3, resolved): a StatusChanged of a
//     terminal record whose revision is above the recorded Removed
//     revision was created after the removal and is rejected as
//     ErrWriterInvalid instead of being stale-recorded;
//  3. ordinal continuity: the ordinals of one activation's events arrive
//     at consecutive steps — no gaps, no duplicates per activation;
//  4. the fold: the inner automaton applies the descriptor with the
//     record_digest of the verified descriptor as the content
//     discriminator (open question 4, resolved).
func (s *SchemaAutomaton) Apply(e SchemaEvent) (Result, error) {
	d, err := e.Validate()
	if err != nil {
		return Result{}, err
	}
	key := KeyOf(d)
	if rec, ok := s.inner.Record(key); ok && writerInvalidStatus(rec, d) {
		return Result{}, fmt.Errorf("%w: status revision %d of activation %d/%d is above the removed revision %d: the event was created after the terminal event",
			ErrWriterInvalid, d.Revision, key.Activation.DecisionSequence,
			key.Activation.EntityOrdinal, rec.RemovedRevision)
	}
	if err := s.stepOrdinal(key, e.Descriptor.Ordinal); err != nil {
		return Result{}, err
	}
	return s.inner.Apply(d, Digest(e.Descriptor.RecordDigest))
}

// stepOrdinal advances the per-activation ordinal ledger. The first event
// of an activation fixes its ordinal base; every later event of the same
// activation must carry exactly the previous ordinal plus one. Ordinals are
// batch-scoped (they restart at 0 in every public batch), so cross-batch
// global continuity is not well-defined; the stream rule this seam can
// enforce is that one activation's events arrive at consecutive ordinals
// with no gap and no duplicate.
func (s *SchemaAutomaton) stepOrdinal(key ActivationKey, ordinal uint64) error {
	want, seen := s.ledger[key]
	if !seen {
		next, err := addCounters(ordinal, 1)
		if err != nil {
			return fmt.Errorf("%w: ordinal %d of activation %d/%d has no successor",
				ErrOrdinalContinuity, ordinal, key.Activation.DecisionSequence,
				key.Activation.EntityOrdinal)
		}
		s.ledger[key] = next
		return nil
	}
	if ordinal != want {
		if ordinal < want {
			return fmt.Errorf("%w: duplicate ordinal %d of activation %d/%d, want %d",
				ErrOrdinalContinuity, ordinal, key.Activation.DecisionSequence,
				key.Activation.EntityOrdinal, want)
		}
		return fmt.Errorf("%w: ordinal %d of activation %d/%d leaves a gap, want %d",
			ErrOrdinalContinuity, ordinal, key.Activation.DecisionSequence,
			key.Activation.EntityOrdinal, want)
	}
	next, err := addCounters(want, 1)
	if err != nil {
		return fmt.Errorf("%w: ordinal %d of activation %d/%d has no successor",
			ErrOrdinalContinuity, want, key.Activation.DecisionSequence,
			key.Activation.EntityOrdinal)
	}
	s.ledger[key] = next
	return nil
}

// writerInvalidStatus reports whether the event is the writer-invalid
// post-terminal StatusChanged of open question 3: the record is terminal
// and the status revision is above the revision that terminalized it, so
// the status was created after the removal (one monotonic activation
// counter). A status at or below the removed revision was written before
// the removal and is legitimate late evidence; a status at exactly the
// removed revision is the ordinary revision-clash Conflict. A post-terminal
// Removed or a late Applied keeps its semantic-half outcome (Stale
// evidence, origin merge): the contract names only new Status events as
// forbidden for a terminal activation.
func writerInvalidStatus(rec Record, d semantic.SemanticDescriptor) bool {
	return rec.State == StateRemoved && d.Kind == semantic.KindStatus && d.Revision > rec.RemovedRevision
}

// networkMatches checks a frame network against the semantic network of the
// descriptor: the one registered EVM family, same chain id and genesis
// hash.
func networkMatches(net proto.NetworkIdentity, want semantic.Network) error {
	if err := net.Validate(); err != nil {
		return err
	}
	var chain, genesis [32]byte
	copy(chain[:], want.ChainID[:])
	copy(genesis[:], want.GenesisHash[:])
	if net.ChainID != chain || net.GenesisHash != genesis {
		return fmt.Errorf("%w: frame network differs from the descriptor network", ErrFrameMismatch)
	}
	return nil
}

// describeDescriptorDiff names the first field, in the descriptor encoding
// order, in which the claimed descriptor differs from the recomputed one.
func describeDescriptorDiff(claimed, computed proto.EventDescriptor) string {
	switch {
	case claimed.Ordinal != computed.Ordinal:
		return fmt.Sprintf("ordinal %d, want %d", claimed.Ordinal, computed.Ordinal)
	case claimed.EventID != computed.EventID:
		return "event_id differs from the envelope event id"
	case claimed.Family != computed.Family:
		return fmt.Sprintf("family %s, want %s", claimed.Family, computed.Family)
	case claimed.Kind != computed.Kind:
		return fmt.Sprintf("kind %s, want %s", claimed.Kind, computed.Kind)
	case claimed.RecordLength != computed.RecordLength:
		return fmt.Sprintf("record_length %d, want %d", claimed.RecordLength, computed.RecordLength)
	case claimed.RecordDigest != computed.RecordDigest:
		return "record_digest differs from the payload fragments"
	case claimed.FragmentCount != computed.FragmentCount:
		return fmt.Sprintf("fragment_count %d, want %d", claimed.FragmentCount, computed.FragmentCount)
	case claimed.FragmentManifestDigest != computed.FragmentManifestDigest:
		return "fragment_manifest_digest differs from the payload fragments"
	}
	return "no difference"
}

// addCounters adds two unsigned counters and rejects the wrap the wire
// contract forbids.
func addCounters(a, b uint64) (uint64, error) {
	sum := a + b
	if sum < a {
		return 0, fmt.Errorf("lifecycle: counter overflow at %d + %d", a, b)
	}
	return sum, nil
}
