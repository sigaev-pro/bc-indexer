package eth

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

// ReceiptEncodingKind is the closed D75 receipt encoding registry, re-exported
// from the shared profile registry. Zero is INVALID and never a default
// success.
type ReceiptEncodingKind = profiles.ReceiptEncodingKind

// The D75 kinds.
const (
	KindLegacyConsensusRLP     = profiles.KindLegacyConsensusRLP
	KindTypedConsensusEnvelope = profiles.KindTypedConsensusEnvelope
)

// Bloom is the 256-byte logs bloom of a receipt.
type Bloom [256]byte

// Receipt errors.
var (
	// ErrReceiptShape reports a structurally invalid receipt.
	ErrReceiptShape = errors.New("eth: invalid receipt")
	// ErrReceiptEncoding reports a receipt encoding that violates the
	// closed D75 registry or whose bytes disagree with the typed receipt.
	ErrReceiptEncoding = errors.New("eth: invalid source receipt encoding")
)

// Log is one committed receipt log (MESSAGE_CONTRACT section 3). The
// receipt carries only committed logs; reverted log attempts stay out of
// the receipt and out of the first mandatory contract.
type Log struct {
	Address Address20
	Topics  []Hash32
	Data    []byte
	// TxLogIndex is the log's position inside the transaction logs.
	TxLogIndex uint64
	// BlockLogIndex is the log's position inside the block logs.
	BlockLogIndex uint64
}

// EthReceiptDetails is the explicitly present, empty ETH network-details
// variant of D75: ETH body receipts add no fields beyond the common ones.
// The variant is present and typed; emptiness is not a missing receipt.
type EthReceiptDetails struct{}

// NetworkDetails is the closed receipt network-details oneof. For the ETH
// profile exactly the Eth variant is valid (D75 section 3.2).
type NetworkDetails struct {
	Eth *EthReceiptDetails
}

// Family returns the closed family name of the present variant.
func (n NetworkDetails) Family() string {
	switch {
	case n.Eth != nil:
		return "ETH"
	default:
		return ""
	}
}

// Validate requires exactly one variant and checks it against the closed
// ETH profile.
func (n NetworkDetails) Validate() error {
	seen := 0
	for _, p := range []bool{n.Eth != nil} {
		if p {
			seen++
		}
	}
	if seen != 1 {
		return fmt.Errorf("%w: network details has %d variants, want exactly 1", ErrReceiptShape, seen)
	}
	profile, err := profiles.Default(profiles.FamilyEth)
	if err != nil {
		return err
	}
	return profile.ValidateDetails(profiles.DetailsEth)
}

// Receipt is the typed native receipt of a body transaction
// (MESSAGE_CONTRACT section 3):
//
//   - the native outcome is Status(success) or PreStatusStateRoot(hash),
//     exactly one of the two; a pre-Byzantium source root never becomes an
//     RPC status and the execution result comes from native execution;
//   - cumulative_gas_used is present; gas_used, effective_gas_price,
//     blob_gas_used and blob_gas_price carry explicit presence;
//   - logs and logs_bloom are the committed source values;
//   - attempted_create_address and created_contract keep the D81
//     distinction; final existence lives in AccountChange;
//   - fee_summary aggregates the fee actions and never adds a second
//     monetary event.
type Receipt struct {
	// Status is 0 or 1 when the native outcome is a status.
	Status Optional[uint8]
	// PostStateRoot is the pre-Byzantium state root outcome.
	PostStateRoot Optional[Hash32]
	// Result is the native execution result SUCCESS/REVERT/FAILURE.
	Result Result
	// CumulativeGasUsed is the block-cumulative gas after this tx.
	CumulativeGasUsed uint64
	// GasUsed is this transaction's gas when applicable.
	GasUsed Optional[uint64]
	// EffectiveGasPrice is the actual effective gas price.
	EffectiveGasPrice Optional[UInt256]
	// BlobGasUsed and BlobGasPrice are the blob charge fields with explicit
	// presence.
	BlobGasUsed  Optional[uint64]
	BlobGasPrice Optional[UInt256]
	// Logs are the committed logs in source order.
	Logs []Log
	// LogsBloom is the source bloom of the committed logs.
	LogsBloom Bloom
	// AttemptedCreateAddress is the D81 attempt address of this
	// transaction's creation, when native determined one.
	AttemptedCreateAddress *Address20
	// CreatedContract is present only on locally successful creation.
	CreatedContract *Address20
	// FeeSummary is the aggregate of the fee actions (D62).
	FeeSummary *balance.FeeSummary
	// NetworkDetails is the closed network-details oneof.
	NetworkDetails NetworkDetails
	// Source is the retained native consensus encoding (D75/D76).
	Source Optional[SourceReceiptEncoding]
}

