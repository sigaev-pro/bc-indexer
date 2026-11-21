package block

import (
	"fmt"
)

// Header is the typed common header of the current ETH, BSC and Bor
// profiles. Common fields follow CURRENT_SEMANTICS; optional fork fields
// carry their own presence, decided by the native header and fork. A field
// absent from the native source is never filled with a zero value.
//
// Geth's extra block_access_list_hash and slot_number are family context,
// not common fields; they live in EthBlockDetails. Bor's ActualTime and
// AbortRecovery are local runtime fields, not consensus header fields, and
// are deliberately absent from this schema.
type Header struct {
	ParentHash       Hash32    `json:"parent_hash"`
	UncleHash        Hash32    `json:"uncle_hash"`
	Beneficiary      Address20 `json:"beneficiary"`
	StateRoot        Hash32    `json:"state_root"`
	TransactionsRoot Hash32    `json:"transactions_root"`
	ReceiptsRoot     Hash32    `json:"receipts_root"`
	LogsBloom        Bloom     `json:"logs_bloom"`
	Difficulty       UInt256   `json:"difficulty"`
	Number           uint64    `json:"number"`
	GasLimit         uint64    `json:"gas_limit"`
	GasUsed          uint64    `json:"gas_used"`
	TimestampSeconds uint64    `json:"timestamp_seconds"`
	ExtraData        []byte    `json:"extra_data"`
	MixDigest        Hash32    `json:"mix_digest"`
	Nonce            Nonce     `json:"nonce"`

	// Optional fork fields. Presence follows the native header of the
	// pinned profile; a present zero (for example BSC's pre-London base
	// fee 0 or the zero parent beacon root in a fixture header) stays a
	// present value.
	BaseFee               Optional[UInt256] `json:"base_fee"`
	WithdrawalsRoot       Optional[Hash32]  `json:"withdrawals_root"`
	BlobGasUsed           Optional[uint64]  `json:"blob_gas_used"`
	ExcessBlobGas         Optional[uint64]  `json:"excess_blob_gas"`
	ParentBeaconBlockRoot Optional[Hash32]  `json:"parent_beacon_block_root"`
	RequestsHash          Optional[Hash32]  `json:"requests_hash"`

	// SourceRLP is the exact native header encoding of the pinned client.
	// It is mandatory for the current EVM profiles; the block hash comes
	// from the native source and this package adds no whole-block
	// recomputation.
	SourceRLP []byte `json:"source_rlp"`
}

// Validate checks the schema-level header rules. Fork applicability of the
// optional fields is a profile decision (E02-10) and is not re-derived here.
func (h *Header) Validate() error {
	if len(h.SourceRLP) == 0 {
		return fmt.Errorf("%w: header source_rlp is empty", ErrInvalidValue)
	}
	return nil
}

// StateSyncMode is how Bor committed the observed StateSync records of a
// block. Pre-Madhugiri they were committed at the end of the block; after
// Madhugiri they are body transactions (native type 127, 0x7f).
type StateSyncMode uint8

// StateSync modes. Zero is not a valid mode.
const (
	StateSyncEndOfBlockCommit StateSyncMode = iota + 1
	StateSyncBodyTransaction
)

var stateSyncModeNames = []string{"END_OF_BLOCK_COMMIT", "BODY_TRANSACTION"}

// String returns the contract name of m.
func (m StateSyncMode) String() string {
	if m == 0 || int(m) > len(stateSyncModeNames) {
		return "INVALID"
	}
	return stateSyncModeNames[m-1]
}

// Validate reports whether m is a known mode.
func (m StateSyncMode) Validate() error {
	if m == 0 || int(m) > len(stateSyncModeNames) {
		return fmt.Errorf("%w: state sync mode %d", ErrInvalidValue, uint8(m))
	}
	return nil
}

// MarshalJSON encodes the mode as its contract name.
func (m StateSyncMode) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return jsonMarshal(m.String())
}

