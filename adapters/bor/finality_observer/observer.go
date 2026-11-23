package finalityobserver

import (
	"errors"
	"fmt"
	"sync"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// Observer errors. Each names one rule of the Polygon finality contract;
// none is silently tolerated and none collapses into an Unavailable.
var (
	// ErrConfig reports an observer used before its sources are bound.
	ErrConfig = errors.New("finality_observer: sources not bound")
	// ErrIdentity reports a wrong network, chain, contract or parameter
	// binding, or a RootChain result that does not match the checkpoint:
	// an explicit source/identity error, never an Unavailable-success and
	// never an accepted anchor (AC-LIF-07-04).
	ErrIdentity = errors.New("finality_observer: source identity mismatch")
	// ErrInvariant reports a violation of the durable finality
	// invariants: a whitelist without the canonical match, a failed native
	// range verification, or a candidate that conflicts below the
	// verified frontier.
	ErrInvariant = errors.New("finality_observer: source invariant violation")
)

// UnavailableError is one concrete transient availability state of one
// optional finality/GC lookup: the own Heimdall or L1 node offline or
// syncing, the L1 state pruned at F, or a pending checkpoint. It is not an
// error of the observation pipeline: GC waits with this explicit
// availability, the retained accepted anchor stays, and the publication of
// ready Polygon records continues.
type UnavailableError struct {
	Reason block.UnavailableReason
}

func (e *UnavailableError) Error() string {
	return "finality_observer: source unavailable: " + e.Reason.String()
}

// Unavailable builds one unavailable lookup error.
func Unavailable(reason block.UnavailableReason) error {
	return &UnavailableError{Reason: reason}
}

// UnavailableReasonOf returns the explicit reason when err is an
// UnavailableError.
func UnavailableReasonOf(err error) (block.UnavailableReason, bool) {
	var u *UnavailableError
	if errors.As(err, &u) {
		return u.Reason, true
	}
	return 0, false
}

// Response is one exact source response with its request identity: the
// exact bytes stay local, only their digest enters the public evidence.
type Response struct {
	Bytes           []byte
	RequestSequence uint64
	CommittedBlock  block.Optional[block.AnchorRef]
}

// ChainParams are the checked Heimdall chain parameters of one L1
// verification: the RootChain contract address.
type ChainParams struct {
	RootChainAddress block.Address20
}

// CheckpointParams are the checked Heimdall checkpoint parameters: the
// child chain block interval.
type CheckpointParams struct {
	ChildBlockInterval uint64
}

// SourceMilestone is one native Heimdall milestone status as observed.
type SourceMilestone struct {
	Number          block.Optional[uint64]
	MilestoneID     string
	Proposer        block.Address20
	Start           uint64
	End             uint64
	Timestamp       uint64
	TotalDifficulty uint64
	EndHash         block.Hash32
	Outcome         MilestoneOutcome
}

// SourceCheckpoint is one native stored Heimdall checkpoint as observed.
// The CheckpointID is optional because the native callbacks may not carry
// the number; nothing invents it, and the L1 verification needs the id of
// a full query.
type SourceCheckpoint struct {
	CheckpointID     block.Optional[uint64]
	Proposer         block.Address20
	Start            uint64
	End              uint64
	Timestamp        uint64
	RootHash         block.Hash32
	State            CheckpointState
	LocalVerifiedEnd block.Optional[block.AnchorRef]
}

// HeimdallSource is the own Heimdall seam. Network reads run outside hooks
// and native locks; implementations return UnavailableError for the
// transient states and a hard error for malformed results.
type HeimdallSource interface {
	StoreIdentity() block.Id16
	Identity() HeimdallIdentity
	ChainParams() (ChainParams, Response, error)
	CheckpointParams() (CheckpointParams, Response, error)
	MilestoneLatest() (SourceMilestone, Response, error)
	CheckpointLatest() (SourceCheckpoint, Response, error)
}

// L1Source is the own Polygon L1 Geth seam. It serves finality evidence of
// the native Polygon dependency, never transactions of the extractor.
type L1Source interface {
	NodeID() block.Id16
	Network() block.NetworkIdentity
	// Finalized returns the current finalized L1 anchor F. latest, safe
	// and confirmation counts never substitute it; implementations return
	// UnavailableError while it cannot be proven.
	Finalized() (block.AnchorRef, error)
	// IsCanonicalAncestor reports whether descendant extends the canonical
	// L1 chain through ancestor: the exact hash ancestry, never a height
	// comparison alone.
	IsCanonicalAncestor(ancestor, descendant block.AnchorRef) (bool, error)
	// HeaderBlocks runs the exact hash-pinned RootChain read: calldata at
	// {blockHash: at.Hash, requireCanonical: true}, returning the exact
	// result bytes.
	HeaderBlocks(calldata []byte, at block.AnchorRef) ([]byte, error)
}

// Ancestry is the native Bor verifier seam: the canonical ancestry of the
// Bor chain and the checkpoint root recomputation over the pinned header
// inputs of a continuous canonical range.
type Ancestry interface {
	IsAncestorOrSelf(ancestor, descendant block.AnchorRef) (bool, error)
	VerifyCheckpointRoot(start, end uint64, root block.Hash32) (block.AnchorRef, error)
}

// RetainedWhitelist is one native whitelist handed over by the Bor bridge.
// Response is absent when the full source response was not retained: the
// whitelist then publishes as the narrower Known form with absent
// source_evidence and without an invented digest (AC-LIF-07-01, D80).
type RetainedWhitelist struct {
	Kind     WhitelistKind
	Anchor   block.AnchorRef
	Decision block.NativeDecisionRef
	Request  Request
	Response block.Optional[Response]
}

// NativeInput is one native observation round of the Bor bridge.
type NativeInput struct {
	// Head is the observed execution head the whitelists must match
	// canonically.
	Head block.AnchorRef
	// Decision is the native decision that produced this observation.
	Decision block.NativeDecisionRef
	// Origin records whether this is a live observation, a restart
	// refresh or a retained durable one.
	Origin block.EvidenceOrigin
	// MilestonePurged marks one native milestone purge: the milestone
	// whitelist falls away, the checkpoint fallback takes over and the
	// retained L1 anchor is not erased (AC-LIF-07-06).
	MilestonePurged bool
	// Milestone is the observed native milestone status with its exact
	// response; without a retained response no milestone evidence is
	// invented.
	Milestone         block.Optional[SourceMilestone]
	MilestoneResponse block.Optional[Response]
	// Checkpoint is the observed native stored checkpoint with its exact
	// response.
	Checkpoint         block.Optional[SourceCheckpoint]
	CheckpointResponse block.Optional[Response]
	// MilestoneWhitelist and CheckpointWhitelist are the native
	// whitelists accepted after their canonical match.
	MilestoneWhitelist  block.Optional[RetainedWhitelist]
	CheckpointWhitelist block.Optional[RetainedWhitelist]
}

// Observer is the PolygonCheckpointObserver of the Bor adapter. It builds
// the durable typed observation stream: the native milestone/checkpoint
// statuses and the separately verified L1-finalized checkpoint evidence.
// Network reads happen only in ObserveL1 and in the whitelist canonical
// check of ObserveNative; the projection for tx/status snapshots (Markers,
// Projection) reads only the durable stream, so publication never waits
// for the GC source. The observer never releases capture pins itself;
// anchor acceptance beyond the verified evidence frontier belongs to
// E05-01.activation_gc.
type Observer struct {
	mu       sync.Mutex
	store    Store
	sources  *Sources
	heimdall HeimdallSource
	l1       L1Source
	ancestry Ancestry
	nextObs  uint64
}

// NewObserver builds the observer over a durable store with its pinned
// sources, the own Heimdall and L1 seams and the native Bor verifier seam.
// The current sources digest binds every new observation; the durable
// observations of the store keep the digest they were made with.
func NewObserver(store Store, sources *Sources, heimdall HeimdallSource, l1 L1Source, ancestry Ancestry) (*Observer, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: nil store", ErrConfig)
	}
	if sources == nil {
		return nil, fmt.Errorf("%w: nil sources", ErrConfig)
	}
	if heimdall == nil || l1 == nil || ancestry == nil {
		return nil, fmt.Errorf("%w: nil source seam", ErrConfig)
	}
	observed, err := store.Observations()
	if err != nil {
		return nil, err
	}
	return &Observer{
		store:    store,
		sources:  sources,
		heimdall: heimdall,
		l1:       l1,
		ancestry: ancestry,
		nextObs:  uint64(len(observed)) + 1,
	}, nil
}

