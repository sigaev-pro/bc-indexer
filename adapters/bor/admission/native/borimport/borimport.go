// Package borimport binds the admission core (E01-02.admission), the source
// inputs (E01-02.inputs), the pin ledger (E01-02.pins) and the owned capture
// seam (E01-02.owned_capture) to the Bor product import path. It is the Bor
// block of #16; the design is in adapters/bor/admission/native/README.md.
//
// The preparation installs it at extractor/borimport in the accepted Bor tree.
// The decision logic lives in the stdlib-only package obligation; this
// package supplies the native side: the per-block capsule, the pin, the
// header check from capsule inputs, re-execution in recovery, the startup
// identity and the VM safe point.
package borimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/admission/native/obligation"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// The capsule items of one Bor observation, in sealing order.
const (
	ItemGenesis              = "genesis"
	ItemBlock                = "block"
	ItemParentHeader         = "parent-header"
	ItemParentHash           = "parent-hash"
	ItemParentRoot           = "parent-root"
	ItemAncestorHeaders      = "ancestor-headers"
	ItemCheckpointSnapshot   = "checkpoint-snapshot"
	ItemChainConfig          = "chain-config"
	ItemForkID               = "fork-id"
	ItemNativeCommit         = "native-commit"
	ItemNormalizationProfile = "normalization-profile"
)

// ItemNames lists the capsule items in sealing order.
var ItemNames = []string{ItemGenesis, ItemBlock, ItemParentHeader, ItemParentHash, ItemParentRoot,
	ItemAncestorHeaders, ItemCheckpointSnapshot, ItemChainConfig, ItemForkID, ItemNativeCommit,
	ItemNormalizationProfile}

// runPrefix starts the startup run record. Every such key is longer than 33
// bytes and starts with "e", so the native offline pruner never deletes it.
const runPrefix = "extractor-admission-run-v1/producer/"

// pinPrefix is the ledger key prefix of state_provider (provider.go
// pinPrefix). The tests check it against stateprovider.Pins.
const pinPrefix = "extractor-state-provider-pin-v1-"

var (
	// ErrConfig rejects a startup configuration or a running chain that does
	// not match the sealed inputs.
	ErrConfig = errors.New("admission startup configuration mismatch")
	// ErrCapsule reports a sealed capsule that does not hold the expected items.
	ErrCapsule = errors.New("capsule items do not match the observation")
)

// Config is the startup configuration of the Bor admission.
type Config struct {
	// InstanceDir is the native instance directory (the one with chaindata).
	InstanceDir string
	// StoreID is the producer binding of the native store (32 hex digits).
	StoreID string
	// Producer is the producer ID; Run is the run ID used when the store has
	// no saved run.
	Producer string
	Run      string
	// Genesis is the genesis file; ChainConfig the effective chain config
	// object. Both are capsule items in the shared canonical JSON form.
	Genesis     []byte
	ChainConfig []byte
	// Commit is the native source commit; Normalization the normalization
	// profile name.
	Commit        string
	Normalization string
	// HeimdallHost is the host of the Heimdall URL; the router logs only it.
	HeimdallHost string
}

// ProfileDigest is the pin profile: SHA-256 of the canonical JSON of the
// effective Bor capture profile (state_provider EffectiveProfile).
func ProfileDigest() string {
	sum := sha256.Sum256([]byte(`{"gcmode":"archive","state_scheme":"hash","syncmode":"full"}`))
	return hex.EncodeToString(sum[:])
}

func hexOf(hash common.Hash) string { return hex.EncodeToString(hash[:]) }

// Preflight refuses a stopped native store of another identity before the
// node opens it. A new instance (no chaindata yet) passes.
func Preflight(cfg Config, chainID string, genesis common.Hash) error {
	if _, err := os.Stat(filepath.Join(cfg.InstanceDir, "chaindata")); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	_, err := stateprovider.Preflight(cfg.InstanceDir, stateprovider.StoreIdentity{StoreID: cfg.StoreID,
		ChainID: chainID, GenesisHash: genesis, StateScheme: "hash"})
	return err
}

