// Package poly is the POLY slot of E05-02.reorg (#55): the reorg, reapply,
// known-branch, reordered-event and StateSync lifecycle evidence of the
// Bor adapter on an in-memory chain and the real E03-01 producer journal.
//
// One native reorg decision that replaces several blocks is one delivery
// batch: the manifest is built from bounded chunks, a crash inside the
// build and the repeat of the same native marker restore the one former
// batch_id with the full ordered set of Removed/Applied/status
// descriptors, a partial manifest is never READY, and the events are never
// split into per-block batches. Removed is created for a READY Applied
// activation regardless of the transport PubAck.
//
// The lifecycle fold runs on the protocol/block automaton and the
// snapshots carry the Polygon finality markers of the E05-01
// finality_observer, so the reorg evidence exercises the real POLY
// evidence seam. This is test evidence, not a production consumer.
package poly

import (
	"fmt"
	"sync"

	"github.com/sigaev-pro/bc-indexer/core/journal/producer"
	"github.com/sigaev-pro/bc-indexer/core/journal/storage"
	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// The fixture chain: a short canonical Bor chain with a 4-block sprint, so
// the sprint ends 4, 8, 12 carry the observed StateSync work, plus side
// blocks stored without a head change (the known branch).
const (
	sprintLength = 4
	chunkSize    = 96 // small chunks, so one manifest spans several writes
)

var (
	polyNetwork = block.NetworkIdentity{
		Family:      "EVM",
		ChainID:     block.UInt256(hashOf("poly-chain-id")),
		GenesisHash: hashOf("poly-genesis"),
	}
	genesisParent = hashOf("poly-genesis")
	testBinding   = hashOf("poly destination binding")
)

func hashOf(name string) block.Hash32 {
	var h block.Hash32
	b := []byte(name)
	for i := range h {
		h[i] = b[i%len(b)]
	}
	h[0] |= 0x80
	return h
}

func idOf(name string) block.Id16 {
	var id block.Id16
	b := []byte(name)
	for i := range id {
		id[i] = b[i%len(b)]
	}
	return id
}

// blockInfo is one observed Bor block of the fixture chain.
type blockInfo struct {
	Number    uint64
	Hash      block.Hash32
	Parent    block.Hash32
	StateSync uint64 // observed StateSync records committed at this block
	Side      bool   // stored through the side-chain path, no head change
}

// chain is the in-memory canonical Bor chain with its known side branches.
type chain struct {
	mu     sync.Mutex
	blocks map[block.Hash32]*blockInfo
	canon  map[uint64]block.Hash32
	head   block.AnchorRef
}

func newChain() *chain {
	return &chain{blocks: map[block.Hash32]*blockInfo{}, canon: map[uint64]block.Hash32{}}
}

// extend imports one new canonical block building on the head.
func (c *chain) extend(name string, stateSync uint64) *blockInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	number := c.head.Height + 1
	parent := genesisParent
	if number > 1 {
		parent = c.canon[number-1]
	}
	b := &blockInfo{
		Number:    number,
		Hash:      hashOf(fmt.Sprintf("%s-%d", name, number)),
		Parent:    parent,
		StateSync: stateSync,
	}
	c.add(b)
	return b
}

// side stores one block of a known branch without a head change.
func (c *chain) side(name string, number uint64, parent block.Hash32, stateSync uint64) *blockInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := &blockInfo{
		Number:    number,
		Hash:      hashOf(fmt.Sprintf("%s-side-%d", name, number)),
		Parent:    parent,
		StateSync: stateSync,
		Side:      true,
	}
	c.add(b)
	return b
}

func (c *chain) add(b *blockInfo) {
	c.blocks[b.Hash] = b
	if !b.Side {
		c.canon[b.Number] = b.Hash
		c.head = block.AnchorRef{Height: b.Number, Hash: b.Hash}
	}
}

