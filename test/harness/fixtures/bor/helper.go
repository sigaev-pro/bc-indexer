// This test-only helper builds solely in the accepted Bor native module graph.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor"
	borabi "github.com/ethereum/go-ethereum/consensus/bor/abi"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
)

const signerAddress = "0x71562b71999873db5b286df957af199ec94617f7"

func readBounded(path string, maximum int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, errors.New("input is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, errors.New("input grew beyond bound")
	}
	return data, nil
}

func saveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func testKey(source string) (*ecdsa.PrivateKey, error) {
	data, err := readBounded(source, 1024*1024)
	if err != nil {
		return nil, err
	}
	pattern := regexp.MustCompile(`(?m)^\s*privKey\s*=\s*"([0-9a-fA-F]{64})"\s*$`)
	matches := pattern.FindAllSubmatch(data, -1)
	if len(matches) != 1 {
		return nil, errors.New("missing unique tracked span-0 test key")
	}
	key, err := crypto.HexToECDSA(string(matches[0][1]))
	if err != nil {
		return nil, errors.New("invalid tracked test key")
	}
	if crypto.PubkeyToAddress(key.PublicKey) != common.HexToAddress(signerAddress) {
		return nil, errors.New("tracked test signer identity mismatch")
	}
	return key, nil
}

func generate(ctx context.Context, genesisPath, keySource, output string) error {
	data, err := readBounded(genesisPath, 1024*1024)
	if err != nil {
		return err
	}
	var genesis core.Genesis
	if err = json.Unmarshal(data, &genesis); err != nil {
		return err
	}
	if genesis.Config == nil || genesis.Config.Bor == nil || genesis.Config.ChainID.Cmp(big.NewInt(15001)) != 0 {
		return errors.New("expected private Bor genesis")
	}
	key, err := testKey(keySource)
	if err != nil {
		return err
	}
	author := crypto.PubkeyToAddress(key.PublicKey)
	stack, err := node.New(&node.Config{DataDir: filepath.Join(output, "generation-data"),
		P2P: p2p.Config{NoDiscovery: true, MaxPeers: 0, ListenAddr: "127.0.0.1:0"}})
	if err != nil {
		return err
	}
	defer stack.Close()
	cfg := ethconfig.Defaults
	cfg.Genesis, cfg.NetworkId, cfg.SyncMode = &genesis, 15001, downloader.FullSync
	cfg.NoPruning, cfg.Preimages, cfg.EnablePreimageRecording = true, true, true
	cfg.StateScheme, cfg.DatabaseCache, cfg.DatabaseHandles = "hash", 128, 128
	cfg.TrieCleanCache, cfg.TrieDirtyCache, cfg.SnapshotCache = 64, 64, 0
	cfg.WithoutHeimdall, cfg.DevFakeAuthor = true, false
	cfg.ParallelEVM.Enable, cfg.EnablePipelinedImportSRC = false, false
	cfg.EnableParallelStatelessImport, cfg.EnablePreconfs, cfg.AcceptPreconfTx = false, false, false
	backend, err := eth.New(stack, &cfg)
	if err != nil {
		return fmt.Errorf("create native Bor backend: %w", err)
	}
	if err = stack.Start(); err != nil {
		return fmt.Errorf("start native preparation backend: %w", err)
	}
	engine, ok := backend.Engine().(*bor.Bor)
	if !ok {
		return errors.New("native engine is not Bor")
	}
	chain := backend.BlockChain()
	engine.Authorize(author, func(account accounts.Account, mime string, payload []byte) ([]byte, error) {
		if account.Address != author {
			return nil, errors.New("wrong signing account")
		}
		return crypto.Sign(crypto.Keccak256(payload), key)
	})
	spanner := engine.GetSpanner()
	if spanner == nil {
		return errors.New("missing real native Bor spanner")
	}
	validators, err := spanner.GetCurrentValidatorsByBlockNrOrHash(ctx, rpc.BlockNumberOrHashWithNumber(0), 0)
	if err != nil {
		return fmt.Errorf("read actual genesis contract validators: %w", err)
	}
	if len(validators) != 1 || validators[0].Address != author {
		return errors.New("actual genesis contract validator mismatch")
	}
	if err = saveJSON(filepath.Join(output, "genesis-validators.json"), validators); err != nil {
		return err
	}
	blocksFile, err := os.OpenFile(filepath.Join(output, "blocks.rlp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer blocksFile.Close()
	fullFile, err := os.OpenFile(filepath.Join(output, "full-chain.rlp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer fullFile.Close()
	genesisBlock := chain.GetBlockByNumber(0)
	if genesisBlock == nil {
		return errors.New("missing committed genesis")
	}
	if err = rlp.Encode(fullFile, genesisBlock); err != nil {
		return err
	}
	prepared := make([]map[string]any, 0, 2)
	sender, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		return err
	}
	for number := uint64(1); number <= 2; number++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		parent := chain.CurrentBlock()
		header := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).SetUint64(number),
			GasLimit: parent.GasLimit, Time: parent.Time + 1, Difficulty: big.NewInt(1)}
		if genesis.Config.IsLondon(header.Number) {
			header.BaseFee = eip1559.CalcBaseFee(genesis.Config, parent)
			if !genesis.Config.IsLondon(parent.Number) {
				limit := parent.GasLimit * genesis.Config.ElasticityMultiplier()
				header.GasLimit = core.CalcGasLimit(limit, limit)
			}
		}
		if err = engine.Prepare(chain, header, false); err != nil {
			return fmt.Errorf("prepare native block %d: %w", number, err)
		}
		state, err := chain.StateAt(parent.Root)
		if err != nil {
			return err
		}
		body := &types.Body{}
		var receipts []*types.Receipt
		if number == 1 {
			tx := types.NewTransaction(0, common.HexToAddress("0x1000000000000000000000000000000000000000"),
				big.NewInt(1000000000000000000), 21000, big.NewInt(5000000000), nil)
			tx, err = types.SignTx(tx, types.LatestSigner(genesis.Config), sender)
			if err != nil {
				return fmt.Errorf("sign fixture transaction: %w", err)
			}
			state.SetTxContext(tx.Hash(), 0)
			evm := vm.NewEVM(core.NewEVMBlockContext(header, chain, &author), state, genesis.Config, vm.Config{})
			receipt, err := core.ApplyTransaction(evm, new(core.GasPool).AddGas(header.GasLimit), state, header, tx, &header.GasUsed)
			if err != nil {
				return fmt.Errorf("execute native block %d transaction: %w", number, err)
			}
			body.Transactions, receipts = []*types.Transaction{tx}, []*types.Receipt{receipt}
		}
		block, _, _, err := engine.FinalizeAndAssemble(chain, header, state, body, receipts)
		if err != nil {
			return fmt.Errorf("finalize native block %d: %w", number, err)
		}
		sealed := make(chan *consensus.NewSealedBlockEvent, 1)
		if err = engine.Seal(chain, block, nil, sealed, ctx.Done()); err != nil {
			return fmt.Errorf("seal native block %d: %w", number, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-sealed:
			if event == nil || event.Block == nil {
				return errors.New("missing genuinely sealed block")
			}
			block = event.Block
		}
		if _, err = chain.InsertChain([]*types.Block{block}, false); err != nil {
			return fmt.Errorf("import generated native block %d: %w", number, err)
		}
		if chain.CurrentBlock().Hash() != block.Hash() {
			return errors.New("native preparation import did not advance head")
		}
		if err = rlp.Encode(blocksFile, block); err != nil {
			return err
		}
		if err = rlp.Encode(fullFile, block); err != nil {
			return err
		}
		prepared = append(prepared, map[string]any{"number": number, "hash": block.Hash().Hex(),
			"parent_hash": block.ParentHash().Hex(), "state_root": block.Root().Hex(), "timestamp": block.Time(),
			"transactions": len(block.Transactions()), "gas_used": block.GasUsed()})
	}
	if err = blocksFile.Sync(); err != nil {
		return err
	}
	if err = fullFile.Sync(); err != nil {
		return err
	}
	return saveJSON(filepath.Join(output, "generated.json"), map[string]any{
		"genesis_hash": genesisBlock.Hash().Hex(), "prepared_blocks": prepared, "validator": author.Hex(), "config": genesis.Config})
}

// Every server stream and the entire IPC connection has a fixed byte budget.
type boundedWriter struct {
	file      *os.File
	remaining int64
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	keep := len(data)
	if int64(keep) > w.remaining {
		keep = int(w.remaining)
	}
	n, err := w.file.Write(data[:keep])
	w.remaining -= int64(n)
	if err != nil {
		return n, err
	}
	if keep != len(data) {
		return n, errors.New("retained output byte limit exceeded")
	}
	return n, nil
}

type boundedReader struct {
	conn   net.Conn
	output *boundedWriter
}

func (r *boundedReader) Read(data []byte) (int, error) {
	if int64(len(data)) > r.output.remaining+1 {
		data = data[:r.output.remaining+1]
	}
	n, err := r.conn.Read(data)
	if n > 0 {
		if _, writeErr := r.output.Write(data[:n]); writeErr != nil {
			return 0, writeErr
		}
	}
	return n, err
}

func stream(path string, maximum int64) (*boundedWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &boundedWriter{file: f, remaining: maximum}, nil
}

func serverArguments(genesisPath, output string) []string {
	return []string{"--chain=" + genesisPath, "--datadir=" + filepath.Join(output, "data"),
		"--ipcpath=" + filepath.Join(output, "bor.ipc"), "--ipcdisable=false", "--syncmode=full", "--gcmode=archive",
		"--cache=256",
		"--state.scheme=hash", "--db.engine=pebble", "--cache.preimages=true", "--bor.withoutheimdall=true",
		"--bor.devfakeauthor=false", "--mine=false", "--maxpeers=0", "--port=0", "--nodiscover=true", "--v5disc=false",
		"--http=false", "--ws=false", "--pipeline.enable-import-src=false", "--parallelevm.enable=false",
		"--witness.parallelstatelessimport=false", "--relay.enable-preconfs=false", "--accept-preconf-tx=false", "--log.json=true"}
}

func lower(address common.Address) string { return strings.ToLower(address.Hex()) }

func observe(ctx context.Context, genesisPath, blocksPath, output string) (returned error) {
	data, err := readBounded(genesisPath, 1024*1024)
	if err != nil {
		return err
	}
	var genesis core.Genesis
	if err = json.Unmarshal(data, &genesis); err != nil {
		return err
	}
	var input map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err = decoder.Decode(&input); err != nil {
		return err
	}
	configBytes, err := json.Marshal(input["config"])
	if err != nil {
		return err
	}
	configDigest := fmt.Sprintf("%x", sha256.Sum256(configBytes))
	args := serverArguments(genesisPath, output)
	if err = saveJSON(filepath.Join(output, "server.argv.json"), append([]string{"/inputs/bor", "server"}, args...)); err != nil {
		return err
	}
	configOut, err := stream(filepath.Join(output, "server-config.toml"), 1024*1024)
	if err != nil {
		return err
	}
	defer configOut.file.Close()
	configErr, err := stream(filepath.Join(output, "config.stderr"), 1024*1024)
	if err != nil {
		return err
	}
	defer configErr.file.Close()
	configCmd := exec.CommandContext(ctx, "/inputs/bor", append([]string{"dumpconfig"}, args...)...)
	configCmd.Stdout, configCmd.Stderr = configOut, configErr
	if err = configCmd.Run(); err != nil {
		return fmt.Errorf("native dumpconfig: %w", err)
	}
	stdout, err := stream(filepath.Join(output, "server.stdout"), 4*1024*1024)
	if err != nil {
		return err
	}
	defer stdout.file.Close()
	stderr, err := stream(filepath.Join(output, "server.stderr"), 4*1024*1024)
	if err != nil {
		return err
	}
	defer stderr.file.Close()
	command := exec.CommandContext(ctx, "/inputs/bor", append([]string{"server"}, args...)...)
	command.Stdout, command.Stderr = stdout, stderr
	if err = command.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	defer func() {
		var exit error
		unexpected := false
		forced := false
		select {
		case exit = <-exited:
			unexpected = true
			if returned == nil {
				returned = errors.New("server exited before owned shutdown")
			}
		default:
			_ = command.Process.Signal(os.Interrupt)
			select {
			case exit = <-exited:
			case <-time.After(5 * time.Second):
				forced = true
				_ = command.Process.Kill()
				select {
				case exit = <-exited:
				case <-time.After(5 * time.Second):
					returned = errors.New("server cleanup not confirmed")
					return
				}
			}
		}
		message := ""
		if exit != nil {
			message = exit.Error()
		}
		exitCode := command.ProcessState.ExitCode()
		if returned == nil && (exit != nil || forced || exitCode != 0) {
			returned = errors.New("native server did not stop cleanly")
		}
		if err := saveJSON(filepath.Join(output, "server-cleanup.json"), map[string]any{"pid": command.Process.Pid,
			"wait_completed": true, "unexpected_exit": unexpected, "forced_kill": forced, "exit_code": exitCode, "exit": message}); err != nil {
			returned = err
		}
	}()
	var conn net.Conn
	readiness, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		conn, err = (&net.Dialer{}).DialContext(readiness, "unix", filepath.Join(output, "bor.ipc"))
		if err == nil {
			break
		}
		select {
		case <-readiness.Done():
			return fmt.Errorf("IPC readiness: %w", readiness.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("missing operation deadline")
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	requests, err := stream(filepath.Join(output, "rpc.requests"), 1024*1024)
	if err != nil {
		return err
	}
	defer requests.file.Close()
	responses, err := stream(filepath.Join(output, "rpc.responses"), 16*1024*1024)
	if err != nil {
		return err
	}
	defer responses.file.Close()
	client, err := rpc.DialIO(ctx, &boundedReader{conn: conn, output: responses}, io.MultiWriter(requests, conn))
	if err != nil {
		return err
	}
	defer func() { conn.Close(); client.Close() }()
	var head hexutil.Uint64
	if err = client.CallContext(ctx, &head, "eth_blockNumber"); err != nil {
		return fmt.Errorf("read fresh native head: %w", err)
	}
	if head != 0 {
		return errors.New("server did not start at fresh head zero")
	}
	var rawGenesis hexutil.Bytes
	if err = client.CallContext(ctx, &rawGenesis, "debug_getRawBlock", "0x0"); err != nil {
		return fmt.Errorf("read native genesis bytes: %w", err)
	}
	var block0 types.Block
	if err = rlp.DecodeBytes(rawGenesis, &block0); err != nil {
		return err
	}
	if block0.Hash() != genesis.ToBlock().Hash() {
		return errors.New("server genesis mismatch")
	}
	var nodeInfo json.RawMessage
	if err = client.CallContext(ctx, &nodeInfo, "admin_nodeInfo"); err != nil {
		return fmt.Errorf("read native node configuration: %w", err)
	}
	var info struct {
		Protocols struct {
			Eth struct {
				Network uint64             `json:"network"`
				Config  params.ChainConfig `json:"config"`
			} `json:"eth"`
		} `json:"protocols"`
	}
	if err = json.Unmarshal(nodeInfo, &info); err != nil {
		return err
	}
	nativeConfig, err := json.Marshal(&info.Protocols.Eth.Config)
	if err != nil {
		return err
	}
	wantedConfig, err := json.Marshal(genesis.Config)
	if err != nil {
		return err
	}
	if info.Protocols.Eth.Network != 15001 || !bytes.Equal(nativeConfig, wantedConfig) {
		return errors.New("server native config mismatch")
	}
	validatorABI := borabi.ValidatorSet()
	validatorCall, err := validatorABI.Pack("getBorValidators", big.NewInt(0))
	if err != nil {
		return fmt.Errorf("pack native genesis validator call: %w", err)
	}
	var validatorResult hexutil.Bytes
	if err = client.CallContext(ctx, &validatorResult, "eth_call", map[string]any{
		"to": genesis.Config.Bor.ValidatorContract, "data": hexutil.Bytes(validatorCall), "gas": borabi.SystemTxGas}, "0x0"); err != nil {
		return fmt.Errorf("IPC genesis validator contract call: %w", err)
	}
	addresses, powers := new([]common.Address), new([]*big.Int)
	if err = validatorABI.UnpackIntoInterface(&[]interface{}{addresses, powers}, "getBorValidators", validatorResult); err != nil {
		return fmt.Errorf("decode native genesis validators: %w", err)
	}
	if len(*addresses) != 1 || len(*powers) != 1 || (*addresses)[0] != common.HexToAddress(signerAddress) || (*powers)[0].Sign() <= 0 {
		return errors.New("server genesis validator mismatch")
	}
	validators := []map[string]any{{"address": lower((*addresses)[0]), "voting_power": (*powers)[0].String()}}
	if err = saveJSON(filepath.Join(output, "readiness.json"), map[string]any{"head": 0, "genesis_hash": block0.Hash().Hex(), "node_info": nodeInfo, "validators": validators}); err != nil {
		return err
	}
	var imported bool
	if err = client.CallContext(ctx, &imported, "admin_importChain", blocksPath); err != nil {
		return fmt.Errorf("native IPC import: %w", err)
	}
	if !imported {
		return errors.New("native IPC import returned false")
	}
	if err = client.CallContext(ctx, &head, "eth_blockNumber"); err != nil {
		return fmt.Errorf("read imported native head: %w", err)
	}
	if head != 2 {
		return errors.New("native import terminal head is not two")
	}
	full, err := os.OpenFile(filepath.Join(output, "full-chain.rlp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer full.Close()
	if _, err = full.Write(rawGenesis); err != nil {
		return err
	}
	blocks := make([]map[string]any, 0, 2)
	for number := uint64(1); number <= 2; number++ {
		arg := hexutil.EncodeUint64(number)
		var raw hexutil.Bytes
		if err = client.CallContext(ctx, &raw, "debug_getRawBlock", arg); err != nil {
			return fmt.Errorf("read imported block %d: %w", number, err)
		}
		var block types.Block
		if err = rlp.DecodeBytes(raw, &block); err != nil {
			return err
		}
		if block.NumberU64() != number {
			return errors.New("native block number mismatch")
		}
		if _, err = full.Write(raw); err != nil {
			return err
		}
		var author common.Address
		if err = client.CallContext(ctx, &author, "bor_getAuthor", arg); err != nil {
			return fmt.Errorf("read native author for block %d: %w", number, err)
		}
		var rawReceipts []hexutil.Bytes
		if err = client.CallContext(ctx, &rawReceipts, "debug_getRawReceipts", arg); err != nil {
			return fmt.Errorf("read native body receipts for block %d: %w", number, err)
		}
		if len(rawReceipts) != len(block.Transactions()) {
			return errors.New("native body receipt count mismatch")
		}
		if err = saveJSON(filepath.Join(output, fmt.Sprintf("receipts-%d.json", number)), rawReceipts); err != nil {
			return err
		}
		receipts := make(types.Receipts, len(rawReceipts))
		for i, raw := range rawReceipts {
			receipts[i] = new(types.Receipt)
			if err = receipts[i].UnmarshalBinary(raw); err != nil {
				return err
			}
		}
		if types.DeriveSha(block.Transactions(), trie.NewStackTrie(nil)) != block.TxHash() || types.DeriveSha(receipts, trie.NewStackTrie(nil)) != block.ReceiptHash() {
			return errors.New("native transaction/receipt root mismatch")
		}
		if err = receipts.DeriveFields(genesis.Config, block.Hash(), number, block.Time(), block.BaseFee(), nil, block.Transactions()); err != nil {
			return err
		}
		txs, receiptRows := make([]map[string]any, 0), make([]map[string]any, 0)
		for i, tx := range block.Transactions() {
			if tx.To() == nil {
				return errors.New("unexpected creation")
			}
			from, err := types.Sender(types.MakeSigner(genesis.Config, block.Number(), block.Time()), tx)
			if err != nil {
				return err
			}
			txs = append(txs, map[string]any{"hash": tx.Hash().Hex(), "index": i, "type": tx.Type(), "from": lower(from), "to": lower(*tx.To()), "nonce": tx.Nonce(), "gas": fmt.Sprint(tx.Gas()), "gas_price": tx.GasPrice().String(), "value": tx.Value().String(), "input": hexutil.Encode(tx.Data())})
			r := receipts[i]
			logs := make([]map[string]any, 0)
			for _, log := range r.Logs {
				topics := make([]string, len(log.Topics))
				for j, t := range log.Topics {
					topics[j] = t.Hex()
				}
				logs = append(logs, map[string]any{"address": lower(log.Address), "topics": topics, "data": hexutil.Encode(log.Data), "transaction_hash": log.TxHash.Hex(), "transaction_index": log.TxIndex, "block_hash": log.BlockHash.Hex(), "block_number": log.BlockNumber, "log_index": log.Index, "removed": log.Removed})
			}
			var contract any
			if r.ContractAddress != (common.Address{}) {
				contract = lower(r.ContractAddress)
			}
			if r.EffectiveGasPrice == nil {
				return errors.New("missing derived receipt gas price")
			}
			receiptRows = append(receiptRows, map[string]any{"transaction_hash": r.TxHash.Hex(), "transaction_index": r.TransactionIndex, "block_hash": r.BlockHash.Hex(), "block_number": r.BlockNumber.Uint64(), "status": r.Status, "type": r.Type, "gas_used": r.GasUsed, "cumulative_gas_used": r.CumulativeGasUsed, "effective_gas_price": r.EffectiveGasPrice.String(), "contract_address": contract, "logs": logs})
		}
		var dump json.RawMessage
		if err = client.CallContext(ctx, &dump, "debug_dumpBlock", arg); err != nil {
			return fmt.Errorf("dump imported state at block %d: %w", number, err)
		}
		var state struct {
			Next     string                     `json:"next"`
			Accounts map[string]json.RawMessage `json:"accounts"`
		}
		if err = json.Unmarshal(dump, &state); err != nil {
			return err
		}
		if state.Next != "" || state.Accounts == nil {
			return errors.New("incomplete native state dump")
		}
		for address := range genesis.Alloc {
			found := false
			for key := range state.Accounts {
				if strings.EqualFold(key, address.Hex()) {
					found = true
				}
			}
			if !found {
				return errors.New("native dump omitted genesis account")
			}
		}
		if err = saveJSON(filepath.Join(output, fmt.Sprintf("state-%d.json", number)), dump); err != nil {
			return err
		}
		blocks = append(blocks, map[string]any{"number": number, "hash": block.Hash().Hex(), "parent_hash": block.ParentHash().Hex(), "state_root": block.Root().Hex(), "timestamp": block.Time(), "coinbase": lower(block.Coinbase()), "author": lower(author), "gas_limit": block.GasLimit(), "gas_used": block.GasUsed(), "base_fee": block.BaseFee().String(), "withdrawals": len(block.Withdrawals()), "transactions": txs, "receipts": receiptRows})
	}
	if err = full.Sync(); err != nil {
		return err
	}
	return saveJSON(filepath.Join(output, "observed.json"), map[string]any{"schema_version": 1, "chain_id": 15001, "genesis_hash": block0.Hash().Hex(), "config_sha256": configDigest, "head_number": 2, "head_hash": blocks[1]["hash"], "blocks": blocks})
}

func verifyIPCShutdown(ctx context.Context) error {
	conn, peer := net.Pipe()
	defer peer.Close()
	defer conn.Close()
	client, err := rpc.DialIO(ctx, conn, conn)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { conn.Close(); client.Close(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		conn.Close()
		peer.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		return errors.New("native IPC read loop did not stop within two seconds")
	}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	var err error
	switch {
	case len(os.Args) == 3 && os.Args[1] == "verify-key":
		_, err = testKey(os.Args[2])
		if err == nil {
			err = verifyIPCShutdown(ctx)
		}
		if err == nil {
			fmt.Println(signerAddress)
		}
	case len(os.Args) == 5 && os.Args[1] == "generate":
		err = generate(ctx, os.Args[2], os.Args[3], os.Args[4])
	case len(os.Args) == 5 && os.Args[1] == "observe":
		err = observe(ctx, os.Args[2], os.Args[3], os.Args[4])
	default:
		err = errors.New("unsupported helper mode or arguments")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
