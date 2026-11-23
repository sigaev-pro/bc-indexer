package finalityobserver

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
	"github.com/sigaev-pro/bc-indexer/protocol/profiles"
)

// ErrInvalidValue rejects a malformed evidence value. A native malformed or
// wrong-identity result is an error, never an Unavailable and never an
// invented anchor.
var ErrInvalidValue = errors.New("finality_observer: invalid evidence value")

// Operation is the closed operation/selector union of one source request
// against the own Heimdall (CURRENT_SEMANTICS SourceObservationRef).
type Operation uint8

// Operations. Zero is not a valid operation.
const (
	OpMilestoneLatest Operation = iota + 1
	OpCheckpointLatest
	OpCheckpointBuffer
	OpChainParams
	OpCheckpointParams
	OpMilestoneByNumber
	OpCheckpointByNumber
	OpMilestoneEvent
)

var operationNames = []string{
	"MilestoneLatest", "CheckpointLatest", "CheckpointBuffer", "ChainParams",
	"CheckpointParams", "MilestoneByNumber", "CheckpointByNumber", "MilestoneEvent",
}

// String returns the contract name of op.
func (op Operation) String() string {
	if op == 0 || int(op) > len(operationNames) {
		return "INVALID"
	}
	return operationNames[op-1]
}

// Validate reports whether op is a known operation.
func (op Operation) Validate() error {
	if op == 0 || int(op) > len(operationNames) {
		return fmt.Errorf("%w: operation %d", ErrInvalidValue, uint8(op))
	}
	return nil
}

// MarshalJSON encodes the operation as its contract name.
func (op Operation) MarshalJSON() ([]byte, error) {
	if err := op.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(op.String())
}

