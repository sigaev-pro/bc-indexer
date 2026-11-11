// Package native wires the shared admission core (E01-02.admission) into the
// accepted Geth import path. It is Geth-only: the preparation copies it from
// adapters/geth/admission/native into the Geth tree as
// extractor/admission/native, next to the shared packages
// extractor/admission, extractor/source_inputs and extractor/state_provider.
//
// It joins four seams:
//
//   - the pin adapter over the native pin ledger (E01-02.pins);
//   - the inputs adapter over the static capsule and dependency log
//     (E01-02.inputs);
//   - the per-block observation that the patched ProcessBlock calls before
//     processor.Process (core.ExtractorAdmission), with the recovery driver
//     that resumes saved ADMITTED obligations at startup;
//   - the startup identity and the retention guards of the shipped binary.
//
// The design is in adapters/geth/admission/native/README.md.
package native

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/rlp"
)

// The retention guards and the capture-store refusal are active in every
// binary that links this package, with or without an admission config: pins
// of an earlier run stay live.
func init() {
	core.ExtractorRewindAllowed = stateprovider.RequireRewindAllowed
	core.ExtractorRequireNoCapture = RequireNoCapture
	rawdb.ExtractorFreezeLimit = stateprovider.FreezeLimit
}

// ConfigEnv names the environment variable that holds the path of the
// admission config file of the shipped binary. Without it, the node runs
// without admission; the retention guards stay active.
const ConfigEnv = "EXTRACTOR_ADMISSION_CONFIG"

// pinPrefix is the key prefix of the native pin ledger (E01-02.pins). The
// unit test checks it against stateprovider.Pins.
const pinPrefix = "extractor-state-provider-pin-v1-"

// runKey holds the run record. It is neither 32 nor 33 bytes long and does
// not start with the native code prefix, so the offline pruner keeps it.
const runKey = "extractor-admission-geth-run-v1"

// Domain tags of the Geth digests. Each is followed by one zero byte.
const (
	tagExecResult = "extractor-geth-exec-result-v1"
	tagRejection  = "extractor-geth-rejection-v1"
)

// Capsule item names. Every BLOCKHASH window ancestor below the parent is
// "ancestor-header-<number>".
const (
	itemHeader    = "header-rlp"
	itemBody      = "body-rlp"
	itemEncodings = "encodings"
	itemParent    = "parent-header"
	itemParentRef = "parent-hash"
	itemRoot      = "parent-root"
	itemConfig    = "chain-config"
	itemForkID    = "fork-id"
	itemCommit    = "native-commit"
	itemProfile   = "normalization-profile"
	ancestorStem  = "ancestor-header-"
)

// encodings names the byte format of every encoded capsule item.
const encodings = `{"ancestor-header":"rlp(types.Header)","body-rlp":"rlp(types.Body)","header-rlp":"rlp(types.Header)","parent-header":"rlp(types.Header)"}`

// windowSize is the BLOCKHASH window of the EVM: a block reads the hashes of
// its 256 most recent ancestors.
const windowSize = 256

var (
	// ErrConfig rejects a malformed admission config.
	ErrConfig = errors.New("invalid admission config")
	// ErrRun rejects a store whose run record names another store or producer.
	ErrRun = errors.New("store belongs to another producer run")
	// ErrGuardMissing refuses ProcessBlock without the observation guard,
	// which means an import entry point without the guard patch.
	ErrGuardMissing = errors.New("observation guard is not held by the import")
	// ErrCapsule rejects a saved capsule that does not describe the block.
	ErrCapsule = errors.New("saved capsule does not match the block")
	// ErrRecovery stops startup when an ADMITTED obligation stays open.
	ErrRecovery = errors.New("admitted obligation was not resolved")
)

var (
	storeIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	partPattern    = regexp.MustCompile(`^[0-9a-z][0-9a-z-]{0,39}$`)
)

// Config is the admission config of one capture node.
type Config struct {
	StoreID     string      `json:"store_id"`
	ProducerID  string      `json:"producer_id"`
	ChainID     string      `json:"chain_id"`
	GenesisHash common.Hash `json:"genesis_hash"`
	// NativeCommit is the source commit of the native binary, recorded in
	// every capsule.
	NativeCommit string `json:"native_commit"`
	// Profile is the normalization profile ID recorded in every capsule.
	// E04-01.block_record defines the product profile (D99 item 4).
	Profile string `json:"normalization_profile"`
}

