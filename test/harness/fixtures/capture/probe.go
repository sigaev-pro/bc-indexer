// This test-only probe runs core/capture inside the native block import of one
// accepted client (Geth, BSC or Bor). Preparation builds it inside that
// client's source tree, next to a test-only VM safe-point hook. It reports
// observations only; the separately written oracle (expected.json) judges them.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/sigaev-pro/bc-indexer/core/capture"
)

const (
	tagCallInput = 1
	tagCallNote  = 2
	tagBlock     = 3
	hold         = 300 * time.Millisecond
)

// Contract pairs: the caller copies P into memory and calls the callee with
// overlapping input and output regions; the callee returns Q.
var pairs = []struct {
	caller, callee common.Address
	size           int
	inputSeed      int
	outputSeed     int
}{
	{common.HexToAddress("0x00000000000000000000000000000000000000a1"), common.HexToAddress("0x00000000000000000000000000000000000000b1"), 4096, 1, 2},
	{common.HexToAddress("0x00000000000000000000000000000000000000a2"), common.HexToAddress("0x00000000000000000000000000000000000000b2"), 262144, 3, 4},
}

// pattern is the byte formula stated in ORACLE.md.
func pattern(size, seed int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte(i*131 + seed*17 + i>>8)
	}
	return out
}

func push4(v int) []byte { return []byte{0x63, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }

// calleeCode returns Q: CODECOPY(0, 21, n); RETURN(0, n).
func calleeCode(n int, data []byte) []byte {
	code := append(push4(n), push4(21)...)
	code = append(code, 0x60, 0x00, 0x39)
	code = append(code, push4(n)...)
	code = append(code, 0x60, 0x00, 0xf3)
	return append(code, data...)
}

// callerCode copies P to memory 0, calls the callee with input [0,n) and
// output [0,n), logs memory [0,n) and stores the first word in slot 0.
func callerCode(n int, callee common.Address, data []byte) []byte {
	code := append(push4(n), push4(68)...)
	code = append(code, 0x60, 0x00, 0x39)
	code = append(code, push4(n)...)
	code = append(code, 0x60, 0x00)
	code = append(code, push4(n)...)
	code = append(code, 0x60, 0x00, 0x60, 0x00, 0x73)
	code = append(code, callee.Bytes()...)
	code = append(code, 0x5a, 0xf1, 0x50)
	code = append(code, push4(n)...)
	code = append(code, 0x60, 0x00, 0xa0)
	code = append(code, 0x60, 0x00, 0x51, 0x60, 0x00, 0x55, 0x00)
	if len(code) != 68 {
		panic(fmt.Sprintf("caller code prefix is %d bytes", len(code)))
	}
	return append(code, data...)
}

type fieldResult struct {
	Tag    uint32 `json:"tag"`
	Length uint64 `json:"length"`
	SHA256 string `json:"sha256"`
	Chunks int    `json:"chunks"`
}

type pauseResult struct {
	Observed            bool   `json:"observed"`
	Kind                string `json:"kind"`
	HeadNumber          uint64 `json:"head_number"`
	FramesAtPause       uint64 `json:"frames_at_pause"`
	FramesAfterHold     uint64 `json:"frames_after_hold"`
	ImportDuringHold    bool   `json:"import_returned_during_hold"`
	CallbackDuringPause bool   `json:"callback_open_during_pause"`
	HoldMilliseconds    int64  `json:"hold_milliseconds"`
}

type scenarioResult struct {
	Name                 string            `json:"name"`
	HeadNumber           uint64            `json:"head_number"`
	HeadHash             string            `json:"head_hash"`
	Fields               []fieldResult     `json:"fields"`
	Notes                int               `json:"notes"`
	Logs                 []string          `json:"log_sha256"`
	Storage              map[string]string `json:"storage_slot0"`
	ChunkBytes           int               `json:"chunk_bytes"`
	BufferBytes          int64             `json:"buffer_bytes"`
	PeakHeld             int64             `json:"peak_held"`
	Waits                uint64            `json:"waits"`
	Pause                *pauseResult      `json:"pause,omitempty"`
	CallbackWaitRejected bool              `json:"callback_wait_rejected"`
	Errors               []string          `json:"errors"`
}

type probe struct {
	genesis *core.Genesis
	blocks  []*types.Block
	mu      sync.Mutex
	errors  []string
}

func (p *probe) fail(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errors = append(p.errors, fmt.Sprintf(format, args...))
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func newProbe(genesisPath string) *probe {
	data, err := os.ReadFile(genesisPath)
	check(err)
	genesis := new(core.Genesis)
	check(json.Unmarshal(data, genesis))
	for _, pair := range pairs {
		genesis.Alloc[pair.caller] = types.Account{Balance: new(big.Int), Code: callerCode(pair.size, pair.callee, pattern(pair.size, pair.inputSeed))}
		genesis.Alloc[pair.callee] = types.Account{Balance: new(big.Int), Code: calleeCode(pair.size, pattern(pair.size, pair.outputSeed))}
	}
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	check(err)
	signer := types.LatestSigner(genesis.Config)
	sign := func(nonce uint64, to common.Address, value int64) *types.Transaction {
		tx, err := types.SignTx(types.NewTransaction(nonce, to, big.NewInt(value), 5_000_000, big.NewInt(2_000_000_000), nil), signer, key)
		check(err)
		return tx
	}
	db, blocks, _ := core.GenerateChainWithGenesis(genesis, ethash.NewFaker(), 3, func(index int, block *core.BlockGen) {
		block.SetCoinbase(common.HexToAddress("0x2000000000000000000000000000000000000000"))
		switch index {
		case 0:
			block.AddTx(sign(0, pairs[0].caller, 0))
		case 1:
			block.AddTx(sign(1, pairs[1].caller, 0))
		default:
			block.AddTx(sign(2, common.HexToAddress("0x1000000000000000000000000000000000000000"), 1))
		}
	})
	db.Close()
	return &probe{genesis: genesis, blocks: blocks}
}

func (p *probe) chain(hooks *tracing.Hooks) *core.BlockChain {
	cfg := core.DefaultConfig()
	if hooks != nil {
		cfg.VmConfig = vm.Config{Tracer: hooks}
	}
	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), p.genesis, ethash.NewFaker(), cfg)
	check(err)
	return chain
}

