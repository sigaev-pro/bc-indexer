// The NATS seam: the publish surface of the pinned JetStream client, its
// typed errors, and the in-memory adapter with subject routing, per-stream
// Nats-Msg-Id dedup, the exact v2 header layout and message-size limits.
package publisher

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/sigaev-pro/bc-indexer/gateway/destination"
)

// PubAck is the JetStream publish acknowledgment of one message: the
// stream that stored it, the store sequence and whether the message id
// deduplicated against an earlier copy.
type PubAck struct {
	Stream    string
	Sequence  uint64
	Duplicate bool
}

// The typed errors of the publish seam. The error engine classifies by
// these sentinels: transient errors retry with backoff, config errors
// stop publication with a named reason, and no error ever composes an ack.
var (
	// ErrTransient marks a disconnect, timeout or offline server: the
	// publish is retried later; nothing is confirmed or lost.
	ErrTransient = errors.New("publisher: nats transient failure")

	// ErrNoStream marks a subject no stream admits: a route or stream
	// configuration error, not a retryable state.
	ErrNoStream = errors.New("publisher: no stream admits the subject")

	// ErrUnexpectedStream marks a mismatch of the Nats-Expected-Stream
	// header or of the PubAck stream against the plan: the frame reached
	// a stream the plan did not name.
	ErrUnexpectedStream = errors.New("publisher: unexpected stream in publish or ack")

	// ErrMsgTooLarge marks a frame above the stream message-size limit.
	ErrMsgTooLarge = errors.New("publisher: message exceeds the stream limit")

	// ErrBadHeader marks publish bytes that do not carry the exact v2
	// header layout.
	ErrBadHeader = errors.New("publisher: malformed publish header")
)

// Conn is the JetStream publish seam: one bounded transport frame with
// its exact header bytes and payload. The production adapter wraps
// nats.go's JetStream Publish with headers; the reply inbox, connection
// id and timeout are transport properties the adapter owns, not parts of
// the logical request.
type Conn interface {
	Publish(subject string, header, payload []byte) (PubAck, error)
}

// StreamConfig is the configuration of one in-memory stream.
type StreamConfig struct {
	Name       string
	Subjects   []string
	MaxMsgSize uint64
}

// MemoryMessage is one stored message of the in-memory adapter.
type MemoryMessage struct {
	Stream   string
	Subject  string
	MsgID    string
	Header   []byte
	Payload  []byte
	Sequence uint64
}

// MemoryJetStream is the in-memory Conn adapter: subject routing by NATS
// pattern, dedup by Nats-Msg-Id, exact header parsing and size limits. It
// records every publish attempt, duplicates included, so tests can prove
// that retries preserve id, bytes and route.
type MemoryJetStream struct {
	mu       sync.Mutex
	streams  []StreamConfig
	msgs     []MemoryMessage
	byID     map[string]PubAck
	up       bool
	failNext int
	nextSeq  uint64
}

// NewMemoryJetStream returns an available server with the given streams.
func NewMemoryJetStream(streams ...StreamConfig) (*MemoryJetStream, error) {
	s := &MemoryJetStream{streams: append([]StreamConfig(nil), streams...),
		byID: make(map[string]PubAck), up: true}
	for _, cfg := range s.streams {
		if cfg.Name == "" || len(cfg.Subjects) == 0 {
			return nil, fmt.Errorf("%w: stream %q needs a name and subjects",
				ErrNoStream, cfg.Name)
		}
	}
	return s, nil
}

// SetAvailable switches the server availability, modeling an outage.
func (m *MemoryJetStream) SetAvailable(up bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.up = up
}

// FailNextPublish makes the next publish fail once with a transient error.
func (m *MemoryJetStream) FailNextPublish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNext = 1
}

// Messages returns a copy of the stored messages in sequence order.
func (m *MemoryJetStream) Messages() []MemoryMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MemoryMessage(nil), m.msgs...)
}