// SourceReceiptEncoding is the retained source encoding of one receipt:
// the closed kind, the table-mandated native_type and the exact native
// consensus bytes. The bytes are never transcoded, truncated or rebuilt
// from the normalized projection.
type SourceReceiptEncoding struct {
	Kind       ReceiptEncodingKind
	NativeType Optional[uint32]
	Bytes      []byte
}

// Validate checks the typed receipt shape: exactly one outcome branch,
// closed enums, result consistency with the status, and the network
// details closure.
func (r *Receipt) Validate() error {
	status, hasStatus := r.Status.Get()
	_, hasRoot := r.PostStateRoot.Get()
	switch {
	case hasStatus && hasRoot:
		return fmt.Errorf("%w: both status and post-state root", ErrReceiptShape)
	case !hasStatus && !hasRoot:
		return fmt.Errorf("%w: no native outcome", ErrReceiptShape)
	case hasStatus && status > 1:
		return fmt.Errorf("%w: status %d", ErrReceiptShape, status)
	}
	if !r.Result.Valid() {
		return fmt.Errorf("%w: result %d", ErrReceiptShape, uint8(r.Result))
	}
	if hasStatus {
		if status == 1 && r.Result != ResultSuccess {
			return fmt.Errorf("%w: status 1 with result %s", ErrReceiptShape, r.Result)
		}
		if status == 0 && r.Result == ResultSuccess {
			return fmt.Errorf("%w: status 0 with result SUCCESS", ErrReceiptShape)
		}
	}
	if err := r.NetworkDetails.Validate(); err != nil {
		return err
	}
	if s, ok := r.Source.Get(); ok {
		if err := ValidateSourceEncodingShape(s); err != nil {
			return err
		}
	}
	return nil
}

// consensusPostState returns the first consensus field: the one-byte
// status string, or the 32-byte post-state root string.
func (r *Receipt) consensusPostState() ([]byte, error) {
	if status, ok := r.Status.Get(); ok {
		return []byte{status}, nil
	}
	if root, ok := r.PostStateRoot.Get(); ok {
		return root[:], nil
	}
	return nil, fmt.Errorf("%w: no native outcome", ErrReceiptShape)
}

// ConsensusContent is the native consensus receipt content of D75: the
// post-state-or-status field, the cumulative gas, the bloom and the logs.
// It is the exact content of Receipt.MarshalBinary of the pinned clients;
// gas_used, prices, hashes, indexes and fee summaries are not part of it
// and stay in the typed record only.
type ConsensusContent struct {
	// PostStateOrStatus is the status byte (0/1) or the 32-byte root.
	PostStateOrStatus []byte
	// CumulativeGasUsed is the block-cumulative gas.
	CumulativeGasUsed uint64
	// Bloom is the 256-byte logs bloom.
	Bloom Bloom
	// Logs are the consensus logs in source order.
	Logs []Log
}

