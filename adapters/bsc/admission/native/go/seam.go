package bsc

// The BSC import seam: core.ExtractorHook over the admission core.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/extractor/admission"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
)

var (
	// ErrUnresolved reports an admitted observation that the import left
	// ADMITTED: it neither reached EXEC_READY nor NATIVE_REJECTED. The
	// obligation stays, keeps its pin and blocks new admissions until a
	// retry or the recovery driver resolves it.
	ErrUnresolved = errors.New("admitted observation is unresolved")
	// ErrImportProfile refuses a block outside the supported import profile:
	// sequential import on top of the current head, with the parent state.
	ErrImportProfile = errors.New("block outside the sequential import profile")
	// ErrRunIdentity refuses a saved producer run that differs from the
	// configured one or names another store.
	ErrRunIdentity = errors.New("saved producer run differs")
	// ErrEngine reports a consensus engine without the extractor patch.
	ErrEngine = errors.New("consensus engine is not the patched Parlia")
	// ErrGuard reports a seam call without the observation guard.
	ErrGuard = errors.New("observation guard is not held")
)

// RunRecordKey holds the saved producer run of the native store. The key is
// not 32 bytes long and starts with "e", so the native offline pruner never
// deletes it.
const RunRecordKey = "extractor-bsc-admission-run-v1"

// RunRecord is the saved producer run.
type RunRecord struct {
	ProducerID string `json:"producer_id"`
	RunID      string `json:"run_id"`
	StoreID    string `json:"store_id"`
}

// Config configures a Seam.
type Config struct {
	// Producer is the run to start when the store has no saved run. When the
	// store has one, Producer is either empty or equal to it.
	Producer admission.Producer
	// Identity is the native store identity of E01-02.pins.
	Identity stateprovider.StoreIdentity
	// GenesisFile is the genesis file of the home; its canonical JSON is the
	// genesis-json item.
	GenesisFile []byte
	// NativeCommit is the BSC source commit of the build.
	NativeCommit string
	// Store, when set, holds the admission and source inputs records instead
	// of the native database (the probe passes a power-loss model over it).
	Store sourceinputs.Store
	// Emit receives the seam's trace events; it must not call the Seam.
	Emit func(Event)
	// SafePoint, when set, receives the VM safe points of the import tracer
	// inside an admitted observation: the owned-capture seam waits there.
	SafePoint func(vm.OpCode, *vm.ScopeContext)
}

// Seam admits one observation per imported block. Create it with Start.
type Seam struct {
	guard     *admission.Guard
	token     *admission.Token
	db        ethdb.Database
	store     sourceinputs.Store
	provider  *stateprovider.Provider
	pins      LedgerPins
	adm       *admission.Admission
	parlia    *ParliaDB
	genesis   *Genesis
	commit    string
	identity  stateprovider.StoreIdentity
	producer  admission.Producer
	emit      func(Event)
	tracer    *tracing.Hooks
	safePoint func(vm.OpCode, *vm.ScopeContext)

	active  atomic.Pointer[observation]
	outside atomic.Int64
}

