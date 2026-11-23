package finalityobserver

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/sigaev-pro/bc-indexer/protocol/block"
)

// The shared test fixture: one own Polygon L1, one own Heimdall and one
// canonical Bor chain, with injectable failures per seam.

func mustHash(name string) block.Hash32 {
	var h block.Hash32
	sum := []byte(name)
	for i := range h {
		h[i] = sum[i%len(sum)]
	}
	h[0] |= 0x80
	return h
}

func mustAddress(name string) block.Address20 {
	var a block.Address20
	sum := []byte(name)
	for i := range a {
		a[i] = sum[i%len(sum)]
	}
	a[0] |= 0x80
	return a
}

func mustID(name string) block.Id16 {
	var id block.Id16
	sum := []byte(name)
	for i := range id {
		id[i] = sum[i%len(sum)]
	}
	return id
}

var (
	testL1Network = block.NetworkIdentity{
		Family:      "EVM",
		ChainID:     block.UInt256(mustHash("l1-chain-id")),
		GenesisHash: mustHash("l1-genesis"),
	}
	testHeimdallIdentity = HeimdallIdentity{
		ChainID:               "heimdall-137",
		GenesisDocumentSHA256: mustHash("heimdall-genesis"),
	}
	testRootChain    = mustAddress("rootchain-address")
	testProposer     = mustAddress("checkpoint-proposer")
	testCheckpointID = uint64(3)
	testInterval     = uint64(10000)
	testBorStart     = uint64(20001)
	testBorEnd       = uint64(30000)
	testCheckpointRt = mustHash("checkpoint-root")
)

// testSources builds one valid sources_file with the given deployment IDs;
// spacing controls the indentation, so a whitespace-only change produces a
// different D80 digest of the same content.
func testSources(spacing string) []byte {
	return []byte(fmt.Sprintf(`{%s
  "schema_version": 1,%s
  "l1": {%s
    "deployment_id": "%s",%s
    "role": "POLYGON_L1_EXECUTION",%s
    "endpoint": "http://polygon-l1:8545",%s
    "network": {"family": "EVM", "chain_id": "%s", "genesis_hash": "%s"}%s
  },%s
  "heimdall": {%s
    "deployment_id": "%s",%s
    "role": "POLYGON_HEIMDALL",%s
    "endpoint": "http://heimdall:1317",%s
    "chain_id": "heimdall-137",%s
    "genesis_document_sha256": "%s"%s
  }%s
}`,
		spacing, spacing, spacing,
		mustID("l1-deployment"), spacing, spacing, spacing,
		testL1Network.ChainID, testL1Network.GenesisHash, spacing, spacing,
		spacing,
		mustID("heimdall-deployment"), spacing, spacing, spacing, spacing,
		testHeimdallIdentity.GenesisDocumentSHA256, spacing, spacing))
}

// emptyEvidence builds one all-NotObserved evidence set for observation
// fixtures.
func emptyEvidence() PolygonEvidence {
	return PolygonEvidence{
		Milestone:           NotObservedOf[Milestone](),
		Checkpoint:          NotObservedOf[Checkpoint](),
		MilestoneWhitelist:  NotObservedOf[Whitelist](),
		CheckpointWhitelist: NotObservedOf[Whitelist](),
		NativeFinalized:     NotObservedOf[NativeFinalized](),
		L1Checkpoint:        NotObservedOf[L1Checkpoint](),
	}
}

// headerBlockResult encodes one static HeaderBlock result.
func headerBlockResult(start, end uint64, createdAt uint64, root block.Hash32, proposer block.Address20) []byte {
	out := make([]byte, HeaderBlockWordCount*32)
	binary.BigEndian.PutUint64(out[32-8:], start)
	binary.BigEndian.PutUint64(out[2*32-8:], end)
	binary.BigEndian.PutUint64(out[3*32-8:], createdAt)
	copy(out[3*32:], root[:])
	copy(out[4*32+12:], proposer[:])
	return out
}

// borChain is one canonical Bor chain for the ancestry fake.
type borChain struct {
	heights map[uint64]block.Hash32
	parents map[block.Hash32]block.Hash32
}

func newBorChain(hashes ...block.Hash32) *borChain {
	c := &borChain{heights: map[uint64]block.Hash32{}, parents: map[block.Hash32]block.Hash32{}}
	for i, h := range hashes {
		c.heights[uint64(i+1)] = h
		if i > 0 {
			c.parents[h] = hashes[i-1]
		}
	}
	return c
}

func (c *borChain) anchor(height uint64) block.AnchorRef {
	return block.AnchorRef{Height: height, Hash: c.heights[height]}
}