func (c Config) validate() error {
	if !storeIDPattern.MatchString(c.StoreID) || !partPattern.MatchString(c.ProducerID) ||
		!commitPattern.MatchString(c.NativeCommit) || c.Profile == "" || c.GenesisHash == (common.Hash{}) {
		return fmt.Errorf("%w: store, producer, commit, profile or genesis", ErrConfig)
	}
	if c.ChainID == "" || strings.TrimLeft(c.ChainID, "0123456789") != "" || c.ChainID[0] == '0' {
		return fmt.Errorf("%w: chain ID %q", ErrConfig, c.ChainID)
	}
	return nil
}

func (c Config) identity() stateprovider.StoreIdentity {
	return stateprovider.StoreIdentity{StoreID: c.StoreID, ChainID: c.ChainID, GenesisHash: c.GenesisHash,
		StateScheme: rawdb.HashScheme}
}

// LoadConfig reads a config file strictly: unknown fields and trailing data
// are refused.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := strictDecode(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("%w: %v", ErrConfig, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ConfigFromEnvironment loads the config named by ConfigEnv, or returns nil
// when the variable is unset.
func ConfigFromEnvironment() (*Config, error) {
	path := os.Getenv(ConfigEnv)
	if path == "" {
		return nil, nil
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Preflight runs before native code opens the store of instanceDir. A store
// bound to this identity, or an unbound store without pins, passes; a store
// of another identity, a forbidden layout or an unreadable pin is refused,
// and nothing is written. A new instance without chaindata passes.
func Preflight(instanceDir string, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(instanceDir, "chaindata")); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	_, err := stateprovider.Preflight(instanceDir, cfg.identity())
	if err == nil {
		return nil
	}
	unbound := cfg.identity()
	unbound.StoreID = ""
	if _, unboundErr := stateprovider.Preflight(instanceDir, unbound); unboundErr == nil {
		return nil
	}
	return err
}

// RecoverPruning resumes an interrupted offline prune of instanceDir only
// when no pin is live; with a live pin the node must not start. Without an
// interrupted prune it does nothing.
func RecoverPruning(db ethdb.Database, instanceDir string) error {
	matches, err := filepath.Glob(filepath.Join(instanceDir, "statebloom.*"))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return nil
	}
	return stateprovider.RecoverPruning(db, instanceDir)
}

// RequireNoCapture refuses a store that belongs to a capture producer: a
// bound store, a live pin or a run record. The shipped binary calls it when
// no admission config is given, so such a store never imports without its
// admission and recovery.
func RequireNoCapture(db ethdb.Database) error {
	if identity, bound, err := stateprovider.BoundIdentity(db); err != nil {
		return err
	} else if bound {
		return fmt.Errorf("%w: store is bound to capture producer store %s; set %s", ErrConfig, identity.StoreID, ConfigEnv)
	}
	if err := stateprovider.RequireNoLivePins(db); err != nil {
		return err
	}
	if ok, err := db.Has([]byte(runKey)); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: store holds a producer run record; set %s", ErrConfig, ConfigEnv)
	}
	return nil
}

// runRecord binds the store to one producer and its run.
type runRecord struct {
	StoreID    string `json:"store_id"`
	ProducerID string `json:"producer_id"`
	RunID      string `json:"run_id"`
}

// OpenRun returns the saved producer run of the store. The first call
// records a run with runID, or a new random ID if runID is empty, and syncs
// it. A record of another store or producer is ErrRun, so a restart can
// never open a new run and lose the obligations of the saved one.
func OpenRun(db ethdb.Database, storeID, producerID, runID string) (string, error) {
	ok, err := db.Has([]byte(runKey))
	if err != nil {
		return "", err
	}
	if ok {
		data, err := db.Get([]byte(runKey))
		if err != nil {
			return "", err
		}
		var record runRecord
		if err := strictDecode(data, &record); err != nil || !partPattern.MatchString(record.RunID) {
			return "", fmt.Errorf("%w: malformed run record", ErrRun)
		}
		if record.StoreID != storeID || record.ProducerID != producerID {
			return "", fmt.Errorf("%w: store %s producer %s", ErrRun, record.StoreID, record.ProducerID)
		}
		return record.RunID, nil
	}
	if runID == "" {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		runID = "r" + hex.EncodeToString(random[:])
	}
	if !partPattern.MatchString(runID) {
		return "", fmt.Errorf("%w: run ID %q", ErrConfig, runID)
	}
	data, err := json.Marshal(runRecord{StoreID: storeID, ProducerID: producerID, RunID: runID})
	if err != nil {
		return "", err
	}
	if err := db.Put([]byte(runKey), data); err != nil {
		return "", err
	}
	if err := db.SyncKeyValue(); err != nil {
		return "", err
	}
	return runID, nil
}