// ReplaceSources applies a new sources_file to new observations only. The
// D80 digest of every earlier observation stays as it was pinned, and the
// durable evidence bytes of a replayed observation never change
// (AC-LIF-07-08).
func (o *Observer) ReplaceSources(fileBytes []byte) error {
	parsed, err := ParseSources(fileBytes)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sources = parsed
	return nil
}

// SourcesDigest returns the D80 digest of the current sources file: the
// digest new observations bind.
func (o *Observer) SourcesDigest() block.Hash32 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sources.Digest
}

// binding builds the observation binding from the current sources and the
// verified source identities, after the identity checks. A wrong network
// or Heimdall identity of a source is an explicit identity error: the
// expected binding comes from the bootstrap identity, never from a random
// RPC answer.
func (o *Observer) binding() (ObservationBinding, error) {
	if err := o.heimdall.Identity().equal(o.sources.Heimdall.Identity()); err != nil {
		return ObservationBinding{}, err
	}
	if o.l1.Network() != o.sources.L1.Network {
		return ObservationBinding{}, fmt.Errorf("%w: l1 network %s differs from the pinned %s", ErrIdentity, o.l1.Network().ChainID, o.sources.L1.Network.ChainID)
	}
	return ObservationBinding{
		SourcesBinding: o.sources.Digest,
		L1NodeID:       o.l1.NodeID(),
		HeimdallNodeID: o.heimdall.StoreIdentity(),
		Heimdall:       o.sources.Heimdall.Identity(),
		L1Network:      o.sources.L1.Network,
	}, nil
}

