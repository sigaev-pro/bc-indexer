// Package journal is the gateway delivery journal of E03-01.gateway_store:
// the state machine PREPARING -> PREPARED (reservation) -> RECEIVING ->
// CLOSED -> NATS_ACKED -> RELEASING -> RETIRED of DELIVERY_STATE_MACHINE,
// on the shared storage seam.
//
// Rules this journal holds (references are DELIVERY_STATE_MACHINE and
// CONTROL_WIRE_CONTRACT unless noted):
//
//   - A reservation is allocated exactly once per (batch, manifest digest):
//     FinalizePrepare reserves a sequence range and returns the immutable
//     reservation seed bytes. A retry with the same batch and digest returns
//     the saved seed byte-for-byte and never allocates a new sequence; a
//     different digest after the reservation is an immutable conflict.
//   - The reservation seed is the exact binary v2 layout of
//     CONTROL_WIRE_CONTRACT ("READY manifest and reservation"); the gateway
//     store_epoch is the issuer for normal prepares. In IMPORT_SEALED
//     recovery the producer supplies the original seed: no sequence or
//     reservation id is allocated, and the original issuer epoch is
//     preserved.
//   - Chunk streams are offset-addressed and idempotent: the same bytes at
//     the same offset return the same next offset, a conflicting rewrite is
//     an immutable conflict, a gap reports the expected cursor, and every
//     data chunk is at most MaxDataChunkBytes (64 KiB). A manifest or frame
//     stream only closes after all contiguous chunks are verified.
//   - CLOSED is the only publishable state: FinalizePublish verifies the
//     layout manifest digest over the declared entries and the completeness
//     of every transport frame before the atomic CLOSED commit. Publication
//     side (PubAck accounting) only runs on CLOSED batches.
//   - Release is ordered per producer/run: retired_prefix advances exactly
//     one batch at a time, Prepare/Publish for a retired seq answers
//     ALREADY_RETIRED without creating keys, and the RELEASING marker
//     survives bounded cleanup until its final atomic commit.
//   - One serialized writer: every mutating call runs under the journal
//     mutex; sequence and reservation counters are checked for overflow
//     before commit and never reuse values.
//
// The journal performs no network I/O and imports no database driver; the
// publisher (E03-03) and the ingest RPC boundary (E03-02) call into it.
package journal

import (
	"crypto/rand"
	"encoding/binary"
	"sync"

	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
)

// Wire versions pinned into the reservation seed (CONTROL_WIRE_CONTRACT:
// transport/layout/header/manifest = 2, record_format = 1).
const (
	TransportVersion = 2
	LayoutVersion    = 2
	HeaderVersion    = 2
	RecordFormat     = 1
	ManifestVersion  = 2
)

// MaxDataChunkBytes is the largest payload of one control/chunk RPC
// (CONTROL_WIRE_CONTRACT: data chunk <= 64 KiB; the whole RPC including its
// wrapper is <= 256 KiB, enforced by the ingest server).
const MaxDataChunkBytes = 64 << 10

// DefaultFrameLimit is the default transport.max_frame_bytes.
const DefaultFrameLimit = 2 << 20

// State is a batch's durable state on the gateway.
type State uint8

const (
	// StatePreparing: manifest descriptor chunks arrive.
	StatePreparing State = iota + 1
	// StatePrepared: reservation seed and sequence range committed.
	StatePrepared
	// StateReceiving: transport frames arrive under an accepted plan.
	StateReceiving
	// StateClosed: manifest completeness verified; publication allowed.
	StateClosed
	// StateNatsAcked: every PubAck of the closed batch recorded.
	StateNatsAcked
	// StateReleasing: bounded payload cleanup under a durable cursor.
	StateReleasing
	// StateRetired: retired_prefix advanced; payload absent.
	StateRetired
)

func (s State) String() string {
	switch s {
	case StatePreparing:
		return "PREPARING"
	case StatePrepared:
		return "PREPARED"
	case StateReceiving:
		return "RECEIVING"
	case StateClosed:
		return "CLOSED"
	case StateNatsAcked:
		return "NATS_ACKED"
	case StateReleasing:
		return "RELEASING"
	case StateRetired:
		return "RETIRED"
	default:
		return "UNKNOWN"
	}
}

// BatchID is batch_id = producer_id16 || run_id16 || batch_seq
// (CONTROL_WIRE_CONTRACT "Базовые кодирования и идентичности").
type BatchID struct {
	ProducerID [16]byte
	RunID      [16]byte
	Seq        uint64
}

// Bytes returns the 40-byte big endian form used inside keys and seeds.
func (id BatchID) Bytes() [40]byte {
	var b [40]byte
	copy(b[:16], id.ProducerID[:])
	copy(b[16:32], id.RunID[:])
	copy(b[32:], u64be(id.Seq))
	return b
}

// Journal is one gateway store over a storage seam.
type Journal struct {
	mu     sync.Mutex
	st     storage.Storage
	epoch  [16]byte
	bnd    [32]byte
	frames uint64
	ct     Counters
	// Active destination binding. bnd is its digest (also the digest every
	// reservation and CLOSED marker records); gen is its generation. The
	// content identity is known (hasBinding) once a switch committed it or
	// Open supplied it through Options.Binding.
	gen        uint64
	dstID      [16]byte
	chainID    [32]byte
	genesis    [32]byte
	hasBinding bool
}

