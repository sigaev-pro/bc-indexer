// The restartable release protocol of E05-01.activation_gc: a release is a
// separate sync decision from the anchor acceptance, its intent record is
// durable before any state changes, and a restart completes an interrupted
// release without applying anything twice while the shared pins and the
// compact ancestry records survive.
package recoverygc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// compactRecord is the compact anchor/ancestry/decision-frontier record a
// release leaves behind: enough identity to audit what was released, none
// of the retained state.
type compactRecord struct {
	Entity   EntityID          `json:"entity"`
	Block    AnchorRef         `json:"block"`
	Terminal *TerminalEvidence `json:"terminal,omitempty"`
	// Anchor is the accepted anchor that authorized the release.
	Anchor       AnchorRef `json:"anchor"`
	RecordDigest string    `json:"record_digest"`
}

func (c *compactRecord) seal() error {
	c.RecordDigest = ""
	digest, err := digestRecord("extractor-gc-tombstone-v1", c)
	c.RecordDigest = digest
	return err
}

func (c *compactRecord) verify() error {
	stored := c.RecordDigest
	if err := c.seal(); err != nil {
		return err
	}
	if stored != c.RecordDigest {
		return fmt.Errorf("%w: tombstone of %s", ErrCorrupt, c.Entity)
	}
	return nil
}

// releaseRecord is the durable two-phase release marker: the plan with its
// entities and pins, and whether the release completed.
type releaseRecord struct {
	PlanID   string   `json:"plan_id"`
	Entities []Entity `json:"entities"`
	// RetainedPins are the pins the caller holds for retained entities;
	// the release never touches them.
	RetainedPins []string `json:"retained_pins"`
	Done         bool     `json:"done"`
	RecordDigest string   `json:"record_digest"`
}

func (r *releaseRecord) seal() error {
	r.RecordDigest = ""
	digest, err := digestRecord("extractor-gc-release-v1", r)
	r.RecordDigest = digest
	return err
}

func (r *releaseRecord) verify() error {
	stored := r.RecordDigest
	if err := r.seal(); err != nil {
		return err
	}
	if stored != r.RecordDigest {
		return fmt.Errorf("%w: release %s", ErrCorrupt, r.PlanID)
	}
	return nil
}

// ReleaseResult reports what one release did.
type ReleaseResult struct {
	PlanID       string
	Released     []EntityID
	ReleasedPins []string
	// RetainedPins are the shared pins the release kept.
	RetainedPins []string
}

