// The NATS message identity of one transport frame: the Nats-Msg-Id tuple
// and the exact header bytes (CONTROL_WIRE_CONTRACT, "TransportFrame layout
// v2"). The id stably distinguishes
// (network, event_id, fragment_index, form, part_index), so retries with the
// same identity deduplicate and any other combination gets a new id.
package proto

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// NatsMsgIDPrefix opens every v2 message id.
const NatsMsgIDPrefix = "ex2."

// natsMsgIDBytes is the total ASCII length of a v2 message id: the prefix
// plus base64url without padding of the 89-byte tuple
// (4 + 119 = 123 bytes).
const natsMsgIDBytes = 123

// natsMsgIDTupleBytes is the fixed tuple length: network_digest32 ||
// producer_id16 || run_id16 || event_sequence8 || fragment_index8 ||
// form1 || part_index8, all integers big endian.
const natsMsgIDTupleBytes = 89

// NatsMsgID builds the message id of one frame:
//
//	"ex2." + base64url_no_padding(
//	    network_digest32 || producer_id16 || run_id16 || event_sequence8 ||
//	    fragment_index8 || form1 || part_index8)
//
// COMPLETE uses form 0 with part_index 0; PART uses form 1 with the actual
// part index. The producer, run and sequence come from the event id, so the
// id can never disagree with the frame identity it was built from.
func NatsMsgID(networkDigest Hash32, eventID EventID, fragmentIndex uint64, form Form, partIndex uint64) (string, error) {
	if err := eventID.Validate(); err != nil {
		return "", err
	}
	if !form.Valid() {
		return "", fmt.Errorf("%w: form %d is not COMPLETE=0 or PART=1", ErrClosedRegistry, byte(form))
	}
	tuple := make([]byte, 0, natsMsgIDTupleBytes)
	tuple = append(tuple, networkDigest[:]...)
	tuple = append(tuple, eventID[:]...) // producer16 || run16 || sequence8
	tuple = u64be(tuple, fragmentIndex)
	tuple = append(tuple, byte(form))
	tuple = u64be(tuple, partIndex)
	id := NatsMsgIDPrefix + base64.RawURLEncoding.EncodeToString(tuple)
	if len(id) != natsMsgIDBytes {
		return "", fmt.Errorf("%w: message id length %d, want %d", ErrInvalidValue, len(id), natsMsgIDBytes)
	}
	return id, nil
}

// natsHeaderFixedBytes is the header size without the stream name:
// "NATS/1.0\r\n" + "Nats-Msg-Id: " + 123 id bytes + "\r\n" +
// "Nats-Expected-Stream: " + "\r\n\r\n".
const natsHeaderFixedBytes = 10 + 13 + natsMsgIDBytes + 2 + 22 + 4

// NatsHeader returns the exact header bytes of one published frame:
//
//	NATS/1.0\r\nNats-Msg-Id: <id>\r\nNats-Expected-Stream: <stream>\r\n\r\n
//
// in this order, with no status line and no additional headers. The reply
// inbox is not part of these bytes.
func NatsHeader(msgID, stream string) ([]byte, error) {
	if len(msgID) != natsMsgIDBytes {
		return nil, fmt.Errorf("%w: message id length %d, want %d", ErrInvalidValue, len(msgID), natsMsgIDBytes)
	}
	if !strings.HasPrefix(msgID, NatsMsgIDPrefix) {
		return nil, fmt.Errorf("%w: message id does not start with %q", ErrInvalidValue, NatsMsgIDPrefix)
	}
	if stream == "" {
		return nil, fmt.Errorf("%w: stream name is empty", ErrInvalidValue)
	}
	var out []byte
	out = append(out, "NATS/1.0\r\nNats-Msg-Id: "...)
	out = append(out, msgID...)
	out = append(out, "\r\nNats-Expected-Stream: "...)
	out = append(out, stream...)
	return append(out, "\r\n\r\n"...), nil
}

// NatsHeaderSize returns the exact header size for a stream name. The size
// is fixed by the tuple length: only the stream name length varies.
func NatsHeaderSize(stream string) (uint64, error) {
	if stream == "" {
		return 0, fmt.Errorf("%w: stream name is empty", ErrInvalidValue)
	}
	return natsHeaderFixedBytes + uint64(len(stream)), nil
}
