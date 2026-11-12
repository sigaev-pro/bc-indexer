// This file is Geth-only. The E01-02.decision_bridge Geth preparation copies
// it from adapters/geth/decision/native into the Geth tree as
// extractor/admission/native/decision.go, next to geth.go, so it shares the
// package of the Geth admission wiring.
//
// It joins the shared decision core (extractor/decision) to the Geth write
// path: the child-root barrier over the hash trie database (Roots), the
// transition intent before the native mutation, the native decision marker in
// the final authoritative batch, Resolve, RejectIntent and Cleanup on the
// import path (core.ExtractorDecision), and the recovery driver that settles
// every unresolved intent and every undecided EXEC_READY observation from
// native evidence before decision.Bridge.GoLive. The design is in
// adapters/geth/decision/native/README.md.

package native

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/decision"
)

// Domain tags of the Geth decision evidence digests.
const (
	tagIntentEvidence = "extractor-geth-intent-evidence-v1"
	tagRefusal        = "extractor-geth-native-refusal-v1"
)

// ErrDecisionRecovery stops startup when native data cannot settle a saved
// transition intent or an EXEC_READY observation without a decision.
var ErrDecisionRecovery = errors.New("source invariant error: native data does not settle the decision")

// DecisionOptions are optional parts of the decision bridge. The shipped
// binary passes the zero value.
type DecisionOptions struct {
	// Mirror is the decision mirror of E05-01.registry; nil means that no
	// mirror has applied any decision, so every NATIVE_DECIDED decision with
	// public events keeps its delivery obligation and its marker.
	Mirror decision.Mirror
	// Outcome is a native outcome fixture for probes: an error refuses the
	// transition natively after its intent. Nil in the shipped binary.
	Outcome func(block *types.Block) error
	// Point is called at the named points of a decided write ("committed"
	// before the child-root barrier, "mutated" after the block batch,
	// "intermediate" after a head batch that reorg writes for a lower block
	// of the new branch, "refused" after the bad-block record of a native
	// refusal), for probes that crash there. Nil in the shipped binary.
	Point func(name string)
}

// Decisions is the decision bridge of one Geth node. It implements
// core.ExtractorDecision.
type Decisions struct {
	a      *Admission
	opts   DecisionOptions
	bridge *decision.Bridge
	// completing is the saved unresolved intent that the recovery driver
	// completes through the native import path.
	completing *decision.Decision
}

// Roots checks the child-root barrier in the native key-value store: a root
// is durable when its trie node is in the store (the hash trie database
// flushed it and the store was synced), or when it is the empty root.
type Roots struct {
	DB ethdb.KeyValueReader
}

// Durable reports whether the state root is in the native key-value store.
func (r Roots) Durable(root string) (bool, error) {
	data, err := hex.DecodeString(root)
	if err != nil || len(data) != common.HashLength {
		return false, fmt.Errorf("state root %q", root)
	}
	hash := common.BytesToHash(data)
	return hash == types.EmptyRootHash || rawdb.HasLegacyTrieNode(r.DB, hash), nil
}

// noMirror is the mirror before E05-01.registry: it has applied nothing.
type noMirror struct{}

func (noMirror) Applied(uint64, string) (bool, error) { return false, nil }

// AttachDecisions is Attach with the decision bridge: it binds the store,
// opens the admission with OpenWithCleanup, installs the admission and the
// decision seam into the chain and runs the decision recovery driver, which
// ends with decision.Bridge.GoLive. With opts.SkipRecovery it stays in
// RECOVERY_ONLY without opening the bridge, for probes.
func AttachDecisions(chain *core.BlockChain, db ethdb.Database, cfg Config, opts Options,
	dopts DecisionOptions) (*Decisions, error) {
	a, err := attach(chain, db, cfg, opts, true)
	if err != nil {
		return nil, err
	}
	if dopts.Mirror == nil {
		dopts.Mirror = noMirror{}
	}
	d := &Decisions{a: a, opts: dopts}
	chain.SetExtractorDecision(d)
	if opts.SkipRecovery {
		return d, nil
	}
	if err := d.Recover(); err != nil {
		return nil, err
	}
	return d, nil
}

// Admission returns the Geth admission of the bridge.
func (d *Decisions) Admission() *Admission { return d.a }