// Start opens the seam on a native chain whose engine reads through parlia:
// it binds or checks the store identity, opens the saved producer run (or
// saves the configured one), opens the admission in RECOVERY_ONLY and
// installs the VM safe point. The caller then installs the seam on the chain
// (SetExtractorHook), runs Recover and only then imports new blocks.
func Start(guard *admission.Guard, db ethdb.Database, chain *core.BlockChain, parlia *ParliaDB, cfg Config) (*Seam, error) {
	if _, ok := chain.Engine().(interface{ ExtractorResetSnapshots() }); !ok {
		return nil, ErrEngine
	}
	if parlia == nil || parlia.Database != db {
		return nil, fmt.Errorf("%w: the Parlia handle does not wrap the native database", ErrWiring)
	}
	if _, bound, err := stateprovider.BoundIdentity(db); err != nil {
		return nil, err
	} else if !bound {
		if _, err := stateprovider.Bind(db, cfg.Identity.StoreID); err != nil {
			return nil, err
		}
	}
	provider, err := stateprovider.New(db, chain.TrieDB(), cfg.Identity)
	if err != nil {
		return nil, err
	}
	genesis, err := LoadGenesis(cfg.GenesisFile, chain.Genesis())
	if err != nil {
		return nil, err
	}
	store := cfg.Store
	if store == nil {
		store = db
	}
	producer, err := openRun(store, cfg)
	if err != nil {
		return nil, err
	}
	emit := cfg.Emit
	if emit == nil {
		emit = func(Event) {}
	}
	s := &Seam{guard: guard, db: db, store: store, provider: provider, pins: LedgerPins{DB: db, Provider: provider},
		parlia: parlia, genesis: genesis, commit: cfg.NativeCommit, identity: cfg.Identity, producer: producer,
		emit: emit, tracer: chain.GetVMConfig().Tracer, safePoint: cfg.SafePoint}
	token := guard.Acquire()
	defer token.Release()
	if s.adm, err = admission.Open(token, store, producer, s.pins, Inputs{DB: store}); err != nil {
		return nil, err
	}
	vm.SetExtractorSafePoint(s.onSafePoint)
	emit(Event{"event": "start", "producer_id": producer.ProducerID, "run_id": producer.RunID})
	return s, nil
}

// openRun returns the saved producer run, or saves the configured one.
func openRun(store sourceinputs.Store, cfg Config) (admission.Producer, error) {
	data, err := store.Get([]byte(RunRecordKey))
	if err == nil {
		var saved RunRecord
		if err := json.Unmarshal(data, &saved); err != nil {
			return admission.Producer{}, fmt.Errorf("%w: run record: %v", ErrRunIdentity, err)
		}
		if saved.StoreID != cfg.Identity.StoreID {
			return admission.Producer{}, fmt.Errorf("%w: run record names store %s", ErrRunIdentity, saved.StoreID)
		}
		run := admission.Producer{ProducerID: saved.ProducerID, RunID: saved.RunID}
		if cfg.Producer != (admission.Producer{}) && cfg.Producer != run {
			return admission.Producer{}, fmt.Errorf("%w: saved %s.%s", ErrRunIdentity, run.ProducerID, run.RunID)
		}
		return run, nil
	}
	if present, hasErr := store.Has([]byte(RunRecordKey)); hasErr != nil || present {
		return admission.Producer{}, fmt.Errorf("run record read: %v", err)
	}
	if cfg.Producer == (admission.Producer{}) {
		return admission.Producer{}, fmt.Errorf("%w: no saved run and none configured", ErrRunIdentity)
	}
	record, err := json.Marshal(RunRecord{ProducerID: cfg.Producer.ProducerID, RunID: cfg.Producer.RunID,
		StoreID: cfg.Identity.StoreID})
	if err != nil {
		return admission.Producer{}, err
	}
	if err := store.Put([]byte(RunRecordKey), record); err != nil {
		return admission.Producer{}, err
	}
	return cfg.Producer, store.SyncKeyValue()
}

// Producer returns the producer run that the seam opened.
func (s *Seam) Producer() admission.Producer { return s.producer }

// Acquire implements core.ExtractorHook.
func (s *Seam) Acquire() func() {
	token := s.guard.Acquire()
	s.token = token
	return func() {
		s.token = nil
		token.Release()
	}
}

// Observations lists the non-terminal observations; the caller must not hold
// the guard.
func (s *Seam) Observations() ([]admission.Observation, error) {
	release := s.Acquire()
	defer release()
	return s.adm.Observations(s.token)
}

// SafePointsOutside returns how many safe points of the import tracer ran
// outside an admitted observation.
func (s *Seam) SafePointsOutside() int64 { return s.outside.Load() }

func (s *Seam) onSafePoint(tracer *tracing.Hooks, op vm.OpCode, scope *vm.ScopeContext) {
	if s.tracer == nil || tracer != s.tracer {
		return // another execution (an RPC tracer), not the import
	}
	o := s.active.Load()
	if o == nil {
		s.outside.Add(1)
		return
	}
	o.safePoints.Add(1)
	if s.safePoint != nil {
		s.safePoint(op, scope)
	}
}