// Pins adapts the native pin ledger to admission.Pins.
type Pins struct {
	DB       ethdb.Database
	Provider *stateprovider.Provider
	Identity stateprovider.StoreIdentity
}

// Verify checks that the durable pin id names this store, holds parentRoot
// and is readable, and returns the SHA-256 of its ledger record.
func (p Pins) Verify(id, parentRoot string) (string, error) {
	key := []byte(pinPrefix + id)
	ok, err := p.DB.Has(key)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: %s", stateprovider.ErrUnknownPin, id)
	}
	data, err := p.DB.Get(key)
	if err != nil {
		return "", err
	}
	var pin stateprovider.Pin
	if err := strictDecode(data, &pin); err != nil || pin.ID != id {
		return "", fmt.Errorf("%w: malformed ledger record %s", stateprovider.ErrInvalidPin, id)
	}
	if pin.Store != p.Identity {
		return "", fmt.Errorf("%w: pin %s names another store", stateprovider.ErrWrongStore, id)
	}
	if hex.EncodeToString(pin.ParentRoot[:]) != parentRoot {
		return "", fmt.Errorf("%w: pin %s holds root %s", stateprovider.ErrInvalidPin, id, pin.ParentRoot)
	}
	if err := stateprovider.Verify(p.DB, pin); err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// Release durably removes the pin. An absent pin is already released.
func (p Pins) Release(id string) error {
	if err := p.Provider.Release(id); err != nil && !errors.Is(err, stateprovider.ErrUnknownPin) {
		return err
	}
	return nil
}

// Inputs adapts the static capsule and the dependency log to
// admission.Inputs.
type Inputs struct {
	Store sourceinputs.Store
}

// CapsuleDigest returns the digest of the sealed and checked capsule.
func (i Inputs) CapsuleDigest(observation string) (string, error) {
	capsule, err := sourceinputs.ReadCapsule(i.Store, observation)
	if err != nil {
		return "", err
	}
	return capsule.Digest, nil
}

// Consumed opens the log, which checks every durable entry and the closed
// manifest, and returns the closed manifest.
func (i Inputs) Consumed(observation string) (admission.Consumed, error) {
	log, err := sourceinputs.OpenLog(i.Store, observation)
	if err != nil {
		return admission.Consumed{}, err
	}
	closed, ok := log.Closed()
	if !ok {
		return admission.Consumed{}, fmt.Errorf("%w: dependency log %s is not closed", admission.ErrBinding, observation)
	}
	return admission.Consumed{Observation: closed.Observation, CapsuleDigest: closed.CapsuleDigest,
		Count: closed.Count, Chain: closed.Chain, Digest: closed.Digest}, nil
}

// Options are optional parts of an Admission. The shipped binary passes the
// zero value.
type Options struct {
	// Store is the store of the admission records, the capsule and the log;
	// nil means the native database.
	Store admission.Store
	// RunID is the run ID that the first start records; empty means random.
	RunID string
	// SafePoint is the VM safe point of the owned capture seam for the
	// serial execution of each admitted block. The capture side
	// (E04-04.import_pressure) installs a core/capture-backed function.
	SafePoint func(vm.OpCode, *vm.ScopeContext)
	// ExecDigest is the digest of the immutable record corpus of a block,
	// which the capture side computes (E04-01.block_record). Nil means
	// ExecResultDigest, the digest of the native execution result.
	ExecDigest func(block *types.Block, res *core.ProcessResult) (string, error)
	// Trace receives one event per admission step and BLOCKHASH answer, for
	// probes; nil in the shipped binary.
	Trace func(event string, fields map[string]any)
	// SkipRecovery leaves the admission in RECOVERY_ONLY after Attach, for
	// probes that check the refusals there; the shipped binary never sets it.
	SkipRecovery bool
}