// Bridge returns the decision bridge and a held token for inspection; the
// caller must release the token. The bridge is nil before recovery opens it.
func (d *Decisions) Bridge() (*decision.Bridge, *admission.Token) { return d.bridge, d.a.guard.Acquire() }

// IntentEvidence is the evidence digest of the native transition of a block
// write: H(tag || u64(len(kind)) || kind || block hash || u64(number) ||
// child root).
func IntentEvidence(kind decision.Kind, block *types.Block, root common.Hash) string {
	data := append([]byte(tagIntentEvidence), 0)
	data = binary.BigEndian.AppendUint64(data, uint64(len(kind)))
	data = append(data, kind...)
	data = append(data, block.Hash().Bytes()...)
	data = binary.BigEndian.AppendUint64(data, block.NumberU64())
	sum := sha256.Sum256(append(data, root.Bytes()...))
	return hex.EncodeToString(sum[:])
}

// RefusalEvidence is the evidence digest of a native refusal of a transition:
// H(tag || hash of the refused block), which the native store records as a
// bad block and does not store.
func RefusalEvidence(block common.Hash) string {
	sum := sha256.Sum256(append(append([]byte(tagRefusal), 0), block.Bytes()...))
	return hex.EncodeToString(sum[:])
}

// Write starts the decision of an observed block write. Before the bridge is
// open (the admission part of recovery) the block is not written.
func (d *Decisions) Write(obs core.ExtractorObservation, block *types.Block, setHead bool) (core.ExtractorWrite, error) {
	if d.bridge == nil {
		d.a.trace("deferred", map[string]any{"number": block.NumberU64()})
		return nil, nil
	}
	keyed, ok := obs.(interface{ Key() string })
	if !ok {
		return nil, fmt.Errorf("%w: observation without a key", ErrDecisionRecovery)
	}
	token := d.a.token
	if token == nil {
		return nil, ErrGuardMissing
	}
	return &write{d: d, token: token, key: keyed.Key(), block: block, setHead: setHead}, nil
}

// write is the decision of one block write.
type write struct {
	d       *Decisions
	token   *admission.Token
	key     string
	block   *types.Block
	setHead bool
	intent  decision.IntentRecord
}

// execReady returns the EXEC_READY digest of an open observation.
func (d *Decisions) execReady(token *admission.Token, key string) (string, error) {
	observations, err := d.a.core.Observations(token)
	if err != nil {
		return "", err
	}
	for _, obs := range observations {
		if obs.Key == key && obs.State == admission.ExecReady {
			return obs.ExecReady.Digest, nil
		}
	}
	return "", fmt.Errorf("%w: %s is not EXEC_READY", decision.ErrBinding, key)
}

// build returns the transition intent of the write: SIDE without a head
// change, IMPORT when the block extends the head, CANONICAL when the head
// moves to another branch. A CANONICAL intent also names every block of the
// new branch that a SIDE decision retains.
func (w *write) build(root common.Hash, head *types.Header) (decision.Intent, error) {
	d, block := w.d, w.block
	ready, err := d.execReady(w.token, w.key)
	if err != nil {
		return decision.Intent{}, err
	}
	old := decision.Head{Number: head.Number.Uint64(), Hash: hexHash(head.Hash())}
	in := decision.Intent{OldHead: old, NewHead: decision.Head{Number: block.NumberU64(), Hash: hexHash(block.Hash())},
		Members: []decision.Member{{Observation: w.key, ExecReadyDigest: ready, ChildRoot: hexHash(root)}},
		Events:  true}
	switch {
	case !w.setHead:
		in.Kind, in.NewHead, in.Events = decision.Side, old, false
	case block.ParentHash() == head.Hash():
		in.Kind = decision.Import
	default:
		in.Kind = decision.Canonical
		retained, err := d.retainedOnBranch(w.token, block)
		if err != nil {
			return decision.Intent{}, err
		}
		in.Members = append(in.Members, retained...)
	}
	in.EvidenceDigest = IntentEvidence(in.Kind, block, root)
	return in, nil
}

