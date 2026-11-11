// Extractor patch (E01-02.admission, Bor block): the ancestor rule of the
// per-block capsule and the header check from capsule inputs. The
// preparation adds this file to the accepted Bor tree; see
// adapters/bor/admission/native/README.md.

package bor

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// ExtractorCheckpointInterval is the number of blocks between the vote
// snapshots that Bor stores on disk (checkpointInterval).
const ExtractorCheckpointInterval = checkpointInterval

// errExtractorAncestors reports capsule ancestors that do not follow the rule.
var errExtractorAncestors = errors.New("capsule ancestors do not follow the Bor ancestor rule")

// extractorOverride is a snapshot built from capsule inputs for one target
// header. snapshot returns it for that header only.
type extractorOverride struct {
	target common.Hash
	snap   *Snapshot
}

// extractorSnapshots holds at most one override. The observation guard
// allows one capsule header check at a time; each check removes only its
// own override.
var extractorSnapshots atomic.Pointer[extractorOverride]

// extractorWindow holds back header verification of other blocks while an
// observation window is open. The window takes it exclusively; VerifyHeader,
// VerifyHeaders (per header) and VerifySeal take it shared. The capsule
// check and Finalize of the observed block do not take it.
var extractorWindow sync.RWMutex

// extractorGate waits for the open window, if any, to close and returns the
// release function. The patched exported verification entry points call it.
func (c *Bor) extractorGate() func() {
	extractorWindow.RLock()
	return extractorWindow.RUnlock
}

// ExtractorOpenWindow starts an observation window: it waits for header
// verification in progress to finish, holds back new verification of other
// blocks, and drops the cached Heimdall data and snapshots.
func (c *Bor) ExtractorOpenWindow() {
	extractorWindow.Lock()
	c.ExtractorPurgeInputs()
}

// ExtractorCloseWindow ends the window that ExtractorOpenWindow started.
func (c *Bor) ExtractorCloseWindow() { extractorWindow.Unlock() }

// ExtractorCheckHeimdall requires the single native HTTP Heimdall client at
// host with the process transport, so the router sees every Heimdall read.
// A gRPC client, a failover client over several endpoints, a client with its
// own transport, a WebSocket client or no client is refused.
func (c *Bor) ExtractorCheckHeimdall(host string) error {
	client, ok := c.HeimdallClient.(*heimdall.HeimdallClient)
	if !ok || client == nil {
		return fmt.Errorf("extractor admission needs the native HTTP Heimdall client, not %T", c.HeimdallClient)
	}
	if got := client.ExtractorHost(); got != host {
		return fmt.Errorf("extractor admission: Heimdall host %q, expected %q", got, host)
	}
	if !client.ExtractorProcessTransport() {
		return errors.New("extractor admission: the Heimdall client has its own transport")
	}
	if c.HeimdallWSClient != nil {
		return errors.New("extractor admission: a Heimdall WebSocket client is configured")
	}
	return nil
}

// extractorSnapshot returns the capsule snapshot for targetHeader, if any.
// The patched snapshot method calls it first.
func (c *Bor) extractorSnapshot(targetHeader *types.Header) *Snapshot {
	if override := extractorSnapshots.Load(); override != nil && override.target == targetHeader.Hash() {
		return override.snap
	}
	return nil
}

// ExtractorPurgeInputs drops the cached Heimdall data and snapshots: the span
// store caches (which also stops its poll loop and range warmup) and the
// snapshot cache. Every later Heimdall read and snapshot walk starts cold.
// The verified-header cache stays: it holds no Heimdall data and only lets
// performSpanCheck skip a wait for the next header.
func (c *Bor) ExtractorPurgeInputs() {
	c.recents.DeleteAll()
	c.spanStore.PurgeCache()
}

// ExtractorIsRio reports whether the header at number uses the Rio snapshot
// (getVeBlopSnapshot), which reads the span and walks no ancestors.
func (c *Bor) ExtractorIsRio(number uint64) bool {
	return c.config.IsRio(new(big.Int).SetUint64(number))
}