// Admission is the Geth admission of one node. It implements
// core.ExtractorAdmission.
type Admission struct {
	chain    *core.BlockChain
	db       ethdb.Database
	store    admission.Store
	cfg      Config
	opts     Options
	identity stateprovider.StoreIdentity
	producer admission.Producer
	guard    *admission.Guard
	core     *admission.Admission
	provider *stateprovider.Provider
	pins     Pins
	token    *admission.Token
}

// Attach binds the store, opens the saved producer run, installs the
// admission into the chain, runs the recovery driver and switches to LIVE.
// The chain must not have imported anything since it was opened.
func Attach(chain *core.BlockChain, db ethdb.Database, cfg Config, opts Options) (*Admission, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	identity, err := stateprovider.Bind(db, cfg.StoreID)
	if err != nil {
		return nil, err
	}
	if identity != cfg.identity() {
		return nil, fmt.Errorf("%w: store is chain %s genesis %s", stateprovider.ErrWrongStore,
			identity.ChainID, identity.GenesisHash)
	}
	provider, err := stateprovider.New(db, chain.TrieDB(), identity)
	if err != nil {
		return nil, err
	}
	run, err := OpenRun(db, cfg.StoreID, cfg.ProducerID, opts.RunID)
	if err != nil {
		return nil, err
	}
	store := opts.Store
	if store == nil {
		store = db
	}
	a := &Admission{chain: chain, db: db, store: store, cfg: cfg, opts: opts, identity: identity,
		producer: admission.Producer{ProducerID: cfg.ProducerID, RunID: run}, guard: admission.NewGuard(),
		provider: provider, pins: Pins{DB: db, Provider: provider, Identity: identity}}
	token := a.guard.Acquire()
	a.core, err = admission.Open(token, store, a.producer, a.pins, Inputs{Store: store})
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return nil, err
	}
	chain.SetExtractorAdmission(a)
	a.trace("opened", map[string]any{"producer": cfg.ProducerID, "run": run})
	if !opts.SkipRecovery {
		if err := a.Recover(); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// Producer returns the producer run that this admission owns.
func (a *Admission) Producer() admission.Producer { return a.producer }

// Core returns the admission core and a held token for inspection; the
// caller must release the token.
func (a *Admission) Core() (*admission.Admission, *admission.Token) { return a.core, a.guard.Acquire() }

func (a *Admission) trace(event string, fields map[string]any) {
	if a.opts.Trace != nil {
		a.opts.Trace(event, fields)
	}
}

// Recover is the recovery driver of AC-CAP-02-02. In RECOVERY_ONLY it
// abandons every INPUT_PREPARING observation, which is not a capture, and
// re-executes every ADMITTED obligation through the patched import path
// (InsertBlockWithoutSetHead, which writes the block but does not move the
// head) from its saved capsule and pinned parent root; the import writes
// EXEC_READY or NATIVE_REJECTED. Only then it switches to LIVE. An
// obligation that stays ADMITTED stops startup.
func (a *Admission) Recover() error {
	token := a.guard.Acquire()
	observations, err := a.core.Observations(token)
	var admitted []admission.Observation
	for _, obs := range observations {
		if err != nil {
			break
		}
		switch obs.State {
		case admission.InputPreparing:
			err = a.core.Abandon(token, obs.Key)
			a.trace("abandoned", map[string]any{"observation": obs.Key})
		case admission.Admitted:
			admitted = append(admitted, obs)
		}
	}
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		return err
	}
	for _, obs := range admitted {
		block, err := a.savedBlock(obs.Key)
		if err != nil {
			return err
		}
		a.trace("recovering", map[string]any{"observation": obs.Key, "number": block.NumberU64()})
		// Re-execute without moving the head: the original import may not
		// have set it (engine newPayload), and the next import or the
		// consensus client decides the head as natively.
		_, importErr := a.chain.InsertBlockWithoutSetHead(context.Background(), block, false)
		state, err := a.state(obs.Key)
		if err != nil {
			return err
		}
		if state == admission.Admitted || state == admission.InputPreparing {
			return fmt.Errorf("%w: %s after import error %v", ErrRecovery, obs.Key, importErr)
		}
		a.trace("recovered", map[string]any{"observation": obs.Key, "state": string(state),
			"import_error": errorText(importErr)})
	}
	token = a.guard.Acquire()
	err = a.core.GoLive(token)
	if releaseErr := token.Release(); err == nil {
		err = releaseErr
	}
	if err == nil {
		a.trace("live", nil)
	}
	return err
}