func (h HeimdallIdentity) equal(other HeimdallIdentity) error {
	if h.ChainID != other.ChainID {
		return fmt.Errorf("%w: heimdall chain id %q differs from the pinned %q", ErrIdentity, h.ChainID, other.ChainID)
	}
	if h.GenesisDocumentSHA256 != other.GenesisDocumentSHA256 {
		return fmt.Errorf("%w: heimdall genesis document digest differs from the pinned one", ErrIdentity)
	}
	return nil
}

// allocateID returns the next observation identity.
func (o *Observer) allocateID() string {
	o.nextObs++
	return fmt.Sprintf("poly-obs-%08d", o.nextObs-1)
}

// carried returns the native evidence fields of the latest durable
// observation, re-originated as RETAINED_DURABLE. An unavailable L1 state
// carries its explicit reason forward; nothing is erased.
func carriedFrom(observations []Observation) PolygonEvidence {
	var carried PolygonEvidence
	carried.Milestone = NotObservedOf[Milestone]()
	carried.Checkpoint = NotObservedOf[Checkpoint]()
	carried.MilestoneWhitelist = NotObservedOf[Whitelist]()
	carried.CheckpointWhitelist = NotObservedOf[Whitelist]()
	carried.NativeFinalized = NotObservedOf[NativeFinalized]()
	carried.L1Checkpoint = NotObservedOf[L1Checkpoint]()
	if len(observations) == 0 {
		return carried
	}
	last := observations[len(observations)-1].Evidence
	if v, ok := last.Milestone.Get(); ok {
		carried.Milestone = KnownOf(v, block.OriginRetainedDurable)
	}
	if v, ok := last.Checkpoint.Get(); ok {
		carried.Checkpoint = KnownOf(v, block.OriginRetainedDurable)
	}
	if v, ok := last.MilestoneWhitelist.Get(); ok {
		carried.MilestoneWhitelist = KnownOf(v, block.OriginRetainedDurable)
	}
	if v, ok := last.CheckpointWhitelist.Get(); ok {
		carried.CheckpointWhitelist = KnownOf(v, block.OriginRetainedDurable)
	}
	if v, ok := last.NativeFinalized.Get(); ok {
		carried.NativeFinalized = KnownOf(v, block.OriginRetainedDurable)
	}
	switch last.L1Checkpoint.Kind {
	case EvidenceKnown:
		if v, ok := last.L1Checkpoint.Get(); ok {
			carried.L1Checkpoint = KnownOf(v, block.OriginRetainedDurable)
		}
	case EvidenceUnavailable:
		carried.L1Checkpoint = UnavailableOf[L1Checkpoint](last.L1Checkpoint.Reason)
	}
	return carried
}

// sourceRef builds the SourceObservationRef of one exact response under
// the given binding.
func sourceRef(b ObservationBinding, sourceID block.Id16, observationID string, request Request, resp Response) SourceObservationRef {
	return SourceObservationRef{
		SourceStoreIdentity: sourceID,
		SourcesBinding:      b.SourcesBinding,
		Heimdall:            b.Heimdall,
		ObservationID:       observationID,
		RequestSequence:     resp.RequestSequence,
		Request:             request,
		CommittedBlock:      resp.CommittedBlock,
		ResponseDigest:      DigestBytes(resp.Bytes),
	}
}