// ExtractorAncestors returns the ancestor inputs that verifying a child of
// parent may read. Before Rio, the snapshot walk goes back from the parent to
// the nearest checkpoint at or below it, whose snapshot Bor stored on disk:
// the headers are that checkpoint header up to the parent, and checkpoint is
// the stored snapshot. From Rio on, only the parent header is read (the
// author check of performSpanCheck) and the span comes from Heimdall, so
// checkpoint is nil.
func (c *Bor) ExtractorAncestors(chain consensus.ChainHeaderReader, parent *types.Header) ([]*types.Header, []byte, error) {
	if c.ExtractorIsRio(parent.Number.Uint64() + 1) {
		return []*types.Header{parent}, nil, nil
	}
	first := parent.Number.Uint64() - parent.Number.Uint64()%checkpointInterval
	headers := []*types.Header{parent}
	for header := parent; header.Number.Uint64() > first; {
		header = chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
		if header == nil {
			return nil, nil, consensus.ErrUnknownAncestor
		}
		headers = append(headers, header)
	}
	for i, j := 0, len(headers)-1; i < j; i, j = i+1, j-1 {
		headers[i], headers[j] = headers[j], headers[i]
	}
	hash := headers[0].Hash()
	checkpoint, err := c.db.Get(append([]byte("bor-"), hash[:]...))
	if err != nil {
		return nil, nil, fmt.Errorf("stored snapshot of checkpoint %d: %w", first, err)
	}
	return headers, checkpoint, nil
}

// extractorCapsuleChain serves only the capsule ancestor headers.
type extractorCapsuleChain struct {
	config  *params.ChainConfig
	headers []*types.Header
}

func (h extractorCapsuleChain) Config() *params.ChainConfig { return h.config }
func (h extractorCapsuleChain) CurrentHeader() *types.Header {
	return h.headers[len(h.headers)-1]
}
func (h extractorCapsuleChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	for _, header := range h.headers {
		if header.Hash() == hash && header.Number.Uint64() == number {
			return header
		}
	}
	return nil
}
func (h extractorCapsuleChain) GetHeaderByNumber(number uint64) *types.Header {
	for _, header := range h.headers {
		if header.Number.Uint64() == number {
			return header
		}
	}
	return nil
}
func (h extractorCapsuleChain) GetHeaderByHash(hash common.Hash) *types.Header {
	for _, header := range h.headers {
		if header.Hash() == hash {
			return header
		}
	}
	return nil
}
func (h extractorCapsuleChain) GetTd(common.Hash, uint64) *big.Int { return nil }

// ExtractorVerifySeal verifies the seal of header with the native verifySeal,
// from capsule inputs only: the ancestor headers serve every header read, and
// the snapshot is built from the stored checkpoint snapshot and those headers
// before Rio, or from the span (read through the observation's dependency
// log) from Rio on. The caller opens the window first. nativeErr is the
// verifySeal result, a native validation failure; err is a capsule that does
// not follow the ancestor rule or a snapshot that cannot be built, which is
// not a verdict on the block.
func (c *Bor) ExtractorVerifySeal(header *types.Header, headers []*types.Header, checkpoint []byte) (nativeErr, err error) {
	if len(headers) == 0 || header.Number == nil || !header.Number.IsUint64() {
		return nil, errExtractorAncestors
	}
	parent := headers[len(headers)-1]
	if parent.Hash() != header.ParentHash || parent.Number.Uint64()+1 != header.Number.Uint64() {
		return nil, errExtractorAncestors
	}
	for i := 1; i < len(headers); i++ {
		if headers[i].ParentHash != headers[i-1].Hash() || headers[i].Number.Uint64() != headers[i-1].Number.Uint64()+1 {
			return nil, errExtractorAncestors
		}
	}
	chain := extractorCapsuleChain{config: c.chainConfig, headers: headers}
	var snap *Snapshot
	if c.ExtractorIsRio(header.Number.Uint64()) {
		if len(headers) != 1 || checkpoint != nil {
			return nil, errExtractorAncestors
		}
		snap, err = c.getVeBlopSnapshot(chain, header, nil, false)
	} else {
		first := headers[0]
		if first.Number.Uint64()%checkpointInterval != 0 || parent.Number.Uint64()-first.Number.Uint64() >= checkpointInterval {
			return nil, errExtractorAncestors
		}
		db := rawdb.NewMemoryDatabase()
		hash := first.Hash()
		if err = db.Put(append([]byte("bor-"), hash[:]...), checkpoint); err != nil {
			return nil, err
		}
		snap, err = loadSnapshot(c.chainConfig, c.config, c.signatures, db, hash)
		if err == nil && (snap.Number != first.Number.Uint64() || snap.Hash != hash) {
			err = errExtractorAncestors
		}
		if err == nil && len(headers) > 1 {
			snap, err = snap.apply(headers[1:], c)
		}
	}
	if err != nil {
		return nil, err
	}
	override := &extractorOverride{target: header.Hash(), snap: snap}
	extractorSnapshots.Store(override)
	defer extractorSnapshots.CompareAndSwap(override, nil)
	return c.verifySeal(chain, header, nil), nil
}
