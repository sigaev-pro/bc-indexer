package journal

// Key layout of the gateway journal. The prefixes are the logical contract
// of DELIVERY_STATE_MACHINE; this module fixes their physical encoding.
// <id> is the 40-byte batch_id form, <o>/<f>/<fi> are u64 big endian event
// ordinal, fragment index and frame index.
//
//	meta/store_epoch                      16 bytes
//	meta/counters                         Counters record
//	retired_prefix/<producer16><run16>    u64
//	batch/<id>/state                      u8 state marker
//	batch/<id>/prepare_meta               PrepareMeta record
//	batch/<id>/prepare/<offset>           manifest descriptor chunks
//	batch/<id>/reservation                Reservation record
//	batch/<id>/publish                    Publish record
//	batch/<id>/layout/<o>/<f>             LayoutEntry record
//	batch/<id>/framehead/<o>/<f>/<fi>     u64 frame total bytes
//	batch/<id>/frames/<o>/<f>/<fi>/<off>  transport frame chunks
//	batch/<id>/closed                     Closed record
//	batch/<id>/ackset/<o>/<f>/<fi>        presence = frame PubAck recorded
//	batch/<id>/nats_acked                 NatsAcked record
//	batch/<id>/releasing                  Releasing record
const (
	keyEpoch    = "meta/store_epoch"
	keyCounters = "meta/counters"
	keyRetired  = "retired_prefix/"
	keyBatch    = "batch/"
)

func retiredKey(id BatchID) []byte {
	k := append([]byte(keyRetired), id.ProducerID[:]...)
	return append(k, id.RunID[:]...)
}

func batchKey(id BatchID, suffix string) []byte {
	b := id.Bytes()
	k := append([]byte(keyBatch), b[:]...)
	return append(k, suffix...)
}

func layoutKey(id BatchID, ordinal, fragment uint64) []byte {
	k := batchKey(id, "layout/")
	k = append(k, u64be(ordinal)...)
	return append(k, u64be(fragment)...)
}

func frameKeyPrefix(id BatchID, ordinal, fragment, frame uint64) []byte {
	k := batchKey(id, "frames/")
	k = append(k, u64be(ordinal)...)
	k = append(k, '/')
	k = append(k, u64be(fragment)...)
	k = append(k, '/')
	k = append(k, u64be(frame)...)
	return append(k, '/')
}

func frameHeadKey(id BatchID, ordinal, fragment, frame uint64) []byte {
	k := batchKey(id, "framehead/")
	k = append(k, u64be(ordinal)...)
	k = append(k, '/')
	k = append(k, u64be(fragment)...)
	k = append(k, '/')
	return append(k, u64be(frame)...)
}

func ackKey(id BatchID, ordinal, fragment, frame uint64) []byte {
	k := batchKey(id, "ackset/")
	k = append(k, u64be(ordinal)...)
	k = append(k, '/')
	k = append(k, u64be(fragment)...)
	k = append(k, '/')
	return append(k, u64be(frame)...)
}

// keyRange returns the [lo, hi) bounds covering every key with the given
// prefix.
func keyRange(prefix []byte) (lo, hi []byte) {
	hi = append([]byte(nil), prefix...)
	for i := len(hi) - 1; i >= 0; i-- {
		if hi[i] != 0xFF {
			hi[i]++
			return prefix, hi
		}
	}
	return prefix, nil
}