// state returns the state of an observation; a closed one is no longer
// listed and reports its terminal state from the store.
func (a *Admission) state(key string) (admission.State, error) {
	token := a.guard.Acquire()
	defer token.Release()
	observations, err := a.core.Observations(token)
	if err != nil {
		return "", err
	}
	for _, obs := range observations {
		if obs.Key == key {
			return obs.State, nil
		}
	}
	return admission.Rejected, a.closedAs(key, "rejected")
}

// closedAs checks that the observation key has the given terminal record.
func (a *Admission) closedAs(key, kind string) error {
	sequence := key[strings.LastIndexByte(key, '.')+1:]
	ok, err := a.store.Has([]byte("extractor-admission-v1/" + a.producer.ProducerID + "." + a.producer.RunID + "/" + sequence + "/" + kind))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s is closed without a %s record", ErrRecovery, key, kind)
	}
	return nil
}

// Lock takes the observation guard for one import call.
func (a *Admission) Lock() func() {
	token := a.guard.Acquire()
	a.token = token
	return func() {
		a.token = nil
		_ = token.Release()
	}
}

// Begin starts or resumes the observation of block before processor.Process.
func (a *Admission) Begin(block *types.Block, parentRoot common.Hash) (core.ExtractorObservation, error) {
	token := a.token
	if token == nil {
		return nil, ErrGuardMissing
	}
	candidate := admission.Candidate{Number: block.NumberU64(), Hash: hexHash(block.Hash()),
		ParentHash: hexHash(block.ParentHash()), ParentRoot: hexHash(parentRoot)}
	mode, err := a.core.Mode(token)
	if err != nil {
		return nil, err
	}
	if mode == admission.RecoveryOnly {
		return a.resume(token, block, candidate)
	}
	obs, err := a.core.Begin(token, candidate)
	if err != nil {
		return nil, err
	}
	a.trace("begin", map[string]any{"observation": obs.Key, "number": candidate.Number})
	items, err := a.prepare(obs.Key, block, parentRoot)
	if err != nil {
		// Nothing was admitted: clean the INPUT_PREPARING observation and its
		// pin. If the cleanup fails, the admission stops (fail closed).
		if abandonErr := a.core.Abandon(token, obs.Key); abandonErr != nil {
			return nil, errors.Join(err, abandonErr)
		}
		a.trace("abandoned", map[string]any{"observation": obs.Key})
		return nil, err
	}
	record, err := a.core.Admit(token, obs.Key)
	if err != nil {
		if errors.Is(err, admission.ErrBinding) {
			if abandonErr := a.core.Abandon(token, obs.Key); abandonErr != nil {
				return nil, errors.Join(err, abandonErr)
			}
		}
		return nil, err
	}
	a.trace("admitted", map[string]any{"observation": obs.Key, "digest": record.Digest})
	return a.observe(token, obs.Key, block, items)
}

// prepare pins the parent root and seals the static capsule; both are
// durable when it returns.
func (a *Admission) prepare(key string, block *types.Block, parentRoot common.Hash) ([]sourceinputs.Item, error) {
	parent := a.chain.GetHeader(block.ParentHash(), block.NumberU64()-1)
	if parent == nil || parent.Root != parentRoot {
		return nil, fmt.Errorf("%w: parent header of block %d", ErrCapsule, block.NumberU64())
	}
	profile := sha256.Sum256([]byte(a.cfg.Profile))
	pin := stateprovider.Pin{ID: key, Store: a.identity, Profile: hex.EncodeToString(profile[:]),
		ParentRoot: parentRoot, Headers: []stateprovider.HeaderRef{{Hash: parent.Hash(), Number: parent.Number.Uint64()}}}
	if err := a.provider.Pin(pin); err != nil {
		return nil, err
	}
	a.trace("pinned", map[string]any{"observation": key, "root": hexHash(parentRoot)})
	items, err := CapsuleItems(a.chain, block, parent, a.cfg.NativeCommit, a.cfg.Profile)
	if err != nil {
		return nil, err
	}
	capsule, err := sourceinputs.SealCapsule(a.store, key, items)
	if err != nil {
		return nil, err
	}
	a.trace("sealed", map[string]any{"observation": key, "digest": capsule.Digest, "items": len(items)})
	return items, nil
}