// ObserveNative records one native observation round: the milestone and
// checkpoint statuses, the whitelists after their canonical match, the
// native finalized projection (milestone whitelist first, checkpoint
// fallback) and the carried L1 evidence. A milestone purge drops the
// milestone whitelist and the milestone status, keeps the checkpoint
// fallback and never erases the retained L1 anchor (AC-LIF-07-06). The
// method performs no L1 read: publication of ready records never waits
// for the GC source.
func (o *Observer) ObserveNative(in NativeInput) (Observation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := in.Head.Validate(); err != nil {
		return Observation{}, fmt.Errorf("%w: head: %v", ErrInvalidValue, err)
	}
	if err := in.Decision.Validate(); err != nil {
		return Observation{}, fmt.Errorf("%w: decision: %v", ErrInvalidValue, err)
	}
	if err := in.Origin.Validate(); err != nil {
		return Observation{}, fmt.Errorf("%w: origin: %v", ErrInvalidValue, err)
	}
	binding, err := o.binding()
	if err != nil {
		return Observation{}, err
	}
	durable, err := o.store.Observations()
	if err != nil {
		return Observation{}, err
	}
	evidence := carriedFrom(durable)

	if !in.MilestonePurged {
		if m, ok := in.Milestone.Get(); ok {
			if resp, hasResp := in.MilestoneResponse.Get(); hasResp {
				source := sourceRef(binding, o.heimdall.StoreIdentity(), o.allocateID(), Request{Operation: OpMilestoneLatest}, resp)
				evidence.Milestone = KnownOf(m.milestone(source), in.Origin)
			}
			// Without the retained full response no milestone evidence
			// is invented; the narrower whitelist form below may still
			// publish.
		}
	} else {
		evidence.Milestone = NotObservedOf[Milestone]()
		evidence.MilestoneWhitelist = NotObservedOf[Whitelist]()
	}

	if cp, ok := in.Checkpoint.Get(); ok {
		if resp, hasResp := in.CheckpointResponse.Get(); hasResp {
			source := sourceRef(binding, o.heimdall.StoreIdentity(), o.allocateID(), Request{Operation: OpCheckpointLatest}, resp)
			evidence.Checkpoint = KnownOf(cp.checkpoint(source), in.Origin)
		}
	}

	milestoneWhitelist, err := o.acceptWhitelist(binding, in.Head, in.MilestonePurged, in.MilestoneWhitelist)
	if err != nil {
		return Observation{}, err
	}
	if milestoneWhitelist != nil {
		evidence.MilestoneWhitelist = KnownOf(*milestoneWhitelist, in.Origin)
	}
	checkpointWhitelist, err := o.acceptWhitelist(binding, in.Head, false, in.CheckpointWhitelist)
	if err != nil {
		return Observation{}, err
	}
	if checkpointWhitelist != nil {
		evidence.CheckpointWhitelist = KnownOf(*checkpointWhitelist, in.Origin)
	}

	// The native finalized projection chooses the milestone whitelist,
	// then the checkpoint fallback. It is a native observation Bor can
	// revise, not the L1 GC anchor.
	if w, ok := evidence.MilestoneWhitelist.Get(); ok && !in.MilestonePurged {
		evidence.NativeFinalized = KnownOf(NativeFinalized{
			Basis:             BasisMilestoneWhitelist,
			Anchor:            w.Anchor,
			WhitelistDecision: w.Decision,
		}, in.Origin)
	} else if w, ok := evidence.CheckpointWhitelist.Get(); ok {
		evidence.NativeFinalized = KnownOf(NativeFinalized{
			Basis:             BasisCheckpointWhitelist,
			Anchor:            w.Anchor,
			WhitelistDecision: w.Decision,
		}, in.Origin)
	}

	obs := Observation{
		ID:       o.allocateID(),
		Decision: in.Decision,
		Binding:  binding,
		Evidence: evidence,
	}
	if err := obs.Evidence.Validate(); err != nil {
		return Observation{}, err
	}
	if err := o.store.Append(obs); err != nil {
		return Observation{}, err
	}
	return obs, nil
}