// planID derives the stable identity of a release plan from its full
// content, so an interrupted release resumes under the same identity and a
// repeated plan is the same release.
func planID(entities []Entity, retainedPins []string) string {
	h := sha256.New()
	h.Write([]byte("extractor-gc-plan-v1"))
	h.Write([]byte{0})
	ids := make([]string, len(entities))
	for i, entity := range entities {
		ids[i] = entity.ID.key()
	}
	sort.Strings(ids)
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	pins := make([]string, len(retainedPins))
	copy(pins, retainedPins)
	sort.Strings(pins)
	for _, pin := range pins {
		h.Write([]byte(pin))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Release releases the eligible entities of one plan. Every entity is
// re-evaluated under the current anchor and pending state first: a stale
// plan authorizes nothing. The release intent is durable before any state
// changes; then each entity's retained state is dropped and a compact
// record is left; then the pins the plan owns and no retained entity shares
// are released; then the completion marker is synced. A power loss at any
// step is completed by the next Open or Reconcile, idempotently
// (AC-LIF-08-02). A payload acknowledgement is not a release input: it
// authorizes nothing (AC-LIF-08-01).
func (e *Engine) Release(entities []Entity, retainedPins []string) (ReleaseResult, error) {
	for _, entity := range entities {
		if err := entity.Validate(); err != nil {
			return ReleaseResult{}, err
		}
	}
	seen := map[EntityID]bool{}
	for _, entity := range entities {
		if seen[entity.ID] {
			return ReleaseResult{}, fmt.Errorf("%w: %s listed twice", ErrInvalid, entity.ID)
		}
		seen[entity.ID] = true
	}
	decisions, err := e.Evaluate(entities)
	if err != nil {
		return ReleaseResult{}, err
	}
	retainedLines := []string{}
	for _, decision := range decisions {
		if !decision.Release {
			retainedLines = append(retainedLines, decision.String())
		}
	}
	if len(retainedLines) > 0 {
		return ReleaseResult{}, fmt.Errorf("%w: %v", ErrNotEligible, retainedLines)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.live(); err != nil {
		return ReleaseResult{}, err
	}
	record := releaseRecord{PlanID: planID(entities, retainedPins), Entities: entities,
		RetainedPins: retainedPins}
	return e.applyRelease(record)
}

// applyRelease writes the intent, applies the release steps and writes the
// completion marker. It is idempotent: every step is either a no-op or the
// same durable write. An error in a step stops before the completion
// marker, so the next reconcile retries the whole release safely.
func (e *Engine) applyRelease(record releaseRecord) (ReleaseResult, error) {
	key := []byte(keyPrefix + "release/" + record.PlanID)
	if existing, ok, err := e.loadRelease(key); err != nil {
		return ReleaseResult{}, err
	} else if ok {
		if existing.Done {
			// The same release already completed; report it without
			// applying anything twice.
			return reportDone(existing), nil
		}
		// An interrupted release of the same plan resumes with its durable
		// record, never with a different in-memory copy.
		record = existing
	}
	if err := e.writeRelease(key, record); err != nil {
		return ReleaseResult{}, err
	}
	result, err := e.runRelease(&record)
	if err != nil {
		return ReleaseResult{}, err
	}
	record.Done = true
	if err := e.writeRelease(key, record); err != nil {
		return ReleaseResult{}, err
	}
	return result, nil
}

// writeRelease seals, writes and syncs one release record.
func (e *Engine) writeRelease(key []byte, record releaseRecord) error {
	if err := record.seal(); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := e.db.Put(key, data); err != nil {
		return err
	}
	return e.db.SyncKeyValue()
}

// runRelease applies the release steps of a durable record: drop each
// entity's retained state, leave the compact record, then release the pins
// no retained entity shares. Every seam is idempotent by contract, so a
// step that already ran applies nothing.
func (e *Engine) runRelease(record *releaseRecord) (ReleaseResult, error) {
	result := ReleaseResult{PlanID: record.PlanID, RetainedPins: record.RetainedPins}
	retained := map[string]bool{}
	for _, pin := range record.RetainedPins {
		retained[pin] = true
	}
	released := map[string]bool{}
	for _, entity := range record.Entities {
		if err := e.releaser.ReleaseEntity(entity.ID); err != nil {
			return ReleaseResult{}, fmt.Errorf("release of %s: %w", entity.ID, err)
		}
		tombstone := compactRecord{Entity: entity.ID, Block: entity.Block, Terminal: entity.Terminal,
			Anchor: e.anchors[entity.ID.Network].Anchor}
		if err := tombstone.seal(); err != nil {
			return ReleaseResult{}, err
		}
		data, err := json.Marshal(tombstone)
		if err != nil {
			return ReleaseResult{}, err
		}
		if err := e.db.Put([]byte(keyPrefix+"tombstone/"+entity.ID.key()), data); err != nil {
			return ReleaseResult{}, err
		}
		result.Released = append(result.Released, entity.ID)
	}
	for _, entity := range record.Entities {
		for _, pin := range entity.Pins {
			if retained[pin] || released[pin] {
				continue
			}
			if err := e.pins.Release(pin); err != nil {
				return ReleaseResult{}, fmt.Errorf("pin %s: %w", pin, err)
			}
			released[pin] = true
			result.ReleasedPins = append(result.ReleasedPins, pin)
		}
	}
	return result, nil
}

// reportDone builds the result of a release that already completed: the
// entities of the record and the pins no retained entity shared.
func reportDone(record releaseRecord) ReleaseResult {
	result := ReleaseResult{PlanID: record.PlanID, RetainedPins: record.RetainedPins}
	retained := map[string]bool{}
	for _, pin := range record.RetainedPins {
		retained[pin] = true
	}
	released := map[string]bool{}
	for _, entity := range record.Entities {
		result.Released = append(result.Released, entity.ID)
		for _, pin := range entity.Pins {
			if !retained[pin] && !released[pin] {
				released[pin] = true
				result.ReleasedPins = append(result.ReleasedPins, pin)
			}
		}
	}
	sort.Strings(result.ReleasedPins)
	return result
}

// loadRelease reads and verifies one release record.
func (e *Engine) loadRelease(key []byte) (releaseRecord, bool, error) {
	ok, err := e.db.Has(key)
	if err != nil {
		return releaseRecord{}, false, err
	}
	if !ok {
		return releaseRecord{}, false, nil
	}
	data, err := e.db.Get(key)
	if err != nil {
		return releaseRecord{}, false, err
	}
	var record releaseRecord
	if err := strictDecode(data, &record); err != nil {
		return releaseRecord{}, false, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	if err := record.verify(); err != nil {
		return releaseRecord{}, false, err
	}
	return record, true, nil
}

// Reconcile completes the releases a power loss interrupted. Every step is
// idempotent, so the restart applies nothing twice; the shared pins named
// by the record's retained list survive (AC-LIF-08-02). It returns the
// number of releases completed.
func (e *Engine) Reconcile() (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reconcile()
}

func (e *Engine) reconcile() (int, error) {
	if err := e.live(); err != nil {
		return 0, err
	}
	keys := [][]byte{}
	err := e.db.Range([]byte(keyPrefix+"release/"), func(key, value []byte) error {
		var record releaseRecord
		if err := strictDecode(value, &record); err != nil {
			return err
		}
		if err := record.verify(); err != nil {
			return err
		}
		if !record.Done {
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	completed := 0
	for _, key := range keys {
		record, ok, err := e.loadRelease(key)
		if err != nil || !ok {
			return completed, err
		}
		if _, err := e.runRelease(&record); err != nil {
			return completed, err
		}
		record.Done = true
		if err := e.writeRelease(key, record); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}
