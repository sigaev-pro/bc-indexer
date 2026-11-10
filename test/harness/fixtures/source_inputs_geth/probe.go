// This test-only probe runs the E01-02.inputs core inside the native block
// import of the accepted Geth tree. Preparation copies it to
// cmd/extractor-inputs-probe, copies the shared package to
// extractor/source_inputs, and adds a test-only hook (hook.go.txt) that lets
// the probe resolve the BLOCKHASH ancestor lookups of the serial state
// processor through the dependency log. The probe reports observations only;
// the driver's separately written oracle judges them against expected.json.
//
// Usage:
//
//	probe run GENESIS WORKDIR OUTPUT NATIVE_COMMIT
//	probe attempt SPEC   (started by run in a child process)
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/forkid"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	sourceinputs "github.com/ethereum/go-ethereum/extractor/source_inputs"
	"github.com/ethereum/go-ethereum/rlp"
)

// The native key-value store of the accepted tree satisfies the core's Store.
var _ sourceinputs.Store = ethdb.Database(nil)

const (
	observation = "geth-probe-obs-4"
	target      = 4
)

// reader is the contract that stores BLOCKHASH(NUMBER-k) in slot i for each
// k in lookbacks, in order: PUSH1 k NUMBER SUB BLOCKHASH PUSH1 i SSTORE.
var (
	reader    = common.HexToAddress("0x00000000000000000000000000000000000000c1")
	lookbacks = []byte{1, 3, 1}
)

func readerCode() []byte {
	var code []byte
	for slot, back := range lookbacks {
		code = append(code, 0x60, back, 0x43, 0x03, 0x40, 0x60, byte(slot), 0x55)
	}
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
	// Resolver is "native", "poisoned_below" (poisoned below PoisonBelow,
	// native beyond), "poisoned_all" or "direct_poison" (no log at all).
	Resolver    string `json:"resolver"`
	PoisonBelow uint64 `json:"poison_below"`
	// Crash is "", "after_seal" or "before_handoff"; CrashSequence is the
	// sequence whose hand-off the attempt never reaches.
	Crash         string `json:"crash"`
	CrashSequence uint64 `json:"crash_sequence"`
	// Rewind sets the head back to the parent first, for a native-only replay
	// of a block that an earlier attempt imported.
	Rewind bool `json:"rewind"`
}

// chainInputs builds the deterministic genesis and blocks 1..target.
func chainInputs(path string) (*core.Genesis, []*types.Block) {
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
	// The generator answers BLOCKHASH of the target block from a separate
	// in-memory chain, independently of the import under test.
	generator := newChain(rawdb.NewMemoryDatabase(), genesis)
	_, err = generator.InsertChain(blocks)
	check(err)
	last, _ := core.GenerateChain(genesis.Config, blocks[len(blocks)-1], ethash.NewFaker(), db, 1, func(index int, block *core.BlockGen) {
		block.SetCoinbase(coinbase)
		block.AddTxWithChain(generator, sign(target-1, reader))
	})
	generator.Stop()
	check(db.Close())
	return genesis, append(blocks, last...)
}