// acceptWhitelist accepts one native whitelist after its canonical match:
// the whitelist anchor must be an ancestor of the observed head. Without
// the match the whitelist is not accepted (ErrInvariant). The narrower
// retained form publishes without source_evidence and without an invented
// digest.
func (o *Observer) acceptWhitelist(binding ObservationBinding, head block.AnchorRef, purged bool, retained block.Optional[RetainedWhitelist]) (*Whitelist, error) {
	w, ok := retained.Get()
	if !ok {
		return nil, nil
	}
	if purged {
		// A purged milestone whitelist is not re-accepted after the purge
		// event; only a fresh native decision brings it back.
		if w.Kind == WhitelistMilestone {
			return nil, nil
		}
	}
	if err := w.Kind.Validate(); err != nil {
		return nil, err
	}
	if err := w.Anchor.Validate(); err != nil {
		return nil, fmt.Errorf("%w: whitelist anchor: %v", ErrInvalidValue, err)
	}
	if err := w.Decision.Validate(); err != nil {
		return nil, fmt.Errorf("%w: whitelist decision: %v", ErrInvalidValue, err)
	}
	matched, err := o.ancestry.IsAncestorOrSelf(w.Anchor, head)
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, fmt.Errorf("%w: %s whitelist anchor %s is not canonical at head %s", ErrInvariant, w.Kind, w.Anchor.Hash, head.Hash)
	}
	out := &Whitelist{
		Kind:                  w.Kind,
		Anchor:                w.Anchor,
		Decision:              w.Decision,
		AncestryBindingDigest: WhitelistAncestryDigest(w.Kind, w.Anchor, w.Decision),
	}
	if resp, hasResp := w.Response.Get(); hasResp {
		out.SourceEvidence = block.Some(sourceRef(binding, o.heimdall.StoreIdentity(), o.allocateID(), w.Request, resp))
	}
	return out, nil
}