// Candidate returns the admission candidate of block on top of parent.
func Candidate(block *types.Block, parent *types.Header) admission.Candidate {
	return admission.Candidate{Number: block.NumberU64(), Hash: hexHash(block.Hash()),
		ParentHash: hexHash(block.ParentHash()), ParentRoot: hexHash(parent.Root)}
}

// Begin implements core.ExtractorHook. It runs with the guard and the chain
// mutex held.
func (s *Seam) Begin(chain *core.BlockChain, block *types.Block) (core.ExtractorObservation, error) {
	if s.token == nil {
		return nil, ErrGuard
	}
	n := block.NumberU64()
	if n == 0 {
		return nil, fmt.Errorf("%w: genesis", ErrImportProfile)
	}
	if chain.HasBlockAndState(block.Hash(), n) {
		return nil, nil
	}
	parent := chain.GetHeader(block.ParentHash(), n-1)
	if parent == nil || !chain.HasState(parent.Root) {
		return nil, fmt.Errorf("%w: block %d has no parent with state", ErrImportProfile, n)
	}
	if head := chain.CurrentBlock(); head == nil || head.Hash() != parent.Hash() {
		return nil, fmt.Errorf("%w: block %d is not on top of the head", ErrImportProfile, n)
	}
	candidate := Candidate(block, parent)
	observations, err := s.adm.Observations(s.token)
	if err != nil {
		return nil, err
	}
	// An INPUT_PREPARING observation is left only by a restart (Begin
	// abandons its own on error); admission.Begin refuses it and Recover
	// abandons it.
	for _, obs := range observations {
		if obs.State != admission.Admitted {
			continue
		}
		if obs.Candidate != candidate {
			return nil, fmt.Errorf("%w: %s (block %d) blocks block %d", ErrUnresolved, obs.Key, obs.Candidate.Number, n)
		}
		s.emit(Event{"event": "resume", "observation": obs.Key, "number": n})
		return s.resume(chain, obs.Key, block)
	}
	obs, err := s.adm.Begin(s.token, candidate)
	if err != nil {
		return nil, err
	}
	key := obs.Key
	s.emit(Event{"event": "begin", "observation": key, "number": n})
	if err := s.prepare(chain, key, block, parent); err != nil {
		if abandonErr := s.adm.Abandon(s.token, key); abandonErr != nil {
			return nil, errors.Join(err, abandonErr)
		}
		s.emit(Event{"event": "abandon", "observation": key})
		return nil, err
	}
	if _, err := s.adm.Admit(s.token, key); err != nil {
		return nil, err
	}
	s.emit(Event{"event": "admitted", "observation": key, "number": n})
	return s.resume(chain, key, block)
}

// prepare pins the parent and seals the static capsule (INPUT_PREPARING).
func (s *Seam) prepare(chain *core.BlockChain, key string, block *types.Block, parent *types.Header) error {
	items, err := BlockItems(block, func(number uint64) *types.Header { return chain.GetHeaderByNumber(number) },
		s.genesis, s.commit)
	if err != nil {
		return err
	}
	static, err := DecodeStatic(itemMap(items), s.genesis, s.commit)
	if err != nil {
		return err
	}
	refs := []stateprovider.HeaderRef{{Hash: parent.Hash(), Number: parent.Number.Uint64()}}
	for _, header := range static.Ancestors {
		refs = append(refs, stateprovider.HeaderRef{Hash: header.Hash(), Number: header.Number.Uint64()})
	}
	pin := stateprovider.Pin{ID: key, Store: s.identity, Profile: ProfileDigest(), ParentRoot: parent.Root,
		Headers: refs, Codes: []common.Hash{}}
	if err := s.provider.Pin(pin); err != nil {
		return err
	}
	s.emit(Event{"event": "pinned", "observation": key})
	if _, err := sourceinputs.SealCapsule(s.store, key, items); err != nil {
		return err
	}
	s.emit(Event{"event": "sealed", "observation": key})
	return nil
}

