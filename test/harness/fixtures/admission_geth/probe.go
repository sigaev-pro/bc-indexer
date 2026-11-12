// This test-only probe runs the E01-02.admission Geth wiring, and with
// Decisions the E01-02.decision_bridge wiring, on the product import path of
// the accepted Geth tree. Preparation copies it to
// cmd/extractor-admission-probe and installs the product patch set and the
// extractor packages (adapters/geth/admission/native). The probe imports
// blocks with core.BlockChain.InsertChain, so every observation goes through
// the patched ProcessBlock: Begin, pin, seal, Admit, processor.Process,
// ValidateState, then EXEC_READY or NATIVE_REJECTED.
//
// Each attempt runs in a child process. The admission records, the capsule
// and the log are written through a power-loss store model: writes stay in
// process memory until SyncKeyValue, so a SIGKILL before a sync loses every
// unsynced extractor write, as a power loss may. Native chain data and the pin
// ledger are written to the native store directly. The probe reports
// observations only; the driver's separately written oracle judges them.
//
// Usage:
//
//	probe run GENESIS WORKDIR OUTPUT NATIVE_COMMIT GETH
//	probe attempt SPEC   (started by run in a child process)
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/extractor/admission"
	extractorgeth "github.com/ethereum/go-ethereum/extractor/admission/native"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/sigaev-pro/bc-indexer/core/capture"
)

const (
	// target is the first block whose BLOCKHASH window holds 256 ancestors
	// plus a margin; block target+1 is the new H after recovery.
	target     = 300
	storeID    = "0123456789abcdef0123456789abcdef"
	producerID = "geth-probe"
	runID      = "r1"
	profile    = "test-only-probe-v1"
	// sideFork is the canonical parent of the synthetic side-branch header
	// of the freezer scenario; finalNumber is the finalized marker there.
	sideFork    = 100
	finalNumber = 250
)

// reader stores BLOCKHASH(NUMBER-k) in slot i for each k in lookbacks, then
// calls the identity precompile with the 32-byte NUMBER word as input.
var (
	reader    = common.HexToAddress("0x00000000000000000000000000000000000000c1")
	lookbacks = []uint16{1, 256, 257, 128}
)

func readerCode() []byte {
	var code []byte
	for slot, back := range lookbacks {
		code = append(code, 0x61, byte(back>>8), byte(back), 0x43, 0x03, 0x40, 0x60, byte(slot), 0x55)
	}
	code = append(code, 0x43, 0x60, 0x00, 0x52) // MSTORE(0, NUMBER)
	// CALL(GAS, 4, 0, 0, 32, 32, 32); POP; STOP
	code = append(code, 0x60, 0x20, 0x60, 0x20, 0x60, 0x20, 0x60, 0x00, 0x60, 0x00, 0x60, 0x04, 0x5a, 0xf1, 0x50)
	return append(code, 0x00)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

// Spec is one child attempt.
type Spec struct {
	Datadir      string `json:"datadir"`
	Genesis      string `json:"genesis"`
	NativeCommit string `json:"native_commit"`
	Events       string `json:"events"`
	Result       string `json:"result"`
	// Imports names the blocks to import in order: "target", "next" (target+1)
	// or "bad" (target with a wrong state root) through InsertChain;
	// "target-without-head" through InsertBlockWithoutSetHead, the engine
	// newPayload path; "canonical-target" through SetCanonical.
	Imports []string `json:"imports"`
	// Profile overrides the normalization profile of the config.
	Profile string `json:"profile"`
	// Witness runs ProcessBlock of block target+1 the way
	// debug_executionWitness does: no guard, no state write.
	Witness bool `json:"witness"`
	// CrashBeforeSync kills the process inside the first sync whose unsynced
	// writes include a key with this suffix, before the writes reach the
	// store. CrashAfterSync kills it right after such a sync is durable.
	CrashBeforeSync string `json:"crash_before_sync"`
	CrashAfterSync  string `json:"crash_after_sync"`
	// Hold opens the admission without the recovery driver and checks the
	// refusals of RECOVERY_ONLY.
	Hold bool `json:"hold"`
	// Rewind tries a native rewind below the pinned headers.
	Rewind bool `json:"rewind"`
	// Freezer runs the side-branch freezer scenario instead of an import.
	Freezer bool `json:"freezer"`
	// Items reports the bytes of every capsule item.
	Items bool `json:"items"`
	// Decisions attaches the decision bridge (AttachDecisions, the shipped
	// configuration) instead of the admission alone.
	Decisions bool `json:"decisions"`
	// MirrorApplied is the answer of the probe mirror: whether it applied
	// every NATIVE_DECIDED decision it is asked about.
	MirrorApplied bool `json:"mirror_applied"`
	// CrashPoint kills the process at this named point of a decided write
	// ("committed", "mutated", "refused").
	CrashPoint string `json:"crash_point"`
	// Refuse names the blocks whose transition the native outcome fixture
	// refuses after the intent.
	Refuse []string `json:"refuse"`
	// CrashSkip lets this many matching crash triggers pass before the crash.
	CrashSkip int `json:"crash_skip"`
}

// chainInputs builds the deterministic genesis and blocks 1..target+1, and
// the bad block: block target with a wrong state root.
func chainInputs(path string) (*core.Genesis, []*types.Block, *types.Block) {
	data, err := os.ReadFile(path)
	check(err)
	genesis := new(core.Genesis)
	check(json.Unmarshal(data, genesis))
	genesis.Alloc[reader] = types.Account{Balance: new(big.Int), Code: readerCode()}
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	check(err)
	signer := types.LatestSigner(genesis.Config)
	sign := func(nonce uint64, to common.Address) *types.Transaction {
		tx, err := types.SignTx(types.NewTransaction(nonce, to, big.NewInt(1), 200_000, big.NewInt(2_000_000_000), nil), signer, key)
		check(err)
		return tx
	}
	coinbase := common.HexToAddress("0x2000000000000000000000000000000000000000")
	db, blocks, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), target-1, func(index int, block *core.BlockGen) {
		block.SetCoinbase(coinbase)
		block.AddTx(sign(uint64(index), common.HexToAddress("0x1000000000000000000000000000000000000000")))
	})
	// The generator answers BLOCKHASH of the reader blocks from a separate
	// in-memory chain, independently of the import under test.
	generator := newChain(rawdb.NewMemoryDatabase(), genesis)
	_, err = generator.InsertChain(blocks)
	check(err)
	for index := 0; index < 2; index++ {
		next, _ := core.GenerateChain(genesis.Config, blocks[len(blocks)-1], ethash.NewFaker(), db, 1, func(_ int, block *core.BlockGen) {
			block.SetCoinbase(coinbase)
			block.AddTxWithChain(generator, sign(uint64(target-1+index), reader))
		})
		_, err = generator.InsertChain(next)
		check(err)
		blocks = append(blocks, next...)
	}
	generator.Stop()
	check(db.Close())
	header := blocks[target-1].Header()
	header.Root = common.Hash{0xba, 0xd0}
	bad := types.NewBlockWithHeader(header).WithBody(*blocks[target-1].Body())
	return genesis, blocks, bad
}