// UnmarshalJSON decodes the operation name strictly.
func (op *Operation) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range operationNames {
		if n == name {
			*op = Operation(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown operation %q", ErrInvalidValue, name)
}

// Request is one source request: the operation and, when the operation has
// one, its selector. MilestoneLatest, CheckpointLatest, CheckpointBuffer,
// ChainParams and CheckpointParams carry no selector; MilestoneByNumber and
// CheckpointByNumber carry a u64 number; MilestoneEvent carries a height
// and an event ordinal. No selector is invented for an operation that has
// none.
type Request struct {
	Operation Operation `json:"operation"`
	// Number is the ByNumber selector.
	Number block.Optional[uint64] `json:"number"`
	// Height and EventOrdinal are the MilestoneEvent selector.
	Height       block.Optional[uint64] `json:"height"`
	EventOrdinal block.Optional[uint64] `json:"event_ordinal"`
}

// Validate checks the selector presence against the closed union.
func (r Request) Validate() error {
	if err := r.Operation.Validate(); err != nil {
		return err
	}
	number, hasNumber := r.Number.Get()
	_, hasHeight := r.Height.Get()
	_, hasOrdinal := r.EventOrdinal.Get()
	switch r.Operation {
	case OpMilestoneByNumber, OpCheckpointByNumber:
		if !hasNumber || hasHeight || hasOrdinal {
			return fmt.Errorf("%w: %s requires exactly a number selector", ErrInvalidValue, r.Operation)
		}
	case OpMilestoneEvent:
		if hasNumber || !hasHeight || !hasOrdinal {
			return fmt.Errorf("%w: MilestoneEvent requires exactly a height and an event ordinal", ErrInvalidValue)
		}
	default:
		if hasNumber || hasHeight || hasOrdinal {
			return fmt.Errorf("%w: %s takes no selector", ErrInvalidValue, r.Operation)
		}
	}
	if hasNumber && number == 0 {
		return fmt.Errorf("%w: number selector 0", ErrInvalidValue)
	}
	return nil
}

// SourceObservationRef binds one observation to its pinned sources and its
// exact response. The sources_binding_digest is the mandatory D80 Hash32:
// the SHA-256 of the exact verified bytes of the sources_file pinned for
// this observation; it is carried with the observation, so a later file
// replacement never rewrites it. The exact response bytes stay local, only
// their digest is public.
type SourceObservationRef struct {
	SourceStoreIdentity block.Id16                      `json:"source_store_identity"`
	SourcesBinding      block.Hash32                    `json:"sources_binding_digest"`
	Heimdall            HeimdallIdentity                `json:"heimdall_identity"`
	ObservationID       string                          `json:"observation_id"`
	RequestSequence     uint64                          `json:"request_sequence"`
	Request             Request                         `json:"request"`
	CommittedBlock      block.Optional[block.AnchorRef] `json:"committed_heimdall_block"`
	ResponseDigest      block.Hash32                    `json:"response_digest"`
}

// Validate checks the binding shape. The D80 digest is a full Hash32; a
// zero digest is absent, and absence is a schema error.
func (r SourceObservationRef) Validate() error {
	if r.SourceStoreIdentity.IsZero() {
		return fmt.Errorf("%w: source store identity is zero", ErrInvalidValue)
	}
	if r.SourcesBinding.IsZero() {
		return fmt.Errorf("%w: sources_binding_digest is mandatory (D80)", ErrInvalidValue)
	}
	if r.Heimdall.ChainID == "" || r.Heimdall.GenesisDocumentSHA256.IsZero() {
		return fmt.Errorf("%w: heimdall identity is incomplete", ErrInvalidValue)
	}
	if r.ObservationID == "" {
		return fmt.Errorf("%w: observation id is empty", ErrInvalidValue)
	}
	if err := r.Request.Validate(); err != nil {
		return err
	}
	if ref, ok := r.CommittedBlock.Get(); ok {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: committed heimdall block: %v", ErrInvalidValue, err)
		}
	}
	if r.ResponseDigest.IsZero() {
		return fmt.Errorf("%w: response digest is zero", ErrInvalidValue)
	}
	return nil
}

// BindingDigest returns the D80 sources_binding_digest of this observation
// binding. It is exactly profiles.ComputeSourcesBindingDigest of the file
// bytes that were pinned when the observation was made.
func (r SourceObservationRef) BindingDigest() profiles.SourcesBindingDigest {
	return profiles.SourcesBindingDigest(r.SourcesBinding)
}

// MilestoneOutcome is the closed native outcome of one milestone
// observation.
type MilestoneOutcome uint8

// Milestone outcomes. Zero is not a valid outcome.
const (
	MilestoneObserved MilestoneOutcome = iota + 1
	MilestoneFutureQueued
	MilestoneLocallyVerified
	MilestoneRejected
)

var milestoneOutcomeNames = []string{"OBSERVED", "FUTURE_QUEUED", "LOCALLY_VERIFIED", "REJECTED"}

// String returns the contract name of m.
func (m MilestoneOutcome) String() string {
	if m == 0 || int(m) > len(milestoneOutcomeNames) {
		return "INVALID"
	}
	return milestoneOutcomeNames[m-1]
}

// Validate reports whether m is a known outcome.
func (m MilestoneOutcome) Validate() error {
	if m == 0 || int(m) > len(milestoneOutcomeNames) {
		return fmt.Errorf("%w: milestone outcome %d", ErrInvalidValue, uint8(m))
	}
	return nil
}

// MarshalJSON encodes the outcome as its contract name.
func (m MilestoneOutcome) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(m.String())
}