// resume continues the saved ADMITTED obligation of block in RECOVERY_ONLY.
// Any other block is refused, so no new observation passes an open one.
func (a *Admission) resume(token *admission.Token, block *types.Block, candidate admission.Candidate) (core.ExtractorObservation, error) {
	observations, err := a.core.Observations(token)
	if err != nil {
		return nil, err
	}
	for _, obs := range observations {
		if obs.State != admission.Admitted || obs.Candidate != candidate {
			continue
		}
		items, err := savedItems(a.store, obs.Key)
		if err != nil {
			return nil, err
		}
		// Every static input must be what this node would seal now: the same
		// block, ancestors, chain config, fork ID, native commit and profile.
		// A changed config or binary refuses the resume, so EXEC_READY never
		// binds a capsule that differs from the execution.
		parent := a.chain.GetHeader(block.ParentHash(), block.NumberU64()-1)
		if parent == nil {
			return nil, fmt.Errorf("%w: parent header of block %d", ErrCapsule, block.NumberU64())
		}
		current, err := CapsuleItems(a.chain, block, parent, a.cfg.NativeCommit, a.cfg.Profile)
		if err != nil {
			return nil, err
		}
		if err := sameItems(items, current); err != nil {
			return nil, err
		}
		a.trace("resumed", map[string]any{"observation": obs.Key, "number": candidate.Number})
		return a.observe(token, obs.Key, block, items)
	}
	return nil, fmt.Errorf("%w: block %d %s is not a saved obligation in %s", admission.ErrMode,
		candidate.Number, candidate.Hash, admission.RecoveryOnly)
}

// observe opens the dependency log and returns the observation of one
// ProcessBlock call.
func (a *Admission) observe(token *admission.Token, key string, block *types.Block, items []sourceinputs.Item) (core.ExtractorObservation, error) {
	answers, err := blockHashAnswers(items, block.NumberU64())
	if err != nil {
		return nil, err
	}
	log, err := sourceinputs.OpenLog(a.store, key)
	if err != nil {
		return nil, err
	}
	return &observation{a: a, token: token, key: key, block: block.Hash(), answers: answers, log: log}, nil
}

// savedBlock decodes the block of an observation from its saved capsule.
func (a *Admission) savedBlock(key string) (*types.Block, error) {
	items, err := savedItems(a.store, key)
	if err != nil {
		return nil, err
	}
	byName := map[string][]byte{}
	for _, item := range items {
		byName[item.Name] = item.Data
	}
	header, body := new(types.Header), new(types.Body)
	if err := rlp.DecodeBytes(byName[itemHeader], header); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCapsule, err)
	}
	if err := rlp.DecodeBytes(byName[itemBody], body); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCapsule, err)
	}
	return types.NewBlockWithHeader(header).WithBody(*body), nil
}

// observation is one admitted observation during ProcessBlock.
type observation struct {
	a       *Admission
	token   *admission.Token
	key     string
	block   common.Hash
	answers map[uint64]common.Hash
	log     *sourceinputs.Log
	missing []uint64
	// executing is set when the block's execution is configured; only a
	// failure from there on is a native validation verdict.
	executing bool
	done      bool
}

// Configure installs the saved BLOCKHASH answers and the safe point, and
// keeps the execution serial: the parallel processor reads neither.
func (o *observation) Configure(cfg *vm.Config) {
	cfg.ExtractorGetHash = o.getHash
	cfg.ExtractorSafePoint = o.a.opts.SafePoint
	cfg.DisableParallelExecution = true
	o.executing = true
}

// getHash answers BLOCKHASH from the saved ancestor headers. The EVM asks
// only inside the window, which the capsule covers; a miss is recorded and
// keeps the obligation ADMITTED.
func (o *observation) getHash(number uint64) common.Hash {
	hash, ok := o.answers[number]
	if !ok {
		o.missing = append(o.missing, number)
	}
	o.a.trace("lookup", map[string]any{"observation": o.key, "number": number, "hash": hexHash(hash), "found": ok})
	return hash
}

// ExecReady closes the log and writes EXEC_READY after native validation.
func (o *observation) ExecReady(block *types.Block, res *core.ProcessResult) error {
	o.done = true
	if len(o.missing) != 0 {
		return fmt.Errorf("%w: BLOCKHASH %v is not in the capsule of %s", ErrCapsule, o.missing, o.key)
	}
	if err := o.current(); err != nil {
		return err
	}
	consumed, err := o.log.Finish()
	if err != nil {
		return err
	}
	digest := ExecResultDigest
	if o.a.opts.ExecDigest != nil {
		digest = o.a.opts.ExecDigest
	}
	execDigest, err := digest(block, res)
	if err != nil {
		return err
	}
	record, err := o.a.core.ExecReady(o.token, o.key, execDigest)
	if err != nil {
		return err
	}
	o.a.trace("exec_ready", map[string]any{"observation": o.key, "consumed": consumed.Digest,
		"exec": execDigest, "digest": record.Digest})
	return nil
}