// ObserveL1 runs the D63 L1 verification and records its observation:
//
//  1. the native stored checkpoint and the chain/checkpoint parameters
//     from the own Heimdall, with the exact responses and source identity;
//     without a checkpoint id from a full query nothing is invented and
//     the lookup is Unavailable{CHECKPOINT_PENDING};
//  2. the finalized L1 anchor F from the own L1 node; without F the pins
//     stay and the lookup retries later — latest/safe/confirmation
//     counts never substitute F;
//  3. the checked header block id checkpoint_id * child_chain_block_interval
//     (see HeaderBlockID) and the exact hash-pinned RootChain.headerBlocks
//     read at {blockHash: F.hash, requireCanonical: true};
//  4. the strict decode of the result and the cross-check of root, range
//     and proposer against the checkpoint, and of the parameters before
//     and after the read; a mismatch is an explicit identity error and
//     does not raise the anchor;
//  5. the native Bor verifier recomputes the checkpoint root over the
//     pinned header inputs of the continuous canonical range and re-checks
//     the end hash ancestry; only then the verified evidence is durable.
//
// A transient source state returns one Unavailable observation with the
// explicit reason: GC waits, the retained anchor stays, publication
// continues (AC-LIF-07-03). A newer finalized F2 creates a new observation
// bound to F2; the earlier observation and its F are not rewritten
// (AC-LIF-07-05). A candidate that conflicts below the verified frontier
// is a source invariant error.
func (o *Observer) ObserveL1(decision block.NativeDecisionRef, head block.AnchorRef, origin block.EvidenceOrigin) (Observation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := decision.Validate(); err != nil {
		return Observation{}, fmt.Errorf("%w: decision: %v", ErrInvalidValue, err)
	}
	if err := head.Validate(); err != nil {
		return Observation{}, fmt.Errorf("%w: head: %v", ErrInvalidValue, err)
	}
	if err := origin.Validate(); err != nil {
		return Observation{}, fmt.Errorf("%w: origin: %v", ErrInvalidValue, err)
	}
	binding, err := o.binding()
	if err != nil {
		return Observation{}, err
	}
	durable, err := o.store.Observations()
	if err != nil {
		return Observation{}, err
	}
	unavailable := func(reason block.UnavailableReason) (Observation, error) {
		evidence := carriedFrom(durable)
		evidence.L1Checkpoint = UnavailableOf[L1Checkpoint](reason)
		obs := Observation{
			ID:       o.allocateID(),
			Decision: decision,
			Binding:  binding,
			Evidence: evidence,
		}
		if err := o.store.Append(obs); err != nil {
			return Observation{}, err
		}
		return obs, nil
	}

	// Step 1: parameters and the native stored checkpoint.
	chainParams, _, err := o.heimdall.ChainParams()
	if reason, ok := UnavailableReasonOf(err); ok {
		return unavailable(reason)
	}
	if err != nil {
		return Observation{}, err
	}
	checkpointParams, _, err := o.heimdall.CheckpointParams()
	if reason, ok := UnavailableReasonOf(err); ok {
		return unavailable(reason)
	}
	if err != nil {
		return Observation{}, err
	}
	cp, cpResp, err := o.heimdall.CheckpointLatest()
	if reason, ok := UnavailableReasonOf(err); ok {
		return unavailable(reason)
	}
	if err != nil {
		return Observation{}, err
	}
	checkpointID, hasID := cp.CheckpointID.Get()
	if !hasID || checkpointID == 0 {
		// The callbacks carried no number: nothing is invented; the L1
		// verification needs the checkpoint id of a full query.
		return unavailable(block.UnavailableCheckpointPending)
	}
	if cp.State != CheckpointStoredAcknowledged {
		// A buffered checkpoint is not the stored checkpoint the RootChain
		// read verifies.
		return unavailable(block.UnavailableCheckpointPending)
	}

	// Step 2: the finalized L1 anchor F.
	fin, err := o.l1.Finalized()
	if reason, ok := UnavailableReasonOf(err); ok {
		return unavailable(reason)
	}
	if err != nil {
		return Observation{}, err
	}
	if err := fin.Validate(); err != nil {
		return Observation{}, fmt.Errorf("%w: finalized l1 anchor: %v", ErrInvalidValue, err)
	}

	// Step 3: the checked header block id and the hash-pinned read.
	headerBlockID, err := HeaderBlockID(checkpointID, checkpointParams.ChildBlockInterval)
	if err != nil {
		return Observation{}, err
	}
	calldata := HeaderBlocksCalldata(headerBlockID)
	raw, err := o.l1.HeaderBlocks(calldata, fin)
	if reason, ok := UnavailableReasonOf(err); ok {
		return unavailable(reason)
	}
	if err != nil {
		return Observation{}, err
	}

	// Step 4: strict decode and the identity cross-checks.
	result, err := DecodeHeaderBlockResult(raw)
	if err != nil {
		return Observation{}, err
	}
	if result.Root != cp.RootHash {
		return Observation{}, fmt.Errorf("%w: rootchain root %s differs from the checkpoint root %s", ErrIdentity, result.Root, cp.RootHash)
	}
	if result.Start != cp.Start || result.End != cp.End {
		return Observation{}, fmt.Errorf("%w: rootchain range %d..%d differs from the checkpoint range %d..%d", ErrIdentity, result.Start, result.End, cp.Start, cp.End)
	}
	if result.Proposer != cp.Proposer {
		return Observation{}, fmt.Errorf("%w: rootchain proposer %s differs from the checkpoint proposer %s", ErrIdentity, result.Proposer, cp.Proposer)
	}
	afterParams, _, err := o.heimdall.ChainParams()
	if err != nil {
		return Observation{}, err
	}
	afterCheckpointParams, _, err := o.heimdall.CheckpointParams()
	if err != nil {
		return Observation{}, err
	}
	if afterParams.RootChainAddress != chainParams.RootChainAddress ||
		afterCheckpointParams.ChildBlockInterval != checkpointParams.ChildBlockInterval {
		return Observation{}, fmt.Errorf("%w: checkpoint parameters drifted during the read", ErrIdentity)
	}
	current, err := o.l1.Finalized()
	if reason, ok := UnavailableReasonOf(err); ok {
		return unavailable(reason)
	}
	if err != nil {
		return Observation{}, err
	}
	canonical, err := o.l1.IsCanonicalAncestor(fin, current)
	if err != nil {
		return Observation{}, err
	}
	if !canonical {
		return Observation{}, fmt.Errorf("%w: finalized l1 anchor %s is not canonical at %s", ErrInvariant, fin.Hash, current.Hash)
	}

	// Step 5: the native Bor verifier over the continuous canonical range.
	endRef, err := o.ancestry.VerifyCheckpointRoot(cp.Start, cp.End, result.Root)
	if err != nil {
		return Observation{}, err
	}
	if err := endRef.Validate(); err != nil {
		return Observation{}, fmt.Errorf("%w: verified bor end: %v", ErrInvalidValue, err)
	}
	if endRef.Height != cp.End {
		return Observation{}, fmt.Errorf("%w: verified bor end height %d differs from the checkpoint end %d", ErrInvariant, endRef.Height, cp.End)
	}
	onCanonical, err := o.ancestry.IsAncestorOrSelf(endRef, head)
	if err != nil {
		return Observation{}, err
	}
	if !onCanonical {
		return Observation{}, fmt.Errorf("%w: verified bor end %s is not canonical at head %s", ErrInvariant, endRef.Hash, head.Hash)
	}

	evidence := carriedFrom(durable)
	cpSource := sourceRef(binding, o.heimdall.StoreIdentity(), o.allocateID(), Request{Operation: OpCheckpointLatest}, cpResp)
	evidence.Checkpoint = KnownOf(cp.checkpoint(cpSource), origin)
	evidence.L1Checkpoint = KnownOf(L1Checkpoint{
		L1Network:          binding.L1Network,
		SupportingNodeID:   binding.L1NodeID,
		SourcesBinding:     binding.SourcesBinding,
		RootChainAddress:   chainParams.RootChainAddress,
		CheckpointID:       checkpointID,
		HeaderBlockID:      headerBlockID,
		ChildBlockInterval: checkpointParams.ChildBlockInterval,
		BorStart:           cp.Start,
		BorEnd:             cp.End,
		CheckpointRoot:     result.Root,
		BorEndRef:          endRef,
		BorRangeDigest:     BorRangeDigest(cp.Start, cp.End, endRef),
		FinalizedL1Anchor:  fin,
		Verification: FinalizedContractState{
			CallBlockHash:         fin.Hash,
			RootChainParamsDigest: ParamsDigest(chainParams.RootChainAddress, checkpointParams.ChildBlockInterval),
			CalldataDigest:        DigestBytes(calldata),
			ResultDigest:          DigestBytes(raw),
			ResultCreatedAt:       result.CreatedAt,
			ResultProposer:        result.Proposer,
		},
	}, origin)

	// The verified frontier only advances. A conflict below or at the
	// verified anchor is a source invariant error; the same anchor at a
	// newer finalized F2 is a new observation, not a rewrite.
	if frontier, ok, err := o.store.Frontier(); err != nil {
		return Observation{}, err
	} else if ok {
		switch {
		case evidence.L1Checkpoint.Value.BorEnd < frontier.BorEnd:
			return Observation{}, fmt.Errorf("%w: candidate bor end %d below the verified frontier %d", ErrInvariant, evidence.L1Checkpoint.Value.BorEnd, frontier.BorEnd)
		case evidence.L1Checkpoint.Value.BorEnd == frontier.BorEnd:
			if evidence.L1Checkpoint.Value.CheckpointRoot != frontier.CheckpointRoot ||
				evidence.L1Checkpoint.Value.BorEndRef.Hash != frontier.BorEndRef.Hash {
				return Observation{}, fmt.Errorf("%w: candidate conflicts with the verified anchor at bor end %d", ErrInvariant, frontier.BorEnd)
			}
		default:
			if err := o.store.SaveFrontier(evidence.L1Checkpoint.Value); err != nil {
				return Observation{}, err
			}
		}
	} else if err := o.store.SaveFrontier(evidence.L1Checkpoint.Value); err != nil {
		return Observation{}, err
	}

	obs := Observation{
		ID:       o.allocateID(),
		Decision: decision,
		Binding:  binding,
		Evidence: evidence,
	}
	if err := obs.Evidence.Validate(); err != nil {
		return Observation{}, err
	}
	if err := o.store.Append(obs); err != nil {
		return Observation{}, err
	}
	return obs, nil
}