// Counters holds the monotonic allocation counters; values start at 1.
type Counters struct {
	SequenceNext    uint64 // next chain_sequence to reserve
	ReservationNext uint64 // next reservation id
}

// Options configures Open.
type Options struct {
	// BindingDigest is the destination binding digest this gateway admits;
	// both processes save it before any new commitment.
	BindingDigest [32]byte
	// FrameLimit is transport.max_frame_bytes recorded in reservation seeds;
	// zero selects DefaultFrameLimit.
	FrameLimit uint64
	// Binding is the content identity of the active destination binding
	// (destination id, generation and network) of a store that never
	// committed a switch. It lets PrepareSwitch check that a desired
	// descriptor continues the same destination line and network. When the
	// store already holds a committed binding (meta/destination_binding),
	// the durable record wins over this option and over BindingDigest.
	// Without either, a switch is refused: its continuity checks need the
	// active binding identity.
	Binding *Binding
}

// Open loads or initializes the journal. On an empty storage it creates the
// persistent store_epoch from cryptographic randomness and starts counters
// at 1.
func Open(st storage.Storage, opt Options) (*Journal, error) {
	j := &Journal{st: st, bnd: opt.BindingDigest, frames: opt.FrameLimit}
	if j.frames == 0 {
		j.frames = DefaultFrameLimit
	}
	// Binding generations start at 1.
	j.gen = 1
	if raw, err := st.Get([]byte(keyBinding)); err == nil {
		ab, derr := decodeBinding(raw)
		if derr != nil {
			return nil, derr
		}
		// The durable active binding is the only authority: a descriptor
		// file or a stale option never activates or reverts a binding.
		j.applyBinding(ab)
	} else if err != storage.ErrNotFound {
		return nil, err
	} else if opt.Binding != nil {
		if opt.Binding.DestinationID == ([16]byte{}) || opt.Binding.Generation == 0 ||
			opt.Binding.DescriptorDigest != opt.BindingDigest {
			return nil, ErrInvalidBinding
		}
		j.applyBinding(*opt.Binding)
	}
	if raw, err := st.Get([]byte(keyEpoch)); err == nil {
		if len(raw) != 16 {
			return nil, errCorrupt
		}
		copy(j.epoch[:], raw)
	} else if err == storage.ErrNotFound {
		var epoch [16]byte
		if _, err := rand.Read(epoch[:]); err != nil {
			return nil, err
		}
		if err := st.Put([]byte(keyEpoch), epoch[:]); err != nil {
			return nil, err
		}
		j.epoch = epoch
	} else {
		return nil, err
	}
	if raw, err := st.Get([]byte(keyCounters)); err == nil {
		if j.ct, err = decodeCounters(raw); err != nil {
			return nil, err
		}
	} else if err == storage.ErrNotFound {
		j.ct = Counters{SequenceNext: 1, ReservationNext: 1}
		if err := st.Put([]byte(keyCounters), encodeCounters(j.ct)); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	return j, nil
}

// StoreEpoch returns the persistent gateway store identity; it is the issuer
// epoch of every reservation this store allocates.
func (j *Journal) StoreEpoch() [16]byte {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.epoch
}

// BindingDigest returns the configured destination binding digest.
func (j *Journal) BindingDigest() [32]byte {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.bnd
}

// RetiredPrefix returns the retired frontier for one producer/run.
func (j *Journal) RetiredPrefix(id BatchID) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	raw, err := j.st.Get(retiredKey(id))
	if err == storage.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(raw) != 8 {
		return 0, errCorrupt
	}
	return beU64(raw), nil
}

func (j *Journal) readRetired(id BatchID) uint64 {
	raw, err := j.st.Get(retiredKey(id))
	if err != nil || len(raw) != 8 {
		return 0
	}
	return beU64(raw)
}

func (j *Journal) readState(id BatchID) (State, error) {
	raw, err := j.st.Get(batchKey(id, "/state"))
	if err == storage.ErrNotFound {
		return 0, ErrBatchNotFound
	}
	if err != nil {
		return 0, err
	}
	if len(raw) != 1 || raw[0] < uint8(StatePreparing) || raw[0] > uint8(StateRetired) {
		return 0, errCorrupt
	}
	return State(raw[0]), nil
}

func (j *Journal) putState(b *storage.Batch, id BatchID, s State) {
	b.Put(batchKey(id, "/state"), []byte{uint8(s)})
}

// chunkCover returns the contiguous covered length of an offset-addressed
// chunk stream and the visited chunk count; a hole is an error.
func chunkCover(st storage.Storage, prefix []byte) (uint64, int, error) {
	lo, hi := keyRange(prefix)
	var cover uint64
	var n int
	err := st.Iterate(lo, hi, func(k, v []byte) error {
		off := beU64(k[len(prefix):])
		if off != cover {
			return ErrOffsetMismatch
		}
		cover += uint64(len(v))
		n++
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return cover, n, nil
}

// hashChunks feeds a chunk stream in offset order into a digest builder.
func hashChunks(st storage.Storage, prefix []byte, write func([]byte)) error {
	lo, hi := keyRange(prefix)
	var cover uint64
	return st.Iterate(lo, hi, func(k, v []byte) error {
		off := beU64(k[len(prefix):])
		if off != cover {
			return ErrOffsetMismatch
		}
		write(v)
		cover += uint64(len(v))
		return nil
	})
}

func u64be(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

func beU64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

func u32be(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

func beU32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