// Close writes NATIVE_REJECTED when native execution or validation failed
// before ExecReady. A failure before the execution was configured (for
// example missing parent state) or a missing BLOCKHASH answer is not a native
// verdict: the obligation stays ADMITTED for recovery.
func (o *observation) Close(cause error) error {
	if cause == nil || o.done {
		return nil
	}
	o.done = true
	if !o.executing {
		return fmt.Errorf("%w: %s stopped before execution: %v", ErrRecovery, o.key, cause)
	}
	if len(o.missing) != 0 {
		return fmt.Errorf("%w: BLOCKHASH %v is not in the capsule of %s", ErrCapsule, o.missing, o.key)
	}
	if err := o.current(); err != nil {
		return err
	}
	evidence := RejectionDigest(o.block, cause)
	record, err := o.a.core.Reject(o.token, o.key, evidence)
	if err != nil {
		return err
	}
	o.a.trace("rejected", map[string]any{"observation": o.key, "evidence": evidence, "digest": record.Digest,
		"cause": cause.Error()})
	return nil
}

// current checks that the import still holds the guard of this observation.
func (o *observation) current() error {
	if o.a.token != o.token {
		return ErrGuardMissing
	}
	return nil
}

// CapsuleItems builds the static capsule of block from the native chain: the
// block encodings, the parent, every other header of the BLOCKHASH window
// (found through parent links, so a side branch gets its own ancestors), the
// chain config and fork ID in the shared canonical JSON form (D99 item 3),
// the native commit and the normalization profile.
func CapsuleItems(chain *core.BlockChain, block *types.Block, parent *types.Header, commit, profile string) ([]sourceinputs.Item, error) {
	header, err := rlp.EncodeToBytes(block.Header())
	if err != nil {
		return nil, err
	}
	body, err := rlp.EncodeToBytes(block.Body())
	if err != nil {
		return nil, err
	}
	parentBytes, err := rlp.EncodeToBytes(parent)
	if err != nil {
		return nil, err
	}
	enc, err := admission.CanonicalJSON([]byte(encodings))
	if err != nil {
		return nil, err
	}
	config, err := json.Marshal(chain.Config())
	if err != nil {
		return nil, err
	}
	if config, err = admission.CanonicalJSON(config); err != nil {
		return nil, err
	}
	id := forkid.NewID(chain.Config(), chain.Genesis(), block.NumberU64(), block.Time())
	fork, err := json.Marshal(map[string]string{"hash": hex.EncodeToString(id.Hash[:]), "next": fmt.Sprint(id.Next)})
	if err != nil {
		return nil, err
	}
	if fork, err = admission.CanonicalJSON(fork); err != nil {
		return nil, err
	}
	// Walk the window down from the parent, then store it in ascending order.
	var ancestors []sourceinputs.Item
	current := parent
	for number := parent.Number.Uint64(); number > windowStart(block.NumberU64()); number-- {
		current = chain.GetHeader(current.ParentHash, number-1)
		if current == nil {
			return nil, fmt.Errorf("%w: ancestor header %d is missing", ErrCapsule, number-1)
		}
		data, err := rlp.EncodeToBytes(current)
		if err != nil {
			return nil, err
		}
		ancestors = append(ancestors, sourceinputs.Item{Name: fmt.Sprintf("%s%d", ancestorStem, number-1), Data: data})
	}
	items := []sourceinputs.Item{
		{Name: itemHeader, Data: header},
		{Name: itemBody, Data: body},
		{Name: itemEncodings, Data: enc},
		{Name: itemParent, Data: parentBytes},
		{Name: itemParentRef, Data: parent.Hash().Bytes()},
		{Name: itemRoot, Data: parent.Root.Bytes()},
	}
	for index := len(ancestors) - 1; index >= 0; index-- {
		items = append(items, ancestors[index])
	}
	return append(items,
		sourceinputs.Item{Name: itemConfig, Data: config},
		sourceinputs.Item{Name: itemForkID, Data: fork},
		sourceinputs.Item{Name: itemCommit, Data: []byte(commit)},
		sourceinputs.Item{Name: itemProfile, Data: []byte(profile)},
	), nil
}