func openStore(datadir string) ethdb.Database {
	chaindata := filepath.Join(datadir, "geth", "chaindata")
	kv, err := pebble.New(chaindata, 16, 16, "", false)
	check(err)
	db, err := rawdb.Open(kv, rawdb.OpenOptions{Ancient: filepath.Join(chaindata, "ancient")})
	check(err)
	return db
}

func newChain(db ethdb.Database, genesis *core.Genesis) *core.BlockChain {
	chain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), core.DefaultConfig())
	check(err)
	return chain
}

// events appends one JSON line per observation. Each line is one write
// system call, so it survives a SIGKILL of this process.
type events struct{ file *os.File }

func (e *events) add(value map[string]any) {
	data, err := json.Marshal(value)
	check(err)
	_, err = e.file.Write(append(data, '\n'))
	check(err)
}

func crash(log *events, point string) {
	log.add(map[string]any{"op": "crash", "point": point})
	check(syscall.Kill(os.Getpid(), syscall.SIGKILL))
	select {}
}

// powerLossStore keeps writes in memory until SyncKeyValue, then writes and
// syncs them. Unsynced writes are lost when the process is killed.
type powerLossStore struct {
	db      ethdb.Database
	log     *events
	pending map[string][]byte
	order   []string
	spec    Spec
	skipped int
}

// trigger reports whether a matching crash trigger crashes now, after
// spec.CrashSkip matches have passed.
func (s *powerLossStore) trigger(match bool) bool {
	if !match {
		return false
	}
	if s.skipped < s.spec.CrashSkip {
		s.skipped++
		return false
	}
	return true
}

func (s *powerLossStore) Has(key []byte) (bool, error) {
	if value, ok := s.pending[string(key)]; ok {
		return value != nil, nil
	}
	return s.db.Has(key)
}

func (s *powerLossStore) Get(key []byte) ([]byte, error) {
	if value, ok := s.pending[string(key)]; ok {
		if value == nil {
			return nil, fmt.Errorf("deleted key %q", key)
		}
		return append([]byte{}, value...), nil
	}
	return s.db.Get(key)
}

func (s *powerLossStore) Put(key, value []byte) error {
	k := string(key)
	if _, ok := s.pending[k]; !ok {
		s.order = append(s.order, k)
	}
	s.pending[k] = append([]byte{}, value...)
	s.log.add(map[string]any{"op": "put", "key": k, "length": len(value)})
	return nil
}

// Delete keeps the deletion in memory until SyncKeyValue, like a write.
func (s *powerLossStore) Delete(key []byte) error {
	k := string(key)
	if _, ok := s.pending[k]; !ok {
		s.order = append(s.order, k)
	}
	s.pending[k] = nil
	s.log.add(map[string]any{"op": "delete", "key": k})
	return nil
}