// worker drains the capture, rebuilding fields and counting notes.
func worker(c *capture.Capturer, result *scenarioResult, done chan<- error) {
	var open *fieldResult
	whole := sha256.New()
	for {
		f, err := c.Next(context.Background())
		if errors.Is(err, io.EOF) {
			done <- nil
			return
		}
		if err != nil {
			done <- err
			return
		}
		switch f.Kind {
		case capture.BeginField:
			open = &fieldResult{Tag: f.Tag, Length: f.Length}
			whole.Reset()
		case capture.Chunk:
			if sha256.Sum256(f.Data()) != f.Digest {
				done <- errors.New("chunk digest mismatch")
				return
			}
			f.WriteTo(whole)
			open.Chunks++
		case capture.EndField:
			sum := whole.Sum(nil)
			if [32]byte(sum) != f.Digest || f.Length != open.Length {
				done <- errors.New("field digest or length mismatch")
				return
			}
			open.SHA256 = hex.EncodeToString(sum)
			result.Fields = append(result.Fields, *open)
		case capture.Note:
			result.Notes++
		}
		if err := c.Release(f); err != nil {
			done <- err
			return
		}
	}
}

// callHooks returns the tracer hooks and the VM safe point for a capture.
// The safe point copies the CALL input region and reserves credit; the hook
// only records a note with that credit and never waits.
func (p *probe) callHooks(c *capture.Capturer, safePoints *atomic.Int64, guard *atomic.Bool) (*tracing.Hooks, func(vm.OpCode, *vm.ScopeContext)) {
	ctx := context.Background()
	safePoint := func(op vm.OpCode, scope *vm.ScopeContext) {
		if op != vm.CALL {
			return
		}
		safePoints.Add(1)
		stack := scope.StackData()
		offset, length := stack[len(stack)-4], stack[len(stack)-5]
		if !offset.IsUint64() || !length.IsUint64() {
			p.fail("CALL input bounds exceed 64 bits")
			return
		}
		start := offset.Uint64()
		memory := scope.MemoryData()
		if err := c.Reserve(ctx, capture.FrameOverhead+16); err != nil {
			p.fail("safe point reserve: %v", err)
			return
		}
		err := c.CaptureField(ctx, tagCallInput, length.Uint64(), func(at uint64, dst []byte) error {
			clear(dst) // bytes past the current memory size read as zero after expansion
			if begin := start + at; begin < uint64(len(memory)) {
				copy(dst, memory[begin:])
			}
			return nil
		})
		if err != nil {
			p.fail("safe point capture: %v", err)
		}
	}
	hooks := &tracing.Hooks{OnOpcode: func(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
		if vm.OpCode(op) != vm.CALL {
			return
		}
		started := time.Now()
		if err := c.EnterCallback(); err != nil {
			p.fail("enter callback: %v", err)
			return
		}
		var note [16]byte
		binary.BigEndian.PutUint64(note[:8], pc)
		binary.BigEndian.PutUint64(note[8:], uint64(depth))
		if err := c.Note(tagCallNote, note[:]); err != nil {
			p.fail("callback note: %v", err)
		}
		if err := c.ExitCallback(); err != nil {
			p.fail("exit callback: %v", err)
		}
		// A separate capture, opened on this native callback goroutine during the
		// opcode hook, checks that a waiting call there fails at once. The real
		// capture is not used because the refusal stops it closed.
		if !guard.Load() {
			probeCapture, err := capture.New(capture.Config{ChunkBytes: 64, BufferBytes: 4096})
			if err == nil {
				err = probeCapture.EnterCallback()
			}
			if err != nil {
				p.fail("callback guard setup: %v", err)
			} else {
				refused := time.Now()
				rejected := errors.Is(probeCapture.Reserve(ctx, 1), capture.ErrInCallback)
				guard.Store(rejected && time.Since(refused) < 100*time.Millisecond)
			}
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			p.fail("callback took %v", elapsed)
		}
	}}
	return hooks, safePoint
}