// retainedOnBranch lists the members of SIDE decisions whose blocks are on
// the branch that block extends, below block and above the canonical chain,
// in block order.
func (d *Decisions) retainedOnBranch(token *admission.Token, block *types.Block) ([]decision.Member, error) {
	branch := map[string]bool{}
	chain := d.a.chain
	for header := chain.GetHeader(block.ParentHash(), block.NumberU64()-1); header != nil &&
		chain.GetCanonicalHash(header.Number.Uint64()) != header.Hash(); header = chain.GetHeader(header.ParentHash,
		header.Number.Uint64()-1) {
		branch[hexHash(header.Hash())] = true
	}
	decisions, err := d.bridge.Decisions(token)
	if err != nil {
		return nil, err
	}
	observations, err := d.a.core.Observations(token)
	if err != nil {
		return nil, err
	}
	open := map[string]admission.Observation{}
	for _, obs := range observations {
		open[obs.Key] = obs
	}
	members := []decision.Member{}
	for _, dec := range decisions {
		if dec.State != decision.Decided || dec.Intent.Kind != decision.Side {
			continue
		}
		for _, m := range dec.Intent.Members {
			if obs, ok := open[m.Observation]; ok && obs.State == admission.ExecReady && branch[m.Block] {
				members = append(members, decision.Member{Observation: m.Observation,
					ExecReadyDigest: m.ExecReadyDigest, ChildRoot: m.ChildRoot})
			}
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Observation < members[j].Observation })
	return members, nil
}

// sameIntent reports whether a saved intent records exactly the transition
// that the native write would intend now.
func sameIntent(saved decision.IntentRecord, in decision.Intent) bool {
	if saved.Kind != in.Kind || saved.OldHead != in.OldHead || saved.NewHead != in.NewHead ||
		saved.EvidenceDigest != in.EvidenceDigest || saved.Events != in.Events || len(saved.Members) != len(in.Members) {
		return false
	}
	for i, m := range in.Members {
		s := saved.Members[i]
		if s.Observation != m.Observation || s.ExecReadyDigest != m.ExecReadyDigest || s.ChildRoot != m.ChildRoot {
			return false
		}
	}
	return true
}

// Intend records the transition intent after the child-root barrier. While
// the recovery driver completes a saved intent, the write must intend
// exactly that transition and reuses its record.
func (w *write) Intend(root common.Hash, head *types.Header) ([]byte, []byte, error) {
	d := w.d
	in, err := w.build(root, head)
	if err != nil {
		return nil, nil, err
	}
	if saved := d.completing; saved != nil {
		if !sameIntent(saved.Intent, in) {
			return nil, nil, fmt.Errorf("%w: the native write of block %d differs from saved intent %d",
				ErrDecisionRecovery, w.block.NumberU64(), saved.Sequence)
		}
		w.intent = saved.Intent
	} else if w.intent, err = d.bridge.Intend(w.token, in); err != nil {
		return nil, nil, err
	}
	value, err := decision.MarkerValue(w.intent)
	if err != nil {
		return nil, nil, err
	}
	d.a.trace("intent", map[string]any{"sequence": w.intent.Sequence, "kind": string(w.intent.Kind),
		"digest": w.intent.Digest, "marker": string(value)})
	return decision.MarkerKey(d.a.producer, w.intent.Sequence), value, nil
}

// Outcome is the native outcome fixture of the probes.
func (w *write) Outcome() error {
	if w.d.opts.Outcome == nil {
		return nil
	}
	return w.d.opts.Outcome(w.block)
}

// Reject records the native refusal of the intent.
func (w *write) Reject(cause error) error {
	return w.d.reject(w.token, w.intent, cause.Error())
}

func (d *Decisions) reject(token *admission.Token, intent decision.IntentRecord, cause string) error {
	record, err := d.bridge.RejectIntent(token, intent.Sequence,
		RefusalEvidence(common.HexToHash(intent.Members[0].Block)))
	if err != nil {
		return err
	}
	d.a.trace("intent_rejected", map[string]any{"sequence": record.Sequence, "digest": record.Digest, "cause": cause})
	return nil
}

// Point reports a named point of the native mutation.
func (w *write) Point(name string) {
	w.d.a.trace(name, map[string]any{"number": w.block.NumberU64()})
	if w.d.opts.Point != nil {
		w.d.opts.Point(name)
	}
}