func (c *borChain) isAncestorOrSelf(ancestor, descendant block.AnchorRef) bool {
	if c.heights[descendant.Height] != descendant.Hash {
		return false
	}
	if c.heights[ancestor.Height] != ancestor.Hash {
		return false
	}
	walk := descendant
	for walk.Height > ancestor.Height {
		parent, ok := c.parents[walk.Hash]
		if !ok {
			return false
		}
		walk = block.AnchorRef{Height: walk.Height - 1, Hash: parent}
	}
	return walk == ancestor
}

// fakeHeimdall serves the parameters, the stored checkpoint and the
// milestone status, with one injectable error per method.
type fakeHeimdall struct {
	mu               sync.Mutex
	storeID          block.Id16
	identity         HeimdallIdentity
	chain            ChainParams
	checkpointParams CheckpointParams
	checkpoint       SourceCheckpoint
	checkpointResp   Response
	milestone        SourceMilestone
	milestoneResp    Response
	chainErr         error
	checkpointErr    error
	paramsErr        error
}

func (f *fakeHeimdall) StoreIdentity() block.Id16 { return f.storeID }

func (f *fakeHeimdall) Identity() HeimdallIdentity { return f.identity }

func (f *fakeHeimdall) ChainParams() (ChainParams, Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chain, Response{}, f.chainErr
}

func (f *fakeHeimdall) CheckpointParams() (CheckpointParams, Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkpointParams, Response{}, f.paramsErr
}

func (f *fakeHeimdall) MilestoneLatest() (SourceMilestone, Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.milestone, f.milestoneResp, nil
}

func (f *fakeHeimdall) CheckpointLatest() (SourceCheckpoint, Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkpoint, f.checkpointResp, f.checkpointErr
}

func (f *fakeHeimdall) setCheckpoint(cp SourceCheckpoint) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkpoint = cp
}

func (f *fakeHeimdall) setChainErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chainErr = err
}

func (f *fakeHeimdall) setCheckpointErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkpointErr = err
}

func (f *fakeHeimdall) setParamsErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paramsErr = err
}

// fakeL1 serves the finalized anchor, the canonical ancestry and the
// RootChain read, recording every headerBlocks call.
type fakeL1 struct {
	mu          sync.Mutex
	nodeID      block.Id16
	network     block.NetworkIdentity
	finalized   block.AnchorRef
	finErr      error
	result      []byte
	headerErr   error
	headerCalls []headerCall
	ancestorErr error
	onHeader    func() // runs inside HeaderBlocks, before the recorded error
}

type headerCall struct {
	calldata []byte
	at       block.AnchorRef
}

func (f *fakeL1) NodeID() block.Id16 { return f.nodeID }

func (f *fakeL1) Network() block.NetworkIdentity { return f.network }

func (f *fakeL1) Finalized() (block.AnchorRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.finalized, f.finErr
}

func (f *fakeL1) IsCanonicalAncestor(ancestor, descendant block.AnchorRef) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ancestorErr != nil {
		return false, f.ancestorErr
	}
	return ancestor == descendant, nil
}

func (f *fakeL1) HeaderBlocks(calldata []byte, at block.AnchorRef) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headerCalls = append(f.headerCalls, headerCall{calldata: append([]byte(nil), calldata...), at: at})
	if f.onHeader != nil {
		f.onHeader()
	}
	if f.headerErr != nil {
		return nil, f.headerErr
	}
	return append([]byte(nil), f.result...), nil
}

func (f *fakeL1) calls() []headerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]headerCall, len(f.headerCalls))
	copy(out, f.headerCalls)
	return out
}

func (f *fakeL1) setFinErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finErr = err
}

func (f *fakeL1) setFinalized(a block.AnchorRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalized = a
}

func (f *fakeL1) setResult(r []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result = r
}

func (f *fakeL1) setHeaderErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headerErr = err
}

// fakeAncestry serves the Bor ancestry and the native range verification.
type fakeAncestry struct {
	mu        sync.Mutex
	chain     *borChain
	verifyFn  func(start, end uint64, root block.Hash32) (block.AnchorRef, error)
	verifyErr error
}

func (f *fakeAncestry) IsAncestorOrSelf(ancestor, descendant block.AnchorRef) (bool, error) {
	f.mu.Lock()
	chain := f.chain
	f.mu.Unlock()
	if chain == nil {
		return false, nil
	}
	return chain.isAncestorOrSelf(ancestor, descendant), nil
}

func (f *fakeAncestry) VerifyCheckpointRoot(start, end uint64, root block.Hash32) (block.AnchorRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.verifyErr != nil {
		return block.AnchorRef{}, f.verifyErr
	}
	if f.verifyFn != nil {
		return f.verifyFn(start, end, root)
	}
	return f.chain.anchor(end), nil
}