func (p *probe) finish(name string, chain *core.BlockChain, c *capture.Capturer, cfg capture.Config, result *scenarioResult) {
	head := chain.CurrentBlock()
	result.Name = name
	result.HeadNumber = head.Number.Uint64()
	result.HeadHash = head.Hash().Hex()
	result.ChunkBytes = cfg.ChunkBytes
	result.BufferBytes = cfg.BufferBytes
	stats := c.Stats()
	result.PeakHeld = stats.PeakHeld
	result.Waits = stats.Waits
	state, err := chain.State()
	check(err)
	result.Storage = map[string]string{}
	for _, pair := range pairs {
		result.Storage[pair.caller.Hex()] = state.GetState(pair.caller, common.Hash{}).Hex()
	}
	for _, block := range p.blocks {
		for _, receipt := range chain.GetReceiptsByHash(block.Hash()) {
			for _, log := range receipt.Logs {
				sum := sha256.Sum256(log.Data)
				result.Logs = append(result.Logs, hex.EncodeToString(sum[:]))
			}
		}
	}
	p.mu.Lock()
	result.Errors = append([]string{}, p.errors...)
	p.errors = nil
	p.mu.Unlock()
	chain.Stop()
}

// hold waits until the producer is stopped at a safe point, checks that it
// makes no progress for the hold period, then starts the worker.
func (p *probe) hold(kind string, chain *core.BlockChain, c *capture.Capturer, imported <-chan error) *pauseResult {
	pause := &pauseResult{Kind: kind}
	deadline := time.Now().Add(60 * time.Second)
	for !c.Stats().ProducerWaiting {
		select {
		case err := <-imported:
			p.fail("import returned before the safe point waited: %v", err)
			return pause
		default:
		}
		if time.Now().After(deadline) {
			p.fail("the producer never waited at a safe point")
			return pause
		}
		time.Sleep(time.Millisecond)
	}
	at := c.Stats()
	pause.Observed = true
	pause.HeadNumber = chain.CurrentBlock().Number.Uint64()
	pause.FramesAtPause = at.Frames
	pause.CallbackDuringPause = at.InCallback
	started := time.Now()
	select {
	case err := <-imported:
		pause.ImportDuringHold = true
		p.fail("import returned while the worker was held: %v", err)
	case <-time.After(hold):
	}
	after := c.Stats()
	pause.FramesAfterHold = after.Frames
	pause.HoldMilliseconds = time.Since(started).Milliseconds()
	if !after.ProducerWaiting {
		p.fail("the producer left the safe point while the worker was held")
	}
	return pause
}

// overlap holds the worker for the whole import (AC-CAP-05-01).
func (p *probe) overlap() scenarioResult {
	var result scenarioResult
	cfg := capture.Config{ChunkBytes: 1024, BufferBytes: 1 << 20}
	c, err := capture.New(cfg)
	check(err)
	var safePoints atomic.Int64
	var guard atomic.Bool
	hooks, safePoint := p.callHooks(c, &safePoints, &guard)
	vm.SetExtractorSafePoint(safePoint)
	chain := p.chain(hooks)
	if _, err := insertChain(chain, p.blocks); err != nil {
		p.fail("import: %v", err)
	}
	vm.SetExtractorSafePoint(nil)
	check(c.Close())
	done := make(chan error, 1)
	worker(c, &result, done) // the worker starts only after the import changed native memory
	if err := <-done; err != nil {
		p.fail("worker: %v", err)
	}
	result.CallbackWaitRejected = guard.Load()
	p.finish("overlap_delayed_worker", chain, c, cfg, &result)
	return result
}

