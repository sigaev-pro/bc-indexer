// The durable store seam and the Registry engine of E05-01.registry. The
// engine mirrors native decisions, applies decided canonical decisions to
// the per-network activation records in one atomic commit, defers
// applications whose corpus is missing until a targeted replay creates it,
// and completes interrupted applications at restart.
package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"sync"

	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// keyPrefix starts every key. Every key is longer than 33 bytes and starts
// with "e", so a native key-value store that hosts it never deletes it in an
// offline prune (the E01-02.pins ledger convention).
const keyPrefix = "extractor-lifecycle-registry-v1/"

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Store is the durable key-value store of the registry. Put makes a write
// visible but does not promise durability; SyncKeyValue makes every write
// since the last sync durable. A power loss may keep any subset of the
// unsynced writes, which is why every multi-record commit of the registry
// stores its full intent in one record before it changes per-activation
// records.
type Store interface {
	Has(key []byte) (bool, error)
	Get(key []byte) ([]byte, error)
	Put(key, value []byte) error
	SyncKeyValue() error
	// Range visits every key that starts with prefix, in ascending byte
	// order, with the value visible through unsynced writes. Stopping the
	// visit with an error returns that error.
	Range(prefix []byte, visit func(key, value []byte) error) error
}

// decisionKey builds the key of one decision record. The network is part of
// the record, not of the key: one producer run observes one network, and
// Intend refuses a second network for a run.
func decisionKey(producer, run semantic.ID16, sequence uint64) []byte {
	return []byte(keyPrefix + "decision/" + producer.String() + "/" + run.String() + "/" +
		strconv.FormatUint(sequence, 10))
}

// activationKeyOf builds the key of one activation record.
func activationKeyOf(key ActivationKey) []byte {
	return []byte(keyPrefix + "activation/" + key.ProducerID.String() + "/" + key.RunID.String() + "/" +
		strconv.FormatUint(key.Activation.DecisionSequence, 10) + "/" +
		strconv.FormatUint(key.Activation.EntityOrdinal, 10))
}

// corpusKey builds the key of one corpus record.
func corpusKey(producer, run semantic.ID16, block semantic.Hash32) []byte {
	return []byte(keyPrefix + "corpus/" + producer.String() + "/" + run.String() + "/" + block.String())
}

// runDecisionsPrefix is the key prefix of every decision of one producer run.
func runDecisionsPrefix(producer, run semantic.ID16) []byte {
	return []byte(keyPrefix + "decision/" + producer.String() + "/" + run.String() + "/")
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}

// sealer is a record that carries a record digest.
type sealer interface {
	seal() error
}

// putRecord seals, writes and syncs one record.
func putRecord(value sealer, key []byte, db Store) error {
	if err := value.seal(); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := db.Put(key, data); err != nil {
		return err
	}
	return db.SyncKeyValue()
}

// PublishedEvent is one event of an applied decision that the layer in front
// of the registry may publish.
type PublishedEvent struct {
	Descriptor semantic.SemanticDescriptor
	Content    string
}

// ApplyResult reports what one application did.
type ApplyResult struct {
	Decision NativeDecisionRef
	// Duplicate reports an idempotent repeat of an already applied
	// decision: nothing changed and no new activation, revision or batch
	// was created (AC-CAP-07-05).
	Duplicate bool
	// Deferred reports an application stored but waiting for the targeted
	// corpus replay of Blocks.
	Deferred bool
	Blocks   []semantic.Hash32
	Applied  []ActivationKey
	Statuses []ActivationKey
	Removed  []ActivationKey
}

// Registry is the durable activation and revision registry with the native
// decision mirror of one native store. It is safe for concurrent use; the
// single-writer discipline of the native observation guard serializes the
// callers, and the mutex keeps the mirror state consistent.
type Registry struct {
	mu sync.Mutex
	db Store
}

