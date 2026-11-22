package producer

import (
	"encoding/binary"
)

// Key layout of the producer journal. The prefixes are the logical contract
// of DELIVERY_STATE_MACHINE ("Identity, режимы и atomicity"); this module
// fixes their physical encoding, which that document assigns to E03. All
// integers are unsigned big endian.
//
//	meta/identity                Identity record
//	meta/mode                    ModeState record
//	meta/counters                Counters record
//	meta/destination_binding     32-byte binding digest
//	meta/retired_prefix          u64
//	decision/<decision u64>      u64 batch_seq (decision replay index)
//	batch/<seq>/state            u8 state marker
//	batch/<seq>/intent           Intent record
//	batch/<seq>/ready            Ready record (closing READY marker)
//	batch/<seq>/checkpoint       opaque capture/lifecycle checkpoint bytes
//	batch/<seq>/attempt/<gen>/data/<offset>       attempt payload chunks
//	batch/<seq>/attempt/<gen>/manifest/<offset>   manifest byte chunks
//	plan/<seq>/current           PlanCurrent record
//	plan/<seq>/gen/<gen>/header  PlanHeader record (seed + layout digest)
//	plan/<seq>/gen/<gen>/chunks/<offset>          plan payload chunks
//	plan/<seq>/sealed            Sealed record (closing SEALED marker)
//	cleanup/<seq>                Cleanup record
//	release_outbox/<seq>         IngestAck record
const (
	keyIdentity    = "meta/identity"
	keyMode        = "meta/mode"
	keyCounters    = "meta/counters"
	keyBinding     = "meta/destination_binding"
	keyRetired     = "meta/retired_prefix"
	keyDecision    = "decision/"
	keyBatchPrefix = "batch/"
	keyPlanPrefix  = "plan/"
	keyCleanup     = "cleanup/"
	keyOutbox      = "release_outbox/"
)

func u64be(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

func beU64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

func batchKey(seq uint64, suffix string) []byte {
	return append(append([]byte(keyBatchPrefix), u64be(seq)...), suffix...)
}

func attemptKey(seq, generation uint64, kind string) []byte {
	k := batchKey(seq, "attempt/")
	k = append(k, u64be(generation)...)
	k = append(k, '/')
	k = append(k, kind...)
	return append(k, '/')
}

func planGenKey(seq, generation uint64, suffix string) []byte {
	k := append([]byte(keyPlanPrefix), u64be(seq)...)
	k = append(k, "/gen/"...)
	k = append(k, u64be(generation)...)
	if suffix != "" {
		k = append(k, '/')
		k = append(k, suffix...)
	}
	return k
}

func decisionKey(decision uint64) []byte {
	return append([]byte(keyDecision), u64be(decision)...)
}

// keyRange returns the [lo, hi) bounds covering every key with the given
// prefix (hi is the prefix with its last byte increased by one; callers use
// prefixes that do not end with 0xFF).
func keyRange(prefix []byte) (lo, hi []byte) {
	hi = append([]byte(nil), prefix...)
	i := len(hi) - 1
	for i >= 0 && hi[i] == 0xFF {
		i--
	}
	if i < 0 {
		return prefix, nil // prefix is all 0xFF: open-ended range
	}
	hi[i]++
	return prefix, hi[:len(prefix)]
}