func (s *powerLossStore) matches(suffix string) bool {
	for _, key := range s.order {
		if suffix != "" && strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

func (s *powerLossStore) SyncKeyValue() error {
	if s.trigger(s.matches(s.spec.CrashBeforeSync)) {
		crash(s.log, "before_sync"+s.spec.CrashBeforeSync)
	}
	after := s.trigger(s.matches(s.spec.CrashAfterSync))
	for _, key := range s.order {
		var err error
		if value := s.pending[key]; value == nil {
			err = s.db.Delete([]byte(key))
		} else {
			err = s.db.Put([]byte(key), value)
		}
		if err != nil {
			return err
		}
	}
	if err := s.db.SyncKeyValue(); err != nil {
		return err
	}
	s.log.add(map[string]any{"op": "sync", "writes": len(s.order)})
	s.pending, s.order = map[string][]byte{}, nil
	if after {
		crash(s.log, "after_sync"+s.spec.CrashAfterSync)
	}
	return nil
}

// safePoint captures the input of every CALL through the owned capture seam
// (core/capture), as the capture side will.
type safePoint struct {
	capturer *capture.Capturer
	inputs   [][]byte
	fields   chan []byte
	done     chan error
}

func newSafePoint() *safePoint {
	c, err := capture.New(capture.Config{ChunkBytes: 64, BufferBytes: 4096})
	check(err)
	s := &safePoint{capturer: c, fields: make(chan []byte, 64), done: make(chan error, 1)}
	go func() {
		var field []byte
		for {
			frame, err := c.Next(context.Background())
			if err != nil {
				close(s.fields)
				s.done <- err
				return
			}
			switch frame.Kind {
			case capture.BeginField:
				field = []byte{}
			case capture.Chunk:
				field = append(field, frame.Data()...)
			case capture.EndField:
				s.fields <- field
			}
			check(c.Release(frame))
		}
	}()
	return s
}

func (s *safePoint) at(op vm.OpCode, scope *vm.ScopeContext) {
	if op != vm.CALL {
		return
	}
	stack := scope.StackData()
	offset, length := stack[len(stack)-4].Uint64(), stack[len(stack)-5].Uint64()
	memory := scope.MemoryData()
	check(s.capturer.CaptureField(context.Background(), 1, length, func(at uint64, dst []byte) error {
		clear(dst)
		if begin := offset + at; begin < uint64(len(memory)) {
			copy(dst, memory[begin:])
		}
		return nil
	}))
}

// finish closes the capture and returns every captured field.
func (s *safePoint) finish() []string {
	check(s.capturer.Close())
	out := []string{}
	for field := range s.fields {
		out = append(out, hex.EncodeToString(field))
	}
	return out
}

// mirror is the probe decision mirror: it answers every query with
// spec.MirrorApplied and logs it.
type mirror struct {
	log     *events
	applied bool
}

func (m mirror) Applied(sequence uint64, digest string) (bool, error) {
	m.log.add(map[string]any{"op": "mirror", "sequence": sequence, "digest": digest, "applied": m.applied})
	return m.applied, nil
}

// prefixed returns every key and value under prefix, values as text.
func prefixed(db ethdb.Database, prefix string) map[string]string {
	out := map[string]string{}
	iterator := db.NewIterator([]byte(prefix), nil)
	defer iterator.Release()
	for iterator.Next() {
		out[string(iterator.Key())] = string(iterator.Value())
	}
	check(iterator.Error())
	return out
}

// capsules reports the sealed capsule of every observation key.
func capsules(db ethdb.Database, keys []string, items bool) map[string]any {
	out := map[string]any{}
	for _, key := range keys {
		capsule, err := sourceinputs.ReadCapsule(db, key)
		if err != nil {
			continue
		}
		row := map[string]any{"capsule": capsule}
		if items {
			data := []map[string]string{}
			for index, ref := range capsule.Items {
				item, err := sourceinputs.ReadItem(db, key, index)
				check(err)
				data = append(data, map[string]string{"name": ref.Name, "data": hex.EncodeToString(item)})
			}
			row["items"] = data
		}
		out[key] = row
	}
	return out
}

func attempt(specPath string) {
	data, err := os.ReadFile(specPath)
	check(err)
	var spec Spec
	check(json.Unmarshal(data, &spec))
	file, err := os.OpenFile(spec.Events, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	check(err)
	log := &events{file: file}
	result := map[string]any{}
	genesis, blocks, bad := chainInputs(spec.Genesis)
	if spec.Freezer {
		freezer(spec, genesis, blocks, result)
		writeResult(spec, result, file)
		return
	}
	cfg := extractorgeth.Config{StoreID: storeID, ProducerID: producerID, ChainID: genesis.Config.ChainID.String(),
		GenesisHash: genesis.ToBlock().Hash(), NativeCommit: spec.NativeCommit, Profile: profile}
	if spec.Profile != "" {
		cfg.Profile = spec.Profile
	}
	check(extractorgeth.Preflight(filepath.Join(spec.Datadir, "geth"), cfg))
	db := openStore(spec.Datadir)
	// Which child roots are on disk before the chain opens: a volatile child
	// root (AC-CAP-07-02) is not.
	roots := map[string]bool{}
	for name, block := range map[string]*types.Block{"target": blocks[target-1], "next": blocks[target]} {
		roots[name] = rawdb.HasLegacyTrieNode(db, block.Root())
	}
	result["roots_at_start"] = roots
	result["records_at_start"] = prefixed(db, "extractor-admission-v1/")
	// The native head at the restart, before NewBlockChain repairs anything.
	result["head_at_start"] = rawdb.ReadHeadBlockHash(db).Hex()
	chain := newChain(db, genesis)
	store := &powerLossStore{db: db, log: log, pending: map[string][]byte{}, spec: spec}
	safe := newSafePoint()
	trace := func(event string, fields map[string]any) {
		row := map[string]any{"op": "trace", "event": event}
		for key, value := range fields {
			row[key] = value
		}
		log.add(row)
	}
	opts := extractorgeth.Options{Store: store, RunID: runID, SafePoint: safe.at, Trace: trace, SkipRecovery: spec.Hold}
	var a *extractorgeth.Admission
	if spec.Decisions {
		refused := map[common.Hash]bool{}
		for _, name := range spec.Refuse {
			refused[map[string]*types.Block{"target": blocks[target-1], "next": blocks[target]}[name].Hash()] = true
		}
		dopts := extractorgeth.DecisionOptions{Mirror: mirror{log: log, applied: spec.MirrorApplied},
			Outcome: func(block *types.Block) error {
				if refused[block.Hash()] {
					return errors.New("native outcome fixture: transition refused")
				}
				return nil
			},
			Point: func(name string) {
				if store.trigger(name == spec.CrashPoint) {
					// A power loss may keep any subset of unsynced native writes;
					// this one keeps every native write so far, including the
					// unsynced intermediate head batch of reorg.
					check(db.SyncKeyValue())
					crash(log, "native/"+name)
				}
			}}
		var d *extractorgeth.Decisions
		if d, err = extractorgeth.AttachDecisions(chain, db, cfg, opts, dopts); d != nil {
			a = d.Admission()
		}
	} else {
		a, err = extractorgeth.Attach(chain, db, cfg, opts)
	}
	if err != nil {
		result["attach_error"] = err.Error()
	} else {
		result["run"] = a.Producer().RunID
	}
	if a != nil && spec.Hold {
		// RECOVERY_ONLY: a new H, a competing block on the same parent, is
		// refused before execution; GoLive is refused.
		_, err := chain.InsertChain(types.Blocks{bad})
		result["new_h_error"] = fmt.Sprint(err)
		c, token := a.Core()
		result["go_live_error"] = fmt.Sprint(c.GoLive(token))
		check(token.Release())
	}
	imports := []map[string]any{}
	for _, name := range spec.Imports {
		block := map[string]*types.Block{"target": blocks[target-1], "next": blocks[target], "bad": bad,
			"target-without-head": blocks[target-1], "canonical-target": blocks[target-1]}[name]
		log.add(map[string]any{"op": "import", "name": name, "number": block.NumberU64()})
		var err error
		switch name {
		case "target-without-head":
			_, err = chain.InsertBlockWithoutSetHead(context.Background(), block, false)
		case "canonical-target":
			_, err = chain.SetCanonical(block)
		default:
			_, err = chain.InsertChain(types.Blocks{block})
		}
		imports = append(imports, map[string]any{"name": name, "hash": block.Hash().Hex(), "error": errorText(err)})
	}
	result["imports"] = imports
	if spec.Witness {
		parent := blocks[target-1]
		_, err := chain.ProcessBlock(context.Background(), parent.Root(), blocks[target], core.ExecuteConfig{MakeWitness: true})
		result["witness_error"] = errorText(err)
	}
	stored := map[string]bool{}
	for name, block := range map[string]*types.Block{"target": blocks[target-1], "next": blocks[target], "bad": bad} {
		stored[name] = chain.GetBlockByHash(block.Hash()) != nil && chain.HasState(block.Root())
	}
	result["stored"] = stored
	if spec.Rewind {
		before := chain.CurrentBlock().Number.Uint64()
		result["rewind_error"] = fmt.Sprint(chain.SetHead(target - 2))
		result["rewind_head"] = []uint64{before, chain.CurrentBlock().Number.Uint64()}
	}
	if a != nil {
		c, token := a.Core()
		mode, err := c.Mode(token)
		result["mode"], result["mode_error"] = string(mode), errorText(err)
		check(token.Release())
	}
	result["captured_inputs"] = safe.finish()
	head := chain.CurrentBlock()
	result["head_number"] = head.Number.Uint64()
	result["head_hash"] = head.Hash().Hex()
	if head.Number.Uint64() >= target {
		state, err := chain.StateAt(head.Root, head.Number, head.Time)
		check(err)
		slots := []string{}
		for slot := range lookbacks {
			slots = append(slots, state.GetState(reader, common.BigToHash(big.NewInt(int64(slot)))).Hex())
		}
		result["reader_slots"] = slots
	}
	chain.Stop()
	records := prefixed(db, "extractor-admission-v1/")
	result["records"] = records
	result["pins"] = prefixed(db, "extractor-state-provider-pin-v1-")
	result["run_record"] = prefixed(db, "extractor-admission-geth-run-v1")
	keys := map[string]bool{}
	for key, value := range records {
		if strings.HasSuffix(key, "/preparing") {
			var prep admission.Preparing
			check(json.Unmarshal([]byte(value), &prep))
			keys[prep.Observation] = true
		}
	}
	sorted := []string{}
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)
	result["capsules"] = capsules(db, sorted, spec.Items)
	if spec.Decisions {
		result["decision_records"] = prefixed(db, "extractor-decision-v1/")
		result["markers"] = prefixed(db, "extractor-decision-marker-v1/")
		bad := map[string]bool{}
		for name, block := range map[string]*types.Block{"target": blocks[target-1], "next": blocks[target]} {
			bad[name] = rawdb.ReadBadBlock(db, block.Hash()) != nil
		}
		result["bad_blocks"] = bad
		// Inputs left of every observation that this or an earlier attempt
		// began: closed observations have none (OpenWithCleanup).
		left := map[string]int{}
		for key := range prefixed(db, "extractor-source-inputs-v1/") {
			parts := strings.SplitN(strings.TrimPrefix(key, "extractor-source-inputs-v1/"), "/", 2)
			left[parts[0]]++
		}
		result["inputs_left"] = left
	}
	check(db.Close())
	writeResult(spec, result, file)
}

// freezer pins a synthetic side-branch header above sideFork, marks block
// finalNumber finalized and reopens the store, so the native chain freezer
// runs once. Without the FreezeLimit patch it freezes up to finalNumber and
// deletes the side-branch header.
func freezer(spec Spec, genesis *core.Genesis, blocks []*types.Block, result map[string]any) {
	db := openStore(spec.Datadir)
	chain := newChain(db, genesis)
	identity, err := stateprovider.Bind(db, storeID)
	check(err)
	provider, err := stateprovider.New(db, chain.TrieDB(), identity)
	check(err)
	side := types.CopyHeader(blocks[sideFork].Header())
	side.Extra = []byte("side branch")
	side.Root = chain.Genesis().Root()
	body := blocks[sideFork].Body()
	rawdb.WriteBlock(db, types.NewBlockWithHeader(side).WithBody(*body))
	rawdb.WriteReceipts(db, side.Hash(), side.Number.Uint64(), nil)
	check(provider.Pin(stateprovider.Pin{ID: "geth-probe.side.0000000000000000", Store: identity,
		Profile: strings.Repeat("0", 64), ParentRoot: side.Root,
		Headers: []stateprovider.HeaderRef{{Hash: side.Hash(), Number: side.Number.Uint64()}}}))
	rawdb.WriteFinalizedBlockHash(db, blocks[finalNumber-1].Hash())
	chain.Stop()
	check(db.Close())
	db = openStore(spec.Datadir)
	// The freezer runs at open; wait until the frozen count stays stable.
	var frozen uint64
	stable := 0
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline) && stable < 20; {
		time.Sleep(100 * time.Millisecond)
		current, err := db.Ancients()
		check(err)
		if current == frozen && current > 0 {
			stable++
		} else {
			frozen, stable = current, 0
		}
	}
	result["side_hash"] = side.Hash().Hex()
	result["frozen"] = frozen
	result["side_header_kept"] = rawdb.ReadHeader(db, side.Hash(), side.Number.Uint64()) != nil
	result["canonical_fork_kept"] = rawdb.ReadHeader(db, blocks[sideFork-1].Hash(), sideFork) != nil
	check(db.Close())
}

func writeResult(spec Spec, result map[string]any, file *os.File) {
	out, err := json.MarshalIndent(result, "", "  ")
	check(err)
	check(os.WriteFile(spec.Result, append(out, '\n'), 0o600))
	check(file.Close())
}

func errorText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func readLines(path string) []map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rows []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		row := map[string]any{}
		check(json.Unmarshal(line, &row))
		rows = append(rows, row)
	}
	return rows
}