// ProfileDigest is the profile field of every pin: the SHA-256 of the
// state-profile item, as 64 lowercase hex digits.
func ProfileDigest() string {
	digest := sha256.Sum256([]byte(StateProfile))
	return hex.EncodeToString(digest[:])
}

func itemMap(items []sourceinputs.Item) map[string][]byte {
	out := make(map[string][]byte, len(items))
	for _, item := range items {
		out[item.Name] = item.Data
	}
	return out
}

// resume attaches an admitted observation of block to the Parlia handle.
// The candidate, which the caller matched, holds the block hash, and Admit
// bound the capsule digest, so the capsule holds this block.
func (s *Seam) resume(chain *core.BlockChain, key string, block *types.Block) (core.ExtractorObservation, error) {
	static, err := ReadStatic(s.store, key, s.genesis, s.commit)
	if err != nil {
		return nil, err
	}
	keys, err := static.Snapshot.KeyBytes()
	if err != nil {
		return nil, err
	}
	log, err := sourceinputs.OpenLog(s.store, key)
	if err != nil {
		return nil, err
	}
	if err := s.parlia.Attach(key, log, keys); err != nil {
		return nil, err
	}
	chain.Engine().(interface{ ExtractorResetSnapshots() }).ExtractorResetSnapshots()
	o := &observation{seam: s, key: key, hash: block.Hash(), log: log}
	s.active.Store(o)
	s.emit(Event{"event": "execute", "observation": key, "number": block.NumberU64()})
	return o, nil
}

// observation is one admitted block import.
type observation struct {
	seam       *Seam
	key        string
	hash       [32]byte
	log        *sourceinputs.Log
	detached   bool
	resolved   admission.State
	err        error
	safePoints atomic.Int64
}

// Hash implements core.ExtractorObservation.
func (o *observation) Hash() [32]byte { return o.hash }

func (o *observation) detach() error {
	if o.detached {
		return nil
	}
	o.detached = true
	o.seam.active.CompareAndSwap(o, nil)
	return o.seam.parlia.Detach()
}

// Validated implements core.ExtractorObservation.
func (o *observation) Validated(block *types.Block, _ *state.StateDB, res *core.ProcessResult) error {
	s := o.seam
	if block.Hash() != o.hash || o.resolved != "" || o.err != nil {
		return o.fail(fmt.Errorf("%w: unexpected validation of block %x", ErrWiring, block.Hash()))
	}
	if err := o.detach(); err != nil {
		return o.fail(err)
	}
	consumed, err := o.log.Finish()
	if err != nil {
		return o.fail(err)
	}
	digest, err := ExecDigest(block, res)
	if err != nil {
		return o.fail(err)
	}
	record, err := s.adm.ExecReady(s.token, o.key, digest)
	if err != nil {
		return o.fail(err)
	}
	o.resolved = admission.ExecReady
	s.emit(Event{"event": "exec-ready", "observation": o.key, "consumed_digest": consumed.Digest,
		"exec_digest": digest, "digest": record.Digest, "safe_points": o.safePoints.Load()})
	return nil
}

// Rejected implements core.ExtractorObservation. A rejection that may come
// from a wiring error (a stopped Parlia handle makes Parlia regenerate its
// snapshot) is not authoritative, so the observation stays ADMITTED.
func (o *observation) Rejected(block *types.Block, _ *core.ProcessResult, cause error) {
	s := o.seam
	if o.resolved != "" || o.err != nil {
		return
	}
	if err := o.detach(); err != nil {
		o.fail(err)
		return
	}
	evidence := RejectionEvidence(block, cause)
	record, err := s.adm.Reject(s.token, o.key, evidence)
	if err != nil {
		o.fail(err)
		return
	}
	o.resolved = admission.Rejected
	s.emit(Event{"event": "rejected", "observation": o.key, "evidence_digest": evidence, "digest": record.Digest,
		"cause": cause.Error()})
}

