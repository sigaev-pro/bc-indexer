package block

import (
	"errors"
	"fmt"
)

// Lifecycle errors. Each names one rule of MESSAGE_CONTRACT section 5; none
// is silently tolerated and none collapses into a success value.
var (
	// ErrTerminal reports an event on a REMOVED activation. A removed
	// activation is never reactivated; the same block returning to the
	// canonical chain gets a new activation record.
	ErrTerminal = errors.New("block: activation is terminal")
	// ErrStaleRevision reports an event revision lower than the durable
	// revision of the lifecycle object. An older revision never replaces
	// a newer one.
	ErrStaleRevision = errors.New("block: stale lifecycle revision")
	// ErrRevisionConflict reports the same revision with different
	// content. This is a conflict, not last-write-wins.
	ErrRevisionConflict = errors.New("block: lifecycle revision conflict")
	// ErrWrongActivation reports an event that references a different
	// activation than the lifecycle object. Lifecycle references of
	// different activations are never mixed.
	ErrWrongActivation = errors.New("block: event references a different activation")
	// ErrNotRemoved reports a new canonical period requested while the old
	// activation is still live.
	ErrNotRemoved = errors.New("block: activation is not removed")
	// ErrSameDecision reports a new canonical period that reuses the old
	// activation's native decision. A returning block is a new decision.
	ErrSameDecision = errors.New("block: new period reuses the old decision")
)

// NativeDecisionRef references one native decision of one producer run: the
// node instance, producer, run and the decision sequence. It is the
// imported/canonical/finality marker reference of a lifecycle snapshot; the
// decision kind itself is native adapter state.
type NativeDecisionRef struct {
	NodeInstanceID   Id16   `json:"node_instance_id"`
	ProducerID       Id16   `json:"producer_id"`
	RunID            Id16   `json:"run_id"`
	DecisionSequence uint64 `json:"decision_sequence"`
}

// Validate requires assigned identities.
func (r NativeDecisionRef) Validate() error {
	if r.NodeInstanceID.IsZero() {
		return fmt.Errorf("%w: decision node instance id is zero", ErrInvalidValue)
	}
	if r.ProducerID.IsZero() {
		return fmt.Errorf("%w: decision producer id is zero", ErrInvalidValue)
	}
	if r.RunID.IsZero() {
		return fmt.Errorf("%w: decision run id is zero", ErrInvalidValue)
	}
	return nil
}

// ActivationID identifies one activation of one block entity for one
// producer run: the native decision sequence that created it and the entity
// ordinal. A block entity always has ordinal 0.
type ActivationID struct {
	DecisionSequence uint64 `json:"decision_sequence"`
	EntityOrdinal    uint64 `json:"entity_ordinal"`
}

// BlockActivationRef is the public activation reference of a block record.
type BlockActivationRef struct {
	Network      NetworkIdentity `json:"network"`
	BlockHash    Hash32          `json:"block_hash"`
	ProducerID   Id16            `json:"producer_id"`
	RunID        Id16            `json:"run_id"`
	ActivationID ActivationID    `json:"activation_id"`
}

// Validate checks the identities and the block ordinal.
func (r BlockActivationRef) Validate() error {
	if err := r.Network.Validate(); err != nil {
		return err
	}
	if r.BlockHash.IsZero() {
		return fmt.Errorf("%w: activation block hash is zero", ErrInvalidValue)
	}
	if r.ProducerID.IsZero() {
		return fmt.Errorf("%w: activation producer id is zero", ErrInvalidValue)
	}
	if r.RunID.IsZero() {
		return fmt.Errorf("%w: activation run id is zero", ErrInvalidValue)
	}
	if r.ActivationID.EntityOrdinal != 0 {
		return fmt.Errorf("%w: block activation has entity ordinal %d, want 0", ErrInvalidValue, r.ActivationID.EntityOrdinal)
	}
	return nil
}

// EvidenceOrigin records where a finality reference came from.
type EvidenceOrigin uint8

// Evidence origins. Zero is not a valid origin.
const (
	OriginNativeLive EvidenceOrigin = iota + 1
	OriginNativeRestart
	OriginRetainedDurable
)

var evidenceOriginNames = []string{"NATIVE_LIVE", "NATIVE_RESTART", "RETAINED_DURABLE"}

// String returns the contract name of o.
func (o EvidenceOrigin) String() string {
	if o == 0 || int(o) > len(evidenceOriginNames) {
		return "INVALID"
	}
	return evidenceOriginNames[o-1]
}

// Validate reports whether o is a known origin.
func (o EvidenceOrigin) Validate() error {
	if o == 0 || int(o) > len(evidenceOriginNames) {
		return fmt.Errorf("%w: evidence origin %d", ErrInvalidValue, uint8(o))
	}
	return nil
}