// switchTo promotes a known branch: the suffix of the canonical chain from
// the divergence point is replaced by the branch path to tip. It returns
// the removed canonical suffix and the promoted path, both ascending by
// number.
func (c *chain) switchTo(tip *blockInfo) (removed, promoted []*blockInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Walk the promoted path back to the common ancestor.
	path := []*blockInfo{}
	for b := tip; b != nil; b = c.blocks[b.Parent] {
		if cur, ok := c.canon[b.Number]; ok && cur == b.Hash {
			break // the common ancestor stays canonical
		}
		path = append(path, b)
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	divergence := path[0].Number
	for n := divergence; n <= c.head.Height; n++ {
		if h, ok := c.canon[n]; ok {
			removed = append(removed, c.blocks[h])
		}
	}
	for n := divergence; n <= c.head.Height; n++ {
		delete(c.canon, n)
	}
	for _, b := range path {
		c.canon[b.Number] = b.Hash
	}
	c.head = block.AnchorRef{Height: tip.Number, Hash: tip.Hash}
	return removed, path
}

// headAnchor returns the canonical head.
func (c *chain) headAnchor() block.AnchorRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.head
}

// nextHeight returns the number the next canonical block extends.
func (c *chain) nextHeight() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.head.Height + 1
}

// canonicalAnchor returns the canonical anchor at a height.
func (c *chain) canonicalAnchor(height uint64) (block.AnchorRef, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.canon[height]
	if !ok {
		return block.AnchorRef{}, false
	}
	return block.AnchorRef{Height: height, Hash: h}, true
}

// isCanonical reports whether the anchor is on the canonical chain.
func (c *chain) isCanonical(a block.AnchorRef) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.canon[a.Height] == a.Hash
}

// ancestorOrSelf walks the exact hashes: the canonical descendant extends
// the ancestor chain, never a height comparison alone.
func (c *chain) ancestorOrSelf(ancestor, descendant block.AnchorRef) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.canon[descendant.Height] != descendant.Hash {
		return false
	}
	if c.canon[ancestor.Height] != ancestor.Hash {
		return false
	}
	if descendant.Height < ancestor.Height {
		return false
	}
	walk := descendant
	for walk.Height > ancestor.Height {
		b, ok := c.blocks[walk.Hash]
		if !ok {
			return false
		}
		walk = block.AnchorRef{Height: b.Number - 1, Hash: b.Parent}
	}
	return walk == ancestor
}

// chainAncestry adapts the chain to the finality_observer ancestry seam.
type chainAncestry struct{ c *chain }

func (a chainAncestry) IsAncestorOrSelf(ancestor, descendant block.AnchorRef) (bool, error) {
	return a.c.ancestorOrSelf(ancestor, descendant), nil
}

func (a chainAncestry) VerifyCheckpointRoot(start, end uint64, root block.Hash32) (block.AnchorRef, error) {
	return block.AnchorRef{}, fmt.Errorf("the native range verification is not part of this fixture")
}

// failingStorage injects power loss inside the manifest build: the write
// with the given 1-based number fails atomically and nothing after it
// reaches the store.
type failingStorage struct {
	inner    storage.Storage
	failFrom int // fail every write from this 1-based number on; 0 never
	writes   int
	err      error
}

func (f *failingStorage) Get(key []byte) ([]byte, error) { return f.inner.Get(key) }

func (f *failingStorage) Put(key, value []byte) error {
	f.writes++
	if f.failFrom > 0 && f.writes >= f.failFrom {
		return f.err
	}
	return f.inner.Put(key, value)
}

func (f *failingStorage) Delete(key []byte) error {
	f.writes++
	if f.failFrom > 0 && f.writes >= f.failFrom {
		return f.err
	}
	return f.inner.Delete(key)
}

func (f *failingStorage) Apply(b *storage.Batch) error {
	f.writes++
	if f.failFrom > 0 && f.writes >= f.failFrom {
		return f.err
	}
	return f.inner.Apply(b)
}

func (f *failingStorage) Iterate(lo, hi []byte, visit func(key, value []byte) error) error {
	return f.inner.Iterate(lo, hi, visit)
}

// countWrites reports how many writes reached the injection point.
func (f *failingStorage) countWrites() int { return f.writes }

// openJournal opens the producer journal over a storage seam with the
// destination binding saved.
func openJournal(st storage.Storage) (*producer.Store, error) {
	journal, err := producer.Open(st)
	if err != nil {
		return nil, err
	}
	if err := journal.SetDestinationBinding(testBinding); err != nil {
		return nil, err
	}
	return journal, nil
}