// StartupRun returns the saved run of producer, or saves proposed and syncs
// it. A restart therefore reopens the run whose obligations are stored.
func StartupRun(db ethdb.KeyValueStore, producer, proposed string) (string, error) {
	key := []byte(runPrefix + producer)
	if ok, err := db.Has(key); err != nil {
		return "", err
	} else if ok {
		data, err := db.Get(key)
		if err != nil {
			return "", err
		}
		var record struct {
			Run string `json:"run_id"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil || record.Run == "" {
			return "", fmt.Errorf("%w: malformed run record of %s", ErrConfig, producer)
		}
		return record.Run, nil
	}
	data, err := json.Marshal(map[string]string{"run_id": proposed})
	if err != nil {
		return "", err
	}
	if err := db.Put(key, data); err != nil {
		return "", err
	}
	return proposed, db.SyncKeyValue()
}

// ledger reads the native pin ledger.
type ledger struct {
	db       ethdb.Database
	provider *stateprovider.Provider
}

func (l ledger) read(id string) ([]byte, stateprovider.Pin, bool, error) {
	key := []byte(pinPrefix + id)
	if ok, err := l.db.Has(key); err != nil || !ok {
		return nil, stateprovider.Pin{}, false, err
	}
	record, err := l.db.Get(key)
	if err != nil {
		return nil, stateprovider.Pin{}, false, err
	}
	var pin stateprovider.Pin
	if err := json.Unmarshal(record, &pin); err != nil || pin.ID != id {
		return nil, stateprovider.Pin{}, false, fmt.Errorf("%w: ledger record %s", stateprovider.ErrInvalidPin, id)
	}
	return record, pin, true, nil
}

func (l ledger) Record(id string) ([]byte, string, bool, error) {
	record, pin, ok, err := l.read(id)
	if err != nil || !ok {
		return nil, "", ok, err
	}
	return record, hexOf(pin.ParentRoot), true, nil
}

func (l ledger) Readable(id string) error {
	_, pin, ok, err := l.read(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", stateprovider.ErrUnknownPin, id)
	}
	return stateprovider.Verify(l.db, pin)
}

func (l ledger) Release(id string) error { return l.provider.Release(id) }

// inputs adapts source_inputs to admission.Inputs.
type inputs struct{ db sourceinputs.Store }

func (i inputs) CapsuleDigest(key string) (string, error) {
	capsule, err := sourceinputs.ReadCapsule(i.db, key)
	return capsule.Digest, err
}

func (i inputs) Consumed(key string) (admission.Consumed, error) {
	log, err := sourceinputs.OpenLog(i.db, key)
	if err != nil {
		return admission.Consumed{}, err
	}
	closed, ok := log.Closed()
	if !ok {
		return admission.Consumed{}, fmt.Errorf("dependency log %s is not closed", key)
	}
	return admission.Consumed{Observation: closed.Observation, CapsuleDigest: closed.CapsuleDigest,
		Count: closed.Count, Chain: closed.Chain, Digest: closed.Digest}, nil
}

// depLog adapts sourceinputs.Log to obligation.Log.
type depLog struct{ log *sourceinputs.Log }

func (d depLog) Next(request []byte, fetch func() ([]byte, error)) ([]byte, error) {
	return d.log.Next(request, fetch)
}

func (d depLog) Finish() error {
	_, err := d.log.Finish()
	return err
}

// Node is the admission of one running Bor node.
type Node struct {
	cfg      Config
	db       ethdb.Database
	chain    *core.BlockChain
	engine   *bor.Bor
	identity stateprovider.StoreIdentity
	provider *stateprovider.Provider
	Driver   *obligation.Driver
	Router   *obligation.Router
	// Run is the producer run that this start opened.
	Run string
	// current and parent are the block of the observation being opened.
	// Observe sets them before the driver takes the guard; the native chain
	// lock (chainmu) already serializes every import, so no other Observe
	// runs at the same time.
	current *types.Block
	parent  *types.Header
}

// Start wires the admission into a native Bor chain before any import runs:
// it checks the Heimdall client, binds the store identity, opens the saved
// producer run, installs the Heimdall router as the process transport, opens
// the admission in RECOVERY_ONLY and returns the node. Recover then resolves
// the saved obligations and installs the observer.
func Start(cfg Config, db ethdb.Database, chain *core.BlockChain) (*Node, error) {
	engine, ok := chain.Engine().(*bor.Bor)
	if !ok {
		return nil, fmt.Errorf("%w: the native engine is not Bor", ErrConfig)
	}
	if err := engine.ExtractorCheckHeimdall(cfg.HeimdallHost); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	if err := checkConfig(cfg, chain.Config()); err != nil {
		return nil, err
	}
	identity, err := stateprovider.Bind(db, cfg.StoreID)
	if err != nil {
		return nil, err
	}
	provider, err := stateprovider.New(db, chain.TrieDB(), identity)
	if err != nil {
		return nil, err
	}
	run, err := StartupRun(db, cfg.Producer, cfg.Run)
	if err != nil {
		return nil, err
	}
	n := &Node{cfg: cfg, db: db, chain: chain, engine: engine, identity: identity, provider: provider, Run: run}
	guard := admission.NewGuard()
	token := guard.Acquire()
	adm, err := admission.Open(token, db, admission.Producer{ProducerID: cfg.Producer, RunID: run},
		obligation.PinAdapter{Ledger: ledger{db: db, provider: provider}, Unknown: stateprovider.ErrUnknownPin}, inputs{db: db})
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return nil, err
	}
	n.Router = &obligation.Router{Inner: http.DefaultTransport, Host: cfg.HeimdallHost}
	http.DefaultTransport = n.Router
	n.Driver = &obligation.Driver{Admission: adm, Guard: guard, Native: n, Router: n.Router, Reexecutor: n}
	return n, nil
}

// checkConfig requires the running chain config to be the decoding of the
// canonical chain-config item, and both JSON items to be canonical.
func checkConfig(cfg Config, running *params.ChainConfig) error {
	for name, data := range map[string][]byte{ItemGenesis: cfg.Genesis, ItemChainConfig: cfg.ChainConfig} {
		canonical, err := admission.CanonicalJSON(data)
		if err != nil || !bytes.Equal(canonical, data) {
			return fmt.Errorf("%w: %s is not canonical JSON", ErrConfig, name)
		}
	}
	var sealed params.ChainConfig
	if err := json.Unmarshal(cfg.ChainConfig, &sealed); err != nil {
		return fmt.Errorf("%w: chain-config: %w", ErrConfig, err)
	}
	want, err := json.Marshal(&sealed)
	if err != nil {
		return err
	}
	got, err := json.Marshal(running)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return fmt.Errorf("%w: native Bor does not run with the chain-config item", ErrConfig)
	}
	return nil
}

// Recover runs the recovery driver, then installs the observer on the chain
// so every later import runs per-block observations.
func (n *Node) Recover() (obligation.Report, error) {
	report, err := n.Driver.Recover(n)
	if err != nil {
		return report, err
	}
	n.chain.SetExtractorObserver(n)
	return report, nil
}

// Detach removes the observer and restores the process transport.
func (n *Node) Detach() {
	n.chain.SetExtractorObserver(nil)
	if http.DefaultTransport == n.Router {
		http.DefaultTransport = n.Router.Inner
	}
}

// Candidate returns the admission candidate of block on parent.
func Candidate(block *types.Block, parent *types.Header) admission.Candidate {
	return admission.Candidate{Number: block.NumberU64(), Hash: hexOf(block.Hash()), ParentHash: hexOf(parent.Hash()),
		ParentRoot: hexOf(parent.Root)}
}

// observation adapts obligation.Observation to core.ExtractorObservation.
type observation struct {
	node *Node
	obs  *obligation.Observation
}

// Observe implements core.ExtractorObserver. It returns after the closing
// ADMITTED sync, the cache purge and the header check from the capsule.
func (n *Node) Observe(block *types.Block, parent *types.Header) (core.ExtractorObservation, error) {
	if block.ParentHash() != parent.Hash() {
		return nil, fmt.Errorf("%w: block %d does not extend its parent", ErrCapsule, block.NumberU64())
	}
	n.current, n.parent = block, parent
	obs, err := n.Driver.Observe(Candidate(block, parent))
	if err != nil {
		return nil, err
	}
	return observation{node: n, obs: obs}, nil
}

// Done implements core.ExtractorObservation.
func (o observation) Done(block *types.Block, receipts types.Receipts, usedGas uint64, err error) error {
	result := ""
	if err == nil {
		result = ResultDigest(block, usedGas)
	}
	return o.obs.Done(result, err)
}

// Fail implements core.ExtractorObservation: the obligation stays ADMITTED.
func (o observation) Fail(err error) error { return o.obs.Fail(err) }

// ResultDigest binds the native result of a validated block: ValidateState
// has checked the state root, receipt root and gas used against the header.
func ResultDigest(block *types.Block, usedGas uint64) string {
	return obligation.ResultDigest(hexOf(block.Hash()), hexOf(block.Root()), hexOf(block.ReceiptHash()), fmt.Sprint(usedGas))
}

// Pin implements obligation.Native: the parent root with the parent header.
func (n *Node) Pin(key string, c admission.Candidate) error {
	if n.parent == nil || hexOf(n.parent.Root) != c.ParentRoot {
		return fmt.Errorf("%w: no parent for %s", ErrCapsule, key)
	}
	return n.provider.Pin(stateprovider.Pin{ID: key, Store: n.identity, Profile: ProfileDigest(),
		ParentRoot: n.parent.Root, Headers: []stateprovider.HeaderRef{{Hash: n.parent.Hash(), Number: n.parent.Number.Uint64()}}})
}

// Items builds the capsule items of block on parent.
func (n *Node) Items(block *types.Block, parent *types.Header) ([]sourceinputs.Item, error) {
	ancestors, checkpoint, err := n.engine.ExtractorAncestors(n.chain, parent)
	if err != nil {
		return nil, err
	}
	encodedBlock, err := rlp.EncodeToBytes(block)
	if err != nil {
		return nil, err
	}
	encodedParent, err := rlp.EncodeToBytes(parent)
	if err != nil {
		return nil, err
	}
	encodedAncestors, err := rlp.EncodeToBytes(ancestors)
	if err != nil {
		return nil, err
	}
	fork, err := ForkItem(n.chain.Config(), n.chain.Genesis(), block)
	if err != nil {
		return nil, err
	}
	if checkpoint == nil {
		checkpoint = []byte{}
	}
	return []sourceinputs.Item{
		{Name: ItemGenesis, Data: n.cfg.Genesis},
		{Name: ItemBlock, Data: encodedBlock},
		{Name: ItemParentHeader, Data: encodedParent},
		{Name: ItemParentHash, Data: parent.Hash().Bytes()},
		{Name: ItemParentRoot, Data: parent.Root.Bytes()},
		{Name: ItemAncestorHeaders, Data: encodedAncestors},
		{Name: ItemCheckpointSnapshot, Data: checkpoint},
		{Name: ItemChainConfig, Data: n.cfg.ChainConfig},
		{Name: ItemForkID, Data: fork},
		{Name: ItemNativeCommit, Data: []byte(n.cfg.Commit)},
		{Name: ItemNormalizationProfile, Data: []byte(n.cfg.Normalization)},
	}, nil
}

// ForkItem is the EIP-2124 fork ID at block, as JSON.
func ForkItem(config *params.ChainConfig, genesis *types.Block, block *types.Block) ([]byte, error) {
	id := forkid.NewID(config, genesis, block.NumberU64(), block.Time())
	return json.Marshal(map[string]string{"hash": hex.EncodeToString(id.Hash[:]), "next": fmt.Sprint(id.Next)})
}

// Seal implements obligation.Native.
func (n *Node) Seal(key string, c admission.Candidate) error {
	if n.current == nil || hexOf(n.current.Hash()) != c.Hash {
		return fmt.Errorf("%w: no block for %s", ErrCapsule, key)
	}
	items, err := n.Items(n.current, n.parent)
	if err != nil {
		return err
	}
	_, err = sourceinputs.SealCapsule(n.db, key, items)
	return err
}

// OpenLog implements obligation.Native.
func (n *Node) OpenLog(key string) (obligation.Log, error) {
	log, err := sourceinputs.OpenLog(n.db, key)
	if err != nil {
		return nil, err
	}
	return depLog{log: log}, nil
}

// OpenWindow implements obligation.Native: it holds back header
// verification of other blocks and drops the span store caches and the
// snapshot cache (which also stops the span poll loop), so every Heimdall
// read of the window belongs to this block and goes through the router.
func (n *Node) OpenWindow() { n.engine.ExtractorOpenWindow() }

// CloseWindow implements obligation.Native.
func (n *Node) CloseWindow() { n.engine.ExtractorCloseWindow() }

// Capsule holds the decoded capsule items of one observation.
type Capsule struct {
	Block      *types.Block
	Parent     *types.Header
	Ancestors  []*types.Header
	Checkpoint []byte
}

// ReadCapsule reads and decodes the sealed items of key.
func ReadCapsule(db sourceinputs.Store, key string) (Capsule, error) {
	sealed, err := sourceinputs.ReadCapsule(db, key)
	if err != nil {
		return Capsule{}, err
	}
	if len(sealed.Items) != len(ItemNames) {
		return Capsule{}, fmt.Errorf("%w: %s has %d items", ErrCapsule, key, len(sealed.Items))
	}
	data := map[string][]byte{}
	for index, ref := range sealed.Items {
		if ref.Name != ItemNames[index] {
			return Capsule{}, fmt.Errorf("%w: %s item %d is %s", ErrCapsule, key, index, ref.Name)
		}
		if data[ref.Name], err = sourceinputs.ReadItem(db, key, index); err != nil {
			return Capsule{}, err
		}
	}
	var c Capsule
	c.Block = new(types.Block)
	if err := rlp.DecodeBytes(data[ItemBlock], c.Block); err != nil {
		return Capsule{}, fmt.Errorf("%w: block: %w", ErrCapsule, err)
	}
	c.Parent = new(types.Header)
	if err := rlp.DecodeBytes(data[ItemParentHeader], c.Parent); err != nil {
		return Capsule{}, fmt.Errorf("%w: parent-header: %w", ErrCapsule, err)
	}
	if err := rlp.DecodeBytes(data[ItemAncestorHeaders], &c.Ancestors); err != nil || len(c.Ancestors) == 0 {
		return Capsule{}, fmt.Errorf("%w: ancestor-headers", ErrCapsule)
	}
	if c.Parent.Hash() != c.Block.ParentHash() || c.Ancestors[len(c.Ancestors)-1].Hash() != c.Parent.Hash() ||
		!bytes.Equal(data[ItemParentHash], c.Parent.Hash().Bytes()) || !bytes.Equal(data[ItemParentRoot], c.Parent.Root.Bytes()) {
		return Capsule{}, fmt.Errorf("%w: %s parent items disagree", ErrCapsule, key)
	}
	if len(data[ItemCheckpointSnapshot]) > 0 {
		c.Checkpoint = data[ItemCheckpointSnapshot]
	}
	return c, nil
}

// Verify implements obligation.Native: the header seal checked from the
// capsule's ancestor headers and checkpoint snapshot, with every Heimdall
// read through the open window. A capsule that cannot be read or decoded is
// not a native verdict.
func (n *Node) Verify(key string) (error, error) {
	c, err := ReadCapsule(n.db, key)
	if err != nil {
		return nil, err
	}
	return n.engine.ExtractorVerifySeal(c.Block.Header(), c.Ancestors, c.Checkpoint)
}

// Reexecute implements obligation.Reexecutor: native execution and
// validation of the capsule block on its pinned parent state, with the
// counted Heimdall reads replayed through the open window.
func (n *Node) Reexecute(key string, cand admission.Candidate) (string, error, error) {
	c, err := ReadCapsule(n.db, key)
	if err != nil {
		return "", nil, err
	}
	if Candidate(c.Block, c.Parent) != cand {
		return "", nil, fmt.Errorf("%w: %s capsule is not the saved candidate", ErrCapsule, key)
	}
	if !n.chain.HasState(c.Parent.Root) {
		return "", nil, fmt.Errorf("pinned parent state %s of %s is unavailable", c.Parent.Root, key)
	}
	_, _, usedGas, _, _, nativeErr := n.chain.ProcessBlock(c.Block, c.Parent, nil, nil, nil)
	if nativeErr != nil {
		return "", nativeErr, nil
	}
	return ResultDigest(c.Block, usedGas), nil, nil
}