// runAttempt starts one child attempt and reports how it ended.
func runAttempt(work, name string, spec Spec) map[string]any {
	spec.Events = filepath.Join(work, name+".events.jsonl")
	spec.Result = filepath.Join(work, name+".result.json")
	specPath := filepath.Join(work, name+".spec.json")
	data, err := json.Marshal(spec)
	check(err)
	check(os.WriteFile(specPath, data, 0o600))
	cmd := exec.Command(os.Args[0], "attempt", specPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	row := map[string]any{"name": name, "spec": spec, "events": readLines(spec.Events), "signal": ""}
	if exit, ok := err.(*exec.ExitError); ok {
		row["exit_code"] = exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			row["signal"] = status.Signal().String()
		}
	} else {
		check(err)
		row["exit_code"] = 0
	}
	row["stderr_tail"] = tail(stderr.String())
	if data, err := os.ReadFile(spec.Result); err == nil {
		result := map[string]any{}
		check(json.Unmarshal(data, &result))
		row["result"] = result
	}
	return row
}

func tail(text string) string {
	if len(text) > 4000 {
		return text[len(text)-4000:]
	}
	return text
}

// shipped runs the patched native geth binary on a datadir with a cap and
// reports its exit and output.
func shipped(geth, name string, env []string, args ...string) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, geth, args...)
	cmd.Env = append([]string{"HOME=/tmp"}, env...)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	row := map[string]any{"name": name, "args": args, "timed_out": ctx.Err() != nil, "exit_code": 0}
	if exit, ok := err.(*exec.ExitError); ok {
		row["exit_code"] = exit.ExitCode()
	} else if err != nil {
		row["exit_code"] = -1
		row["error"] = err.Error()
	}
	row["output_tail"] = tail(output.String())
	return row
}