// MarshalJSON encodes the origin as its contract name.
func (o EvidenceOrigin) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return jsonMarshal(o.String())
}

// UnmarshalJSON decodes the origin name strictly.
func (o *EvidenceOrigin) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range evidenceOriginNames {
		if n == name {
			*o = EvidenceOrigin(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown evidence origin %q", ErrInvalidValue, name)
}

// UnavailableReason names why an optional finality or GC lookup was
// unavailable. It never masks an error of mandatory execution capture.
type UnavailableReason uint8

// Unavailable reasons. Zero is not a valid reason.
const (
	UnavailableSourceOffline UnavailableReason = iota + 1
	UnavailableSourceSyncing
	UnavailableStateUnavailable
	UnavailableCheckpointPending
)

var unavailableReasonNames = []string{"SOURCE_OFFLINE", "SOURCE_SYNCING", "STATE_UNAVAILABLE", "CHECKPOINT_PENDING"}

// String returns the contract name of r.
func (r UnavailableReason) String() string {
	if r == 0 || int(r) > len(unavailableReasonNames) {
		return "INVALID"
	}
	return unavailableReasonNames[r-1]
}

// Validate reports whether r is a known reason.
func (r UnavailableReason) Validate() error {
	if r == 0 || int(r) > len(unavailableReasonNames) {
		return fmt.Errorf("%w: unavailable reason %d", ErrInvalidValue, uint8(r))
	}
	return nil
}

// MarshalJSON encodes the reason as its contract name.
func (r UnavailableReason) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return jsonMarshal(r.String())
}

// UnmarshalJSON decodes the reason name strictly.
func (r *UnavailableReason) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range unavailableReasonNames {
		if n == name {
			*r = UnavailableReason(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown unavailable reason %q", ErrInvalidValue, name)
}

// ReferenceKind is the closed state of one finality reference observation.
type ReferenceKind uint8

// Reference kinds. Zero is not a valid kind.
const (
	ReferenceKnown ReferenceKind = iota + 1
	ReferenceNotObserved
	ReferenceNativeUnset
	ReferenceUnavailable
)

var referenceKindNames = []string{"KNOWN", "NOT_OBSERVED", "NATIVE_REFERENCE_UNSET", "UNAVAILABLE"}

// String returns the contract name of k.
func (k ReferenceKind) String() string {
	if k == 0 || int(k) > len(referenceKindNames) {
		return "INVALID"
	}
	return referenceKindNames[k-1]
}

// Validate reports whether k is a known kind.
func (k ReferenceKind) Validate() error {
	if k == 0 || int(k) > len(referenceKindNames) {
		return fmt.Errorf("%w: reference kind %d", ErrInvalidValue, uint8(k))
	}
	return nil
}

// MarshalJSON encodes the kind as its contract name.
func (k ReferenceKind) MarshalJSON() ([]byte, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return jsonMarshal(k.String())
}

// UnmarshalJSON decodes the kind name strictly.
func (k *ReferenceKind) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range referenceKindNames {
		if n == name {
			*k = ReferenceKind(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown reference kind %q", ErrInvalidValue, name)
}

// ReferenceObservation is one native finality reference: Known carries the
// anchor and its origin; the unknown states carry no invented reference. An
// unknown optional reference on Applied is allowed; an unknown mandatory
// execution detail is a capture error elsewhere.
type ReferenceObservation struct {
	Kind   ReferenceKind     `json:"kind"`
	Anchor AnchorRef         `json:"anchor"`
	Origin EvidenceOrigin    `json:"origin"`
	Reason UnavailableReason `json:"reason"`
}

// Known builds a known reference observation.
func Known(anchor AnchorRef, origin EvidenceOrigin) ReferenceObservation {
	return ReferenceObservation{Kind: ReferenceKnown, Anchor: anchor, Origin: origin}
}

// NotObserved builds the not-observed reference state.
func NotObserved() ReferenceObservation {
	return ReferenceObservation{Kind: ReferenceNotObserved}
}

// Validate checks the consistency of the kind with the anchor, origin and
// reason fields.
func (o ReferenceObservation) Validate() error {
	if err := o.Kind.Validate(); err != nil {
		return err
	}
	switch o.Kind {
	case ReferenceKnown:
		if err := o.Anchor.Validate(); err != nil {
			return err
		}
		if err := o.Origin.Validate(); err != nil {
			return err
		}
		if o.Reason != 0 {
			return fmt.Errorf("%w: known reference carries an unavailable reason", ErrInvalidValue)
		}
	case ReferenceUnavailable:
		if err := o.Reason.Validate(); err != nil {
			return err
		}
		if !o.Anchor.Hash.IsZero() || o.Origin != 0 {
			return fmt.Errorf("%w: unavailable reference carries an anchor or origin", ErrInvalidValue)
		}
	default:
		if !o.Anchor.Hash.IsZero() || o.Origin != 0 || o.Reason != 0 {
			return fmt.Errorf("%w: %s reference carries anchor, origin or reason", ErrInvalidValue, o.Kind)
		}
	}
	return nil
}

// Equal reports observational equality of two references.
func (o ReferenceObservation) Equal(other ReferenceObservation) bool {
	return o == other
}

// MarshalJSON encodes the reference with only the fields its kind allows,
// so an unknown state never carries an invented anchor.
func (o ReferenceObservation) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	type referenceJSON struct {
		Kind   string     `json:"kind"`
		Anchor *AnchorRef `json:"anchor,omitempty"`
		Origin string     `json:"origin,omitempty"`
		Reason string     `json:"reason,omitempty"`
	}
	out := referenceJSON{Kind: o.Kind.String()}
	switch o.Kind {
	case ReferenceKnown:
		anchor := o.Anchor
		out.Anchor = &anchor
		out.Origin = o.Origin.String()
	case ReferenceUnavailable:
		out.Reason = o.Reason.String()
	}
	return jsonMarshal(out)
}

// UnmarshalJSON decodes the kind-dependent shape strictly.
func (o *ReferenceObservation) UnmarshalJSON(data []byte) error {
	type referenceJSON struct {
		Kind   string     `json:"kind"`
		Anchor *AnchorRef `json:"anchor,omitempty"`
		Origin string     `json:"origin,omitempty"`
		Reason string     `json:"reason,omitempty"`
	}
	var v referenceJSON
	if err := jsonUnmarshal(data, &v); err != nil {
		return err
	}
	var kind ReferenceKind
	switch v.Kind {
	case "KNOWN":
		kind = ReferenceKnown
	case "NOT_OBSERVED":
		kind = ReferenceNotObserved
	case "NATIVE_REFERENCE_UNSET":
		kind = ReferenceNativeUnset
	case "UNAVAILABLE":
		kind = ReferenceUnavailable
	case "":
		return fmt.Errorf("%w: reference without a kind", ErrInvalidValue)
	default:
		return fmt.Errorf("%w: unknown reference kind %q", ErrInvalidValue, v.Kind)
	}
	out := ReferenceObservation{Kind: kind}
	switch kind {
	case ReferenceKnown:
		if v.Anchor == nil {
			return fmt.Errorf("%w: known reference without an anchor", ErrInvalidValue)
		}
		out.Anchor = *v.Anchor
		origin, err := evidenceOriginByName(v.Origin)
		if err != nil {
			return err
		}
		out.Origin = origin
	case ReferenceUnavailable:
		reason, err := unavailableReasonByName(v.Reason)
		if err != nil {
			return err
		}
		out.Reason = reason
	}
	*o = out
	return out.Validate()
}

func evidenceOriginByName(name string) (EvidenceOrigin, error) {
	for i, n := range evidenceOriginNames {
		if n == name {
			return EvidenceOrigin(i + 1), nil
		}
	}
	return 0, fmt.Errorf("%w: unknown evidence origin %q", ErrInvalidValue, name)
}

func unavailableReasonByName(name string) (UnavailableReason, error) {
	for i, n := range unavailableReasonNames {
		if n == name {
			return UnavailableReason(i + 1), nil
		}
	}
	return 0, fmt.Errorf("%w: unknown unavailable reason %q", ErrInvalidValue, name)
}

// EthereumSafeBasis records what set the Ethereum safe reference.
type EthereumSafeBasis uint8

// Safe bases. Zero is not a valid basis.
const (
	SafeBasisConsensusSetter EthereumSafeBasis = iota + 1
	SafeBasisStartupFinalizedFallback
	SafeBasisUnset
)

var ethereumSafeBasisNames = []string{"CONSENSUS_SETTER", "STARTUP_FINALIZED_FALLBACK", "UNSET"}

// String returns the contract name of b.
func (b EthereumSafeBasis) String() string {
	if b == 0 || int(b) > len(ethereumSafeBasisNames) {
		return "INVALID"
	}
	return ethereumSafeBasisNames[b-1]
}

// Validate reports whether b is a known basis.
func (b EthereumSafeBasis) Validate() error {
	if b == 0 || int(b) > len(ethereumSafeBasisNames) {
		return fmt.Errorf("%w: safe basis %d", ErrInvalidValue, uint8(b))
	}
	return nil
}

// MarshalJSON encodes the basis as its contract name.
func (b EthereumSafeBasis) MarshalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return jsonMarshal(b.String())
}

// UnmarshalJSON decodes the basis name strictly.
func (b *EthereumSafeBasis) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range ethereumSafeBasisNames {
		if n == name {
			*b = EthereumSafeBasis(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown safe basis %q", ErrInvalidValue, name)
}

// BscEffectiveBasis records which BSC basis produced the effective finalized
// reference.
type BscEffectiveBasis uint8

// Effective bases. Zero is not a valid basis.
const (
	BscBasisHeaderSnapshot BscEffectiveBasis = iota + 1
	BscBasisVotePoolQuorum
	BscBasisGenesisFallback
	BscBasisUnset
)

var bscEffectiveBasisNames = []string{"HEADER_SNAPSHOT", "VOTE_POOL_QUORUM", "GENESIS_FALLBACK", "UNSET"}

// String returns the contract name of b.
func (b BscEffectiveBasis) String() string {
	if b == 0 || int(b) > len(bscEffectiveBasisNames) {
		return "INVALID"
	}
	return bscEffectiveBasisNames[b-1]
}

// Validate reports whether b is a known basis.
func (b BscEffectiveBasis) Validate() error {
	if b == 0 || int(b) > len(bscEffectiveBasisNames) {
		return fmt.Errorf("%w: bsc effective basis %d", ErrInvalidValue, uint8(b))
	}
	return nil
}

// MarshalJSON encodes the basis as its contract name.
func (b BscEffectiveBasis) MarshalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return jsonMarshal(b.String())
}

// UnmarshalJSON decodes the basis name strictly.
func (b *BscEffectiveBasis) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	for i, n := range bscEffectiveBasisNames {
		if n == name {
			*b = BscEffectiveBasis(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown bsc effective basis %q", ErrInvalidValue, name)
}

// EthereumMarkers are the ETH finality references observed at snapshot time.
// They are references, not truth: deriving ETH_FINALIZED, ETH_SAFE and the
// other statuses from ancestry coverage is the lifecycle validator's work.
type EthereumMarkers struct {
	Safe      ReferenceObservation `json:"safe"`
	Finalized ReferenceObservation `json:"finalized"`
	SafeBasis EthereumSafeBasis    `json:"safe_basis"`
}

// Validate checks both references and the basis. Unknown safe does not
// cancel known finalized; both unknown states stay allowed here.
func (m *EthereumMarkers) Validate() error {
	if err := m.Safe.Validate(); err != nil {
		return fmt.Errorf("%w: eth safe: %v", ErrInvalidValue, err)
	}
	if err := m.Finalized.Validate(); err != nil {
		return fmt.Errorf("%w: eth finalized: %v", ErrInvalidValue, err)
	}
	if err := m.SafeBasis.Validate(); err != nil {
		return err
	}
	return nil
}

// AttestedSnapshot is the merged native Parlia header snapshot: the
// snapshot head with the attested justified and finalized anchors.
type AttestedSnapshot struct {
	SnapshotHead AnchorRef `json:"snapshot_head"`
	Justified    AnchorRef `json:"justified"`
	Finalized    AnchorRef `json:"finalized"`
}

// Validate requires real anchors.
func (s AttestedSnapshot) Validate() error {
	if err := s.SnapshotHead.Validate(); err != nil {
		return err
	}
	if err := s.Justified.Validate(); err != nil {
		return err
	}
	return s.Finalized.Validate()
}

// BscMarkers are the BSC finality references observed at snapshot time.
// Header attestation and vote-pool results stay separate; they never
// collapse into one unconditional finalized flag.
type BscMarkers struct {
	// Attested is the merged native header snapshot when observed.
	Attested Optional[AttestedSnapshot] `json:"attested"`
	// EffectiveFinalized is the native effective finalized reference with
	// its basis.
	EffectiveFinalized ReferenceObservation `json:"effective_finalized"`
	EffectiveBasis     BscEffectiveBasis    `json:"effective_basis"`
}

// Validate checks the present snapshot and the effective reference.
func (m *BscMarkers) Validate() error {
	if s, ok := m.Attested.Get(); ok {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("%w: bsc attested: %v", ErrInvalidValue, err)
		}
	}
	if err := m.EffectiveFinalized.Validate(); err != nil {
		return fmt.Errorf("%w: bsc effective finalized: %v", ErrInvalidValue, err)
	}
	return m.EffectiveBasis.Validate()
}

// PolygonMarkers are the Polygon finality references observed at snapshot
// time: milestone, checkpoint, native finalized and the L1 checkpoint. Each
// is an independent reference; none implies block ancestry coverage by
// itself.
type PolygonMarkers struct {
	Milestone       ReferenceObservation `json:"milestone"`
	Checkpoint      ReferenceObservation `json:"checkpoint"`
	NativeFinalized ReferenceObservation `json:"native_finalized"`
	L1Checkpoint    ReferenceObservation `json:"l1_checkpoint"`
}

// Validate checks all four references.
func (m *PolygonMarkers) Validate() error {
	for name, ref := range map[string]ReferenceObservation{
		"milestone":        m.Milestone,
		"checkpoint":       m.Checkpoint,
		"native_finalized": m.NativeFinalized,
		"l1_checkpoint":    m.L1Checkpoint,
	} {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: polygon %s: %v", ErrInvalidValue, name, err)
		}
	}
	return nil
}

// FinalityMarkers is the closed per-family finality reference set of one
// lifecycle snapshot: exactly one variant, matching the record's family.
// The full D62/D63 evidence payloads (early vote spools,
// SourceObservationRef with the D80 digest, whitelists and the L1Checkpoint
// contract state) belong to the lifecycle evidence schema; this type fixes
// only the marker skeleton a block record cites.
type FinalityMarkers struct {
	Eth     *EthereumMarkers
	Bsc     *BscMarkers
	Polygon *PolygonMarkers
}

// Family returns the closed family of the present variant.
func (m FinalityMarkers) Family() Family {
	switch {
	case m.Eth != nil:
		return FamilyEth
	case m.Bsc != nil:
		return FamilyBsc
	case m.Polygon != nil:
		return FamilyPoly
	default:
		return ""
	}
}

// Validate requires exactly one variant and validates it.
func (m FinalityMarkers) Validate() error {
	seen := 0
	for _, p := range []bool{m.Eth != nil, m.Bsc != nil, m.Polygon != nil} {
		if p {
			seen++
		}
	}
	if seen != 1 {
		return fmt.Errorf("%w: finality markers have %d variants, want exactly 1", ErrInvalidValue, seen)
	}
	switch {
	case m.Eth != nil:
		return m.Eth.Validate()
	case m.Bsc != nil:
		return m.Bsc.Validate()
	default:
		return m.Polygon.Validate()
	}
}

// MarshalJSON encodes the oneof as {"family":"ETH","eth":{...}}.
func (m FinalityMarkers) MarshalJSON() ([]byte, error) {
	switch {
	case m.Eth != nil:
		return jsonMarshal(struct {
			Family string           `json:"family"`
			Eth    *EthereumMarkers `json:"eth"`
		}{string(FamilyEth), m.Eth})
	case m.Bsc != nil:
		return jsonMarshal(struct {
			Family string      `json:"family"`
			Bsc    *BscMarkers `json:"bsc"`
		}{string(FamilyBsc), m.Bsc})
	case m.Polygon != nil:
		return jsonMarshal(struct {
			Family string          `json:"family"`
			Poly   *PolygonMarkers `json:"polygon"`
		}{string(FamilyPoly), m.Polygon})
	default:
		return nil, fmt.Errorf("%w: finality markers have no variant", ErrInvalidValue)
	}
}

// UnmarshalJSON decodes the oneof strictly.
func (m *FinalityMarkers) UnmarshalJSON(data []byte) error {
	var raw struct {
		Family  string           `json:"family"`
		Eth     *EthereumMarkers `json:"eth"`
		Bsc     *BscMarkers      `json:"bsc"`
		Polygon *PolygonMarkers  `json:"polygon"`
	}
	if err := jsonUnmarshal(data, &raw); err != nil {
		return err
	}
	m.Eth, m.Bsc, m.Polygon = raw.Eth, raw.Bsc, raw.Polygon
	var f Family
	switch raw.Family {
	case string(FamilyEth):
		f = FamilyEth
	case string(FamilyBsc):
		f = FamilyBsc
	case string(FamilyPoly):
		f = FamilyPoly
	case "":
		return fmt.Errorf("%w: finality markers without a family name", ErrInvalidValue)
	default:
		return fmt.Errorf("%w: unknown finality family %q", ErrInvalidValue, raw.Family)
	}
	if m.Family() != f {
		return fmt.Errorf("%w: finality family %q does not match the present variant", ErrInvalidValue, raw.Family)
	}
	return m.Validate()
}

// LifecycleSnapshot is the typed lifecycle evidence of one event: the
// native decision that produced it, the observed execution head and the
// family finality markers. Applied carries the initial snapshot; each
// StatusChanged carries a full replacement snapshot, never a delta patch.
type LifecycleSnapshot struct {
	NativeDecision        NativeDecisionRef `json:"native_decision"`
	ObservedExecutionHead AnchorRef         `json:"observed_execution_head"`
	Markers               FinalityMarkers   `json:"markers"`
}

// Validate checks the decision, the observed head and the marker closure.
// The observed execution head is a known anchor and never falls back to a
// zero or genesis hash.
func (s LifecycleSnapshot) Validate() error {
	if err := s.NativeDecision.Validate(); err != nil {
		return err
	}
	if err := s.ObservedExecutionHead.Validate(); err != nil {
		return fmt.Errorf("%w: observed execution head: %v", ErrInvalidValue, err)
	}
	return s.Markers.Validate()
}

// Equal reports evidence equality of two snapshots.
func (s LifecycleSnapshot) Equal(other LifecycleSnapshot) bool {
	return s == other
}

// LifecycleState is the public lifecycle state of one activation.
type LifecycleState uint8

// Lifecycle states. Zero is the absent state: no record exists yet.
const (
	StateActive LifecycleState = iota + 1
	StatePendingApplied
	StateRemoved
)

var lifecycleStateNames = []string{"ACTIVE", "PENDING_APPLIED", "REMOVED"}

// String returns the contract name of s.
func (s LifecycleState) String() string {
	if s == 0 {
		return "ABSENT"
	}
	if int(s) > len(lifecycleStateNames) {
		return "INVALID"
	}
	return lifecycleStateNames[s-1]
}

// Validate reports whether s is a known state.
func (s LifecycleState) Validate() error {
	if s == 0 || int(s) > len(lifecycleStateNames) {
		return fmt.Errorf("%w: lifecycle state %d", ErrInvalidValue, uint8(s))
	}
	return nil
}

// MarshalJSON encodes the state as its contract name.
func (s LifecycleState) MarshalJSON() ([]byte, error) {
	if s == 0 {
		return jsonMarshal("ABSENT")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return jsonMarshal(s.String())
}

// UnmarshalJSON decodes the state name strictly.
func (s *LifecycleState) UnmarshalJSON(data []byte) error {
	name, err := decodeEnumName(data)
	if err != nil {
		return err
	}
	if name == "ABSENT" {
		*s = 0
		return nil
	}
	for i, n := range lifecycleStateNames {
		if n == name {
			*s = LifecycleState(i + 1)
			return nil
		}
	}
	return fmt.Errorf("%w: unknown lifecycle state %q", ErrInvalidValue, name)
}

// EventKind is the kind of one lifecycle event.
type EventKind uint8

// Event kinds. Zero is not a valid kind.
const (
	EventApplied EventKind = iota + 1
	EventStatusChanged
	EventRemoved
)

var eventKindNames = []string{"APPLIED", "STATUS_CHANGED", "REMOVED"}

// String returns the contract name of k.
func (k EventKind) String() string {
	if k == 0 || int(k) > len(eventKindNames) {
		return "INVALID"
	}
	return eventKindNames[k-1]
}

// Validate reports whether k is a known kind.
func (k EventKind) Validate() error {
	if k == 0 || int(k) > len(eventKindNames) {
		return fmt.Errorf("%w: event kind %d", ErrInvalidValue, uint8(k))
	}
	return nil
}

// RemovalReason is the closed reason of a Removed event.
type RemovalReason uint8

// Removal reasons. Zero is not a valid reason.
const RemovalReasonNativeCanonicalChange RemovalReason = 1

var removalReasonNames = []string{"NATIVE_CANONICAL_CHANGE"}

// String returns the contract name of r.
func (r RemovalReason) String() string {
	if r == 0 || int(r) > len(removalReasonNames) {
		return "INVALID"
	}
	return removalReasonNames[r-1]
}

// Validate reports whether r is a known reason.
func (r RemovalReason) Validate() error {
	if r == 0 || int(r) > len(removalReasonNames) {
		return fmt.Errorf("%w: removal reason %d", ErrInvalidValue, uint8(r))
	}
	return nil
}

// LifecycleEvent is one lifecycle event of one activation. Applied carries
// revision 0 and the initial snapshot. StatusChanged and Removed carry a
// durable positive revision; StatusChanged carries a full replacement
// snapshot, Removed carries its native decision and reason.
type LifecycleEvent struct {
	Activation     BlockActivationRef `json:"activation"`
	Kind           EventKind          `json:"kind"`
	Revision       uint64             `json:"revision"`
	Snapshot       LifecycleSnapshot  `json:"snapshot"`
	NativeDecision NativeDecisionRef  `json:"native_decision"`
	Reason         RemovalReason      `json:"reason"`
}

// Validate checks the event shape against its kind.
func (e LifecycleEvent) Validate() error {
	if err := e.Activation.Validate(); err != nil {
		return err
	}
	if err := e.Kind.Validate(); err != nil {
		return err
	}
	switch e.Kind {
	case EventApplied:
		if e.Revision != 0 {
			return fmt.Errorf("%w: applied event with revision %d, want 0", ErrInvalidValue, e.Revision)
		}
		if err := e.Snapshot.Validate(); err != nil {
			return fmt.Errorf("%w: applied snapshot: %v", ErrInvalidValue, err)
		}
		if e.Reason != 0 {
			return fmt.Errorf("%w: applied event carries a removal reason", ErrInvalidValue)
		}
	case EventStatusChanged:
		if e.Revision == 0 {
			return fmt.Errorf("%w: status changed event with revision 0", ErrInvalidValue)
		}
		if err := e.Snapshot.Validate(); err != nil {
			return fmt.Errorf("%w: status changed snapshot: %v", ErrInvalidValue, err)
		}
		if e.Reason != 0 {
			return fmt.Errorf("%w: status changed event carries a removal reason", ErrInvalidValue)
		}
	case EventRemoved:
		if e.Revision == 0 {
			return fmt.Errorf("%w: removed event with revision 0", ErrInvalidValue)
		}
		if err := e.NativeDecision.Validate(); err != nil {
			return fmt.Errorf("%w: removed decision: %v", ErrInvalidValue, err)
		}
		if err := e.Reason.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ActivationState is the durable lifecycle state of one block activation
// for one producer run: the activation reference, the state, the durable
// revision and the last full snapshot evidence.
type ActivationState struct {
	Activation BlockActivationRef `json:"activation"`
	State      LifecycleState     `json:"state"`
	Revision   uint64             `json:"revision"`
	Snapshot   LifecycleSnapshot  `json:"snapshot"`
}

// StartApplied creates the lifecycle object from an Applied(revision 0)
// event on the absent state: the record becomes ACTIVE with its initial
// typed lifecycle snapshot.
func StartApplied(ev LifecycleEvent) (ActivationState, error) {
	if ev.Kind != EventApplied {
		return ActivationState{}, fmt.Errorf("%w: start applied with %s event", ErrInvalidValue, ev.Kind)
	}
	if err := ev.Validate(); err != nil {
		return ActivationState{}, err
	}
	return ActivationState{
		Activation: ev.Activation,
		State:      StateActive,
		Revision:   0,
		Snapshot:   ev.Snapshot,
	}, nil
}

// StartPending creates the lifecycle object when a StatusChanged arrives
// before any Applied: the state becomes PENDING_APPLIED, the last full
// evidence is kept and no TransactionRecord is invented.
func StartPending(ev LifecycleEvent) (ActivationState, error) {
	if ev.Kind != EventStatusChanged {
		return ActivationState{}, fmt.Errorf("%w: start pending with %s event", ErrInvalidValue, ev.Kind)
	}
	if err := ev.Validate(); err != nil {
		return ActivationState{}, err
	}
	return ActivationState{
		Activation: ev.Activation,
		State:      StatePendingApplied,
		Revision:   ev.Revision,
		Snapshot:   ev.Snapshot,
	}, nil
}

// Apply folds one lifecycle event into the state and returns the new state.
// The receiver is never mutated: the old state stays valid, which is what
// makes a reorged-away activation immutable.
//
// Rules (MESSAGE_CONTRACT section 5):
//
//	ACTIVE    + Applied retry, same bytes   -> unchanged (same fact)
//	ACTIVE    + StatusChanged, higher rev   -> ACTIVE, new full evidence
//	ACTIVE    + Removed, higher rev         -> REMOVED, terminal
//	PENDING   + Applied(rev 0)              -> ACTIVE, keeps latest evidence
//	PENDING   + StatusChanged, higher rev   -> PENDING_APPLIED, replace
//	PENDING   + Removed, higher rev         -> REMOVED, tombstone
//	REMOVED   + any event                   -> ErrTerminal (no reactivation)
//
// Equal revision with identical content is an idempotent repeat; equal
// revision with different content is ErrRevisionConflict; a lower revision
// is ErrStaleRevision.
func (s ActivationState) Apply(ev LifecycleEvent) (ActivationState, error) {
	if err := ev.Validate(); err != nil {
		return ActivationState{}, err
	}
	if s.Activation != ev.Activation {
		return ActivationState{}, ErrWrongActivation
	}
	next := ActivationState{Activation: s.Activation, Snapshot: s.Snapshot, Revision: s.Revision, State: s.State}
	switch s.State {
	case StateActive, StatePendingApplied:
		switch ev.Kind {
		case EventApplied:
			// Applied on ACTIVE is a retry: the same fact, not a new
			// inclusion; a different snapshot at revision 0 is a
			// conflict.
			if s.State == StateActive && !s.Snapshot.Equal(ev.Snapshot) {
				return ActivationState{}, fmt.Errorf("%w: applied retry with different evidence", ErrRevisionConflict)
			}
			next.State = StateActive
			next.Revision = s.Revision
			return next, nil
		case EventStatusChanged:
			if err := s.checkRevision(ev); err != nil {
				return ActivationState{}, err
			}
			if s.State == StateActive && s.Revision == ev.Revision && s.Snapshot.Equal(ev.Snapshot) {
				return s, nil
			}
			next.State = s.State
			next.Revision = ev.Revision
			next.Snapshot = ev.Snapshot
			return next, nil
		case EventRemoved:
			if s.Revision > ev.Revision {
				return ActivationState{}, fmt.Errorf("%w: removed revision %d below durable %d", ErrStaleRevision, ev.Revision, s.Revision)
			}
			if s.Revision == ev.Revision {
				return ActivationState{}, fmt.Errorf("%w: removed revision %d equals durable %d", ErrRevisionConflict, ev.Revision, s.Revision)
			}
			next.State = StateRemoved
			next.Revision = ev.Revision
			return next, nil
		default:
			return ActivationState{}, fmt.Errorf("%w: event kind %s", ErrInvalidValue, ev.Kind)
		}
	case StateRemoved:
		return ActivationState{}, fmt.Errorf("%w: %s event on REMOVED activation", ErrTerminal, ev.Kind)
	default:
		return ActivationState{}, fmt.Errorf("%w: lifecycle state %s", ErrInvalidValue, s.State)
	}
}

// checkRevision enforces revision monotonicity for replacement events.
func (s ActivationState) checkRevision(ev LifecycleEvent) error {
	switch {
	case ev.Revision > s.Revision:
		return nil
	case ev.Revision == s.Revision:
		if s.Snapshot.Equal(ev.Snapshot) {
			return nil
		}
		return fmt.Errorf("%w: revision %d with different evidence", ErrRevisionConflict, ev.Revision)
	default:
		return fmt.Errorf("%w: revision %d below durable %d", ErrStaleRevision, ev.Revision, s.Revision)
	}
}

// BlockRecord is one canonical period of one block identity as observed by
// one producer run: the activation reference, the block metadata, the
// admitted observation whose decision produced it and the initial lifecycle
// snapshot (revision 0).
//
// A record is a value: later lifecycle events never mutate it. A reorged
// away activation stays exactly as applied; the same block returning to the
// canonical chain produces a new record through NewCanonicalPeriod.
type BlockRecord struct {
	Activation      BlockActivationRef `json:"activation"`
	Admission       ObservationRef     `json:"admission"`
	Metadata        BlockMetadata      `json:"metadata"`
	InitialSnapshot LifecycleSnapshot  `json:"initial_snapshot"`
}

// Validate checks the record shape and the cross-field family consistency.
func (r *BlockRecord) Validate() error {
	if err := r.Activation.Validate(); err != nil {
		return err
	}
	if err := r.Admission.Validate(); err != nil {
		return err
	}
	if r.Activation.BlockHash != r.Metadata.Hash {
		return fmt.Errorf("%w: activation block hash differs from metadata hash", ErrInvalidValue)
	}
	if err := r.Metadata.Validate(); err != nil {
		return err
	}
	if err := r.InitialSnapshot.Validate(); err != nil {
		return err
	}
	if family := r.Metadata.Details.Family(); family != r.InitialSnapshot.Markers.Family() {
		return fmt.Errorf("%w: metadata family %q but snapshot markers family %q", ErrInvalidValue, family, r.InitialSnapshot.Markers.Family())
	}
	return nil
}

// NewCanonicalPeriod builds the record of a new canonical period for the
// same block identity after the old activation ended in REMOVED (a reorg
// took the block away and it later returned). The new record gets a new
// activation id from the new native decision (block ordinal 0), revision 0
// and its own initial snapshot. The old activation is not reactivated and
// its own record stays immutable.
func NewCanonicalPeriod(old ActivationState, metadata BlockMetadata, admission ObservationRef, snapshot LifecycleSnapshot) (BlockRecord, error) {
	if old.State != StateRemoved {
		return BlockRecord{}, fmt.Errorf("%w: state %s", ErrNotRemoved, old.State)
	}
	decision := snapshot.NativeDecision
	if decision.DecisionSequence == old.Activation.ActivationID.DecisionSequence {
		return BlockRecord{}, ErrSameDecision
	}
	rec := BlockRecord{
		Activation: BlockActivationRef{
			Network:      old.Activation.Network,
			BlockHash:    old.Activation.BlockHash,
			ProducerID:   decision.ProducerID,
			RunID:        decision.RunID,
			ActivationID: ActivationID{DecisionSequence: decision.DecisionSequence, EntityOrdinal: 0},
		},
		Admission:       admission,
		Metadata:        metadata,
		InitialSnapshot: snapshot,
	}
	if err := rec.Validate(); err != nil {
		return BlockRecord{}, err
	}
	return rec, nil
}