// vmSafe exhausts the budget inside the large CALL input (AC-CAP-06-02, vm_safe_backpressure).
func (p *probe) vmSafe() scenarioResult {
	var result scenarioResult
	cfg := capture.Config{ChunkBytes: 4096, BufferBytes: 32 << 10}
	c, err := capture.New(cfg)
	check(err)
	var safePoints atomic.Int64
	var guard atomic.Bool
	hooks, safePoint := p.callHooks(c, &safePoints, &guard)
	vm.SetExtractorSafePoint(safePoint)
	chain := p.chain(hooks)
	imported := make(chan error, 1)
	go func() {
		_, err := insertChain(chain, p.blocks)
		imported <- err
	}()
	// The small CALL input fits; the large one must wait.
	pause := p.hold("vm", chain, c, imported)
	done := make(chan error, 1)
	go worker(c, &result, done)
	if err := <-imported; err != nil && !pause.ImportDuringHold {
		p.fail("import: %v", err)
	}
	vm.SetExtractorSafePoint(nil)
	check(c.Close())
	if err := <-done; err != nil {
		p.fail("worker: %v", err)
	}
	result.Pause = pause
	result.CallbackWaitRejected = guard.Load()
	p.finish("vm_safe_backpressure", chain, c, cfg, &result)
	return result
}

// importBoundary waits between blocks before native import (AC-CAP-06-02, import_backpressure).
func (p *probe) importBoundary() scenarioResult {
	var result scenarioResult
	first, err := rlp.EncodeToBytes(p.blocks[0])
	check(err)
	const chunk = 256
	chunks := (len(first) + chunk - 1) / chunk
	// The budget fits exactly the first block's frames, so the second block waits.
	cfg := capture.Config{ChunkBytes: chunk, BufferBytes: int64((2+chunks)*capture.FrameOverhead + len(first))}
	c, err := capture.New(cfg)
	check(err)
	chain := p.chain(nil)
	imported := make(chan error, 1)
	go func() {
		for _, block := range p.blocks {
			encoded, err := rlp.EncodeToBytes(block)
			if err == nil {
				err = c.CaptureBytes(context.Background(), tagBlock, encoded)
			}
			if err == nil {
				_, err = insertChain(chain, types.Blocks{block})
			}
			if err != nil {
				imported <- err
				return
			}
		}
		imported <- nil
	}()
	pause := p.hold("import", chain, c, imported)
	done := make(chan error, 1)
	go worker(c, &result, done)
	if err := <-imported; err != nil && !pause.ImportDuringHold {
		p.fail("import: %v", err)
	}
	check(c.Close())
	if err := <-done; err != nil {
		p.fail("worker: %v", err)
	}
	result.Pause = pause
	p.finish("import_backpressure", chain, c, cfg, &result)
	return result
}

func main() {
	if len(os.Args) != 4 {
		panic("usage: probe FAMILY GENESIS OUTPUT")
	}
	p := newProbe(os.Args[2])
	baseline := p.chain(nil)
	_, err := insertChain(baseline, p.blocks)
	check(err)
	var blockDigests []string
	for _, block := range p.blocks {
		encoded, err := rlp.EncodeToBytes(block)
		check(err)
		sum := sha256.Sum256(encoded)
		blockDigests = append(blockDigests, hex.EncodeToString(sum[:]))
	}
	output := map[string]any{
		"block_rlp_sha256":    blockDigests,
		"family":              os.Args[1],
		"generated_head_hash": p.blocks[len(p.blocks)-1].Hash().Hex(),
		"baseline_head_hash":  baseline.CurrentBlock().Hash().Hex(),
		"scenarios":           []scenarioResult{p.overlap(), p.vmSafe(), p.importBoundary()},
	}
	baseline.Stop()
	data, err := json.MarshalIndent(output, "", "  ")
	check(err)
	check(os.WriteFile(os.Args[3], append(data, '\n'), 0o600))
	fmt.Println("Recorded capture probe observations; the oracle comparison is separate.")
}