// Publish implements Conn.
func (m *MemoryJetStream) Publish(subject string, header, payload []byte) (PubAck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNext > 0 {
		m.failNext--
		return PubAck{}, fmt.Errorf("%w: publish timed out once", ErrTransient)
	}
	if !m.up {
		return PubAck{}, fmt.Errorf("%w: no connection", ErrTransient)
	}
	msgID, expected, err := parseHeader(header)
	if err != nil {
		return PubAck{}, err
	}
	stream := m.routeLocked(subject)
	if stream == "" {
		return PubAck{}, fmt.Errorf("%w: subject %q", ErrNoStream, subject)
	}
	if expected != stream {
		return PubAck{}, fmt.Errorf("%w: header expects %q but subject %q routes to %q",
			ErrUnexpectedStream, expected, subject, stream)
	}
	if cfg := m.streamConfigLocked(stream); cfg != nil && cfg.MaxMsgSize != 0 &&
		uint64(len(header)+len(payload)) > cfg.MaxMsgSize {
		return PubAck{}, fmt.Errorf("%w: %d bytes above the %d limit of %q",
			ErrMsgTooLarge, len(header)+len(payload), cfg.MaxMsgSize, stream)
	}
	if ack, dup := m.byID[msgID]; dup {
		return PubAck{Stream: ack.Stream, Sequence: ack.Sequence, Duplicate: true}, nil
	}
	m.nextSeq++
	ack := PubAck{Stream: stream, Sequence: m.nextSeq}
	m.byID[msgID] = ack
	m.msgs = append(m.msgs, MemoryMessage{Stream: stream, Subject: subject,
		MsgID: msgID, Header: append([]byte(nil), header...),
		Payload: append([]byte(nil), payload...), Sequence: m.nextSeq})
	return ack, nil
}

// routeLocked returns the first stream whose subjects admit the subject.
func (m *MemoryJetStream) routeLocked(subject string) string {
	for _, cfg := range m.streams {
		for _, pattern := range cfg.Subjects {
			if destination.SubjectMatches(pattern, subject) {
				return cfg.Name
			}
		}
	}
	return ""
}

// streamConfigLocked returns the config of the named stream.
func (m *MemoryJetStream) streamConfigLocked(name string) *StreamConfig {
	for i := range m.streams {
		if m.streams[i].Name == name {
			return &m.streams[i]
		}
	}
	return nil
}

// parseHeader parses the exact v2 header layout:
//
//	NATS/1.0\r\nNats-Msg-Id: <id>\r\nNats-Expected-Stream: <stream>\r\n\r\n
//
// in this order, with no status line and no additional headers.
func parseHeader(header []byte) (msgID, expected string, err error) {
	text := string(header)
	const idPrefix = "NATS/1.0\r\nNats-Msg-Id: "
	if !strings.HasPrefix(text, idPrefix) {
		return "", "", fmt.Errorf("%w: header does not open with the pinned layout", ErrBadHeader)
	}
	rest := text[len(idPrefix):]
	idEnd := strings.Index(rest, "\r\n")
	if idEnd < 0 {
		return "", "", fmt.Errorf("%w: header lacks the id terminator", ErrBadHeader)
	}
	msgID = rest[:idEnd]
	const streamPrefix = "Nats-Expected-Stream: "
	rest = rest[idEnd+2:]
	if !strings.HasPrefix(rest, streamPrefix) {
		return "", "", fmt.Errorf("%w: header lacks the expected-stream line", ErrBadHeader)
	}
	rest = rest[len(streamPrefix):]
	if !strings.HasSuffix(rest, "\r\n\r\n") {
		return "", "", fmt.Errorf("%w: header lacks the closing blank line", ErrBadHeader)
	}
	expected = rest[:len(rest)-4]
	if msgID == "" || expected == "" {
		return "", "", fmt.Errorf("%w: empty id or stream in header", ErrBadHeader)
	}
	return msgID, expected, nil
}