// Resolve records the decision after the batch with the marker is durable,
// then cleans up the decisions that no longer need their markers.
func (w *write) Resolve() error {
	return w.d.resolve(w.token, w.intent.Sequence)
}

func (d *Decisions) resolve(token *admission.Token, sequence uint64) error {
	record, err := d.bridge.Resolve(token, sequence)
	if err != nil {
		return err
	}
	d.a.trace("decided", map[string]any{"sequence": record.Sequence, "delivery": string(record.Delivery),
		"digest": record.Digest})
	if err := d.bridge.Cleanup(token); err != nil {
		return err
	}
	return nil
}

// Recover is the recovery driver of AC-CAP-07-01, 07-02, 07-03 and 07-06,
// after the native DB recovery of NewBlockChain:
//
//  1. the admission part: abandon INPUT_PREPARING observations and re-execute
//     every ADMITTED obligation to EXEC_READY or NATIVE_REJECTED, without
//     writing the block (the bridge is not open yet);
//  2. open the bridge (decision.Open), which checks every decision against
//     its records and its native marker;
//  3. settle the unresolved intent from native evidence: a marker resolves
//     it; member blocks that the native store recorded as bad and does not
//     hold reject it; a block stored with its state completes the head (or
//     the SIDE marker); otherwise the saved block is re-imported natively and
//     must intend exactly the saved transition;
//  4. replay natively every EXEC_READY observation without a decision and
//     decide it as a SIDE write;
//  5. Cleanup, then decision.Bridge.GoLive.
func (d *Decisions) Recover() error {
	if err := d.a.RecoverObligations(); err != nil {
		return err
	}
	store, ok := d.a.store.(admission.DeleteStore)
	if !ok {
		return fmt.Errorf("%w: the decision store cannot delete", ErrConfig)
	}
	token := d.a.guard.Acquire()
	bridge, err := decision.Open(token, store, d.a.core, d.a.cfg.StoreID, Roots{DB: d.a.db}, d.opts.Mirror)
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return err
	}
	d.bridge = bridge
	d.a.trace("bridge_opened", nil)
	if err := d.settleIntent(); err != nil {
		return err
	}
	if err := d.replayUndecided(); err != nil {
		return err
	}
	token = d.a.guard.Acquire()
	err = d.bridge.Cleanup(token)
	if err == nil {
		err = d.bridge.GoLive(token)
	}
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err == nil {
		d.a.trace("live", nil)
	}
	return err
}

// unresolved returns the unresolved intent, if any.
func (d *Decisions) unresolved(token *admission.Token) (*decision.Decision, error) {
	decisions, err := d.bridge.Decisions(token)
	if err != nil {
		return nil, err
	}
	for i := range decisions {
		if decisions[i].State == decision.Intended {
			return &decisions[i], nil
		}
	}
	return nil, nil
}