// storeFiles lists every file under the chaindata of datadir as
// "<relative path> <size> <SHA-256>", in path order. Two equal lists show that
// a refused command left the store byte for byte unchanged.
func storeFiles(datadir string) []string {
	root := filepath.Join(datadir, "geth", "chaindata")
	files := []string{}
	check(filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, fmt.Sprintf("%s %d %x", filepath.ToSlash(relative), len(data), sha256.Sum256(data)))
		return nil
	}))
	sort.Strings(files)
	return files
}

// onStore runs a shipped geth command on datadir and records the chaindata
// files before and after it.
func onStore(geth, name, datadir string, args ...string) map[string]any {
	before := storeFiles(datadir)
	row := shipped(geth, name, nil, append([]string{"--datadir", datadir}, args...)...)
	row["store_before"], row["store_after"] = before, storeFiles(datadir)
	return row
}

// setup imports blocks 1..target-1 natively, without admission, into a new
// on-disk store and stops the chain, so the parent state of the target block
// is on disk.
func setup(datadir string, genesis *core.Genesis, blocks []*types.Block) {
	check(os.MkdirAll(filepath.Join(datadir, "geth"), 0o700))
	db := openStore(datadir)
	chain := newChain(db, genesis)
	_, err := chain.InsertChain(blocks[:target-1])
	check(err)
	chain.Stop()
	check(db.Close())
}

// exportBlock writes one block as the RLP file that geth import reads.
func exportBlock(work, name string, block *types.Block) string {
	path := filepath.Join(work, name)
	file, err := os.Create(path)
	check(err)
	check(rlp.Encode(file, block))
	check(file.Close())
	return path
}

func writeConfig(path string, genesis *core.Genesis, commit, store, producer string) {
	data, err := json.Marshal(extractorgeth.Config{StoreID: store, ProducerID: producer,
		ChainID: genesis.Config.ChainID.String(), GenesisHash: genesis.ToBlock().Hash(), NativeCommit: commit,
		Profile: profile})
	check(err)
	check(os.WriteFile(path, data, 0o600))
}