// UnmarshalJSON decodes the outcome name strictly.
func (m *MilestoneOutcome) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range milestoneOutcomeNames {
		if n == name {
			*m = MilestoneOutcome(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown milestone outcome %q", ErrInvalidValue, name)
}

// Milestone is one native Heimdall milestone status. end_hash is the Bor
// end block hash; it is never interchangeable with a checkpoint root.
type Milestone struct {
	Source          SourceObservationRef   `json:"source"`
	Number          block.Optional[uint64] `json:"number"`
	MilestoneID     string                 `json:"milestone_id"`
	Proposer        block.Address20        `json:"proposer"`
	Start           uint64                 `json:"start"`
	End             uint64                 `json:"end"`
	Timestamp       uint64                 `json:"timestamp"`
	TotalDifficulty uint64                 `json:"total_difficulty"`
	EndHash         block.Hash32           `json:"end_hash"`
	Outcome         MilestoneOutcome       `json:"outcome"`
}

// Validate checks the source binding and the range.
func (m Milestone) Validate() error {
	if err := m.Source.Validate(); err != nil {
		return err
	}
	if m.MilestoneID == "" {
		return fmt.Errorf("%w: milestone id is empty", ErrInvalidValue)
	}
	if m.EndHash.IsZero() {
		return fmt.Errorf("%w: milestone end hash is zero", ErrInvalidValue)
	}
	if m.End < m.Start {
		return fmt.Errorf("%w: milestone end %d below start %d", ErrInvalidValue, m.End, m.Start)
	}
	return m.Outcome.Validate()
}

// Anchor is the milestone end anchor: height end, hash end_hash.
func (m Milestone) Anchor() block.AnchorRef {
	return block.AnchorRef{Height: m.End, Hash: m.EndHash}
}

// CheckpointState is the closed native state of one Heimdall checkpoint.
type CheckpointState uint8

// Checkpoint states. Zero is not a valid state.
const (
	CheckpointBuffered CheckpointState = iota + 1
	CheckpointStoredAcknowledged
)

var checkpointStateNames = []string{"BUFFERED", "STORED_ACKNOWLEDGED"}

// String returns the contract name of c.
func (c CheckpointState) String() string {
	if c == 0 || int(c) > len(checkpointStateNames) {
		return "INVALID"
	}
	return checkpointStateNames[c-1]
}

// Validate reports whether c is a known state.
func (c CheckpointState) Validate() error {
	if c == 0 || int(c) > len(checkpointStateNames) {
		return fmt.Errorf("%w: checkpoint state %d", ErrInvalidValue, uint8(c))
	}
	return nil
}

// MarshalJSON encodes the state as its contract name.
func (c CheckpointState) MarshalJSON() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(c.String())
}

// UnmarshalJSON decodes the state name strictly.
func (c *CheckpointState) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range checkpointStateNames {
		if n == name {
			*c = CheckpointState(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown checkpoint state %q", ErrInvalidValue, name)
}

// Checkpoint is one native Heimdall checkpoint status. root_hash is the
// range commitment; it is never used as a block hash. locally_verified_end
// is the verified end anchor when the native range verification produced
// one.
type Checkpoint struct {
	Source           SourceObservationRef            `json:"source"`
	CheckpointID     block.Optional[uint64]          `json:"checkpoint_id"`
	Proposer         block.Address20                 `json:"proposer"`
	Start            uint64                          `json:"start"`
	End              uint64                          `json:"end"`
	Timestamp        uint64                          `json:"timestamp"`
	RootHash         block.Hash32                    `json:"root_hash"`
	State            CheckpointState                 `json:"state"`
	LocalVerifiedEnd block.Optional[block.AnchorRef] `json:"locally_verified_end"`
}

// Validate checks the source binding and the range.
func (c Checkpoint) Validate() error {
	if err := c.Source.Validate(); err != nil {
		return err
	}
	if c.RootHash.IsZero() {
		return fmt.Errorf("%w: checkpoint root hash is zero", ErrInvalidValue)
	}
	if c.End < c.Start {
		return fmt.Errorf("%w: checkpoint end %d below start %d", ErrInvalidValue, c.End, c.Start)
	}
	if err := c.State.Validate(); err != nil {
		return err
	}
	if ref, ok := c.LocalVerifiedEnd.Get(); ok {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: locally verified end: %v", ErrInvalidValue, err)
		}
		if ref.Hash == c.RootHash {
			return fmt.Errorf("%w: locally verified end hash equals the checkpoint root", ErrInvalidValue)
		}
	}
	return nil
}

// WhitelistKind is the closed kind of one native whitelist.
type WhitelistKind uint8

// Whitelist kinds. Zero is not a valid kind.
const (
	WhitelistMilestone WhitelistKind = iota + 1
	WhitelistCheckpoint
)

var whitelistKindNames = []string{"MILESTONE", "CHECKPOINT"}

// String returns the contract name of k.
func (k WhitelistKind) String() string {
	if k == 0 || int(k) > len(whitelistKindNames) {
		return "INVALID"
	}
	return whitelistKindNames[k-1]
}

// Validate reports whether k is a known kind.
func (k WhitelistKind) Validate() error {
	if k == 0 || int(k) > len(whitelistKindNames) {
		return fmt.Errorf("%w: whitelist kind %d", ErrInvalidValue, uint8(k))
	}
	return nil
}

// MarshalJSON encodes the kind as its contract name.
func (k WhitelistKind) MarshalJSON() ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(k.String())
}

// UnmarshalJSON decodes the kind name strictly.
func (k *WhitelistKind) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range whitelistKindNames {
		if n == name {
			*k = WhitelistKind(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown whitelist kind %q", ErrInvalidValue, name)
}

// Whitelist is one accepted native whitelist. It is accepted only after
// the canonical match; the anchor is the exact block anchor, never a
// checkpoint root. The source_evidence is absent in the narrower retained
// form: a persisted whitelist without the saved full response publishes
// without a SourceObservationRef and without an invented digest (D80).
type Whitelist struct {
	Kind                  WhitelistKind                        `json:"kind"`
	Anchor                block.AnchorRef                      `json:"anchor"`
	Decision              block.NativeDecisionRef              `json:"native_decision"`
	SourceEvidence        block.Optional[SourceObservationRef] `json:"source_evidence"`
	AncestryBindingDigest block.Hash32                         `json:"ancestry_binding_digest"`
}

// Validate checks the closed shape.
func (w Whitelist) Validate() error {
	if err := w.Kind.Validate(); err != nil {
		return err
	}
	if err := w.Anchor.Validate(); err != nil {
		return err
	}
	if err := w.Decision.Validate(); err != nil {
		return err
	}
	if ref, ok := w.SourceEvidence.Get(); ok {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: whitelist source evidence: %v", ErrInvalidValue, err)
		}
	}
	if w.AncestryBindingDigest.IsZero() {
		return fmt.Errorf("%w: ancestry binding digest is zero", ErrInvalidValue)
	}
	if w.AncestryBindingDigest != WhitelistAncestryDigest(w.Kind, w.Anchor, w.Decision) {
		return fmt.Errorf("%w: ancestry binding digest mismatch", ErrInvalidValue)
	}
	return nil
}

// Narrower reports whether this is the narrower retained form without the
// saved full source response.
func (w Whitelist) Narrower() bool {
	_, ok := w.SourceEvidence.Get()
	return !ok
}

// NativeFinalizedBasis is the closed basis of the native finalized
// projection: the milestone whitelist first, the checkpoint whitelist as
// the fallback.
type NativeFinalizedBasis uint8

// Native finalized bases. Zero is not a valid basis.
const (
	BasisMilestoneWhitelist NativeFinalizedBasis = iota + 1
	BasisCheckpointWhitelist
)

var nativeFinalizedBasisNames = []string{"MILESTONE_WHITELIST", "CHECKPOINT_WHITELIST"}

// String returns the contract name of b.
func (b NativeFinalizedBasis) String() string {
	if b == 0 || int(b) > len(nativeFinalizedBasisNames) {
		return "INVALID"
	}
	return nativeFinalizedBasisNames[b-1]
}

// Validate reports whether b is a known basis.
func (b NativeFinalizedBasis) Validate() error {
	if b == 0 || int(b) > len(nativeFinalizedBasisNames) {
		return fmt.Errorf("%w: native finalized basis %d", ErrInvalidValue, uint8(b))
	}
	return nil
}

// MarshalJSON encodes the basis as its contract name.
func (b NativeFinalizedBasis) MarshalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(b.String())
}

// UnmarshalJSON decodes the basis name strictly.
func (b *NativeFinalizedBasis) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range nativeFinalizedBasisNames {
		if n == name {
			*b = NativeFinalizedBasis(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown native finalized basis %q", ErrInvalidValue, name)
}

// NativeFinalized is the native Polygon finalized observation. It is a
// native observation, which Bor can revise on recovery; it is not the L1
// GC anchor.
type NativeFinalized struct {
	Basis             NativeFinalizedBasis    `json:"basis"`
	Anchor            block.AnchorRef         `json:"anchor"`
	WhitelistDecision block.NativeDecisionRef `json:"whitelist_decision"`
}

// Validate checks the basis and anchors.
func (f NativeFinalized) Validate() error {
	if err := f.Basis.Validate(); err != nil {
		return err
	}
	if err := f.Anchor.Validate(); err != nil {
		return err
	}
	return f.WhitelistDecision.Validate()
}

// FinalizedContractState is the exact hash-pinned record of the RootChain
// read: the L1 block hash the call ran at, the digests of the checked
// RootChain parameters, of the calldata and of the exact result bytes, and
// the decoded createdAt and proposer.
type FinalizedContractState struct {
	CallBlockHash         block.Hash32    `json:"call_block_hash"`
	RootChainParamsDigest block.Hash32    `json:"root_chain_params_digest"`
	CalldataDigest        block.Hash32    `json:"calldata_digest"`
	ResultDigest          block.Hash32    `json:"result_digest"`
	ResultCreatedAt       block.UInt256   `json:"result_created_at"`
	ResultProposer        block.Address20 `json:"result_proposer"`
}

// Validate requires the digests and identities to be present.
func (v FinalizedContractState) Validate() error {
	if v.CallBlockHash.IsZero() {
		return fmt.Errorf("%w: call block hash is zero", ErrInvalidValue)
	}
	if v.RootChainParamsDigest.IsZero() || v.CalldataDigest.IsZero() || v.ResultDigest.IsZero() {
		return fmt.Errorf("%w: contract state digest is zero", ErrInvalidValue)
	}
	if v.ResultProposer.IsZero() {
		return fmt.Errorf("%w: result proposer is zero", ErrInvalidValue)
	}
	return nil
}

// L1Checkpoint is the separately verified L1-finalized checkpoint evidence
// for GC: the own L1 network and supporting node identity, the mandatory
// D80 sources_binding_digest pinned for the observation, the RootChain
// contract address and checkpoint identity, the checked header block id,
// the Bor range with its binding digest and verified end anchor, the
// finalized L1 anchor F the read ran at, and the exact contract state.
type L1Checkpoint struct {
	L1Network          block.NetworkIdentity  `json:"l1_network"`
	SupportingNodeID   block.Id16             `json:"supporting_node_id"`
	SourcesBinding     block.Hash32           `json:"sources_binding_digest"`
	RootChainAddress   block.Address20        `json:"root_chain_address"`
	CheckpointID       uint64                 `json:"checkpoint_id"`
	HeaderBlockID      block.UInt256          `json:"header_block_id"`
	ChildBlockInterval uint64                 `json:"child_block_interval"`
	BorStart           uint64                 `json:"bor_start"`
	BorEnd             uint64                 `json:"bor_end"`
	CheckpointRoot     block.Hash32           `json:"checkpoint_root"`
	BorEndRef          block.AnchorRef        `json:"bor_end_ref"`
	BorRangeDigest     block.Hash32           `json:"bor_range_binding_digest"`
	FinalizedL1Anchor  block.AnchorRef        `json:"finalized_l1_anchor"`
	Verification       FinalizedContractState `json:"verification"`
}

// Validate checks the closed shape, the D80 digest and the range digests.
func (e L1Checkpoint) Validate() error {
	if err := e.L1Network.Validate(); err != nil {
		return err
	}
	if e.SupportingNodeID.IsZero() {
		return fmt.Errorf("%w: supporting node id is zero", ErrInvalidValue)
	}
	if e.SourcesBinding.IsZero() {
		return fmt.Errorf("%w: sources_binding_digest is mandatory (D80)", ErrInvalidValue)
	}
	if e.RootChainAddress.IsZero() {
		return fmt.Errorf("%w: root chain address is zero", ErrInvalidValue)
	}
	if e.CheckpointID == 0 {
		return fmt.Errorf("%w: checkpoint id is zero", ErrInvalidValue)
	}
	if e.HeaderBlockID.IsZero() {
		return fmt.Errorf("%w: header block id is zero", ErrInvalidValue)
	}
	if e.ChildBlockInterval == 0 {
		return fmt.Errorf("%w: child block interval is zero", ErrInvalidValue)
	}
	if e.BorEnd < e.BorStart {
		return fmt.Errorf("%w: bor end %d below start %d", ErrInvalidValue, e.BorEnd, e.BorStart)
	}
	if e.CheckpointRoot.IsZero() {
		return fmt.Errorf("%w: checkpoint root is zero", ErrInvalidValue)
	}
	if err := e.BorEndRef.Validate(); err != nil {
		return err
	}
	if e.BorEndRef.Hash == e.CheckpointRoot {
		return fmt.Errorf("%w: bor end ref hash equals the checkpoint root", ErrInvalidValue)
	}
	if e.BorEndRef.Height != e.BorEnd {
		return fmt.Errorf("%w: bor end ref height %d differs from bor end %d", ErrInvalidValue, e.BorEndRef.Height, e.BorEnd)
	}
	if e.BorRangeDigest != BorRangeDigest(e.BorStart, e.BorEnd, e.BorEndRef) {
		return fmt.Errorf("%w: bor range binding digest mismatch", ErrInvalidValue)
	}
	if err := e.FinalizedL1Anchor.Validate(); err != nil {
		return err
	}
	if e.FinalizedL1Anchor.Hash != e.Verification.CallBlockHash {
		return fmt.Errorf("%w: finalized l1 anchor differs from the call block hash", ErrInvalidValue)
	}
	return e.Verification.Validate()
}

// BindingDigest returns the D80 digest pinned for this evidence.
func (e L1Checkpoint) BindingDigest() profiles.SourcesBindingDigest {
	return profiles.SourcesBindingDigest(e.SourcesBinding)
}

// EvidenceKind is the closed trichotomy of one Polygon evidence field:
// Known carries the value and its origin, NotObserved carries nothing, and
// Unavailable carries one explicit reason of one concrete optional
// finality/GC lookup attempt. Unavailable is not a substitute for an error
// of mandatory execution capture and never erases a retained accepted
// anchor; a native malformed or wrong-identity result is an error instead.
type EvidenceKind uint8

// Evidence kinds. Zero is not a valid kind.
const (
	EvidenceKnown EvidenceKind = iota + 1
	EvidenceNotObserved
	EvidenceUnavailable
)

var evidenceKindNames = []string{"KNOWN", "NOT_OBSERVED", "UNAVAILABLE"}

// String returns the contract name of k.
func (k EvidenceKind) String() string {
	if k == 0 || int(k) > len(evidenceKindNames) {
		return "INVALID"
	}
	return evidenceKindNames[k-1]
}

// Validate reports whether k is a known kind.
func (k EvidenceKind) Validate() error {
	if k == 0 || int(k) > len(evidenceKindNames) {
		return fmt.Errorf("%w: evidence kind %d", ErrInvalidValue, uint8(k))
	}
	return nil
}

// Evidence is one typed Polygon evidence field. T is a comparable value
// type; every closed evidence type of this package satisfies that.
type Evidence[T comparable] struct {
	Kind   EvidenceKind
	Value  T
	Origin block.EvidenceOrigin
	Reason block.UnavailableReason
}

// KnownOf builds a known evidence value.
func KnownOf[T comparable](v T, origin block.EvidenceOrigin) Evidence[T] {
	return Evidence[T]{Kind: EvidenceKnown, Value: v, Origin: origin}
}

// NotObservedOf builds the not-observed state.
func NotObservedOf[T comparable]() Evidence[T] {
	return Evidence[T]{Kind: EvidenceNotObserved}
}

// UnavailableOf builds one explicit unavailable state of a concrete lookup
// attempt.
func UnavailableOf[T comparable](reason block.UnavailableReason) Evidence[T] {
	return Evidence[T]{Kind: EvidenceUnavailable, Reason: reason}
}

// Get returns the value when this evidence is Known.
func (e Evidence[T]) Get() (T, bool) {
	if e.Kind != EvidenceKnown {
		var zero T
		return zero, false
	}
	return e.Value, true
}

// Validate checks the kind-dependent shape and runs validate on a known
// value.
func (e Evidence[T]) Validate(validate func(T) error) error {
	if err := e.Kind.Validate(); err != nil {
		return err
	}
	switch e.Kind {
	case EvidenceKnown:
		if err := e.Origin.Validate(); err != nil {
			return err
		}
		if e.Reason != 0 {
			return fmt.Errorf("%w: known evidence carries an unavailable reason", ErrInvalidValue)
		}
		if validate != nil {
			return validate(e.Value)
		}
	case EvidenceUnavailable:
		if err := e.Reason.Validate(); err != nil {
			return err
		}
	default:
		var zero T
		if e.Value != zero || e.Origin != 0 || e.Reason != 0 {
			return fmt.Errorf("%w: %s evidence carries value, origin or reason", ErrInvalidValue, e.Kind)
		}
	}
	return nil
}

// evidenceJSON is the wire shape of one evidence field: only the fields
// its kind allows, so an unknown state never carries an invented value.
type evidenceJSON struct {
	Kind   string          `json:"kind"`
	Value  json.RawMessage `json:"value,omitempty"`
	Origin string          `json:"origin,omitempty"`
	Reason string          `json:"reason,omitempty"`
}

// MarshalJSON encodes the kind-dependent shape.
func (e Evidence[T]) MarshalJSON() ([]byte, error) {
	out := evidenceJSON{Kind: e.Kind.String()}
	switch e.Kind {
	case EvidenceKnown:
		if err := e.Origin.Validate(); err != nil {
			return nil, err
		}
		raw, err := json.Marshal(e.Value)
		if err != nil {
			return nil, err
		}
		out.Value = raw
		out.Origin = e.Origin.String()
	case EvidenceUnavailable:
		if err := e.Reason.Validate(); err != nil {
			return nil, err
		}
		out.Reason = e.Reason.String()
	}
	return json.Marshal(out)
}

// UnmarshalJSON decodes the kind-dependent shape strictly.
func (e *Evidence[T]) UnmarshalJSON(data []byte) error {
	var raw evidenceJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch raw.Kind {
	case "KNOWN":
		if raw.Value == nil || raw.Origin == "" || raw.Reason != "" {
			return fmt.Errorf("%w: known evidence needs a value and an origin", ErrInvalidValue)
		}
		var v T
		if err := json.Unmarshal(raw.Value, &v); err != nil {
			return err
		}
		origin := originByName(raw.Origin)
		if origin == 0 {
			return fmt.Errorf("%w: unknown evidence origin %q", ErrInvalidValue, raw.Origin)
		}
		*e = Evidence[T]{Kind: EvidenceKnown, Value: v, Origin: origin}
	case "NOT_OBSERVED":
		if raw.Value != nil || raw.Origin != "" || raw.Reason != "" {
			return fmt.Errorf("%w: not-observed evidence carries fields", ErrInvalidValue)
		}
		*e = NotObservedOf[T]()
	case "UNAVAILABLE":
		if raw.Value != nil || raw.Origin != "" || raw.Reason == "" {
			return fmt.Errorf("%w: unavailable evidence needs a reason only", ErrInvalidValue)
		}
		reason := reasonByName(raw.Reason)
		if reason == 0 {
			return fmt.Errorf("%w: unknown unavailable reason %q", ErrInvalidValue, raw.Reason)
		}
		*e = UnavailableOf[T](reason)
	default:
		return fmt.Errorf("%w: unknown evidence kind %q", ErrInvalidValue, raw.Kind)
	}
	return nil
}

// PolygonEvidence is the full D63 Polygon evidence of one observation.
// Each field is an independent evidence value; none implies block ancestry
// coverage by itself.
type PolygonEvidence struct {
	Milestone           Evidence[Milestone]       `json:"milestone"`
	Checkpoint          Evidence[Checkpoint]      `json:"checkpoint"`
	MilestoneWhitelist  Evidence[Whitelist]       `json:"milestone_whitelist"`
	CheckpointWhitelist Evidence[Whitelist]       `json:"checkpoint_whitelist"`
	NativeFinalized     Evidence[NativeFinalized] `json:"native_finalized"`
	L1Checkpoint        Evidence[L1Checkpoint]    `json:"l1_checkpoint"`
}

// Validate checks every field with its value validator.
func (e PolygonEvidence) Validate() error {
	if err := e.Milestone.Validate(Milestone.Validate); err != nil {
		return fmt.Errorf("milestone: %w", err)
	}
	if err := e.Checkpoint.Validate(Checkpoint.Validate); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	if err := e.MilestoneWhitelist.Validate(Whitelist.Validate); err != nil {
		return fmt.Errorf("milestone whitelist: %w", err)
	}
	if err := e.CheckpointWhitelist.Validate(Whitelist.Validate); err != nil {
		return fmt.Errorf("checkpoint whitelist: %w", err)
	}
	if err := e.NativeFinalized.Validate(NativeFinalized.Validate); err != nil {
		return fmt.Errorf("native finalized: %w", err)
	}
	return e.L1Checkpoint.Validate(L1Checkpoint.Validate)
}

func originByName(name string) block.EvidenceOrigin {
	for i, n := range []string{"NATIVE_LIVE", "NATIVE_RESTART", "RETAINED_DURABLE"} {
		if n == name {
			return block.EvidenceOrigin(i + 1)
		}
	}
	return 0
}

func reasonByName(name string) block.UnavailableReason {
	for i, n := range []string{"SOURCE_OFFLINE", "SOURCE_SYNCING", "STATE_UNAVAILABLE", "CHECKPOINT_PENDING"} {
		if n == name {
			return block.UnavailableReason(i + 1)
		}
	}
	return 0
}

func decodeEnumName(data []byte) (string, error) {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return "", fmt.Errorf("%w: enum value is not a name: %v", ErrInvalidValue, err)
	}
	return name, nil
}

// milestone builds the evidence milestone of one observed native status
// with its source observation ref.
func (m SourceMilestone) milestone(source SourceObservationRef) Milestone {
	return Milestone{
		Source:          source,
		Number:          m.Number,
		MilestoneID:     m.MilestoneID,
		Proposer:        m.Proposer,
		Start:           m.Start,
		End:             m.End,
		Timestamp:       m.Timestamp,
		TotalDifficulty: m.TotalDifficulty,
		EndHash:         m.EndHash,
		Outcome:         m.Outcome,
	}
}

// checkpoint builds the evidence checkpoint of one observed native stored
// checkpoint with its source observation ref.
func (c SourceCheckpoint) checkpoint(source SourceObservationRef) Checkpoint {
	return Checkpoint{
		Source:           source,
		CheckpointID:     c.CheckpointID,
		Proposer:         c.Proposer,
		Start:            c.Start,
		End:              c.End,
		Timestamp:        c.Timestamp,
		RootHash:         c.RootHash,
		State:            c.State,
		LocalVerifiedEnd: c.LocalVerifiedEnd,
	}
}
