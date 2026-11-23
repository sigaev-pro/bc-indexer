package eth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/sigaev-pro/bc-indexer/core/capture"
)

// wireBlockState carries the block currently being imported.
type wireBlockState struct {
	event   wireBlock
	records []*TransactionRecord
}

// Transport is the producer side of the import path: it encodes the wire
// events of one observation stream into a capture. Large byte values
// travel as TagBytes fields — payload = 32-byte SHA-256 content key
// followed by the data — so the frames, not a full second buffer, carry
// them; structured events travel as JSON fields. Every blob field is
// emitted before the event that references it.
type Transport struct {
	capturer *capture.Capturer
}

// NewTransport builds the producer side over one capturer.
func NewTransport(capturer *capture.Capturer) *Transport {
	return &Transport{capturer: capturer}
}

// storer returns the blob storer of this transport.
func (t *Transport) storer(ctx context.Context) blobStorer {
	return func(data []byte) (wireBytes, error) {
		return t.emitBlob(ctx, data)
	}
}

// emitBlob streams one large byte value as a TagBytes field. The content
// key is the SHA-256 of the data; the field payload is the key followed by
// the data, served chunk by chunk without building a joined copy.
func (t *Transport) emitBlob(ctx context.Context, data []byte) (wireBytes, error) {
	key := sha256.Sum256(data)
	read := func(offset uint64, dst []byte) error {
		if offset < sha256.Size {
			n := copy(dst, key[offset:])
			if n < len(dst) {
				n += copy(dst[n:], data[:min(uint64(len(data)), uint64(len(dst)-n))])
			}
			return nil
		}
		copy(dst, data[offset-sha256.Size:])
		return nil
	}
	length := uint64(sha256.Size + len(data))
	if err := t.capturer.CaptureField(ctx, TagBytes, length, read); err != nil {
		return wireBytes{}, err
	}
	return wireBytes{Key: hex.EncodeToString(key[:]), Size: int64(len(data))}, nil
}

// emitEvent captures one structured JSON event field.
func (t *Transport) emitEvent(ctx context.Context, tag uint32, event any) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return t.capturer.CaptureBytes(ctx, tag, data)
}

// EmitBlock emits the block event with its outside-tx observations. The
// referenced header blobs (extra data, source RLP) are emitted first.
func (t *Transport) EmitBlock(ctx context.Context, obs BlockObservation, outside []OutsideTxObservation) error {
	event, err := toWireBlock(obs, outside, t.storer(ctx))
	if err != nil {
		return err
	}
	return t.emitEvent(ctx, TagBlockEvent, event)
}

// EmitTx emits one transaction event. The referenced blobs (raw encodings,
// inputs, code, log data, the retained receipt source bytes) are emitted
// first. When source is nil the collector builds the source encoding from
// the typed receipt; when set it carries the D76 spool form the producer
// already streamed.
func (t *Transport) EmitTx(ctx context.Context, obs TxObservation, source []byte) error {
	var sourceKey *wireBytes
	if source != nil {
		w, err := t.emitBlob(ctx, source)
		if err != nil {
			return err
		}
		sourceKey = &w
	}
	event, err := toWireTx(obs, t.storer(ctx), sourceKey)
	if err != nil {
		return err
	}
	return t.emitEvent(ctx, TagTxEvent, event)
}

// Close ends production; the collector receives EOF after the last field.
func (t *Transport) Close() error { return t.capturer.Close() }