// UnmarshalJSON decodes the mode name strictly.
func (m *StateSyncMode) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range stateSyncModeNames {
		if n == name {
			*m = StateSyncMode(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown state sync mode %q", ErrInvalidValue, name)
}

// CheckpointKind distinguishes the two Heimdall finality sources. A
// milestone hash is the Bor end block hash; a checkpoint root is a range
// commitment. They are not interchangeable.
type CheckpointKind uint8

// Checkpoint kinds. Zero is not a valid kind.
const (
	CheckpointMilestone CheckpointKind = iota + 1
	CheckpointArea
)

var checkpointKindNames = []string{"MILESTONE", "CHECKPOINT"}

// String returns the contract name of k.
func (k CheckpointKind) String() string {
	if k == 0 || int(k) > len(checkpointKindNames) {
		return "INVALID"
	}
	return checkpointKindNames[k-1]
}

// Validate reports whether k is a known kind.
func (k CheckpointKind) Validate() error {
	if k == 0 || int(k) > len(checkpointKindNames) {
		return fmt.Errorf("%w: checkpoint kind %d", ErrInvalidValue, uint8(k))
	}
	return nil
}

// MarshalJSON encodes the kind as its contract name.
func (k CheckpointKind) MarshalJSON() ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return jsonMarshal(k.String())
}

// UnmarshalJSON decodes the kind name strictly.
func (k *CheckpointKind) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range checkpointKindNames {
		if n == name {
			*k = CheckpointKind(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown checkpoint kind %q", ErrInvalidValue, name)
}

// HeimdallCheckpointRef references the Heimdall milestone or checkpoint that
// covers this block. It is a reference to the native observation, not the
// finality evidence itself and not a coverage claim; the full milestone,
// checkpoint, whitelist and L1 checkpoint payloads belong to the Polygon
// evidence schema.
type HeimdallCheckpointRef struct {
	Kind CheckpointKind `json:"kind"`
	// End is the end anchor of the covered range: the Bor end block for a
	// milestone, the range end for a checkpoint.
	End AnchorRef `json:"end"`
	// CheckpointID is the Heimdall checkpoint id when the source provided
	// it; milestones carry their own string identity in the evidence
	// schema and do not invent one here.
	CheckpointID Optional[uint64] `json:"checkpoint_id"`
}

// Validate checks the closed kind and the anchor.
func (r HeimdallCheckpointRef) Validate() error {
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if err := r.End.Validate(); err != nil {
		return err
	}
	return nil
}

// EthBlockDetails is the ETH family fork context of a block. The optional
// beacon root and requests hash (post-Prague) are header fields and stay in
// Header; these are the Geth-only extensions.
type EthBlockDetails struct {
	// BlockAccessListHash is the optional Geth block access list
	// commitment.
	BlockAccessListHash Optional[Hash32] `json:"block_access_list_hash"`
	// SlotNumber is the optional Geth slot number.
	SlotNumber Optional[uint64] `json:"slot_number"`
}

// Validate always passes; both fields are optional observations.
func (d *EthBlockDetails) Validate() error { return nil }

// BscBlockDetails is the BSC family fork context of a block.
type BscBlockDetails struct {
	// ParliaSystemTransactionCount is the observed count of
	// Parlia-classified native system transactions in this block body. It
	// is an observation of the source classification, never a statement
	// that all system work of the block was captured, and never a
	// completeness assertion for the block.
	ParliaSystemTransactionCount Optional[uint64] `json:"parlia_system_transaction_count"`
	// VoteSnapshot cites the Parlia attestation anchors merged into the
	// native header snapshot observed with this block's import. It is a
	// reference; vote-pool state and early vote evidence belong to the
	// BSC evidence schema and the lifecycle validator.
	VoteSnapshot Optional[ParliaVoteSnapshot] `json:"vote_snapshot"`
}

// Validate checks the inner reference when present.
func (d *BscBlockDetails) Validate() error {
	if s, ok := d.VoteSnapshot.Get(); ok {
		return s.Validate()
	}
	return nil
}

// ParliaVoteSnapshot is the merged native snapshot VoteData shape observed
// with one block import: the attested justified and finalized anchors. It is
// not the Source of an arbitrarily chosen last signature.
type ParliaVoteSnapshot struct {
	Justified AnchorRef `json:"justified"`
	Finalized AnchorRef `json:"finalized"`
}

// Validate requires real anchors for attested references.
func (s ParliaVoteSnapshot) Validate() error {
	if err := s.Justified.Validate(); err != nil {
		return err
	}
	return s.Finalized.Validate()
}

// BorBlockDetails is the Polygon (Bor) family fork context of a block.
type BorBlockDetails struct {
	// SprintLength is the applicable Bor sprint length at this height,
	// from the pinned fork configuration. Observed context, not a
	// derivation of this package.
	SprintLength Optional[uint64] `json:"sprint_length"`
	// SprintEnd reports that this block was observed as the last block of
	// its sprint, the boundary where Bor commits span and StateSync work.
	SprintEnd Optional[bool] `json:"sprint_end"`
	// SideChainBlock reports that the block was stored through the native
	// side-chain path (stored without a head change), observed from the
	// import decision. Reference to the import observation, not a
	// canonicality claim.
	SideChainBlock Optional[bool] `json:"side_chain_block"`
	// StateSyncCount is the observed count of StateSync records committed
	// with this block, including body StateSync transactions after
	// Madhugiri. Observation only; zero is a valid observed value.
	StateSyncCount Optional[uint64] `json:"state_sync_count"`
	// StateSyncMode records how the observed StateSync records were
	// committed. Required when StateSyncCount is present and greater than
	// zero.
	StateSyncMode Optional[StateSyncMode] `json:"state_sync_mode"`
	// HeimdallCheckpoints references the Heimdall milestone or checkpoint
	// covering this block, when observed. References only; an empty list
	// means no covering reference was observed, not that none exists.
	HeimdallCheckpoints []HeimdallCheckpointRef `json:"heimdall_checkpoints"`
}

// Validate checks the closed mode and the references.
func (d *BorBlockDetails) Validate() error {
	if count, ok := d.StateSyncCount.Get(); ok && count > 0 {
		if mode, present := d.StateSyncMode.Get(); !present {
			return fmt.Errorf("%w: state sync count %d without a mode", ErrInvalidValue, count)
		} else if err := mode.Validate(); err != nil {
			return err
		}
	}
	for i, ref := range d.HeimdallCheckpoints {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: heimdall checkpoint %d: %v", ErrInvalidValue, i, err)
		}
	}
	return nil
}

// FamilyDetails is the closed per-family fork context: exactly one variant
// is present, and there is no ANY or unknown fallback. It mirrors the
// Receipt.network_details convention: an explicitly present variant may be
// structurally empty when the family adds no fields.
type FamilyDetails struct {
	Eth *EthBlockDetails
	Bsc *BscBlockDetails
	Bor *BorBlockDetails
}

// Family returns the closed family of the present variant.
func (d FamilyDetails) Family() Family {
	switch {
	case d.Eth != nil:
		return FamilyEth
	case d.Bsc != nil:
		return FamilyBsc
	case d.Bor != nil:
		return FamilyPoly
	default:
		return ""
	}
}

// Validate requires exactly one variant and validates it.
func (d FamilyDetails) Validate() error {
	seen := 0
	for _, p := range []bool{d.Eth != nil, d.Bsc != nil, d.Bor != nil} {
		if p {
			seen++
		}
	}
	if seen != 1 {
		return fmt.Errorf("%w: family details has %d variants, want exactly 1", ErrInvalidValue, seen)
	}
	switch {
	case d.Eth != nil:
		return d.Eth.Validate()
	case d.Bsc != nil:
		return d.Bsc.Validate()
	default:
		return d.Bor.Validate()
	}
}

// MarshalJSON encodes the oneof as {"family":"ETH","eth":{...}}.
func (d FamilyDetails) MarshalJSON() ([]byte, error) {
	switch {
	case d.Eth != nil:
		return jsonMarshal(struct {
			Family string           `json:"family"`
			Eth    *EthBlockDetails `json:"eth"`
		}{string(FamilyEth), d.Eth})
	case d.Bsc != nil:
		return jsonMarshal(struct {
			Family string           `json:"family"`
			Bsc    *BscBlockDetails `json:"bsc"`
		}{string(FamilyBsc), d.Bsc})
	case d.Bor != nil:
		return jsonMarshal(struct {
			Family string           `json:"family"`
			Bor    *BorBlockDetails `json:"bor"`
		}{string(FamilyPoly), d.Bor})
	default:
		return nil, fmt.Errorf("%w: family details has no variant", ErrInvalidValue)
	}
}

// UnmarshalJSON decodes the oneof strictly: the family name must match the
// present variant, unknown names and a missing variant are errors.
func (d *FamilyDetails) UnmarshalJSON(data []byte) error {
	var raw struct {
		Family string           `json:"family"`
		Eth    *EthBlockDetails `json:"eth"`
		Bsc    *BscBlockDetails `json:"bsc"`
		Bor    *BorBlockDetails `json:"bor"`
	}
	if err := jsonUnmarshal(data, &raw); err != nil {
		return err
	}
	d.Eth, d.Bsc, d.Bor = raw.Eth, raw.Bsc, raw.Bor
	var f Family
	switch raw.Family {
	case string(FamilyEth):
		f = FamilyEth
	case string(FamilyBsc):
		f = FamilyBsc
	case string(FamilyPoly):
		f = FamilyPoly
	case "":
		return fmt.Errorf("%w: family details without a family name", ErrInvalidValue)
	default:
		return fmt.Errorf("%w: unknown family %q", ErrInvalidValue, raw.Family)
	}
	if d.Family() != f {
		return fmt.Errorf("%w: family %q does not match the present variant", ErrInvalidValue, raw.Family)
	}
	return d.Validate()
}

// BlockMetadata is the observed metadata of one block: identity, typed
// header with source precision timestamp, the observed source counts and
// the closed family fork context.
//
// SourceTransactionCount comes from the native body representation of the
// chosen client. It is an observation; it never asserts block completeness
// (D25), and an empty block is a first-class record with count 0.
type BlockMetadata struct {
	Number     uint64    `json:"number"`
	Hash       Hash32    `json:"hash"`
	ParentHash Hash32    `json:"parent_hash"`
	Timestamp  Timestamp `json:"timestamp"`

	// SourceTransactionCount is the observed transaction count of the
	// native body representation. Zero is the observed empty block.
	SourceTransactionCount uint64 `json:"source_transaction_count"`

	Header  Header        `json:"header"`
	Details FamilyDetails `json:"details"`
}

// Validate checks the schema-level invariants. It deliberately imposes no
// minimum on any count: a count field is an observation and can never
// affect completeness or lifecycle validity.
func (m *BlockMetadata) Validate() error {
	if m.Hash.IsZero() {
		return fmt.Errorf("%w: block hash is zero", ErrInvalidValue)
	}
	// A real genesis has a source hash; only the genesis parent may be the
	// all-zero hash.
	if m.ParentHash.IsZero() && m.Number != 0 {
		return fmt.Errorf("%w: block %d has a zero parent hash", ErrInvalidValue, m.Number)
	}
	if err := m.Timestamp.Validate(); err != nil {
		return fmt.Errorf("%w: timestamp: %v", ErrInvalidValue, err)
	}
	if err := m.Header.Validate(); err != nil {
		return err
	}
	if m.Header.Number != m.Number {
		return fmt.Errorf("%w: block number %d but header number %d", ErrInvalidValue, m.Number, m.Header.Number)
	}
	if m.Header.ParentHash != m.ParentHash {
		return fmt.Errorf("%w: block parent hash differs from header parent hash", ErrInvalidValue)
	}
	if err := m.Details.Validate(); err != nil {
		return err
	}
	return nil
}

// ObservationRef identifies the admitted native observation whose decision
// produced a record: producer, run and the observation sequence. It is a
// reference to the admission boundary, not an assertion about the block.
type ObservationRef struct {
	ProducerID          Id16   `json:"producer_id"`
	RunID               Id16   `json:"run_id"`
	ObservationSequence uint64 `json:"observation_sequence"`
}

// Validate requires assigned producer and run identities.
func (r ObservationRef) Validate() error {
	if r.ProducerID.IsZero() {
		return fmt.Errorf("%w: observation producer id is zero", ErrInvalidValue)
	}
	if r.RunID.IsZero() {
		return fmt.Errorf("%w: observation run id is zero", ErrInvalidValue)
	}
	return nil
}
