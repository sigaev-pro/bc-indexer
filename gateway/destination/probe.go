// The online destination check: the Remote seam over the pinned nats.go
// client surface, the account/stream/route verification and the D77 stream
// metadata identity check (CONTROL_WIRE_CONTRACT, "Stream metadata
// identity D77"; DELIVERY_STATE_MACHINE, "DestinationBinding до первого
// capture"). The check is read-only: a missing or mismatched target blocks
// publication and keeps every pending batch in place; it never redirects
// pending to another destination and never mutates the durable binding.
package destination

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"
)

// The two mandatory stream metadata keys of D77. Keys are case-sensitive;
// values compare exactly after a format check, with no case folding,
// trimming or Unicode normalization.
const (
	MetadataNetworkDigest = "extractor.network_digest"
	MetadataDestinationID = "extractor.destination_id"
)

// ErrUnavailable marks an unreachable or non-answering target: publication
// is blocked and retried under the transient error class; it is never
// proof that a key is missing or that the values match.
var ErrUnavailable = errors.New("destination: target unavailable")

// AccountInfo is the account answer of the remote: the exact account
// field of UserInfo, not a display name.
type AccountInfo struct {
	Account string
}

// StreamInfo is the stream answer of the remote: the literal name, the
// subject patterns the stream admits, the metadata map of the config and
// the message-size limit. Extra metadata keys are preserved and ignored by
// the identity comparison.
type StreamInfo struct {
	Name       string
	Subjects   []string
	Metadata   map[string]string
	MaxMsgSize uint64
}

// Remote is the minimal online seam the pinned nats.go 1.54.0 client
// exposes for the destination check: account information and stream
// information. The production adapter wraps UserInfo and StreamInfo
// requests; tests use MemoryRemote.
type Remote interface {
	// AccountInfo returns the account of the current connection.
	AccountInfo(ctx context.Context) (AccountInfo, error)

	// StreamInfo returns the configuration of the named stream.
	StreamInfo(ctx context.Context, stream string) (StreamInfo, error)
}

// Status is the outcome of one destination check.
type Status uint8

// The check outcomes.
const (
	// StatusVerified means the target matches the binding on account,
	// stream, route, limits and both metadata identity keys.
	StatusVerified Status = iota

	// StatusConfigBlocked means the target answered with a missing,
	// malformed or mismatched identity: publication stops with a named
	// reason, pending and bytes are kept, nothing is redirected.
	StatusConfigBlocked

	// StatusUnavailable means the target did not answer: publication is
	// blocked and retried; this is not evidence about the metadata.
	StatusUnavailable
)

// String returns the status name.
func (s Status) String() string {
	switch s {
	case StatusVerified:
		return "VERIFIED"
	case StatusConfigBlocked:
		return "CONFIG_BLOCKED"
	case StatusUnavailable:
		return "UNAVAILABLE"
	}
	return fmt.Sprintf("Status(%d)", uint8(s))
}

// Report is the result of one check: the status and, for a blocked
// outcome, the named reasons (stream, key and mismatch kind, never
// credentials).
type Report struct {
	Status  Status
	Reasons []string
}

// OK reports whether the check verified the target.
func (r Report) OK() bool { return r.Status == StatusVerified }

// VerifiedTarget is the successful check result the publisher admits a
// CLOSED batch with: the binding, the stream, the concrete subjects that
// were verified and the frame budget that fits the stream limit. A later
// publication after any error re-checks the target; an old success never
// carries over to a new connection or another target.
type VerifiedTarget struct {
	Binding    Binding
	Stream     string
	Subjects   []string
	MaxMsgSize uint64
}