// Markers projects the durable observation stream into the Polygon
// lifecycle markers of one snapshot. It performs no network read: with the
// GC source unavailable it returns the explicit Unavailable reason, and
// the publication of ready records continues. The milestone and checkpoint
// references are the accepted whitelist anchors (the finality-bearing
// references accepted after the canonical match); without an accepted
// whitelist a reference stays NotObserved, and a checkpoint root is never
// substituted for a block hash.
func (o *Observer) Markers() (block.PolygonMarkers, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	durable, err := o.store.Observations()
	if err != nil {
		return block.PolygonMarkers{}, err
	}
	var out block.PolygonMarkers
	out.Milestone = block.NotObserved()
	out.Checkpoint = block.NotObserved()
	out.NativeFinalized = block.NotObserved()
	out.L1Checkpoint = block.NotObserved()
	if len(durable) == 0 {
		return out, nil
	}
	last := durable[len(durable)-1].Evidence
	if w, ok := last.MilestoneWhitelist.Get(); ok {
		out.Milestone = block.Known(w.Anchor, last.MilestoneWhitelist.Origin)
	}
	if w, ok := last.CheckpointWhitelist.Get(); ok {
		out.Checkpoint = block.Known(w.Anchor, last.CheckpointWhitelist.Origin)
	}
	if f, ok := last.NativeFinalized.Get(); ok {
		out.NativeFinalized = block.Known(f.Anchor, last.NativeFinalized.Origin)
	}
	switch last.L1Checkpoint.Kind {
	case EvidenceKnown:
		if v, ok := last.L1Checkpoint.Get(); ok {
			out.L1Checkpoint = block.Known(v.BorEndRef, last.L1Checkpoint.Origin)
		}
	case EvidenceUnavailable:
		out.L1Checkpoint = block.ReferenceObservation{Kind: block.ReferenceUnavailable, Reason: last.L1Checkpoint.Reason}
	}
	if err := out.Validate(); err != nil {
		return block.PolygonMarkers{}, err
	}
	return out, nil
}