func openStore(datadir string) ethdb.Database {
	kv, err := pebble.New(filepath.Join(datadir, "chaindata"), 16, 16, "", false)
	check(err)
	db, err := rawdb.NewDatabaseWithFreezer(kv, filepath.Join(datadir, "chaindata", "ancient"), "", false)
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

// recorder is the Store the core writes through: the native database, with
// every write and sync recorded in order.
type recorder struct {
	db  ethdb.Database
	log *events
}

func (r *recorder) Has(key []byte) (bool, error)   { return r.db.Has(key) }
func (r *recorder) Get(key []byte) ([]byte, error) { return r.db.Get(key) }
func (r *recorder) Put(key, value []byte) error {
	r.log.add(map[string]any{"op": "put", "key": string(key), "length": len(value)})
	return r.db.Put(key, value)
}
func (r *recorder) SyncKeyValue() error {
	r.log.add(map[string]any{"op": "sync"})
	return r.db.SyncKeyValue()
}

func crash(log *events, point string) {
	log.add(map[string]any{"op": "crash", "point": point})
	check(syscall.Kill(os.Getpid(), syscall.SIGKILL))
	select {}
}

// poison is the changed "external" answer: wrong for every lookup.
func poison(number uint64) common.Hash {
	return common.Hash(sha256.Sum256([]byte(fmt.Sprintf("changed external response %d", number))))
}

func request(ref common.Hash, number uint64) []byte {
	return []byte(fmt.Sprintf("geth-blockhash-v1 ref=%s number=%d", ref.Hex(), number))
}

func capsuleItems(chain *core.BlockChain, genesis *core.Genesis, block *types.Block, commit string) []sourceinputs.Item {
	header, err := rlp.EncodeToBytes(block.Header())
	check(err)
	body, err := rlp.EncodeToBytes(block.Body())
	check(err)
	parent := chain.GetHeader(block.ParentHash(), block.NumberU64()-1)
	if parent == nil {
		panic("parent header is missing")
	}
	config, err := json.Marshal(genesis.Config)
	check(err)
	id := forkid.NewID(genesis.Config, chain.Genesis(), block.NumberU64(), block.Time())
	fork, err := json.Marshal(map[string]string{"hash": hex.EncodeToString(id.Hash[:]), "next": fmt.Sprint(id.Next)})
	check(err)
	return []sourceinputs.Item{
		{Name: "header-rlp", Data: header},
		{Name: "body-rlp", Data: body},
		{Name: "parent-hash", Data: parent.Hash().Bytes()},
		{Name: "parent-root", Data: parent.Root.Bytes()},
		{Name: "chain-config", Data: config},
		{Name: "fork-id", Data: fork},
		{Name: "native-commit", Data: []byte(commit)},
		{Name: "normalization-profile", Data: []byte("test-only-probe-v1")},
	}
}

func attempt(specPath string) {
	data, err := os.ReadFile(specPath)
	check(err)
	var spec Spec
	check(json.Unmarshal(data, &spec))
	file, err := os.OpenFile(spec.Events, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	check(err)
	log := &events{file: file}
	result := map[string]any{"errors": []string{}}
	fail := func(err error) { result["errors"] = append(result["errors"].([]string), err.Error()) }

	genesis, blocks := chainInputs(spec.Genesis)
	block := blocks[target-1]
	db := openStore(spec.Datadir)
	chain := newChain(db, genesis)
	if spec.Rewind {
		check(chain.SetHead(target - 1))
	}
	result["head_before"] = chain.CurrentBlock().Number.Uint64()
	store := &recorder{db: db, log: log}

	var input *sourceinputs.Log
	if spec.Resolver != "direct_poison" {
		_, err := sourceinputs.ReadCapsule(store, observation)
		result["capsule_existed"] = err == nil
		if err != nil && !errors.Is(err, sourceinputs.ErrNoCapsule) {
			fail(err)
		}
		capsule, err := sourceinputs.SealCapsule(store, observation, capsuleItems(chain, genesis, block, spec.NativeCommit))
		check(err)
		result["capsule"] = capsule
		log.add(map[string]any{"op": "sealed", "digest": capsule.Digest})
		if spec.Crash == "after_seal" {
			crash(log, spec.Crash)
		}
		input, err = sourceinputs.OpenLog(store, observation)
		check(err)
		result["frontier_at_open"] = input.Frontier()
		log.add(map[string]any{"op": "opened", "frontier": input.Frontier()})
	}

	core.SetExtractorGetHash(func(header *types.Header, native func(uint64) common.Hash) func(uint64) common.Hash {
		if header.Number.Uint64() != target {
			return native
		}
		log.add(map[string]any{"op": "process_start", "number": header.Number.Uint64()})
		ref := header.Hash()
		// resolve is the only "external" source this attempt has. In the
		// poisoned modes it hides the native answer, so the block imports
		// only if native code received the saved bytes.
		resolve := func(seq, number uint64) (common.Hash, bool) {
			if spec.Resolver == "poisoned_all" || (spec.Resolver == "poisoned_below" && seq < spec.PoisonBelow) {
				return poison(number), true
			}
			return native(number), false
		}
		return func(number uint64) common.Hash {
			if spec.Resolver == "direct_poison" {
				log.add(map[string]any{"op": "direct_poison", "number": number})
				return poison(number)
			}
			seq := input.Cursor()
			req := request(ref, number)
			log.add(map[string]any{"op": "request", "sequence": seq, "request": string(req)})
			response, err := input.Next(req, func() ([]byte, error) {
				answer, poisoned := resolve(seq, number)
				log.add(map[string]any{"op": "fetch", "sequence": seq, "poisoned": poisoned})
				return answer.Bytes(), nil
			})
			if err == nil && len(response) != common.HashLength {
				err = fmt.Errorf("response of %d bytes", len(response))
			}
			if err != nil {
				log.add(map[string]any{"op": "error", "sequence": seq, "error": err.Error()})
				fail(err)
				return common.Hash{}
			}
			if spec.Crash == "before_handoff" && seq == spec.CrashSequence {
				crash(log, spec.Crash)
			}
			log.add(map[string]any{"op": "handoff", "sequence": seq, "response": hex.EncodeToString(response)})
			return common.BytesToHash(response)
		}
	})

	if _, err := chain.InsertChain(types.Blocks{block}); err != nil {
		result["import_error"] = err.Error()
	}
	head := chain.CurrentBlock()
	result["head_number"] = head.Number.Uint64()
	result["head_hash"] = head.Hash().Hex()
	if head.Number.Uint64() == target {
		state, err := chain.StateAt(head.Root, head.Number, head.Time)
		check(err)
		slots := []string{}
		for slot := range lookbacks {
			slots = append(slots, state.GetState(reader, common.BigToHash(big.NewInt(int64(slot)))).Hex())
		}
		result["reader_slots"] = slots
	}
	if input != nil && result["import_error"] == nil {
		consumed, err := input.Finish()
		if err != nil {
			fail(err)
		} else {
			result["consumed"] = consumed
			log.add(map[string]any{"op": "finished", "digest": consumed.Digest})
		}
		result["cursor"] = input.Cursor()
		// A closed log replays its prefix but must refuse a new response.
		_, err = input.Next([]byte("geth-probe-extra-request"), func() ([]byte, error) { return []byte{1}, nil })
		result["extra_after_finish"] = fmt.Sprint(err)
	}
	chain.Stop()
	check(db.Close())
	out, err := json.MarshalIndent(result, "", "  ")
	check(err)
	check(os.WriteFile(spec.Result, append(out, '\n'), 0o600))
	check(file.Close())
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
	tail := stderr.String()
	if len(tail) > 4000 {
		tail = tail[len(tail)-4000:]
	}
	row["stderr_tail"] = tail
	if data, err := os.ReadFile(spec.Result); err == nil {
		result := map[string]any{}
		check(json.Unmarshal(data, &result))
		row["result"] = result
	}
	return row
}

// setup imports blocks 1..target-1 into a new on-disk store and stops the
// chain, so the parent state of the target block is on disk.
func setup(datadir string, genesis *core.Genesis, blocks []*types.Block) {
	check(os.MkdirAll(datadir, 0o700))
	db := openStore(datadir)
	chain := newChain(db, genesis)
	_, err := chain.InsertChain(blocks[:target-1])
	check(err)
	chain.Stop()
	check(db.Close())
}

func run(genesisPath, work, output, commit string) {
	check(os.Mkdir(work, 0o700))
	genesis, blocks := chainInputs(genesisPath)
	hashes := []string{}
	for _, block := range blocks {
		hashes = append(hashes, block.Hash().Hex())
	}
	header, err := rlp.EncodeToBytes(blocks[target-1].Header())
	check(err)
	headerSum := sha256.Sum256(header)
	base := Spec{Genesis: genesisPath, NativeCommit: commit, Resolver: "native"}
	scenarios := []map[string]any{}
	scenario := func(name string, attempts ...map[string]any) {
		scenarios = append(scenarios, map[string]any{"name": name, "attempts": attempts})
	}
	dir := func(name string) string {
		path := filepath.Join(work, name)
		setup(path, genesis, blocks)
		return path
	}

	clean := base
	clean.Datadir = dir("clean")
	first := runAttempt(work, "clean-1", clean)
	replay := clean
	replay.Resolver, replay.Rewind = "poisoned_all", true
	scenario("clean_then_closed_replay", first, runAttempt(work, "clean-2-closed-replay", replay))

	sealed := base
	sealed.Datadir = dir("crash-after-seal")
	sealed.Crash = "after_seal"
	retry := sealed
	retry.Crash = ""
	scenario("crash_after_seal", runAttempt(work, "seal-1-crash", sealed), runAttempt(work, "seal-2-retry", retry))

	handoff := base
	handoff.Datadir = dir("crash-before-handoff")
	handoff.Crash, handoff.CrashSequence = "before_handoff", 1
	retry = handoff
	retry.Crash, retry.Resolver, retry.PoisonBelow = "", "poisoned_below", 2
	scenario("crash_before_handoff", runAttempt(work, "handoff-1-crash", handoff), runAttempt(work, "handoff-2-retry", retry))

	control := base
	control.Datadir = dir("control-direct-poison")
	control.Resolver = "direct_poison"
	scenario("control_direct_poison", runAttempt(work, "control-1", control))

	result := map[string]any{
		"family":            "geth",
		"observation":       observation,
		"target":            target,
		"lookbacks":         lookbacks,
		"generated_hashes":  hashes,
		"header_rlp_sha256": hex.EncodeToString(headerSum[:]),
		"scenarios":         scenarios,
	}
	data, err := json.MarshalIndent(result, "", "  ")
	check(err)
	check(os.WriteFile(output, append(data, '\n'), 0o600))
	fmt.Println("Recorded source inputs probe observations; the oracle comparison is separate.")
}

func main() {
	switch {
	case len(os.Args) == 6 && os.Args[1] == "run":
		run(os.Args[2], os.Args[3], os.Args[4], os.Args[5])
	case len(os.Args) == 3 && os.Args[1] == "attempt":
		attempt(os.Args[2])
	default:
		fmt.Fprintln(os.Stderr, "usage: probe run GENESIS WORKDIR OUTPUT NATIVE_COMMIT | probe attempt SPEC")
		os.Exit(2)
	}
}
