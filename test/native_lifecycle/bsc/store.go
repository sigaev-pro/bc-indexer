// Package bsc is the BSC slot of E05-02.reorg (test/native_lifecycle):
// the memory store, the canonical chain oracle and the inert GC seams of
// the scenario model. The model itself is in lifecycle_bsc.go; the tests
// are in reorg_test.go.
package bsc

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	recoverygc "github.com/sigaev-pro/bc-indexer/adapters/recovery_gc"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"

	"github.com/ethereum/go-ethereum/extractor/admission"
	bscfinality "github.com/ethereum/go-ethereum/extractor/bsc/finality"
	"github.com/ethereum/go-ethereum/extractor/decision"
)

// errCrash is the injected power loss of the model store.
var errCrash = errors.New("injected power loss")

type write struct {
	key     string
	value   []byte
	deleted bool
}

// memStore models the native key-value store. Put and Delete stay pending
// until SyncKeyValue makes them durable; powerLoss drops every pending
// write.
type memStore struct {
	durable map[string][]byte
	pending []write
	ops     int
	crashAt int
}

func newStore() *memStore { return &memStore{durable: map[string][]byte{}} }

func (s *memStore) view(key string) ([]byte, bool) {
	for i := len(s.pending) - 1; i >= 0; i-- {
		if s.pending[i].key == key {
			return s.pending[i].value, !s.pending[i].deleted
		}
	}
	value, ok := s.durable[key]
	return value, ok
}

func (s *memStore) Has(key []byte) (bool, error) {
	_, ok := s.view(string(key))
	return ok, nil
}

func (s *memStore) Get(key []byte) ([]byte, error) {
	value, ok := s.view(string(key))
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return bytes.Clone(value), nil
}

func (s *memStore) Put(key, value []byte) error {
	s.ops++
	if s.crashAt != 0 && s.ops >= s.crashAt {
		return errCrash
	}
	s.pending = append(s.pending, write{key: string(key), value: bytes.Clone(value)})
	return nil
}

func (s *memStore) Delete(key []byte) error {
	s.ops++
	if s.crashAt != 0 && s.ops >= s.crashAt {
		return errCrash
	}
	s.pending = append(s.pending, write{key: string(key), deleted: true})
	return nil
}

func (s *memStore) SyncKeyValue() error {
	s.ops++
	if s.crashAt != 0 && s.ops >= s.crashAt {
		return errCrash
	}
	for _, w := range s.pending {
		if w.deleted {
			delete(s.durable, w.key)
		} else {
			s.durable[w.key] = w.value
		}
	}
	s.pending = nil
	return nil
}

// Range visits every visible key that starts with prefix, in ascending
// byte order, with the value visible through unsynced writes; a pending
// delete hides the key. The lifecycle registry and the recovery GC engine
// read their records through it.
func (s *memStore) Range(prefix []byte, visit func(key, value []byte) error) error {
	view := map[string][]byte{}
	for key, value := range s.durable {
		if strings.HasPrefix(key, string(prefix)) {
			view[key] = value
		}
	}
	for _, w := range s.pending {
		if !strings.HasPrefix(w.key, string(prefix)) {
			continue
		}
		if w.deleted {
			delete(view, w.key)
		} else {
			view[w.key] = w.value
		}
	}
	for _, key := range slices.Sorted(maps.Keys(view)) {
		if err := visit([]byte(key), view[key]); err != nil {
			return err
		}
	}
	return nil
}

// powerLoss drops every pending write and returns the reopened store.
func (s *memStore) powerLoss() *memStore {
	out := &memStore{durable: map[string][]byte{}}
	for key, value := range s.durable {
		out.durable[key] = value
	}
	return out
}

// reorgPins is a durable pin ledger of the import path.
type reorgPins struct {
	roots   map[string]string
	digests map[string]string
}

func (p *reorgPins) Verify(id, root string) (string, error) {
	got, ok := p.roots[id]
	if !ok {
		return "", fmt.Errorf("no pin %s", id)
	}
	if got != root {
		return "", fmt.Errorf("pin %s holds %s", id, got)
	}
	return p.digests[id], nil
}

func (p *reorgPins) Release(id string) error {
	delete(p.roots, id)
	delete(p.digests, id)
	return nil
}

// reorgInputs holds the sealed capsules and closed consumed manifests.
type reorgInputs struct {
	capsules map[string]string
	consumed map[string]admission.Consumed
}