// windowStart is the lowest block number that BLOCKHASH of block number can
// read.
func windowStart(number uint64) uint64 {
	if number <= windowSize {
		return 0
	}
	return number - windowSize
}

// savedItems reads every item of a sealed capsule.
func savedItems(store sourceinputs.Store, key string) ([]sourceinputs.Item, error) {
	capsule, err := sourceinputs.ReadCapsule(store, key)
	if err != nil {
		return nil, err
	}
	items := make([]sourceinputs.Item, 0, len(capsule.Items))
	for index, ref := range capsule.Items {
		data, err := sourceinputs.ReadItem(store, key, index)
		if err != nil {
			return nil, err
		}
		items = append(items, sourceinputs.Item{Name: ref.Name, Data: data})
	}
	return items, nil
}

// sameItems checks that the saved capsule items equal the items this node
// builds now, name for name and byte for byte.
func sameItems(saved, current []sourceinputs.Item) error {
	if len(saved) != len(current) {
		return fmt.Errorf("%w: %d saved items, %d now", ErrCapsule, len(saved), len(current))
	}
	for index := range saved {
		if saved[index].Name != current[index].Name || !bytes.Equal(saved[index].Data, current[index].Data) {
			return fmt.Errorf("%w: item %s differs", ErrCapsule, saved[index].Name)
		}
	}
	return nil
}

// blockHashAnswers maps every block number of the BLOCKHASH window of block
// number to the Keccak-256 hash of its saved header. The capsule must hold
// exactly the parent and the window ancestors, each the parent of the next.
func blockHashAnswers(items []sourceinputs.Item, number uint64) (map[uint64]common.Hash, error) {
	var headers []sourceinputs.Item
	for _, item := range items {
		if strings.HasPrefix(item.Name, ancestorStem) {
			headers = append(headers, item)
		}
	}
	for _, item := range items {
		if item.Name == itemParent {
			headers = append(headers, item)
		}
	}
	if number == 0 || uint64(len(headers)) != number-windowStart(number) {
		return nil, fmt.Errorf("%w: %d window headers for block %d", ErrCapsule, len(headers), number)
	}
	answers := map[uint64]common.Hash{}
	var previous common.Hash
	for index, item := range headers {
		header := new(types.Header)
		if err := rlp.DecodeBytes(item.Data, header); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrCapsule, item.Name, err)
		}
		want := windowStart(number) + uint64(index)
		name := fmt.Sprintf("%s%d", ancestorStem, want)
		if want == number-1 {
			name = itemParent
		}
		if header.Number.Uint64() != want || item.Name != name || (index > 0 && header.ParentHash != previous) {
			return nil, fmt.Errorf("%w: %s is not the header of block %d", ErrCapsule, item.Name, want)
		}
		previous = crypto.Keccak256Hash(item.Data)
		answers[want] = previous
	}
	return answers, nil
}

// ExecResultDigest is the digest of the native execution result of a
// validated block: the block hash, the gas used and the consensus encoding
// of every receipt. It stands for the record corpus digest until the
// capture side computes one (Options.ExecDigest).
func ExecResultDigest(block *types.Block, res *core.ProcessResult) (string, error) {
	receipts, err := rlp.EncodeToBytes(res.Receipts)
	if err != nil {
		return "", err
	}
	data := append([]byte(tagExecResult), 0)
	data = append(data, block.Hash().Bytes()...)
	data = binary.BigEndian.AppendUint64(data, res.GasUsed)
	data = binary.BigEndian.AppendUint64(data, uint64(len(receipts)))
	digest := sha256.Sum256(append(data, receipts...))
	return hex.EncodeToString(digest[:]), nil
}

// RejectionDigest is the digest of the native rejection evidence: the block
// hash and the native error text.
func RejectionDigest(block common.Hash, cause error) string {
	text := cause.Error()
	data := append([]byte(tagRejection), 0)
	data = append(data, block.Bytes()...)
	data = binary.BigEndian.AppendUint64(data, uint64(len(text)))
	digest := sha256.Sum256(append(data, text...))
	return hex.EncodeToString(digest[:])
}

func hexHash(hash common.Hash) string { return hex.EncodeToString(hash[:]) }

func errorText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}