// End implements core.ExtractorObservation.
func (o *observation) End(insertErr error) error {
	if err := o.detach(); err != nil && o.err == nil && o.resolved == "" {
		o.err = err
	}
	if o.err != nil {
		return o.err
	}
	if o.resolved == "" {
		o.seam.emit(Event{"event": "unresolved", "observation": o.key})
		return fmt.Errorf("%w: %s: %v", ErrUnresolved, o.key, insertErr)
	}
	return nil
}

func (o *observation) fail(err error) error {
	if o.err == nil {
		o.err = err
	}
	o.seam.emit(Event{"event": "observation-error", "observation": o.key, "error": err.Error()})
	return err
}

func u64(value uint64) []byte { return binary.BigEndian.AppendUint64(nil, value) }

func str(data []byte) []byte { return append(u64(uint64(len(data))), data...) }

// ExecDigest is the digest that EXEC_READY binds as exec_digest: SHA-256 of
// the tag "extractor-bsc-exec-result-v1", a zero byte, the block hash, state
// root and receipt root, the receipt count and each receipt's consensus
// encoding (MarshalBinary) as str. It stands for the native result until the
// capture side (E04) supplies the digest of the record corpus.
func ExecDigest(block *types.Block, res *core.ProcessResult) (string, error) {
	data := append([]byte("extractor-bsc-exec-result-v1"), 0)
	data = append(data, block.Hash().Bytes()...)
	data = append(data, block.Root().Bytes()...)
	data = append(data, block.ReceiptHash().Bytes()...)
	var receipts types.Receipts
	if res != nil {
		receipts = res.Receipts
	}
	data = append(data, u64(uint64(len(receipts)))...)
	for _, receipt := range receipts {
		encoded, err := receipt.MarshalBinary()
		if err != nil {
			return "", err
		}
		data = append(data, str(encoded)...)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// RejectionEvidence is the digest of the native rejection evidence: SHA-256
// of the tag "extractor-bsc-rejection-v1", a zero byte, the block hash and
// the native error text as str.
func RejectionEvidence(block *types.Block, cause error) string {
	data := append([]byte("extractor-bsc-rejection-v1"), 0)
	data = append(data, block.Hash().Bytes()...)
	data = append(data, str([]byte(cause.Error()))...)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// Recover resolves every saved obligation before live import: it abandons
// INPUT_PREPARING observations, re-executes each ADMITTED obligation from its
// pin and capsule through the native import (the seam resumes it and its log
// replays the saved responses), so it reaches EXEC_READY or NATIVE_REJECTED,
// and then switches the admission to LIVE. GoLive refuses while any
// obligation is still open, so an obligation that recovery could not resolve
// keeps the node in RECOVERY_ONLY. The seam must be installed on the chain.
func (s *Seam) Recover(chain *core.BlockChain) error {
	observations, err := s.Observations()
	if err != nil {
		return err
	}
	var failures []error
	for _, obs := range observations {
		switch obs.State {
		case admission.InputPreparing:
			release := s.Acquire()
			err := s.adm.Abandon(s.token, obs.Key)
			release()
			if err != nil {
				return err
			}
			s.emit(Event{"event": "abandon", "observation": obs.Key})
		case admission.Admitted:
			static, err := ReadStatic(s.store, obs.Key, s.genesis, s.commit)
			if err != nil {
				return err
			}
			s.emit(Event{"event": "recover", "observation": obs.Key, "number": static.Block.NumberU64()})
			if _, err := chain.InsertChain(types.Blocks{static.Block}); err != nil {
				// A native rejection closes the obligation and still returns
				// the native error; GoLive below decides.
				failures = append(failures, fmt.Errorf("recovery of %s: %w", obs.Key, err))
			}
		}
	}
	release := s.Acquire()
	defer release()
	if err := s.adm.GoLive(s.token); err != nil {
		return errors.Join(append(failures, err)...)
	}
	s.emit(Event{"event": "live"})
	return nil
}