func (i *reorgInputs) CapsuleDigest(id string) (string, error) {
	d, ok := i.capsules[id]
	if !ok {
		return "", fmt.Errorf("no capsule %s", id)
	}
	return d, nil
}

func (i *reorgInputs) Consumed(id string) (admission.Consumed, error) {
	c, ok := i.consumed[id]
	if !ok {
		return admission.Consumed{}, fmt.Errorf("no consumed manifest %s", id)
	}
	return c, nil
}

type reorgRoots struct{ durable map[string]bool }

func (r reorgRoots) Durable(root string) (bool, error) { return r.durable[root], nil }

// chain is the canonical header chain oracle of the model.
type chain struct {
	numbers map[string]uint64
	parents map[string]string
}

func newChain() *chain { return &chain{numbers: map[string]uint64{}, parents: map[string]string{}} }

func (c *chain) add(number uint64, hash, parent string) *chain {
	c.numbers[hash] = number
	c.parents[hash] = parent
	return c
}

// IsAncestorOrSelf reports whether block is an ancestor of anchor or
// equals it, walking the canonical headers.
func (c *chain) IsAncestorOrSelf(block, anchor decision.Head) (bool, error) {
	current := anchor
	for {
		if current.Hash == block.Hash {
			return current.Number == block.Number, nil
		}
		parent, ok := c.parents[current.Hash]
		if !ok {
			return false, nil
		}
		current = decision.Head{Number: c.numbers[parent], Hash: parent}
	}
}

// headOfBlock returns the chain head whose model block hash is hash: every
// block identity of the model is the fixed hash of its head hash string.
func (c *chain) headOfBlock(hash semantic.Hash32) decision.Head {
	for block, number := range c.numbers {
		if blockHashOf(block) == hash {
			return decision.Head{Number: number, Hash: block}
		}
	}
	return decision.Head{Hash: hash.String()}
}

// The semantic identity of the model run: the network and the ID16
// identities of the GC anchor contract.
var (
	network  = semantic.Network{ChainID: semantic.UInt256{1}, GenesisHash: seedHash32("bsc-genesis")}
	nodeID   = semantic.ID16{0x21}
	epochID  = semantic.ID16{0x24}
	producer = semantic.ID16{0x22}
	run      = semantic.ID16{0x23}
)

// seedHash32 derives the fixed genesis hash of a seed: the seed bytes in a
// 32-byte value.
func seedHash32(seed string) semantic.Hash32 {
	var out semantic.Hash32
	copy(out[:], []byte(seed))
	return out
}

// gcOracle adapts the model chain into the ancestry oracle of the recovery
// GC engine: a lookup failure is unknown evidence, never a guess.
type gcOracle struct{ chain *chain }

func (o gcOracle) Contains(ancestor, descendant recoverygc.AnchorRef) recoverygc.Verdict {
	ok, err := o.chain.IsAncestorOrSelf(
		decision.Head{Number: ancestor.Height, Hash: ancestor.Hash.String()},
		decision.Head{Number: descendant.Height, Hash: descendant.Hash.String()})
	if err != nil {
		return recoverygc.VerdictUnknown
	}
	if ok {
		return recoverygc.VerdictContains
	}
	return recoverygc.VerdictDivergent
}

// The inert GC seams of the model: the reorg evidence releases nothing
// here.
type gcNothingPending struct{}

func (gcNothingPending) Pending(recoverygc.EntityID) (recoverygc.PendingReport, error) {
	return recoverygc.PendingReport{}, nil
}

type gcReleaser struct{}

func (gcReleaser) ReleaseEntity(recoverygc.EntityID) error { return nil }

type gcPins struct{}

func (gcPins) Release(string) error { return nil }

// attestedEvidence builds one full header evidence of an attested snapshot
// on the given execution head.
func attestedEvidence(execHead decision.Head, hf bscfinality.HeaderFinality) bscfinality.Evidence {
	return bscfinality.Evidence{ObservedExecutionHead: execHead,
		ObservedNativeHeaderHead: hf.SnapshotHead, HeaderFinality: hf,
		NativeEffectiveFinalized: bscfinality.KnownReference(hf.Finalized, bscfinality.NativeLive),
		EffectiveBasis:           bscfinality.BasisHeaderSnapshot}
}