// Open validates every stored record and completes the applications that a
// power loss interrupted (AC-CAP-07-04). It returns the number of decisions
// the reconciliation completed.
func Open(db Store) (*Registry, int, error) {
	if db == nil {
		return nil, 0, fmt.Errorf("%w: missing store", ErrInvalid)
	}
	r := &Registry{db: db}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validateAll(); err != nil {
		return nil, 0, err
	}
	completed, err := r.reconcile()
	if err != nil {
		return nil, 0, err
	}
	return r, completed, nil
}

// validateAll decodes and verifies every stored record.
func (r *Registry) validateAll() error {
	wrap := func(err error) error { return fmt.Errorf("%w: %v", ErrCorrupt, err) }
	err := r.db.Range([]byte(keyPrefix), func(key, value []byte) error {
		switch {
		case bytes.HasPrefix(key, []byte(keyPrefix+"decision/")):
			var d DecisionRecord
			if err := strictDecode(value, &d); err != nil {
				return wrap(err)
			}
			if err := d.verify(); err != nil {
				return wrap(err)
			}
		case bytes.HasPrefix(key, []byte(keyPrefix+"activation/")):
			var a ActivationRecord
			if err := strictDecode(value, &a); err != nil {
				return wrap(err)
			}
			if err := a.verify(); err != nil {
				return wrap(err)
			}
		case bytes.HasPrefix(key, []byte(keyPrefix+"corpus/")):
			var c CorpusRecord
			if err := strictDecode(value, &c); err != nil {
				return wrap(err)
			}
			if err := c.verify(); err != nil {
				return wrap(err)
			}
		default:
			return wrap(fmt.Errorf("unexpected key %s", key))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return nil
}

// loadDecision returns the mirrored decision; ok is false when it is absent.
func (r *Registry) loadDecision(ref NativeDecisionRef) (DecisionRecord, bool, error) {
	key := decisionKey(ref.ProducerID, ref.RunID, ref.DecisionSequence)
	ok, err := r.db.Has(key)
	if err != nil {
		return DecisionRecord{}, false, err
	}
	if !ok {
		return DecisionRecord{}, false, nil
	}
	data, err := r.db.Get(key)
	if err != nil {
		return DecisionRecord{}, false, err
	}
	var d DecisionRecord
	if err := strictDecode(data, &d); err != nil {
		return DecisionRecord{}, false, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	if err := d.verify(); err != nil {
		return DecisionRecord{}, false, err
	}
	if d.Ref != ref {
		return DecisionRecord{}, false, fmt.Errorf("%w: decision key holds %s", ErrCorrupt, d.Ref)
	}
	return d, true, nil
}

// loadActivation returns one activation record; ok is false when absent.
func (r *Registry) loadActivation(key ActivationKey) (ActivationRecord, bool, error) {
	storeKey := activationKeyOf(key)
	ok, err := r.db.Has(storeKey)
	if err != nil {
		return ActivationRecord{}, false, err
	}
	if !ok {
		return ActivationRecord{}, false, nil
	}
	data, err := r.db.Get(storeKey)
	if err != nil {
		return ActivationRecord{}, false, err
	}
	var a ActivationRecord
	if err := strictDecode(data, &a); err != nil {
		return ActivationRecord{}, false, fmt.Errorf("%w: %s: %v", ErrCorrupt, storeKey, err)
	}
	if err := a.verify(); err != nil {
		return ActivationRecord{}, false, err
	}
	return a, true, nil
}

// loadCorpus returns the corpus record of one block; ok is false when the
// block has no corpus.
func (r *Registry) loadCorpus(producer, run semantic.ID16, block semantic.Hash32) (CorpusRecord, bool,
	error) {
	key := corpusKey(producer, run, block)
	ok, err := r.db.Has(key)
	if err != nil {
		return CorpusRecord{}, false, err
	}
	if !ok {
		return CorpusRecord{}, false, nil
	}
	data, err := r.db.Get(key)
	if err != nil {
		return CorpusRecord{}, false, err
	}
	var c CorpusRecord
	if err := strictDecode(data, &c); err != nil {
		return CorpusRecord{}, false, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	if err := c.verify(); err != nil {
		return CorpusRecord{}, false, err
	}
	return c, true, nil
}

// Intend mirrors one native transition intent: the decision passes to
// INTENDED. A repeated intent of the same decision is idempotent; a
// different intent of the same decision sequence, a sequence out of order or
// a second network of one producer run is refused.
func (r *Registry) Intend(ref NativeDecisionRef, net semantic.Network, kind DecisionKind,
	intentDigest string) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if !validKind(kind) {
		return fmt.Errorf("%w: decision kind %q", ErrInvalid, kind)
	}
	if net.GenesisHash == (semantic.Hash32{}) {
		return fmt.Errorf("%w: unset network genesis", ErrInvalid)
	}
	if !digestPattern.MatchString(intentDigest) {
		return fmt.Errorf("%w: intent digest %q", ErrInvalid, intentDigest)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if held, ok, err := r.loadDecision(ref); err != nil {
		return err
	} else if ok {
		if held.Network != net || held.Kind != kind || held.IntentDigest != intentDigest {
			return fmt.Errorf("%w: %s already holds %s %s", ErrDecisionConflict, ref, held.Kind,
				held.IntentDigest)
		}
		return nil
	}
	var maxSequence uint64
	var runNetwork semantic.Network
	var seen bool
	err := r.db.Range(runDecisionsPrefix(ref.ProducerID, ref.RunID), func(key, value []byte) error {
		var d DecisionRecord
		if err := strictDecode(value, &d); err != nil {
			return err
		}
		seen = true
		runNetwork = d.Network
		if d.Ref.DecisionSequence > maxSequence {
			maxSequence = d.Ref.DecisionSequence
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if seen {
		if runNetwork != net {
			return fmt.Errorf("%w: run %s.%s observes %s, not %s", ErrDecisionConflict, ref.ProducerID,
				ref.RunID, runNetwork, net)
		}
		if ref.DecisionSequence <= maxSequence {
			return fmt.Errorf("%w: %d after %d", ErrDecisionOrder, ref.DecisionSequence, maxSequence)
		}
	}
	d := DecisionRecord{Ref: ref, Network: net, Kind: kind, State: StateIntended, IntentDigest: intentDigest}
	return putRecord(&d, decisionKey(ref.ProducerID, ref.RunID, ref.DecisionSequence), r.db)
}

// SettleDecided mirrors the settled NATIVE_DECIDED marker. Repeating the
// marker after the decision was decided or applied is idempotent.
func (r *Registry) SettleDecided(ref NativeDecisionRef) error {
	return r.settle(ref, StateDecided)
}

// SettleRejected mirrors the settled NATIVE_REJECTED marker. A rejected
// decision is terminal: its speculative corpus never becomes Applied.
func (r *Registry) SettleRejected(ref NativeDecisionRef) error {
	return r.settle(ref, StateRejected)
}

func (r *Registry) settle(ref NativeDecisionRef, target DecisionState) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok, err := r.loadDecision(ref)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownDecision, ref)
	}
	switch d.State {
	case StateIntended:
		if target == StateDecided && d.Kind == KindSide {
			// A SIDE decision carries no public events: settling it
			// closes its checkpoint, so later decisions of the run are
			// not blocked behind a decision that never applies.
			d.State = StateApplied
			d.Application = &ApplicationPlan{}
		} else {
			d.State = target
		}
		return putRecord(&d, decisionKey(ref.ProducerID, ref.RunID, ref.DecisionSequence), r.db)
	case target:
		return nil
	case StateApplied:
		// An applied decision was decided before; a repeated decided
		// marker changes nothing.
		if target == StateDecided {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is %s, not INTENDED", ErrDecisionState, ref, d.State)
}

// Decision returns the mirrored decision; ok is false when it is absent.
func (r *Registry) Decision(ref NativeDecisionRef) (DecisionRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok, err := r.loadDecision(ref)
	return d, ok, err
}

// CaptureCorpus records the corpus of one block captured under a decision:
// the durable evidence of a completed execution, speculative until a
// canonical decision applies the block. The decision must already be
// mirrored, in any state: execution is observed before native settles the
// branch.
func (r *Registry) CaptureCorpus(ref NativeDecisionRef, block semantic.Hash32, corpusDigest string) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if block == (semantic.Hash32{}) {
		return fmt.Errorf("%w: unset corpus block", ErrInvalid)
	}
	if !digestPattern.MatchString(corpusDigest) {
		return fmt.Errorf("%w: corpus digest %q", ErrInvalid, corpusDigest)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok, err := r.loadDecision(ref)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownDecision, ref)
	}
	if held, ok, err := r.loadCorpus(ref.ProducerID, ref.RunID, block); err != nil {
		return err
	} else if ok {
		if held.CorpusDigest != corpusDigest {
			return fmt.Errorf("%w: %s holds %s, not %s", ErrCorpusConflict, block, held.CorpusDigest,
				corpusDigest)
		}
		return nil
	}
	c := CorpusRecord{Network: d.Network, ProducerID: ref.ProducerID, RunID: ref.RunID, Block: block,
		CorpusDigest: corpusDigest, CapturedBy: ref}
	return putRecord(&c, corpusKey(ref.ProducerID, ref.RunID, block), r.db)
}

// ReplayCorpus records the corpus of one block produced by a targeted
// replay. Only a block that the stored application of a settled canonical
// decision needs and that has no corpus yet may be replayed: the registry
// never scans an unknown downtime range (AC-LIF-01-02).
func (r *Registry) ReplayCorpus(ref NativeDecisionRef, block semantic.Hash32, corpusDigest string) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if block == (semantic.Hash32{}) {
		return fmt.Errorf("%w: unset replay block", ErrInvalid)
	}
	if !digestPattern.MatchString(corpusDigest) {
		return fmt.Errorf("%w: corpus digest %q", ErrInvalid, corpusDigest)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok, err := r.loadDecision(ref)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownDecision, ref)
	}
	if d.State != StateDecided {
		return fmt.Errorf("%w: replay serves a deferred application, but %s is %s", ErrUnknownReplayTarget,
			ref, d.State)
	}
	if d.Application == nil {
		return fmt.Errorf("%w: %s has no stored application", ErrUnknownReplayTarget, ref)
	}
	needed := false
	for _, ev := range d.Application.Events {
		if ev.Descriptor.Kind == semantic.KindApplied && ev.Descriptor.Activation.BlockHash == block {
			needed = true
			break
		}
	}
	if !needed {
		return fmt.Errorf("%w: %s needs no corpus of %s", ErrUnknownReplayTarget, ref, block)
	}
	if held, ok, err := r.loadCorpus(ref.ProducerID, ref.RunID, block); err != nil {
		return err
	} else if ok {
		if held.CorpusDigest != corpusDigest {
			return fmt.Errorf("%w: %s holds %s, not %s", ErrCorpusConflict, block, held.CorpusDigest,
				corpusDigest)
		}
		return nil
	}
	replayedBy := ref
	c := CorpusRecord{Network: d.Network, ProducerID: ref.ProducerID, RunID: ref.RunID, Block: block,
		CorpusDigest: corpusDigest, CapturedBy: ref, ReplayedBy: &replayedBy}
	return putRecord(&c, corpusKey(ref.ProducerID, ref.RunID, block), r.db)
}

// Corpus returns the corpus record of one block; ok is false when the block
// has no corpus.
func (r *Registry) Corpus(producer, run semantic.ID16, block semantic.Hash32) (CorpusRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadCorpus(producer, run, block)
}

// planKinds lists the public event kinds a decided decision of each kind may
// apply. A SIDE decision may not apply any event: its corpus is speculative.
var planKinds = map[DecisionKind][]semantic.Kind{
	KindImport:    {semantic.KindApplied},
	KindCanonical: {semantic.KindApplied, semantic.KindRemoved},
	KindFinality:  {semantic.KindStatus},
	KindSide:      {},
}

// eventKeyOf returns the activation key one event targets. Applied events
// target the activation of the applying decision itself; Status and Removed
// events target the recorded activation they name.
func eventKeyOf(ref NativeDecisionRef, net semantic.Network, ev EventPlan) ActivationKey {
	return ActivationKey{Network: net, ProducerID: ref.ProducerID, RunID: ref.RunID,
		Activation: ev.Descriptor.Activation.ActivationID}
}

// checkPlan validates an application plan against the mirror and the
// activation records. It reads the store but changes nothing. A record that
// an interrupted derivation already wrote exactly as the plan derives it is
// accepted, so the same check serves a fresh application and a restart.
func (r *Registry) checkPlan(d DecisionRecord, plan ApplicationPlan) ([]semantic.Hash32, error) {
	allowed := planKinds[d.Kind]
	missing := []semantic.Hash32{}
	seen := map[ActivationKey]bool{}
	for i, ev := range plan.Events {
		if err := ev.Descriptor.Validate(); err != nil {
			return nil, err
		}
		if !digestPattern.MatchString(ev.Content) {
			return nil, fmt.Errorf("%w: event %d content digest %q", ErrInvalid, i, ev.Content)
		}
		if ev.Descriptor.Network != d.Network {
			return nil, fmt.Errorf("%w: event %d of another network", ErrPlanConflict, i)
		}
		if ev.Descriptor.Activation.ProducerID != d.Ref.ProducerID ||
			ev.Descriptor.Activation.RunID != d.Ref.RunID {
			return nil, fmt.Errorf("%w: event %d of another producer run", ErrPlanConflict, i)
		}
		if !containsKind(allowed, ev.Descriptor.Kind) {
			return nil, fmt.Errorf("%w: %s decision may not apply %s events", ErrPlanConflict, d.Kind,
				ev.Descriptor.Kind)
		}
		key := eventKeyOf(d.Ref, d.Network, ev)
		if seen[key] {
			return nil, fmt.Errorf("%w: two events target %s", ErrPlanConflict, activationName(key))
		}
		seen[key] = true
		rec, ok, err := r.loadActivation(key)
		if err != nil {
			return nil, err
		}
		switch ev.Descriptor.Kind {
		case semantic.KindApplied:
			if ev.Descriptor.Activation.ActivationID.DecisionSequence != d.Ref.DecisionSequence {
				return nil, fmt.Errorf("%w: applied event of decision %d under %d", ErrPlanConflict,
					d.Ref.DecisionSequence, ev.Descriptor.Activation.ActivationID.DecisionSequence)
			}
			corpus, hasCorpus, err := r.loadCorpus(d.Ref.ProducerID, d.Ref.RunID,
				ev.Descriptor.Activation.BlockHash)
			if err != nil {
				return nil, err
			}
			if !hasCorpus {
				missing = append(missing, ev.Descriptor.Activation.BlockHash)
				continue
			}
			if ok {
				if rec.State != ActivationActive || rec.OriginDigest != ev.Content ||
					rec.CorpusDigest != corpus.CorpusDigest {
					return nil, fmt.Errorf("%w: activation %s already holds another application",
						ErrCorrupt, activationName(key))
				}
				continue
			}
		case semantic.KindStatus:
			if !ok {
				return nil, fmt.Errorf("%w: status targets unknown activation %s", ErrPlanConflict,
					activationName(key))
			}
			if rec.MaxRevision == ev.Descriptor.Revision && rec.StatusDigest == ev.Content {
				continue
			}
			if rec.State == ActivationRemoved {
				return nil, fmt.Errorf("%w: status targets %s", ErrTerminalActivation, activationName(key))
			}
			if ev.Descriptor.Revision != rec.MaxRevision+1 {
				return nil, fmt.Errorf("%w: status revision %d, frontier %d", ErrRevision,
					ev.Descriptor.Revision, rec.MaxRevision+1)
			}
		default:
			if !ok {
				return nil, fmt.Errorf("%w: removed targets unknown activation %s", ErrPlanConflict,
					activationName(key))
			}
			if rec.State == ActivationRemoved {
				if rec.RemovedRevision == ev.Descriptor.Revision {
					continue
				}
				return nil, fmt.Errorf("%w: removed targets %s at another revision", ErrTerminalActivation,
					activationName(key))
			}
			if ev.Descriptor.Revision != rec.MaxRevision+1 {
				return nil, fmt.Errorf("%w: removed revision %d, frontier %d", ErrRevision,
					ev.Descriptor.Revision, rec.MaxRevision+1)
			}
		}
	}
	return missing, nil
}

func containsKind(kinds []semantic.Kind, kind semantic.Kind) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// deriveApplied returns the activation record an Applied event creates.
func deriveApplied(d DecisionRecord, ev EventPlan, corpus CorpusRecord) ActivationRecord {
	descriptor := ev.Descriptor
	a := ActivationRecord{Network: d.Network, ProducerID: d.Ref.ProducerID, RunID: d.Ref.RunID,
		Activation: descriptor.Activation.ActivationID, State: ActivationActive,
		BlockHash: descriptor.Activation.BlockHash, Family: descriptor.Family,
		OriginDigest: ev.Content, CorpusDigest: corpus.CorpusDigest,
		Speculative: corpus.CapturedBy != d.Ref}
	if descriptor.Activation.Transaction != nil {
		a.HasTx = true
		a.TxPosition = descriptor.Activation.Transaction.NativePosition
		a.TxHash = descriptor.Activation.Transaction.TransactionHash
	}
	return a
}

// sameOrigin reports whether rec is exactly the record the Applied event
// derived, ignoring the publication flags and the record digest.
func (a ActivationRecord) sameOrigin(other ActivationRecord) bool {
	return a.State == other.State && a.BlockHash == other.BlockHash && a.Family == other.Family &&
		a.HasTx == other.HasTx && a.TxPosition == other.TxPosition && a.TxHash == other.TxHash &&
		a.OriginDigest == other.OriginDigest && a.CorpusDigest == other.CorpusDigest &&
		a.Speculative == other.Speculative
}

// Apply applies one settled canonical decision to the activation records.
// The application plan is stored durably first; if the corpus of an Applied
// event is missing, the application is deferred with *CorpusReplayError and
// ReplayCorpus must create the corpus; otherwise every activation record is
// updated and the decision passes the applied-decision frontier. Repeating
// an applied decision with the same plan is idempotent; a different plan is
// ErrAlreadyApplied (AC-CAP-07-05).
func (r *Registry) Apply(ref NativeDecisionRef, plan ApplicationPlan) (ApplyResult, error) {
	if err := ref.Validate(); err != nil {
		return ApplyResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok, err := r.loadDecision(ref)
	if err != nil {
		return ApplyResult{}, err
	}
	if !ok {
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrUnknownDecision, ref)
	}
	// A SIDE decision never publishes events, whatever its state: its
	// corpus is speculative (AC-LIF-01-01).
	if d.Kind == KindSide && len(plan.Events) != 0 {
		return ApplyResult{}, fmt.Errorf("%w: %s is a SIDE decision", ErrNotCanonical, ref)
	}
	switch d.State {
	case StateRejected:
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrRejected, ref)
	case StateIntended:
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrNotDecided, ref)
	case StateApplied:
		if d.Application != nil && d.Application.equal(plan) {
			return ApplyResult{Decision: ref, Duplicate: true}, nil
		}
		return ApplyResult{}, fmt.Errorf("%w: %s", ErrAlreadyApplied, ref)
	}
	if err := r.checkRunOrder(d); err != nil {
		return ApplyResult{}, err
	}
	if d.Application != nil && !d.Application.equal(plan) {
		return ApplyResult{}, fmt.Errorf("%w: %s keeps its first stored plan", ErrAlreadyApplied, ref)
	}
	missing, err := r.checkPlan(d, plan)
	if err != nil {
		return ApplyResult{}, err
	}
	if d.Application == nil {
		d.Application = &plan
		if err := putRecord(&d, decisionKey(ref.ProducerID, ref.RunID, ref.DecisionSequence), r.db); err != nil {
			return ApplyResult{}, err
		}
	}
	if len(missing) > 0 {
		return ApplyResult{Decision: ref, Deferred: true, Blocks: missing}, &CorpusReplayError{Decision: ref,
			Blocks: missing}
	}
	return r.commitApplication(d)
}

// checkRunOrder refuses an application behind an earlier decision of the
// same producer run that native settled but the registry has not closed yet:
// applications commit in decision order, so reconciliation stays a prefix
// completion.
func (r *Registry) checkRunOrder(d DecisionRecord) error {
	prefix := runDecisionsPrefix(d.Ref.ProducerID, d.Ref.RunID)
	err := r.db.Range(prefix, func(key, value []byte) error {
		var other DecisionRecord
		if err := strictDecode(value, &other); err != nil {
			return err
		}
		if other.Ref.DecisionSequence >= d.Ref.DecisionSequence {
			return nil
		}
		if other.State != StateApplied && other.State != StateRejected {
			return fmt.Errorf("%w: %s waits for %s in %s", ErrDecisionOrder, d.Ref, other.Ref,
				other.State)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// commitApplication writes every activation record of the plan and moves the
// decision over the applied-decision frontier. Both Apply and Reconcile call
// it; the derivation is idempotent, so a restart that already sees a subset
// of the writes converges to the same records.
func (r *Registry) commitApplication(d DecisionRecord) (ApplyResult, error) {
	result := ApplyResult{Decision: d.Ref}
	for _, ev := range d.Application.Events {
		key := eventKeyOf(d.Ref, d.Network, ev)
		rec, ok, err := r.loadActivation(key)
		if err != nil {
			return ApplyResult{}, err
		}
		switch ev.Descriptor.Kind {
		case semantic.KindApplied:
			corpus, _, err := r.loadCorpus(d.Ref.ProducerID, d.Ref.RunID,
				ev.Descriptor.Activation.BlockHash)
			if err != nil {
				return ApplyResult{}, err
			}
			derived := deriveApplied(d, ev, corpus)
			if ok {
				if !rec.sameOrigin(derived) {
					return ApplyResult{}, fmt.Errorf("%w: activation %s holds another application",
						ErrCorrupt, activationName(key))
				}
			} else {
				if err := putRecord(&derived, activationKeyOf(key), r.db); err != nil {
					return ApplyResult{}, err
				}
				result.Applied = append(result.Applied, key)
			}
		case semantic.KindStatus:
			if rec.MaxRevision == ev.Descriptor.Revision && rec.StatusDigest == ev.Content {
				continue
			}
			next := rec
			next.MaxRevision = ev.Descriptor.Revision
			next.StatusDigest = ev.Content
			if err := putRecord(&next, activationKeyOf(key), r.db); err != nil {
				return ApplyResult{}, err
			}
			result.Statuses = append(result.Statuses, key)
		default:
			if rec.State == ActivationRemoved {
				continue
			}
			next := rec
			next.State = ActivationRemoved
			next.MaxRevision = ev.Descriptor.Revision
			next.RemovedRevision = ev.Descriptor.Revision
			removedBy := d.Ref
			next.RemovedDecision = &removedBy
			if err := putRecord(&next, activationKeyOf(key), r.db); err != nil {
				return ApplyResult{}, err
			}
			result.Removed = append(result.Removed, key)
		}
	}
	d.State = StateApplied
	if err := putRecord(&d, decisionKey(d.Ref.ProducerID, d.Ref.RunID, d.Ref.DecisionSequence), r.db); err != nil {
		return ApplyResult{}, err
	}
	return result, nil
}

// Reconcile completes the applications a power loss interrupted: every
// decided decision with a stored plan whose corpus is present passes the
// applied-decision frontier, and its activation records become exactly the
// records the plan derives. A decision still missing its corpus stays
// deferred. Open calls this at startup; it is exported for explicit runs.
func (r *Registry) Reconcile() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reconcile()
}

func (r *Registry) reconcile() (int, error) {
	decisions := []DecisionRecord{}
	err := r.db.Range([]byte(keyPrefix+"decision/"), func(key, value []byte) error {
		var d DecisionRecord
		if err := strictDecode(value, &d); err != nil {
			return err
		}
		if d.State == StateDecided && d.Application != nil {
			decisions = append(decisions, d)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	// Decisions commit in decision order, so the completion of one decision
	// never reads a record a later decision owns.
	sort.Slice(decisions, func(i, j int) bool {
		left, right := decisions[i], decisions[j]
		if left.Ref.ProducerID != right.Ref.ProducerID {
			return left.Ref.ProducerID.String() < right.Ref.ProducerID.String()
		}
		if left.Ref.RunID != right.Ref.RunID {
			return left.Ref.RunID.String() < right.Ref.RunID.String()
		}
		return left.Ref.DecisionSequence < right.Ref.DecisionSequence
	})
	completed := 0
	for _, d := range decisions {
		if missing, err := r.checkPlan(d, *d.Application); err != nil {
			return completed, err
		} else if len(missing) > 0 {
			continue
		}
		if _, err := r.commitApplication(d); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

// Activation returns one activation record; ok is false when it is absent.
func (r *Registry) Activation(key ActivationKey) (ActivationRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadActivation(key)
}

// NextRevision returns the next free lifecycle revision of one activation:
// revisions of StatusChanged and Removed events come from the same counter.
func (r *Registry) NextRevision(key ActivationKey) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok, err := r.loadActivation(key)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrUnknownActivation, activationName(key))
	}
	if rec.State == ActivationRemoved {
		return 0, fmt.Errorf("%w: %s", ErrTerminalActivation, activationName(key))
	}
	return rec.MaxRevision + 1, nil
}

// MarkReady mirrors the READY publication commit of the Applied event of one
// activation. The activation stays cancellable: a later canonical decision
// still removes it, published or not, acknowledged or not.
func (r *Registry) MarkReady(key ActivationKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok, err := r.loadActivation(key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownActivation, activationName(key))
	}
	if rec.State != ActivationActive {
		return fmt.Errorf("%w: %s is %s", ErrActivationState, activationName(key), rec.State)
	}
	if rec.Ready {
		return nil
	}
	rec.Ready = true
	return putRecord(&rec, activationKeyOf(key), r.db)
}

// MarkAcked mirrors the delivery acknowledgement of one activation. It is
// informational: a removal never waits for it, and no GC decision of the
// registry reads it.
func (r *Registry) MarkAcked(key ActivationKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok, err := r.loadActivation(key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownActivation, activationName(key))
	}
	if rec.Acked {
		return nil
	}
	rec.Acked = true
	return putRecord(&rec, activationKeyOf(key), r.db)
}

// Events returns every event of the applied decisions, in a deterministic
// order: only decisions past the applied-decision frontier are visible, so a
// partially applied decision publishes nothing (AC-CAP-07-04). A nil network
// returns the events of every network.
func (r *Registry) Events(net *semantic.Network) ([]PublishedEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []PublishedEvent{}
	err := r.db.Range([]byte(keyPrefix+"decision/"), func(key, value []byte) error {
		var d DecisionRecord
		if err := strictDecode(value, &d); err != nil {
			return err
		}
		if d.State != StateApplied || d.Application == nil {
			return nil
		}
		if net != nil && d.Network != *net {
			return nil
		}
		for _, ev := range d.Application.Events {
			out = append(out, PublishedEvent{Descriptor: ev.Descriptor, Content: ev.Content})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].Descriptor, out[j].Descriptor
		if left.Source.SourceSequence != right.Source.SourceSequence {
			return left.Source.SourceSequence < right.Source.SourceSequence
		}
		if left.Activation.ProducerID != right.Activation.ProducerID {
			return left.Activation.ProducerID.String() < right.Activation.ProducerID.String()
		}
		if left.Activation.RunID != right.Activation.RunID {
			return left.Activation.RunID.String() < right.Activation.RunID.String()
		}
		if left.Activation.ActivationID.DecisionSequence != right.Activation.ActivationID.DecisionSequence {
			return left.Activation.ActivationID.DecisionSequence < right.Activation.ActivationID.DecisionSequence
		}
		if left.Activation.ActivationID.EntityOrdinal != right.Activation.ActivationID.EntityOrdinal {
			return left.Activation.ActivationID.EntityOrdinal < right.Activation.ActivationID.EntityOrdinal
		}
		return left.Revision < right.Revision
	})
	return out, nil
}