// settleIntent completes or rejects the unresolved intent from native
// evidence.
func (d *Decisions) settleIntent() error {
	token := d.a.guard.Acquire()
	saved, err := d.unresolved(token)
	if err != nil || saved == nil {
		return errors.Join(err, token.Release())
	}
	if saved.Marker {
		d.a.trace("settling", map[string]any{"sequence": saved.Sequence, "evidence": "marker"})
		return errors.Join(d.resolve(token, saved.Sequence), token.Release())
	}
	// The first member is the block of the write that intended; the others
	// are side blocks that a SIDE decision retains, which a refusal keeps.
	block, err := d.a.savedBlock(saved.Intent.Members[0].Observation)
	if err != nil {
		return errors.Join(err, token.Release())
	}
	bad := rawdb.ReadBadBlock(d.a.db, block.Hash()) != nil
	stored := d.a.chain.HasBlock(block.Hash(), block.NumberU64())
	if bad && !stored {
		// The native store refused the block and did not keep it: the
		// recorded native outcome is a rejection.
		d.a.trace("settling", map[string]any{"sequence": saved.Sequence, "evidence": "bad_block"})
		return errors.Join(d.reject(token, saved.Intent, "native bad-block record"), token.Release())
	}
	if bad {
		return errors.Join(fmt.Errorf("%w: block %d of intent %d is both bad and stored", ErrDecisionRecovery,
			block.NumberU64(), saved.Sequence), token.Release())
	}
	key := decision.MarkerKey(d.a.producer, saved.Sequence)
	value, err := decision.MarkerValue(saved.Intent)
	if err != nil {
		return errors.Join(err, token.Release())
	}
	if err := token.Release(); err != nil {
		return err
	}
	if d.a.chain.HasBlockAndState(block.Hash(), block.NumberU64()) {
		// The block batch is durable and the final batch is not: complete the
		// saved transition natively, with its marker.
		d.a.trace("settling", map[string]any{"sequence": saved.Sequence, "evidence": "stored_block"})
		if saved.Intent.Kind == decision.Side {
			err = d.a.chain.ExtractorWriteMarker(block, key, value)
		} else {
			if !d.completable(saved.Intent, block) {
				return fmt.Errorf("%w: head %s is neither the saved old head of intent %d nor on its new branch",
					ErrDecisionRecovery, d.a.chain.CurrentBlock().Hash(), saved.Sequence)
			}
			err = d.a.chain.ExtractorCompleteHead(block, key, value)
		}
		if err != nil {
			return err
		}
		token = d.a.guard.Acquire()
		return errors.Join(d.resolve(token, saved.Sequence), token.Release())
	}
	// Nothing of the block is stored: re-execute it natively and write it with
	// exactly the saved intent.
	d.a.trace("settling", map[string]any{"sequence": saved.Sequence, "evidence": "reimport"})
	d.completing = saved
	d.a.replay = map[string]bool{saved.Intent.Members[0].Observation: true}
	if saved.Intent.Kind == decision.Side {
		_, err = d.a.chain.InsertBlockWithoutSetHead(context.Background(), block, false)
	} else {
		_, err = d.a.chain.InsertChain(types.Blocks{block})
	}
	d.completing, d.a.replay = nil, nil
	importErr := err
	// The re-import settles the intent: Resolve after the native write, or
	// RejectIntent when the native side refuses the transition again.
	token = d.a.guard.Acquire()
	left, err := d.unresolved(token)
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err == nil && left != nil {
		err = errors.Join(fmt.Errorf("%w: intent %d stays unresolved after its re-import", ErrDecisionRecovery,
			left.Sequence), importErr)
	}
	return err
}

// completable reports whether the native head allows completing the saved
// head change to block: the head is the saved old head, or a block of the
// new branch below block that an interrupted reorg already made the head.
func (d *Decisions) completable(intent decision.IntentRecord, block *types.Block) bool {
	chain := d.a.chain
	head := chain.CurrentBlock()
	if hexHash(head.Hash()) == intent.OldHead.Hash {
		return true
	}
	if head.Number.Uint64() >= block.NumberU64() {
		return false
	}
	ancestor := chain.GetHeader(block.ParentHash(), block.NumberU64()-1)
	for ancestor != nil && ancestor.Number.Uint64() > head.Number.Uint64() {
		ancestor = chain.GetHeader(ancestor.ParentHash, ancestor.Number.Uint64()-1)
	}
	return ancestor != nil && ancestor.Hash() == head.Hash() && intent.Kind == decision.Canonical
}

// replayUndecided re-executes every EXEC_READY observation without a
// decision natively from its parent state and decides it as a SIDE write:
// the native-only replay of AC-CAP-07-02 makes the same child root durable,
// and the EXEC_READY record stays. A block that the store already holds is
// not something this driver wrote without a decision: it stops recovery.
func (d *Decisions) replayUndecided() error {
	token := d.a.guard.Acquire()
	undecided, err := d.bridge.Undecided(token)
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return err
	}
	for _, obs := range undecided {
		block, err := d.a.savedBlock(obs.Key)
		if err != nil {
			return err
		}
		if d.a.chain.HasBlock(block.Hash(), block.NumberU64()) {
			return fmt.Errorf("%w: %s is EXEC_READY without a decision, but block %d is stored", ErrDecisionRecovery,
				obs.Key, block.NumberU64())
		}
		d.a.replay = map[string]bool{obs.Key: true}
		_, err = d.a.chain.InsertBlockWithoutSetHead(context.Background(), block, false)
		d.a.replay = nil
		if err != nil {
			return fmt.Errorf("%w: replay of %s: %w", ErrDecisionRecovery, obs.Key, err)
		}
	}
	return nil
}