func run(genesisPath, work, output, commit, geth string) {
	check(os.Mkdir(work, 0o700))
	genesis, blocks, bad := chainInputs(genesisPath)
	// generated_hashes[n] is the hash of block n; index 0 is the genesis.
	hashes := []string{genesis.ToBlock().Hash().Hex()}
	headers := []string{}
	for _, block := range blocks {
		hashes = append(hashes, block.Hash().Hex())
	}
	for _, block := range blocks {
		data, err := rlp.EncodeToBytes(block.Header())
		check(err)
		headers = append(headers, hex.EncodeToString(data))
	}
	base := Spec{Genesis: genesisPath, NativeCommit: commit}
	scenarios := []map[string]any{}
	scenario := func(name string, attempts ...map[string]any) {
		scenarios = append(scenarios, map[string]any{"name": name, "attempts": attempts})
	}
	dir := func(name string) string {
		path := filepath.Join(work, name)
		setup(path, genesis, blocks)
		return path
	}
	with := func(spec Spec, change func(*Spec)) Spec {
		change(&spec)
		return spec
	}

	clean := with(base, func(s *Spec) { s.Datadir = dir("clean") })
	first := runAttempt(work, "clean-1", with(clean, func(s *Spec) { s.Imports = []string{"target", "next"}; s.Items = true }))
	restart := runAttempt(work, "clean-2-restart", clean)
	// The shipped binary on the same store, with live EXEC_READY pins.
	instance := filepath.Join(clean.Datadir, "geth")
	shippedRuns := []map[string]any{
		shipped(geth, "prune-state", nil, "--datadir", clean.Datadir, "snapshot", "prune-state"),
	}
	bloom := filepath.Join(instance, "statebloom.0x"+strings.Repeat("ab", 32)+".bf.gz")
	check(os.WriteFile(bloom, []byte("interrupted prune"), 0o600))
	node := []string{"--datadir", clean.Datadir, "--nodiscover", "--maxpeers", "0", "--port", "0", "--authrpc.port", "0",
		"--ipcdisable", "--cache", "64", "--syncmode", "full", "--gcmode", "full", "--state.scheme", "hash"}
	shippedRuns = append(shippedRuns, shipped(geth, "recover-pruning", nil, node...))
	check(os.Remove(bloom))
	foreign := filepath.Join(work, "foreign-store.json")
	writeConfig(foreign, genesis, commit, strings.Repeat("f", 32), producerID)
	shippedRuns = append(shippedRuns, shipped(geth, "foreign-store", []string{extractorgeth.ConfigEnv + "=" + foreign}, node...))
	other := filepath.Join(work, "other-producer.json")
	writeConfig(other, genesis, commit, storeID, "geth-other")
	shippedRuns = append(shippedRuns, shipped(geth, "other-producer", []string{extractorgeth.ConfigEnv + "=" + other}, node...))
	shippedRuns = append(shippedRuns, shipped(geth, "no-config", nil, node...))
	// E01-02.decision_bridge: the shipped node attaches the decision bridge.
	// A store whose blocks were written before the switch, without a
	// decision, is refused at startup.
	config := filepath.Join(work, "capture-store.json")
	writeConfig(config, genesis, commit, storeID, producerID)
	shippedRuns = append(shippedRuns, shipped(geth, "decision-legacy-store", []string{extractorgeth.ConfigEnv + "=" + config},
		node...))
	rewind := runAttempt(work, "clean-3-rewind", with(clean, func(s *Spec) { s.Rewind, s.Witness = true, true }))
	scenario("clean", first, restart, rewind)

	// AC-CAP-02-01: power loss before the counter sync of Begin, right after
	// it (no pin yet), and before the closing ADMITTED sync.
	dirs := map[string]string{}
	for _, row := range []struct{ name, before, after string }{
		{"begin_before_counter_sync", "/counter", ""},
		{"begin_after_counter_sync", "", "/counter"},
		{"admit_before_sync", "/admitted", ""},
	} {
		dirs[row.name] = dir(row.name)
		spec := with(base, func(s *Spec) {
			s.Datadir = dirs[row.name]
			s.Imports = []string{"target"}
			s.CrashBeforeSync, s.CrashAfterSync = row.before, row.after
		})
		retry := with(spec, func(s *Spec) { s.CrashBeforeSync, s.CrashAfterSync = "", "" })
		prefix := strings.ReplaceAll(row.name, "_", "-")
		crash := runAttempt(work, prefix+"-1-crash", spec)
		if row.name == "begin_after_counter_sync" {
			// An INPUT_PREPARING observation without a pin: removedb is
			// refused before any prompt and the store stays unchanged.
			shippedRuns = append(shippedRuns, onStore(geth, "removedb-open-obligation", dirs[row.name],
				"removedb", "--remove.state", "--remove.chain"))
		}
		scenario(row.name, crash, runAttempt(work, prefix+"-2-recover", retry))
	}

	// An interrupted Begin on a store of its own leaves no observation and no
	// pin, only an INPUT_PREPARING record beyond the counter. The manual
	// database guard passes there: geth import of block 300 stops at the
	// capture-store refusal of ProcessBlock, and removedb then deletes the
	// store natively.
	allowed := with(base, func(s *Spec) {
		s.Datadir = dir("removedb_allowed")
		s.Imports = []string{"target"}
		s.CrashBeforeSync = "/counter"
	})
	scenario("removedb_allowed", runAttempt(work, "removedb-allowed-1-crash", allowed))
	shippedRuns = append(shippedRuns, onStore(geth, "import-bound", allowed.Datadir, "--cache", "64",
		"--state.scheme", "hash", "import", exportBlock(work, "block-300.rlp", blocks[target-1])))
	shippedRuns = append(shippedRuns, onStore(geth, "removedb-allowed", allowed.Datadir,
		"removedb", "--remove.state", "--remove.chain"))

	// geth import opens the chain without eth.New. On a capture store (head
	// 300 after the scenario above) it must refuse to import block 301.
	export := exportBlock(work, "block-301.rlp", blocks[target])
	shippedRuns = append(shippedRuns, shipped(geth, "import", nil, "--datadir", dirs["admit_before_sync"], "--cache", "64",
		"--state.scheme", "hash", "import", export))

	// AC-CAP-02-02: power loss right after the closing ADMITTED sync. The
	// restart holds RECOVERY_ONLY first, then recovers, then imports a new H.
	admitted := with(base, func(s *Spec) {
		s.Datadir = dir("admit_after_sync")
		s.Imports = []string{"target"}
		s.CrashAfterSync = "/admitted"
	})
	admittedCrash := runAttempt(work, "admit-after-sync-1-crash", admitted)
	// D100 item 3: with the ADMITTED obligation and its live pin, every
	// guarded manual geth command that deletes or rewrites chaindata is refused
	// before it writes, and the store stays unchanged; recovery then
	// completes below.
	empty := filepath.Join(work, "empty-input")
	check(os.WriteFile(empty, nil, 0o600))
	emptyDir := filepath.Join(work, "empty-era")
	check(os.Mkdir(emptyDir, 0o700))
	admittedKey := hex.EncodeToString([]byte("extractor-admission-v1/" + producerID + "." + runID + "/0000000000000000/admitted"))
	for _, command := range [][]string{
		{"removedb", "removedb", "--remove.state", "--remove.chain"},
		{"init", "init", genesisPath},
		{"import-admitted", "--cache", "64", "--state.scheme", "hash", "import", export},
		{"import-history", "import-history", emptyDir},
		{"import-preimages", "import-preimages", empty},
		{"prune-history", "prune-history", "--history.chain", "100:" + hashes[100]},
		{"db-compact", "db", "compact"},
		{"db-delete", "db", "delete", "0x" + admittedKey},
		{"db-put", "db", "put", "0x" + admittedKey, "0x00"},
		{"db-import", "db", "import", empty},
		{"db-import-badblocks", "db", "import-badblocks", empty},
		{"bintrie-convert", "bintrie", "convert"},
		{"snapshot-prune-state", "snapshot", "prune-state"},
		{"snapshot-generate-trie", "snapshot", "generate-trie"},
	} {
		shippedRuns = append(shippedRuns, onStore(geth, "admitted-"+command[0], admitted.Datadir, command[1:]...))
	}
	scenario("admit_after_sync",
		admittedCrash,
		runAttempt(work, "admit-after-sync-2-hold", with(admitted, func(s *Spec) { s.Imports, s.CrashAfterSync, s.Hold = nil, "", true })),
		runAttempt(work, "admit-after-sync-3-changed-profile", with(admitted, func(s *Spec) {
			s.Imports, s.CrashAfterSync, s.Profile = nil, "", "another-profile"
		})),
		runAttempt(work, "admit-after-sync-4-recover", with(admitted, func(s *Spec) { s.Imports, s.CrashAfterSync = []string{"next"}, "" })))

	execReady := with(base, func(s *Spec) {
		s.Datadir = dir("exec_ready_before_sync")
		s.Imports = []string{"target"}
		s.CrashBeforeSync = "/exec-ready"
	})
	scenario("exec_ready_before_sync",
		runAttempt(work, "exec-ready-before-sync-1-crash", execReady),
		runAttempt(work, "exec-ready-before-sync-2-recover", with(execReady, func(s *Spec) { s.Imports, s.CrashBeforeSync = nil, "" })))

	// Native validation failure after ADMITTED: NATIVE_REJECTED, never
	// EXEC_READY, live and in recovery.
	scenario("reject_live", runAttempt(work, "reject-live-1", with(base, func(s *Spec) {
		s.Datadir = dir("reject_live")
		s.Imports = []string{"bad", "target"}
	})))
	rejected := with(base, func(s *Spec) {
		s.Datadir = dir("reject_after_admit")
		s.Imports = []string{"bad"}
		s.CrashAfterSync = "/admitted"
	})
	scenario("reject_after_admit",
		runAttempt(work, "reject-after-admit-1-crash", rejected),
		runAttempt(work, "reject-after-admit-2-recover", with(rejected, func(s *Spec) { s.Imports, s.CrashAfterSync = []string{"target"}, "" })))

	// The engine path: newPayload writes the block without the head, then
	// forkchoice makes it canonical.
	scenario("without_set_head", runAttempt(work, "without-set-head-1", with(base, func(s *Spec) {
		s.Datadir = dir("without_set_head")
		s.Imports = []string{"target-without-head", "canonical-target"}
	})))

	scenario("freezer", runAttempt(work, "freezer-1", with(base, func(s *Spec) { s.Datadir = dir("freezer"); s.Freezer = true })))

	// E01-02.decision_bridge: the same import path with the decision bridge
	// (AttachDecisions, the shipped configuration). The mirror has applied
	// nothing unless MirrorApplied.
	decided := with(base, func(s *Spec) { s.Decisions = true })
	dclean := with(decided, func(s *Spec) { s.Datadir = dir("decision_clean") })
	scenario("decision_clean",
		runAttempt(work, "decision-clean-1", with(dclean, func(s *Spec) { s.Imports = []string{"target", "next"} })),
		// After a restart the blocks are in the chain with their state:
		// importing them again opens no observation and no decision. The
		// mirror has applied both decisions, so Cleanup removes the markers.
		runAttempt(work, "decision-clean-2-restart", with(dclean, func(s *Spec) {
			s.Imports, s.MirrorApplied = []string{"target", "next"}, true
		})))
	// The AC-CAP-07 crash points. Each recover attempt imports the next
	// block where the recovered decision leaves it importable.
	for _, row := range []struct {
		name, before, after, point string
		refuse                     []string
		imports                    []string
	}{
		// AC-CAP-07-01: power loss before the closing EXEC_READY sync.
		{name: "decision_exec_ready", before: "/exec-ready", imports: []string{"next"}},
		// AC-CAP-07-02: EXEC_READY durable, the child root still volatile.
		{name: "decision_child_root", after: "/exec-ready", imports: []string{"next"}},
		// AC-CAP-07-03: the intent is durable, nothing native is written yet.
		{name: "decision_intent", after: "-decision-v1/" + producerID + "." + runID + "/counter",
			imports: []string{"next"}},
		// AC-CAP-07-03: power loss inside the native mutation, after the
		// block batch and before the head batch with the marker.
		{name: "decision_mutation", point: "mutated", imports: []string{"target", "next"}},
		// The marker is durable, the NATIVE_DECIDED record is not.
		{name: "decision_marker", before: "-decision-v1/" + producerID + "." + runID + "/0000000000000000/decided",
			imports: []string{"next"}},
		// AC-CAP-07-06: the native side refused the transition after the
		// intent; power loss before the rejection is recorded.
		{name: "decision_rejected", before: "-decision-v1/" + producerID + "." + runID + "/0000000000000000/rejected",
			refuse: []string{"target"}},
	} {
		spec := with(decided, func(s *Spec) {
			s.Datadir = dir(row.name)
			s.Imports = []string{"target"}
			s.CrashBeforeSync, s.CrashAfterSync, s.CrashPoint, s.Refuse = row.before, row.after, row.point, row.refuse
		})
		prefix := strings.ReplaceAll(row.name, "_", "-")
		scenario(row.name,
			runAttempt(work, prefix+"-1-crash", spec),
			runAttempt(work, prefix+"-2-recover", with(spec, func(s *Spec) {
				s.Imports, s.CrashBeforeSync, s.CrashAfterSync, s.CrashPoint, s.Refuse = row.imports, "", "", "", nil
			})))
	}
	// AC-CAP-07-03 on a CANONICAL write: the target is first written as a
	// SIDE decision (engine newPayload), then the next block moves the head
	// over it with one CANONICAL decision whose members are 301 and the
	// retained 300. Its native mutation has three batches: the block batch of
	// 301, the head batch of 300 inside reorg, and the head batch of 301 with
	// the marker.
	for _, row := range []struct {
		name, after, point string
	}{
		// The intent is durable, nothing native of 301 is written.
		{name: "decision_canonical_intent", after: "-decision-v1/" + producerID + "." + runID + "/counter"},
		// After the block batch of 301.
		{name: "decision_canonical_mutated", point: "mutated"},
		// After the intermediate head batch of 300, before the final one.
		{name: "decision_canonical_intermediate", point: "intermediate"},
	} {
		spec := with(decided, func(s *Spec) {
			s.Datadir = dir(row.name)
			s.Imports = []string{"target-without-head", "next"}
			s.CrashAfterSync, s.CrashPoint = row.after, row.point
			// The SIDE write of 300 passes the same trigger once ("intermediate"
			// has no earlier match).
			if row.point != "intermediate" {
				s.CrashSkip = 1
			}
		})
		prefix := strings.ReplaceAll(row.name, "_", "-")
		scenario(row.name,
			runAttempt(work, prefix+"-1-crash", spec),
			runAttempt(work, prefix+"-2-recover", with(spec, func(s *Spec) {
				s.Imports, s.CrashAfterSync, s.CrashPoint, s.CrashSkip = nil, "", "", 0
			})))
	}

	// AC-CAP-07-06 live: the native refusal is recorded at once.
	scenario("decision_reject_live", runAttempt(work, "decision-reject-live-1", with(decided, func(s *Spec) {
		s.Datadir = dir("decision_reject_live")
		s.Imports, s.Refuse = []string{"target"}, []string{"target"}
	})))

	result := map[string]any{
		"family":            "geth",
		"target":            target,
		"lookbacks":         lookbacks,
		"generated_hashes":  hashes,
		"generated_headers": headers,
		"bad_hash":          bad.Hash().Hex(),
		"scenarios":         scenarios,
		"shipped":           shippedRuns,
		"side_fork":         sideFork,
		"final_number":      finalNumber,
	}
	data, err := json.MarshalIndent(result, "", "  ")
	check(err)
	check(os.WriteFile(output, append(data, '\n'), 0o600))
	digest := sha256.Sum256(data)
	fmt.Printf("Recorded admission probe observations (%x); the oracle comparison is separate.\n", digest[:8])
}

func main() {
	switch {
	case len(os.Args) == 7 && os.Args[1] == "run":
		run(os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6])
	case len(os.Args) == 3 && os.Args[1] == "attempt":
		attempt(os.Args[2])
	default:
		fmt.Fprintln(os.Stderr, "usage: probe run GENESIS WORKDIR OUTPUT NATIVE_COMMIT GETH | probe attempt SPEC")
		os.Exit(2)
	}
}