// consensusSize returns the encoded size of the consensus list payload
// without building any bytes: the D76 length pre-pass.
func consensusSize(c *ConsensusContent) uint64 {
	size := rlpStringSize(uint64(len(c.PostStateOrStatus)))
	// A one-byte post-state below 0x80 encodes itself (canonical RLP).
	if len(c.PostStateOrStatus) == 1 && c.PostStateOrStatus[0] < 0x80 {
		size = 1
	}
	size += rlpUintSize(c.CumulativeGasUsed)
	size += rlpStringSize(uint64(len(c.Bloom)))
	logsPayload := uint64(0)
	for i := range c.Logs {
		logsPayload += logEncodedSize(&c.Logs[i])
	}
	size += rlpListSize(logsPayload)
	return size
}

// logEncodedSize returns the encoded size of one log.
func logEncodedSize(l *Log) uint64 {
	payload := rlpStringSize(20)
	topics := uint64(0)
	for range l.Topics {
		topics += rlpStringSize(32)
	}
	payload += rlpListSize(topics)
	payload += rlpStringSize(uint64(len(l.Data)))
	return rlpListSize(payload)
}

// writeConsensus streams the consensus RLP list to w. The two-pass shape —
// one exact size computation, then one streaming write — follows D76: no
// second full buffer is built and the memory overhead is one small header
// slice per item.
func writeConsensus(w io.Writer, c *ConsensusContent) error {
	payload := consensusSize(c)
	if err := writeRLPHeader(w, true, payload); err != nil {
		return err
	}
	if err := writeRLPBytes(w, c.PostStateOrStatus); err != nil {
		return err
	}
	if err := writeRLPUint(w, c.CumulativeGasUsed); err != nil {
		return err
	}
	if err := writeRLPBytes(w, c.Bloom[:]); err != nil {
		return err
	}
	topicsPayload := uint64(0) // payload of the outer logs list
	for i := range c.Logs {
		topicsPayload += logEncodedSize(&c.Logs[i])
	}
	if err := writeRLPHeader(w, true, topicsPayload); err != nil {
		return err
	}
	for i := range c.Logs {
		if err := WriteLog(w, &c.Logs[i]); err != nil {
			return err
		}
	}
	return nil
}

// WriteLog streams one consensus log: [address, topics, data].
func WriteLog(w io.Writer, l *Log) error {
	topics := uint64(0)
	for range l.Topics {
		topics += rlpStringSize(32)
	}
	payload := rlpStringSize(20) + rlpListSize(topics) + rlpStringSize(uint64(len(l.Data)))
	if err := writeRLPHeader(w, true, payload); err != nil {
		return err
	}
	if err := writeRLPBytes(w, l.Address[:]); err != nil {
		return err
	}
	if err := writeRLPHeader(w, true, topics); err != nil {
		return err
	}
	for _, t := range l.Topics {
		if err := writeRLPBytes(w, t[:]); err != nil {
			return err
		}
	}
	return writeRLPBytes(w, l.Data)
}

// WriteConsensusReceipt streams the native consensus encoding of one
// receipt body encoding to w: for a legacy receipt (native type 0) the RLP
// list itself, for a typed receipt the single source type byte followed by
// the RLP list, with no outer RLP-string wrapper (D75 table rows 1 and 2).
// The output is byte-equivalent to Receipt.MarshalBinary of the pinned
// client on the same native receipt.
func WriteConsensusReceipt(w io.Writer, nativeType uint32, c *ConsensusContent) error {
	if nativeType == 0 {
		return writeConsensus(w, c)
	}
	if _, err := w.Write([]byte{byte(nativeType)}); err != nil {
		return err
	}
	return writeConsensus(w, c)
}

// ConsensusContentOf extracts the consensus content of a typed receipt.
func ConsensusContentOf(r *Receipt) (ConsensusContent, error) {
	if err := r.Validate(); err != nil {
		return ConsensusContent{}, err
	}
	first, err := r.consensusPostState()
	if err != nil {
		return ConsensusContent{}, err
	}
	return ConsensusContent{
		PostStateOrStatus: first,
		CumulativeGasUsed: r.CumulativeGasUsed,
		Bloom:             r.LogsBloom,
		Logs:              r.Logs,
	}, nil
}