// NativeCoverage is the native projection coverage of one activation.
type NativeCoverage uint8

// Native coverage states. Zero is not a valid state.
const (
	NativeMilestoneCovered NativeCoverage = iota + 1
	NativeCheckpointCovered
	CanonicalNoNativeFinality
)

var nativeCoverageNames = []string{"NATIVE_MILESTONE_COVERED", "NATIVE_CHECKPOINT_COVERED", "CANONICAL_NO_NATIVE_FINALITY"}

// String returns the contract name of c.
func (c NativeCoverage) String() string {
	if c == 0 || int(c) > len(nativeCoverageNames) {
		return "INVALID"
	}
	return nativeCoverageNames[c-1]
}

// L1Coverage is the L1 checkpoint coverage of one activation.
type L1Coverage uint8

// L1 coverage states. Zero is not a valid state.
const (
	L1CheckpointCovered L1Coverage = iota + 1
	L1NotVerified
)

var l1CoverageNames = []string{"L1_CHECKPOINT_COVERED", "L1_NOT_VERIFIED"}

// String returns the contract name of c.
func (c L1Coverage) String() string {
	if c == 0 || int(c) > len(l1CoverageNames) {
		return "INVALID"
	}
	return l1CoverageNames[c-1]
}

// Projection is the coverage projection of one activation anchor: the
// native projection (milestone whitelist first, checkpoint fallback) and
// the L1 checkpoint coverage stay separate; without the block ancestry
// match no coverage is assigned.
type Projection struct {
	Native NativeCoverage
	L1     L1Coverage
}

// Projection computes the coverage projection of one activation anchor
// from the durable observation stream and the verified frontier. It
// performs no network read of the GC sources.
func (o *Observer) Projection(activation block.AnchorRef) (Projection, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := activation.Validate(); err != nil {
		return Projection{}, fmt.Errorf("%w: activation: %v", ErrInvalidValue, err)
	}
	durable, err := o.store.Observations()
	if err != nil {
		return Projection{}, err
	}
	out := Projection{Native: CanonicalNoNativeFinality, L1: L1NotVerified}
	if len(durable) > 0 {
		last := durable[len(durable)-1].Evidence
		if f, ok := last.NativeFinalized.Get(); ok {
			covered, err := o.ancestry.IsAncestorOrSelf(activation, f.Anchor)
			if err != nil {
				return Projection{}, err
			}
			if covered {
				switch f.Basis {
				case BasisMilestoneWhitelist:
					out.Native = NativeMilestoneCovered
				case BasisCheckpointWhitelist:
					out.Native = NativeCheckpointCovered
				}
			}
		}
	}
	if frontier, ok, err := o.store.Frontier(); err != nil {
		return Projection{}, err
	} else if ok {
		covered, err := o.ancestry.IsAncestorOrSelf(activation, frontier.BorEndRef)
		if err != nil {
			return Projection{}, err
		}
		if covered {
			out.L1 = L1CheckpointCovered
		}
	}
	return out, nil
}

// Resend returns the durable observation of one earlier identity without
// any source lookup: recovery and late serialization take the saved
// digest, binding and evidence bytes from the saved observation, never
// from the current sources_file (AC-LIF-07-09).
func (o *Observer) Resend(observationID string) (Observation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.store.Observation(observationID)
}