// Check verifies one binding against the live target in this order:
//
//  1. the connection account equals binding.ExpectedAccount exactly;
//  2. the stream exists and is named exactly binding.Stream;
//  3. every concrete subject of subjects is covered by the stream
//     subjects list;
//  4. the stream message-size limit fits maxFrameBytes;
//  5. the two D77 metadata keys are present, well-formed and exactly
//     equal to the network digest and destination id of the binding.
//
// An unreachable account or stream answer is StatusUnavailable. Every
// concrete finding is StatusConfigBlocked with named reasons. The check
// performs no mutation anywhere.
func Check(ctx context.Context, remote Remote, binding Binding, subjects []string, maxFrameBytes uint64) Report {
	if err := binding.Validate(); err != nil {
		return Report{Status: StatusConfigBlocked,
			Reasons: []string{fmt.Sprintf("binding descriptor invalid: %v", err)}}
	}
	account, err := remote.AccountInfo(ctx)
	if err != nil {
		return unavailable("account information", err)
	}
	if account.Account != binding.ExpectedAccount {
		return Report{Status: StatusConfigBlocked, Reasons: []string{fmt.Sprintf(
			"connection account %q differs from the binding account %q",
			account.Account, binding.ExpectedAccount)}}
	}
	info, err := remote.StreamInfo(ctx, binding.Stream)
	if err != nil {
		return unavailable(fmt.Sprintf("stream %q information", binding.Stream), err)
	}
	if info.Name != binding.Stream {
		return Report{Status: StatusConfigBlocked, Reasons: []string{fmt.Sprintf(
			"stream answered as %q, want %q", info.Name, binding.Stream)}}
	}
	var reasons []string
	for _, s := range subjects {
		if !RouteCovered(info.Subjects, s) {
			reasons = append(reasons, fmt.Sprintf(
				"stream %q subjects %v do not cover route subject %q", binding.Stream, info.Subjects, s))
		}
	}
	if info.MaxMsgSize != 0 && info.MaxMsgSize < maxFrameBytes {
		reasons = append(reasons, fmt.Sprintf(
			"stream %q max message size %d is below the frame budget %d",
			binding.Stream, info.MaxMsgSize, maxFrameBytes))
	}
	reasons = append(reasons, checkMetadata(binding, info)...)
	if len(reasons) > 0 {
		return Report{Status: StatusConfigBlocked, Reasons: reasons}
	}
	return Report{Status: StatusVerified}
}

// NewVerifiedTarget builds the publisher admission token from a verified
// binding: the concrete subjects and the stream message limit the check
// observed. It is the only constructor of VerifiedTarget; a target that
// was never verified cannot be admitted.
func NewVerifiedTarget(binding Binding, subjects []string, maxMsgSize uint64) (VerifiedTarget, error) {
	if err := binding.Validate(); err != nil {
		return VerifiedTarget{}, err
	}
	for _, s := range subjects {
		if s == "" {
			return VerifiedTarget{}, fmt.Errorf("%w: empty route subject", ErrDescriptor)
		}
	}
	return VerifiedTarget{Binding: binding, Stream: binding.Stream,
		Subjects: append([]string(nil), subjects...), MaxMsgSize: maxMsgSize}, nil
}

// unavailable builds the retryable blocked report.
func unavailable(what string, err error) Report {
	return Report{Status: StatusUnavailable, Reasons: []string{fmt.Sprintf(
		"%s unavailable: %v (retry under the transient class; not evidence of missing or matching metadata)", what, err)}}
}

// checkMetadata verifies the two D77 identity keys: exact lowercase hex
// formats and exact values. Unknown extra keys are ignored.
func checkMetadata(binding Binding, info StreamInfo) []string {
	var reasons []string
	networkDigest, err := binding.NetworkDigest()
	if err != nil {
		return []string{fmt.Sprintf("binding network digest: %v", err)}
	}
	checkHexKey(info, MetadataNetworkDigest, networkDigest[:], 64, &reasons)
	checkHexKey(info, MetadataDestinationID, binding.DestinationID[:], 32, &reasons)
	return reasons
}

// checkHexKey verifies one metadata key: presence, type, length, charset
// and exact value, naming the stream, the key and the mismatch kind.
func checkHexKey(info StreamInfo, key string, want []byte, wantLen int, reasons *[]string) {
	value, ok := info.Metadata[key]
	if !ok {
		*reasons = append(*reasons, fmt.Sprintf(
			"stream %q metadata key %q is missing", info.Name, key))
		return
	}
	if value == "" {
		*reasons = append(*reasons, fmt.Sprintf(
			"stream %q metadata key %q is an empty string", info.Name, key))
		return
	}
	if len(value) != wantLen || !utf8.ValidString(value) {
		*reasons = append(*reasons, fmt.Sprintf(
			"stream %q metadata key %q has a malformed value of %d characters, want %d lowercase hex",
			info.Name, key, len(value), wantLen))
		return
	}
	got, err := decodeHexString(value)
	if err != nil {
		*reasons = append(*reasons, fmt.Sprintf(
			"stream %q metadata key %q value %q is not lowercase hex: %v", info.Name, key, value, err))
		return
	}
	if string(got) != string(want) {
		*reasons = append(*reasons, fmt.Sprintf(
			"stream %q metadata key %q value %q mismatches the binding value %q",
			info.Name, key, value, hexEncode(want)))
	}
}

// decodeHexString decodes an already length-checked lowercase hex string.
func decodeHexString(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	if err := hexDecodeInto(s, out); err != nil {
		return nil, err
	}
	return out, nil
}

// hexEncode returns the lowercase hex form of b, the exact form the
// metadata values carry.
func hexEncode(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}
