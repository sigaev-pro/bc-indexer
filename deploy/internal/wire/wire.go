// Package wire is the mock ingest protocol between the mock adapter and
// the mock gateway: length-prefixed JSON messages over one mutually
// authenticated TLS connection. It is a stand-in for the pinned gRPC
// control wire of gateway/ingest; the production protocol lands with the
// real gateway. Message ids follow the exact Nats-Msg-Id form of
// CONTROL_WIRE_CONTRACT so retries dedup identically to the real system.
package wire

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Protocol is the mock wire protocol version.
const Protocol = 1

// MaxMessageBytes bounds one framed message (below the 2 MiB frame
// budget; mock payloads are small JSON records).
const MaxMessageBytes = 1 << 20

// Hello opens one session. The adapter proves its identity through the
// mTLS client certificate; the fields carry the durable producer/run
// identities and the binding digest both sides must agree on.
type Hello struct {
	Type           string `json:"type"`
	Protocol       int    `json:"protocol"`
	Role           string `json:"role"`
	NetworkProfile string `json:"network_profile"`
	ProducerID     string `json:"producer_id"`
	RunID          string `json:"run_id"`
	BindingDigest  string `json:"binding_digest"`
}

// HelloAck accepts a session.
type HelloAck struct {
	Type           string `json:"type"`
	NetworkProfile string `json:"network_profile"`
	BindingDigest  string `json:"binding_digest"`
	GatewayEpoch   string `json:"gateway_epoch"`
}

// ErrorRefuse refuses a session or an event with a named class.
type ErrorRefuse struct {
	Type   string `json:"type"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Event is one captured record handed to the gateway: the durable event
// sequence, the route suffix under the binding prefix and the immutable
// payload bytes. The payload of the mock is a small JSON record; the
// real system carries codec fragments.
type Event struct {
	Type     string `json:"type"`
	EventSeq uint64 `json:"event_seq"`
	Height   uint64 `json:"height"`
	Route    string `json:"route"`
	Payload  []byte `json:"payload"`
}

// Ack confirms one durable NATS publication.
type Ack struct {
	Type      string `json:"type"`
	EventSeq  uint64 `json:"event_seq"`
	Stream    string `json:"stream"`
	Sequence  uint64 `json:"sequence"`
	Duplicate bool   `json:"duplicate"`
	MsgID     string `json:"msg_id"`
}

// Nak refuses one event with a named class: TRANSIENT keeps the event
// pending in the adapter WAL, LIMIT_INCOMPATIBLE and CONFIG_BLOCKED stop
// the publication of that event with a named reason.
type Nak struct {
	Type     string `json:"type"`
	EventSeq uint64 `json:"event_seq"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
}

// Write encodes and frames one message.
func Write(w io.Writer, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageBytes {
		return fmt.Errorf("wire message is %d bytes, bound is %d", len(data), MaxMessageBytes)
	}
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(data)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// Read reads and decodes one framed message into out.
func Read(r io.Reader, out any) error {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(head[:])
	if size == 0 || size > MaxMessageBytes {
		return fmt.Errorf("wire message length %d is out of bounds", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// NATSMsgID builds the exact Nats-Msg-Id of CONTROL_WIRE_CONTRACT:
// "ex2." + base64url without padding of
// network_digest32 || producer_id16 || run_id16 || event_sequence8 ||
// fragment_index8 || form1 || part_index8 (89 bytes, 123 ASCII chars).
// The mock always carries one complete fragment: form 0, part index 0.
func NATSMsgID(networkDigest [32]byte, producerID, runID [16]byte, eventSequence uint64) string {
	var tuple [89]byte
	copy(tuple[0:32], networkDigest[:])
	copy(tuple[32:48], producerID[:])
	copy(tuple[48:64], runID[:])
	binary.BigEndian.PutUint64(tuple[64:72], eventSequence)
	// tuple[72:80] is the fragment index: zero, one complete fragment.
	// tuple[80] is the form: zero, COMPLETE.
	// tuple[81:89] is the part index: zero.
	return "ex2." + base64.RawURLEncoding.EncodeToString(tuple[:])
}

// VerifyNATSMsgID re-computes the expected message id of one event.
func VerifyNATSMsgID(msgID string, networkDigest [32]byte, producerID, runID [16]byte, eventSequence uint64) bool {
	return msgID == NATSMsgID(networkDigest, producerID, runID, eventSequence)
}

// HeaderBytes returns the exact NATS header bytes of one published
// frame: NATS/1.0, Nats-Msg-Id and Nats-Expected-Stream in this fixed
// order (CONTROL_WIRE_CONTRACT, "TransportFrame layout v2").
func HeaderBytes(msgID, stream string) []byte {
	return []byte("NATS/1.0\r\nNats-Msg-Id: " + msgID + "\r\nNats-Expected-Stream: " + stream + "\r\n\r\n")
}

// Digest32Hex decodes a 64-character lowercase hex digest.
func Digest32Hex(s string) ([32]byte, error) {
	var out [32]byte
	if len(s) != 64 {
		return out, fmt.Errorf("digest length %d, want 64", len(s))
	}
	raw, err := hexDecodeLower(s)
	if err != nil {
		return out, err
	}
	copy(out[:], raw)
	return out, nil
}

// ID16Hex decodes a 32-character lowercase hex identity.
func ID16Hex(s string) ([16]byte, error) {
	var out [16]byte
	if len(s) != 32 {
		return out, fmt.Errorf("identity length %d, want 32", len(s))
	}
	raw, err := hexDecodeLower(s)
	if err != nil {
		return out, err
	}
	copy(out[:], raw)
	return out, nil
}

// HexID16 renders a 16-byte identity as 32 lowercase hex characters.
func HexID16(id [16]byte) string {
	return fmt.Sprintf("%x", id)
}

// hexDecodeLower decodes lowercase hex without the encoding import in
// the interface of this package.
func hexDecodeLower(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("hex string has odd length")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		hi, ok := hexNibble(s[i])
		if !ok {
			return nil, fmt.Errorf("character %q is not a lowercase hex digit", string(s[i]))
		}
		lo, ok := hexNibble(s[i+1])
		if !ok {
			return nil, fmt.Errorf("character %q is not a lowercase hex digit", string(s[i+1]))
		}
		out[i/2] = hi<<4 | lo
	}
	return out, nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}
