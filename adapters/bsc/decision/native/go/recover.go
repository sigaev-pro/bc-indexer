package bscdecision

// The recovery driver of a node with the decision bridge (NATIVE_CAPTURE_PROTOCOL
// startup). The native DB recovery runs when the node opens the chain; then:
//
//  1. Admission: abandon INPUT_PREPARING observations and re-execute each
//     ADMITTED obligation to EXEC_READY or NATIVE_REJECTED. The bridge is
//     not open, so the decision step of a re-executed block refuses
//     (ErrDeferred) before any write, and the block stays EXEC_READY.
//  2. decision.Open.
//  3. The unresolved intent, if any, is settled from native evidence: a
//     marker resolves it; a member block that the native store recorded as
//     a bad block and did not store is a native rejection (RejectIntent);
//     a bad block that is also stored cannot be settled (ErrInvariant);
//     otherwise the import completes exactly the saved transition.
//  4. Each EXEC_READY observation without a decision is replayed natively
//     (Seam.Replay) and decided; the replay checks the EXEC_READY record.
//  5. decision.Bridge.GoLive.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	"github.com/ethereum/go-ethereum/extractor/decision"
	"github.com/ethereum/go-ethereum/rlp"
)

// BadBlockTag is the domain tag of the evidence digest of a native
// rejection that the native bad-block record proves.
const BadBlockTag = "extractor-bsc-bad-block-v1"

// BadBlockEvidence is SHA-256 of the tag and a zero byte, then the block
// hash and the SHA-256 of the block's RLP encoding.
func BadBlockEvidence(block *types.Block) (string, error) {
	encoded, err := rlp.EncodeToBytes(block)
	if err != nil {
		return "", err
	}
	body := sha256.Sum256(encoded)
	data := append([]byte(BadBlockTag), 0)
	data = append(data, block.Hash().Bytes()...)
	data = append(data, body[:]...)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// Recover runs the recovery driver and switches the admission to LIVE
// through decision.Bridge.GoLive. The hook must be installed on the chain.
// An error keeps the node in RECOVERY_ONLY.
func (h *Hook) Recover(chain *core.BlockChain) error {
	// The re-executions end with an import error (ErrDeferred, or a native
	// rejection); they only explain a later failure.
	kept, err := h.seam.RecoverObligations(chain)
	if err != nil {
		return err
	}
	if err := h.withGuard(func(token *admission.Token) error {
		adm, _ := h.seam.Admission()
		bridge, err := decision.Open(token, h.store, adm, h.storeID, h.roots, h.mirror)
		if err != nil {
			return err
		}
		h.bridge = bridge
		return nil
	}); err != nil {
		return errors.Join(append(kept, err)...)
	}
	h.emit(bsc.Event{"event": "bridge-open"})
	if err := h.settle(chain); err != nil {
		return errors.Join(append(kept, err)...)
	}
	var undecided []string
	if err := h.withGuard(func(token *admission.Token) error {
		observations, err := h.bridge.Undecided(token)
		for _, obs := range observations {
			undecided = append(undecided, obs.Key)
		}
		return err
	}); err != nil {
		return errors.Join(append(kept, err)...)
	}
	for _, key := range undecided {
		if err := h.reimport(chain, key, "replay-undecided"); err != nil {
			kept = append(kept, err)
		}
	}
	if err := h.withGuard(func(token *admission.Token) error { return h.bridge.GoLive(token) }); err != nil {
		return errors.Join(append(kept, err)...)
	}
	h.emit(bsc.Event{"event": "live"})
	return nil
}

// settle resolves the unresolved intent from native evidence.
func (h *Hook) settle(chain *core.BlockChain) error {
	var pending *decision.Decision
	if err := h.withGuard(func(token *admission.Token) error {
		var err error
		pending, err = h.unresolved(token)
		return err
	}); err != nil || pending == nil {
		return err
	}
	seq := pending.Sequence
	if pending.Marker {
		h.emit(bsc.Event{"event": "settle", "sequence": seq, "outcome": "marker"})
		return h.withGuard(func(token *admission.Token) error {
			decided, err := h.bridge.Resolve(token, seq)
			if err != nil {
				return err
			}
			h.emit(bsc.Event{"event": "decided", "sequence": seq, "delivery": string(decided.Delivery),
				"digest": decided.Digest})
			return h.bridge.Cleanup(token)
		})
	}
	if pending.Intent.Kind != decision.Import && pending.Intent.Kind != decision.Side ||
		len(pending.Intent.Members) != 1 {
		return fmt.Errorf("%w: intent %d is %s with %d members", decision.ErrInvariant, seq, pending.Intent.Kind,
			len(pending.Intent.Members))
	}
	member := pending.Intent.Members[0]
	raw, err := hex.DecodeString(member.Block)
	if err != nil {
		return err
	}
	hash := common.BytesToHash(raw)
	bad := rawdb.ReadBadBlock(h.db, hash)
	stored := chain.GetBlockByHash(hash) != nil
	switch {
	case bad != nil && stored:
		return fmt.Errorf("%w: intent %d: block %x is both a native bad block and stored", decision.ErrInvariant,
			seq, hash)
	case bad != nil:
		evidence, err := BadBlockEvidence(bad)
		if err != nil {
			return err
		}
		h.emit(bsc.Event{"event": "settle", "sequence": seq, "outcome": "native-rejection", "evidence": evidence})
		return h.withGuard(func(token *admission.Token) error {
			rejected, err := h.bridge.RejectIntent(token, seq, evidence)
			if err != nil {
				return err
			}
			h.emit(bsc.Event{"event": "intent-rejected", "sequence": seq, "observation": member.Observation,
				"digest": rejected.Digest})
			return nil
		})
	}
	h.emit(bsc.Event{"event": "settle", "sequence": seq, "outcome": "complete", "stored": stored})
	return h.reimport(chain, member.Observation, "complete-intent")
}

// reimport imports the capsule block of an observation again; the hook
// replays it and its decision step decides it.
func (h *Hook) reimport(chain *core.BlockChain, key, reason string) error {
	static, err := h.seam.Static(key)
	if err != nil {
		return err
	}
	h.emit(bsc.Event{"event": reason, "observation": key, "number": static.Block.NumberU64()})
	if _, err := chain.InsertChain(types.Blocks{static.Block}); err != nil {
		return fmt.Errorf("%s of %s: %w", reason, key, err)
	}
	return nil
}