// SourceEncoding builds the retained source encoding of a receipt: the
// closed D75 kind chosen by the actual native receipt type, validated
// against the ETH profile, and the byte-equivalent consensus bytes. The
// convenience form materializes the bytes once for the record; the
// collector path streams them straight to its spool through
// WriteConsensusReceipt (D76).
func SourceEncoding(r *Receipt, nativeType uint32) (SourceReceiptEncoding, error) {
	profile, err := profiles.Default(profiles.FamilyEth)
	if err != nil {
		return SourceReceiptEncoding{}, err
	}
	kind := KindTypedConsensusEnvelope
	nt := nativeType
	if nativeType == 0 {
		kind = KindLegacyConsensusRLP
		nt = 0
	}
	enc := profiles.ReceiptEncoding{Kind: kind}
	if kind == KindLegacyConsensusRLP {
		zero := uint32(0)
		enc.NativeType = &zero
	} else {
		enc.NativeType = &nt
	}
	if err := profile.ValidateReceiptEncoding(enc, true); err != nil {
		return SourceReceiptEncoding{}, fmt.Errorf("%w: %v", ErrReceiptEncoding, err)
	}
	content, err := ConsensusContentOf(r)
	if err != nil {
		return SourceReceiptEncoding{}, err
	}
	var buf bytes.Buffer
	if err := WriteConsensusReceipt(&buf, nativeType, &content); err != nil {
		return SourceReceiptEncoding{}, err
	}
	out := SourceReceiptEncoding{Kind: kind, Bytes: buf.Bytes()}
	if kind == KindLegacyConsensusRLP {
		out.NativeType = Some(uint32(0))
	} else {
		out.NativeType = Some(nativeType)
	}
	return out, nil
}

// ValidateSourceEncodingShape checks the closed registry shape of a
// retained encoding against the ETH body-receipt profile: kind, mandatory
// native_type presence per the D75 table and non-empty bytes.
func ValidateSourceEncodingShape(s SourceReceiptEncoding) error {
	profile, err := profiles.Default(profiles.FamilyEth)
	if err != nil {
		return err
	}
	enc := profiles.ReceiptEncoding{Kind: s.Kind}
	if nt, ok := s.NativeType.Get(); ok {
		v := nt
		enc.NativeType = &v
	}
	if err := profile.ValidateReceiptEncoding(enc, true); err != nil {
		return fmt.Errorf("%w: %v", ErrReceiptEncoding, err)
	}
	if len(s.Bytes) == 0 {
		return fmt.Errorf("%w: empty source bytes", ErrReceiptEncoding)
	}
	return nil
}

// VerifySourceEncoding decodes the retained bytes as the D76 streaming
// reader: exact framing of the chosen kind, canonical scalar widths, the
// status-or-root width rule, agreement of every consensus field with the
// typed receipt and no trailing bytes. It allocates no second copy of the
// bytes; the decoder walks views of the input.
func VerifySourceEncoding(s SourceReceiptEncoding, r *Receipt) error {
	if err := ValidateSourceEncodingShape(s); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	wantType, hasType := s.NativeType.Get()
	body := s.Bytes
	if s.Kind == KindTypedConsensusEnvelope {
		if !hasType {
			return fmt.Errorf("%w: kind 2 without native_type", ErrReceiptEncoding)
		}
		if len(body) == 0 || uint32(body[0]) != wantType {
			return fmt.Errorf("%w: leading type byte is not native_type %d", ErrReceiptEncoding, wantType)
		}
		body = body[1:]
	} else if hasType && wantType != 0 {
		return fmt.Errorf("%w: kind 1 native_type must be 0", ErrReceiptEncoding)
	}
	item, rest, err := rlpRead(body)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrReceiptEncoding, err)
	}
	if len(rest) != 0 {
		return fmt.Errorf("%w: %d trailing bytes", ErrRLPTrailing, len(rest))
	}
	fields, err := rlpReadListItems(item)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrReceiptEncoding, err)
	}
	if len(fields) != 4 {
		return fmt.Errorf("%w: consensus list has %d fields, want 4", ErrReceiptEncoding, len(fields))
	}
	first := fields[0]
	if first.IsList {
		return fmt.Errorf("%w: post-state field is a list", ErrReceiptEncoding)
	}
	switch len(first.String) {
	case 1:
		if first.String[0] > 1 {
			return fmt.Errorf("%w: status byte %d", ErrReceiptEncoding, first.String[0])
		}
		status, ok := r.Status.Get()
		if !ok || status != first.String[0] {
			return fmt.Errorf("%w: status byte disagrees with the typed receipt", ErrReceiptEncoding)
		}
	case 32:
		root, ok := r.PostStateRoot.Get()
		if !ok || Hash32(first.String) != root {
			return fmt.Errorf("%w: post-state root disagrees with the typed receipt", ErrReceiptEncoding)
		}
	default:
		return fmt.Errorf("%w: post-state field width %d", ErrReceiptEncoding, len(first.String))
	}
	cumulative, err := fields[1].uintValue()
	if err != nil {
		return fmt.Errorf("%w: cumulative gas field: %v", ErrReceiptEncoding, err)
	}
	if cumulative != r.CumulativeGasUsed {
		return fmt.Errorf("%w: cumulative gas %d disagrees with %d", ErrReceiptEncoding, cumulative, r.CumulativeGasUsed)
	}
	if fields[2].IsList || len(fields[2].String) != 256 {
		return fmt.Errorf("%w: bloom width %d", ErrReceiptEncoding, len(fields[2].String))
	}
	if Bloom(fields[2].String) != r.LogsBloom {
		return fmt.Errorf("%w: bloom disagrees with the typed receipt", ErrReceiptEncoding)
	}
	logs, err := rlpReadListItems(fields[3])
	if err != nil {
		return fmt.Errorf("%w: logs: %v", ErrReceiptEncoding, err)
	}
	if len(logs) != len(r.Logs) {
		return fmt.Errorf("%w: %d encoded logs for %d typed logs", ErrReceiptEncoding, len(logs), len(r.Logs))
	}
	for i, raw := range logs {
		if err := verifyEncodedLog(raw, &r.Logs[i]); err != nil {
			return fmt.Errorf("%w: log %d: %w", ErrReceiptEncoding, i, err)
		}
	}
	return nil
}

func verifyEncodedLog(raw rlpItem, want *Log) error {
	fields, err := rlpReadListItems(raw)
	if err != nil {
		return err
	}
	if len(fields) != 3 {
		return fmt.Errorf("log has %d fields, want 3", len(fields))
	}
	if fields[0].IsList || len(fields[0].String) != 20 {
		return fmt.Errorf("log address width %d", len(fields[0].String))
	}
	if Address20(fields[0].String) != want.Address {
		return fmt.Errorf("log address disagrees with the typed log")
	}
	topics, err := rlpReadListItems(fields[1])
	if err != nil {
		return err
	}
	if len(topics) != len(want.Topics) {
		return fmt.Errorf("log has %d topics, want %d", len(topics), len(want.Topics))
	}
	for i, t := range topics {
		if t.IsList || len(t.String) != 32 || Hash32(t.String) != want.Topics[i] {
			return fmt.Errorf("topic %d disagrees with the typed log", i)
		}
	}
	if fields[2].IsList || !bytes.Equal(fields[2].String, want.Data) {
		return fmt.Errorf("log data disagrees with the typed log")
	}
	return nil
}
