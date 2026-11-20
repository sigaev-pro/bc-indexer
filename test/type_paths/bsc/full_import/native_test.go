package bscdecision

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	"github.com/ethereum/go-ethereum/consensus/parlia"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/systemcontracts"
	"github.com/ethereum/go-ethereum/core/systemcontracts/lorentz"
	"github.com/ethereum/go-ethereum/core/systemcontracts/pascal"
	"github.com/ethereum/go-ethereum/core/systemcontracts/pasteur"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/extractor/admission"
	"github.com/ethereum/go-ethereum/extractor/bsc"
	stateprovider "github.com/ethereum/go-ethereum/extractor/state_provider"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
	"github.com/prysmaticlabs/prysm/v5/crypto/bls"
)

const modernGenesisTime uint64 = 1794959990 // Private fixture: 2026-11-17 23:59:50 UTC.

var (
	modernSuccess           = common.HexToAddress("0x3000000000000000000000000000000000000001")
	modernRevert            = common.HexToAddress("0x3000000000000000000000000000000000000002")
	modernValidatorContract = common.HexToAddress("0x1000")
	modernStakeHub          = common.HexToAddress("0x2002")
)

func modernKey(t *testing.T, value byte) *ecdsa.PrivateKey {
	t.Helper()
	b := make([]byte, 32)
	b[31] = value
	k, err := crypto.ToECDSA(b)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func modernCodes() map[string]string {
	return map[string]string{
		"0x1000": lorentz.MainnetValidatorContract, "0x1001": pascal.MainnetSlashContract,
		"0x1002": pascal.MainnetSystemRewardContract, "0x1003": pascal.MainnetLightClientContract,
		"0x1004": pascal.MainnetTokenHubContract, "0x1005": pascal.MainnetRelayerIncentivizeContract,
		"0x1006": pascal.MainnetRelayerHubContract, "0x1007": pascal.MainnetGovHubContract,
		"0x1008": pascal.MainnetTokenManagerContract, "0x2000": pascal.MainnetCrossChainContract,
		"0x2001": pascal.MainnetStakingContract, "0x2002": pasteur.MainnetStakeHubContract,
		"0x2003": pascal.MainnetStakeCreditContract, "0x2004": pasteur.MainnetGovernorContract,
		"0x2005": pascal.MainnetGovTokenContract, "0x2006": pascal.MainnetTimelockContract,
		"0x3000": pascal.MainnetTokenRecoverPortalContract,
	}
}

func modernGenesis(t *testing.T, validator common.Address, vote []byte) *core.Genesis {
	t.Helper()
	encoded, err := json.Marshal(params.BSCChainConfig)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for key, value := range fields {
		if value == nil {
			continue
		}
		if strings.HasSuffix(key, "Block") {
			fields[key] = 0
		}
		if strings.HasSuffix(key, "Time") {
			fields[key] = modernGenesisTime + 6
		}
	}
	fields["chainId"], fields["shanghaiTime"], fields["keplerTime"] = 714, 0, 0
	fields["jennerTime"] = modernGenesisTime + 864000
	encoded, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	config := new(params.ChainConfig)
	if err := json.Unmarshal(encoded, config); err != nil {
		t.Fatal(err)
	}
	if err := config.CheckConfigForkOrder(); err != nil {
		t.Fatal(err)
	}
	alloc := types.GenesisAlloc{}
	for text, encoded := range modernCodes() {
		code, err := hex.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			t.Fatal(err)
		}
		alloc[common.HexToAddress(text)] = types.Account{Balance: big.NewInt(0), Code: code}
	}
	owner := crypto.PubkeyToAddress(modernKey(t, 1).PublicKey)
	alloc[owner] = types.Account{Balance: new(big.Int).Mul(big.NewInt(100000), big.NewInt(params.Ether))}
	alloc[modernSuccess] = types.Account{Balance: big.NewInt(0), Code: common.FromHex("0x600160005500")}
	alloc[modernRevert] = types.Account{Balance: big.NewInt(0), Code: common.FromHex("0x600260005560006000fd")}
	extra := append(make([]byte, 32), byte(1))
	extra = append(extra, validator.Bytes()...)
	extra = append(extra, vote...)
	extra = append(extra, make([]byte, 65)...)
	return &core.Genesis{Config: config, Timestamp: modernGenesisTime, ExtraData: extra,
		GasLimit: 40000000, Difficulty: big.NewInt(1), Alloc: alloc}
}

const modernRegisterABI = `[{"type":"function","name":"createValidator","inputs":[{"name":"consensusAddress","type":"address"},{"name":"voteAddress","type":"bytes"},{"name":"blsProof","type":"bytes"},{"name":"commission","type":"tuple","components":[{"name":"rate","type":"uint64"},{"name":"maxRate","type":"uint64"},{"name":"maxChangeRate","type":"uint64"}]},{"name":"description","type":"tuple","components":[{"name":"moniker","type":"string"},{"name":"identity","type":"string"},{"name":"website","type":"string"},{"name":"details","type":"string"}]}]}]`

func modernRegistration(t *testing.T, config *params.ChainConfig, validator common.Address, vote, proof []byte, moniker string) *types.Transaction {
	t.Helper()
	a, err := abi.JSON(strings.NewReader(modernRegisterABI))
	if err != nil {
		t.Fatal(err)
	}
	commission := struct{ Rate, MaxRate, MaxChangeRate uint64 }{100, 1000, 100}
	description := struct{ Moniker, Identity, Website, Details string }{moniker, "", "", "bounded fixture"}
	data, err := a.Pack("createValidator", validator, vote, proof, commission, description)
	if err != nil {
		t.Fatal(err)
	}
	value := new(big.Int).Mul(big.NewInt(2001), big.NewInt(params.Ether))
	tx, err := types.SignTx(types.NewTransaction(0, modernStakeHub, value, 8000000, big.NewInt(2000000000), data),
		types.LatestSigner(config), modernKey(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// Genuine operator calls materialize legacy validator-extra storage through
// StakeHub's self-delegation check, then restore the active election stake.
func modernBootstrapTx(t *testing.T, config *params.ChainConfig, restore bool) *types.Transaction {
	t.Helper()
	owner := crypto.PubkeyToAddress(modernKey(t, 1).PublicKey)
	nonce, value := uint64(1), big.NewInt(0)
	data := append(common.FromHex("0x4d99dd16"), common.LeftPadBytes(owner.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(big.NewInt(params.Ether).Bytes(), 32)...)
	if restore {
		nonce, value = 2, big.NewInt(params.Ether)
		data = append(common.FromHex("0x982ef0a7"), common.LeftPadBytes(owner.Bytes(), 32)...)
		data = append(data, make([]byte, 32)...)
	}
	tx, err := types.SignTx(types.NewTransaction(nonce, modernStakeHub, value, 2000000, big.NewInt(2000000000), data), types.LatestSigner(config), modernKey(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

// These small ordered values exercise trie branches absent from full-size
// transaction vectors. Both independent native trie implementations must agree.
type modernTrieValues [][]byte

func (values modernTrieValues) Len() int { return len(values) }
func (values modernTrieValues) EncodeIndex(index int, buffer *bytes.Buffer) {
	buffer.Write(values[index])
}

func modernTrieVectors(t *testing.T, work string) {
	rows := []map[string]any{}
	for _, count := range []int{0, 1, 2, 16, 128, 129, 300} {
		values := make(modernTrieValues, count)
		encoded := []string{}
		for i := range values {
			values[i] = []byte{byte(i % 251)}
			encoded = append(encoded, hexOf(values[i]))
		}
		stack := types.DeriveSha(values, trie.NewStackTrie(nil))
		plain := types.DeriveSha(values, trie.NewListHasher())
		if stack != plain {
			t.Fatal("native ordered trie implementations disagree")
		}
		rows = append(rows, map[string]any{"count": count, "values": encoded, "root": stack})
	}
	writeJSON(t, filepath.Join(work, "native-trie-vectors.json"), rows)
}

type modernExit struct {
	Output   string `json:"output"`
	Error    any    `json:"error"`
	Reverted bool   `json:"reverted"`
	GasUsed  uint64 `json:"gas_used"`
}

func (result *modernExit) hook(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	if depth == 0 {
		*result = modernExit{Output: hexOf(output), Error: errText(err), Reverted: reverted, GasUsed: gasUsed}
	}
}

func modernUserTx(t *testing.T, config *params.ChainConfig, kind uint8, nonce uint64, failed bool, blobHash common.Hash) *types.Transaction {
	t.Helper()
	to := modernSuccess
	if failed {
		to = modernRevert
	}
	access := types.AccessList{{Address: to, StorageKeys: []common.Hash{{}, {}}}, {Address: to}}
	var body types.TxData
	switch kind {
	case 0:
		body = &types.LegacyTx{Nonce: nonce, To: &to, Value: big.NewInt(1), Gas: 150000, GasPrice: big.NewInt(2000000000)}
	case 1:
		body = &types.AccessListTx{ChainID: config.ChainID, Nonce: nonce, To: &to, Value: big.NewInt(1), Gas: 150000, GasPrice: big.NewInt(2000000000), AccessList: access}
	case 2:
		body = &types.DynamicFeeTx{ChainID: config.ChainID, Nonce: nonce, To: &to, Value: big.NewInt(1), Gas: 150000, GasFeeCap: big.NewInt(2000000000), GasTipCap: big.NewInt(1000000000), AccessList: access}
	case 3:
		body = &types.BlobTx{ChainID: uint256.MustFromBig(config.ChainID), Nonce: nonce, To: to, Value: uint256.NewInt(1), Gas: 150000, GasFeeCap: uint256.NewInt(2000000000), GasTipCap: uint256.NewInt(1000000000), BlobFeeCap: uint256.NewInt(1000000000), BlobHashes: []common.Hash{blobHash}, AccessList: access}
	case 4:
		authNonce := uint64(0)
		if failed {
			authNonce = 1
		}
		authority := crypto.PubkeyToAddress(modernKey(t, 2).PublicKey)
		auth, err := types.SignSetCode(modernKey(t, 2), types.SetCodeAuthorization{ChainID: *uint256.MustFromBig(config.ChainID), Address: to, Nonce: authNonce})
		if err != nil {
			t.Fatal(err)
		}
		body = &types.SetCodeTx{ChainID: uint256.MustFromBig(config.ChainID), Nonce: nonce, To: authority, Value: uint256.NewInt(1), Gas: 150000, GasFeeCap: uint256.NewInt(2000000000), GasTipCap: uint256.NewInt(1000000000), AccessList: access, AuthList: []types.SetCodeAuthorization{auth}}
	default:
		t.Fatal("unknown fixture type")
	}
	tx, err := types.SignTx(types.NewTx(body), types.LatestSigner(config), modernKey(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func modernGenerate(t *testing.T, work string) fixture {
	t.Helper()
	validatorKey := modernKey(t, 3)
	validator := crypto.PubkeyToAddress(validatorKey.PublicKey)
	blsBytes := make([]byte, 32)
	blsBytes[31] = 4
	blsKey, err := bls.SecretKeyFromBytes(blsBytes)
	if err != nil {
		t.Fatal(err)
	}
	vote := blsKey.PublicKey().Marshal()
	genesis := modernGenesis(t, validator, vote)
	genesisBytes, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeNew(t, filepath.Join(work, "genesis.json"), genesisBytes)
	owner := crypto.PubkeyToAddress(modernKey(t, 1).PublicKey)
	message := crypto.Keccak256(append(append(owner.Bytes(), vote...), common.LeftPadBytes(genesis.Config.ChainID.Bytes(), 32)...))
	proof := blsKey.Sign(message).Marshal()
	writeJSON(t, filepath.Join(work, "registration-input.json"), map[string]any{"operator": owner, "consensus": validator, "vote": hexOf(vote), "bls_proof": hexOf(proof), "message": hexOf(message), "value": "2001000000000000000000", "moniker": "Extractor"})
	db := rawdb.NewMemoryDatabase()
	defer db.Close()
	api, backend := bsc.NewSystemCallAPI()
	engine := parlia.New(genesis.Config, db, api, genesis.ToBlock().Hash())
	defer engine.Close()
	engine.Authorize(validator,
		func(account accounts.Account, mime string, data []byte) ([]byte, error) {
			if account.Address != validator || mime != accounts.MimetypeParlia {
				return nil, fmt.Errorf("unexpected signer")
			}
			return crypto.Sign(crypto.Keccak256(data), validatorKey)
		}, func(account accounts.Account, tx *types.Transaction, id *big.Int) (*types.Transaction, error) {
			if account.Address != validator || id.Cmp(big.NewInt(714)) != 0 {
				return nil, fmt.Errorf("unexpected signer")
			}
			return types.SignTx(tx, types.LatestSigner(genesis.Config), validatorKey)
		})
	cfg := core.DefaultConfig()
	cfg.ArchiveMode, cfg.SnapshotLimit = true, 0
	chain, err := core.NewBlockChain(db, genesis, engine, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer chain.Stop()
	backend.Bind(chain, nil)
	var blob kzg4844.Blob
	commitment, err := kzg4844.BlobToCommitment(&blob)
	if err != nil {
		t.Fatal(err)
	}
	blobProof, err := kzg4844.ComputeBlobProof(&blob, commitment)
	if err != nil {
		t.Fatal(err)
	}
	if err := kzg4844.VerifyBlobProof(&blob, commitment, blobProof); err != nil {
		t.Fatal(err)
	}
	blobHash := kzg4844.CalcBlobHashV1(sha256.New(), &commitment)
	writeJSON(t, filepath.Join(work, "blob-input.json"), map[string]any{"zero_blob": true, "commitment": hexOf(commitment[:]), "proof": hexOf(blobProof[:]), "versioned_hash": hexOf(blobHash[:])})
	fx := fixture{genesisFile: genesisBytes, genesis: genesis, commit: probeEnv(t, envCommit)}
	rows := []map[string]any{}
	registered, elected := false, false
	targets := 0
	for number := uint64(1); number <= 40 && targets < 2; number++ {
		parent := chain.CurrentBlock()
		header := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).SetUint64(number), GasLimit: parent.GasLimit, Time: parent.Time + 3, Difficulty: big.NewInt(1)}
		if err := engine.Prepare(chain, header); err != nil {
			t.Fatal(err)
		}
		// Prepare is a live mining helper and clamps historical time to now.
		// This single in-turn validator uses the parent snapshot's native interval;
		// Seal and InsertChain still enforce every ordinary consensus check.
		interval := uint64(3000)
		if genesis.Config.IsFermi(parent.Number, parent.Time) {
			interval = 450
		}
		millis := parent.MilliTimestamp() + interval
		header.Time = millis / 1000
		header.MixDigest = common.Hash{}
		if genesis.Config.IsLorentz(header.Number, header.Time) {
			header.SetMilliseconds(millis % 1000)
		}
		if err := engine.SetExtraData(chain, header); err != nil {
			t.Fatal(err)
		}
		header.BaseFee = eip1559.CalcBaseFee(genesis.Config, parent)
		body := &types.Body{}
		if genesis.Config.IsCancun(header.Number, header.Time) {
			zero, excess := uint64(0), eip4844.CalcExcessBlobGas(genesis.Config, parent, header.Time)
			header.BlobGasUsed, header.ExcessBlobGas = &zero, &excess
			body.Withdrawals = []*types.Withdrawal{}
		}
		if genesis.Config.IsBohr(header.Number, header.Time) {
			zero := common.Hash{}
			header.ParentBeaconRoot = &zero
		}
		if genesis.Config.IsPrague(header.Number, header.Time) {
			hash := types.EmptyRequestsHash
			header.RequestsHash = &hash
		}
		st, err := chain.StateAt(parent)
		if err != nil {
			t.Fatal(err)
		}
		systemcontracts.TryUpdateBuildInSystemContract(genesis.Config, header.Number, parent.Time, header.Time, st, true)
		var executionExit modernExit
		evm := vm.NewEVM(core.NewEVMBlockContext(header, chain, &validator), st, genesis.Config, vm.Config{Tracer: &tracing.Hooks{OnExit: executionExit.hook}})
		if header.ParentBeaconRoot != nil {
			core.ProcessBeaconBlockRoot(*header.ParentBeaconRoot, evm)
		}
		if genesis.Config.IsPrague(header.Number, header.Time) {
			core.ProcessParentBlockHash(parent.Hash(), evm)
		}
		users := []*types.Transaction{}
		phase := "warmup"
		if number == 3 {
			// Reproduce the prior failure on a separate state copy. No invalid
			// registration is inserted into the canonical or captured chain.
			priorRoot := st.Copy().IntermediateRoot(true)
			priorHead := chain.CurrentBlock().Hash()
			control := st.Copy()
			bad := modernRegistration(t, genesis.Config, validator, vote, proof, "extractor-private")
			control.SetTxContext(bad.Hash(), 0)
			var rejectedExit modernExit
			controlHeader := types.CopyHeader(header)
			controlEVM := vm.NewEVM(core.NewEVMBlockContext(controlHeader, chain, &validator), control, genesis.Config, vm.Config{Tracer: &tracing.Hooks{OnExit: rejectedExit.hook}})
			receipt, err := core.ApplyTransaction(controlEVM, core.NewGasPool(header.GasLimit), control, controlHeader, bad, core.NewReceiptBloomGenerator())
			controlEVM.Release()
			afterRoot, afterHead := st.Copy().IntermediateRoot(true), chain.CurrentBlock().Hash()
			badWire, wireErr := bad.MarshalBinary()
			if wireErr != nil {
				t.Fatal(wireErr)
			}
			writeJSON(t, filepath.Join(work, "invalid-moniker-control.json"), map[string]any{"wire": hexOf(badWire), "moniker": "extractor-private", "receipt": receipt, "apply_error": errText(err), "exit": rejectedExit, "prior_state_root": priorRoot, "after_state_root": afterRoot, "prior_head": priorHead, "after_head": afterHead, "capture_started": false})
			if err != nil || receipt == nil || receipt.Status != 0 || rejectedExit.Output != "5dba5ad7" || priorRoot != afterRoot || priorHead != afterHead {
				t.Fatalf("invalid moniker control: receipt=%v err=%v exit=%+v", receipt, err, rejectedExit)
			}
			users = append(users, modernRegistration(t, genesis.Config, validator, vote, proof, "Extractor"))
			phase = "registration"
		} else if number == 4 || number == 5 {
			users = append(users, modernBootstrapTx(t, genesis.Config, number == 5))
			phase = "bootstrap-undelegate"
			if number == 5 {
				phase = "bootstrap-delegate"
			}
		} else if elected && eip4844.IsBlobEligibleBlock(genesis.Config, header.Number.Uint64(), header.Time) {
			// BEP-657 allows blob transactions only in eligible blocks
			// (every fifth block), so the type matrix lands on those heights;
			// the blocks between stay warmup.
			if !genesis.Config.IsPasteur(header.Number, header.Time) || genesis.Config.IsJenner(header.Number, header.Time) {
				t.Fatal("target not in full current fork interval")
			}
			phase = "success"
			if targets == 1 {
				phase = "revert"
			}
			for kind := uint8(0); kind < 5; kind++ {
				users = append(users, modernUserTx(t, genesis.Config, kind, 3+uint64(targets)*5+uint64(kind), targets == 1, blobHash))
			}
			targets++
		}
		pool := core.NewGasPool(header.GasLimit)
		receipts := []*types.Receipt{}
		for _, tx := range users {
			bootstrapAddresses := []common.Address{owner, validator, modernStakeHub, modernValidatorContract, crypto.CreateAddress(modernStakeHub, 0), common.HexToAddress("0x2005"), common.HexToAddress("0xdead"), consensus.SystemAddress}
			userBefore := modernState(st, bootstrapAddresses, validator)
			executionExit = modernExit{}
			st.SetTxContext(tx.Hash(), len(body.Transactions))
			receipt, err := core.ApplyTransaction(evm, pool, st, header, tx, core.NewReceiptBloomGenerator())
			if err != nil {
				t.Fatalf("apply %s: %v", phase, err)
			}
			want := uint64(1)
			if phase == "revert" {
				want = 0
			}
			if receipt.Status != want {
				writeJSON(t, filepath.Join(work, "failed-user-execution.json"), map[string]any{"phase": phase, "type": tx.Type(), "receipt": receipt, "exit": executionExit})
				t.Fatalf("%s type%d status %d exit=%+v", phase, tx.Type(), receipt.Status, executionExit)
			}
			if phase == "registration" || strings.HasPrefix(phase, "bootstrap-") {
				raw, wireErr := tx.MarshalBinary()
				if wireErr != nil {
					t.Fatal(wireErr)
				}
				writeJSON(t, filepath.Join(work, phase+"-state.json"), map[string]any{"block": number, "time": header.Time, "wire": hexOf(raw), "before": userBefore, "after": modernState(st, bootstrapAddresses, validator), "receipt": receipt, "exit": executionExit})
			}
			body.Transactions = append(body.Transactions, tx)
			receipts = append(receipts, receipt)
			if tx.Type() == types.BlobTxType {
				*header.BlobGasUsed += params.BlobTxBlobGasPerBlob
			}
		}
		header.GasUsed = pool.Used()
		evm.Release()
		block, _, err := core.AssembleBlock(engine, chain, header, st, body, receipts)
		if err != nil {
			t.Fatalf("assemble %d: %v", number, err)
		}
		result, stop := make(chan *types.Block, 1), make(chan struct{})
		if err := engine.Seal(chain, block, result, stop); err != nil {
			t.Fatal(err)
		}
		select {
		case block = <-result:
		case <-time.After(30 * time.Second):
			close(stop)
			t.Fatal("seal deadline")
		}
		close(stop)
		// BSC carries blob sidecars with the block body: the data-availability
		// check of InsertChain requires one verified sidecar per blob
		// transaction, bound to this block and the transaction's position.
		var sidecars types.BlobSidecars
		for i, tx := range body.Transactions {
			if tx.Type() != types.BlobTxType {
				continue
			}
			sidecars = append(sidecars, &types.BlobSidecar{
				BlobTxSidecar: types.BlobTxSidecar{
					Blobs:       []kzg4844.Blob{blob},
					Commitments: []kzg4844.Commitment{commitment},
					Proofs:      []kzg4844.Proof{blobProof},
				},
				BlockNumber: block.Number(),
				BlockHash:   block.Hash(),
				TxIndex:     uint64(i),
				TxHash:      tx.Hash(),
			})
		}
		if len(sidecars) > 0 {
			block = block.WithSidecars(sidecars)
		}
		if n, err := chain.InsertChain(types.Blocks{block}); err != nil || n != 1 {
			t.Fatalf("insert %d: %d %v", number, n, err)
		}
		fx.blocks = append(fx.blocks, block)
		current, err := chain.StateAt(block.Header())
		if err != nil {
			t.Fatal(err)
		}
		mapKey := crypto.Keccak256Hash(common.LeftPadBytes(validator.Bytes(), 32), common.LeftPadBytes([]byte{4}, 32))
		index := current.GetState(modernValidatorContract, mapKey).Big()
		if number == 3 {
			registered = true
		}
		if registered && index.Cmp(big.NewInt(1)) == 0 {
			elected = true
		}
		wire, err := rlp.EncodeToBytes(block)
		if err != nil {
			t.Fatal(err)
		}
		row := map[string]any{"number": number, "time": block.Time(), "hash": block.Hash(), "root": block.Root(), "phase": phase, "validator_index": index.String(), "transactions": len(block.Transactions()), "validator_extra_count": current.GetState(modernValidatorContract, common.BigToHash(big.NewInt(11))).Big().String(), "wire": hexOf(wire)}
		rows = append(rows, row)
		writeJSON(t, filepath.Join(work, fmt.Sprintf("generated-%02d.json", number)), row)
	}
	if !registered || !elected || targets != 2 {
		t.Fatalf("incomplete bootstrap: registered=%v elected=%v targets=%d", registered, elected, targets)
	}
	var encoded bytes.Buffer
	for _, block := range fx.blocks {
		if err := rlp.Encode(&encoded, block); err != nil {
			t.Fatal(err)
		}
	}
	writeNew(t, filepath.Join(work, "blocks.rlp"), encoded.Bytes())
	writeJSON(t, filepath.Join(work, "generated.json"), rows)
	return fx
}

func modernState(st tracing.StateDB, addresses []common.Address, validator common.Address) map[string]any {
	out := map[string]any{}
	operator := addresses[0]
	credit := crypto.CreateAddress(modernStakeHub, 0)
	mappingKey := func(key common.Address, slot int64) common.Hash {
		return crypto.Keccak256Hash(common.LeftPadBytes(key.Bytes(), 32), common.LeftPadBytes(big.NewInt(slot).Bytes(), 32))
	}
	for _, address := range addresses {
		row := map[string]any{"balance": st.GetBalance(address).String(), "nonce": st.GetNonce(address), "code_hash": st.GetCodeHash(address)}
		slots := map[string]string{}
		for _, n := range []int64{0, 1, 2, 3, 4, 5, 6, 11, 15, 20, 23, 24, 51} {
			slots[fmt.Sprint(n)] = st.GetState(address, common.BigToHash(big.NewInt(n))).Hex()
		}
		if address == common.HexToAddress("0x1004") {
			slots["native_decimals"] = st.GetState(address, mappingKey(common.Address{}, 2)).Hex()
		}
		if address == modernValidatorContract {
			key := crypto.Keccak256Hash(common.LeftPadBytes(validator.Bytes(), 32), common.LeftPadBytes([]byte{4}, 32))
			slots["validator_index"] = st.GetState(address, key).Hex()
			incoming := new(big.Int).Add(crypto.Keccak256Hash(common.LeftPadBytes([]byte{1}, 32)).Big(), big.NewInt(3))
			slots["first_validator_incoming"] = st.GetState(address, common.BigToHash(incoming)).Hex()
			extraBase := crypto.Keccak256Hash(common.LeftPadBytes([]byte{11}, 32)).Big()
			for i, name := range []string{"extra_enter_height", "extra_maintaining", "extra_vote"} {
				slots[name] = st.GetState(address, common.BigToHash(new(big.Int).Add(extraBase, big.NewInt(int64(i))))).Hex()
			}
			voteBase := crypto.Keccak256Hash(common.BigToHash(new(big.Int).Add(extraBase, big.NewInt(2))).Bytes()).Big()
			slots["extra_vote_first"] = st.GetState(address, common.BigToHash(voteBase)).Hex()
			slots["extra_vote_second"] = st.GetState(address, common.BigToHash(new(big.Int).Add(voteBase, big.NewInt(1)))).Hex()

		}
		if address == modernStakeHub {
			for _, n := range []int64{53, 54, 55, 56, 57, 63, 73, 74} {
				slots[fmt.Sprint(n)] = st.GetState(address, common.BigToHash(big.NewInt(n))).Hex()
			}
			slots["operator_index"] = st.GetState(address, mappingKey(operator, 64)).Hex()
			slots["consensus_operator"] = st.GetState(address, mappingKey(validator, 67)).Hex()
			base := mappingKey(operator, 65).Big()
			for i, name := range []string{"consensus", "operator", "credit", "created", "vote", "moniker", "identity", "website", "details", "commission", "jailed", "jail_until", "updated", "agent"} {
				key := common.BigToHash(new(big.Int).Add(base, big.NewInt(int64(i))))
				slots["validator_"+name] = st.GetState(address, key).Hex()
			}
			for _, offset := range []int64{10, 11, 12, 13} {
				key := common.BigToHash(new(big.Int).Add(base, big.NewInt(offset)))
				slots[fmt.Sprintf("validator_word_%d", offset)] = st.GetState(address, key).Hex()
			}
		}
		if address == common.HexToAddress("0x2005") {
			slots["gov_supply"] = st.GetState(address, common.BigToHash(big.NewInt(53))).Hex()
			slots["gov_operator"] = st.GetState(address, mappingKey(operator, 51)).Hex()
			slots["gov_dead"] = st.GetState(address, mappingKey(common.HexToAddress("0xdead"), 51)).Hex()
		}
		if address == credit {
			for _, n := range []int64{103, 151, 152} {
				slots[fmt.Sprint(n)] = st.GetState(address, common.BigToHash(big.NewInt(n))).Hex()
			}
			requestHash := crypto.Keccak256Hash(append(operator.Bytes(), make([]byte, 32)...))
			requestBase := crypto.Keccak256Hash(append(requestHash.Bytes(), common.LeftPadBytes([]byte{153}, 32)...)).Big()
			for i, name := range []string{"unbond_shares", "unbond_amount", "unbond_unlock"} {
				slots[name] = st.GetState(address, common.BigToHash(new(big.Int).Add(requestBase, big.NewInt(int64(i))))).Hex()
			}
			queueBase := mappingKey(operator, 154)
			slots["unbond_queue"] = st.GetState(address, queueBase).Hex()
			queueMap := new(big.Int).Add(queueBase.Big(), big.NewInt(1))
			queueKey := crypto.Keccak256Hash(append(make([]byte, 32), common.LeftPadBytes(queueMap.Bytes(), 32)...))
			slots["unbond_first"] = st.GetState(address, queueKey).Hex()
			slots["unbond_sequence"] = st.GetState(address, mappingKey(operator, 155)).Hex()
			slots["operator_shares"] = st.GetState(address, mappingKey(operator, 101)).Hex()
			slots["locked_shares"] = st.GetState(address, mappingKey(common.HexToAddress("0xdead"), 101)).Hex()
			for _, name := range []string{"implementation", "admin"} {
				key := new(big.Int).Sub(crypto.Keccak256Hash([]byte("eip1967.proxy."+name)).Big(), big.NewInt(1))
				slots[name] = st.GetState(address, common.BigToHash(key)).Hex()
			}
		}
		row["storage"] = slots
		out[strings.ToLower(address.Hex())] = row
	}
	return out
}

func modernNode(t *testing.T, fx fixture, home string, hooks *tracing.Hooks, emit func(bsc.Event)) (*node, error) {
	n := &node{t: t, home: home}
	n.db = openDisk(t, home)
	n.store = &probeStore{db: n.db, model: modelNative, emit: emit, pending: map[string][]byte{}}
	n.mirror = probeMirror{db: n.db}
	config := core.DefaultConfig()
	config.StateScheme, config.ArchiveMode, config.SnapshotLimit = rawdb.HashScheme, false, 0
	config.VmConfig.Tracer = hooks
	api, backend := bsc.NewSystemCallAPI()
	engine := parlia.New(fx.genesis.Config, n.db, api, fx.genesis.ToBlock().Hash())
	chain, err := core.NewBlockChain(n.db, fx.genesis, engine, config)
	if err != nil {
		n.db.Close()
		return nil, err
	}
	n.chain = chain
	backend.Bind(chain, nil)
	identity := stateprovider.StoreIdentity{StoreID: probeStoreID, ChainID: "714", GenesisHash: chain.Genesis().Hash(), StateScheme: rawdb.HashScheme}
	hook, err := Start(admission.NewGuard(), n.db, chain, Config{Seam: bsc.Config{Producer: probeProducer, Identity: identity, GenesisFile: fx.genesisFile, NativeCommit: fx.commit, Store: n.store, Emit: emit}, Mirror: n.mirror})
	if err != nil {
		n.close()
		return nil, err
	}
	n.hook = hook
	backend.Bind(chain, hook.Seam())
	chain.SetExtractorHook(hook)
	return n, nil
}

func TestBSCFullTypeImport(t *testing.T) {
	work := probeEnv(t, "EXTRACTOR_MODERN_WORK")
	modernTrieVectors(t, work)
	fx := modernGenerate(t, work)
	validator := crypto.PubkeyToAddress(modernKey(t, 3).PublicKey)
	addresses := []common.Address{crypto.PubkeyToAddress(modernKey(t, 1).PublicKey), crypto.PubkeyToAddress(modernKey(t, 2).PublicKey), validator, modernSuccess, modernRevert, consensus.SystemAddress, common.HexToAddress("0xdead")}
	addresses = append(addresses, crypto.CreateAddress(modernStakeHub, 0))
	for text := range modernCodes() {
		addresses = append(addresses, common.HexToAddress(text))
	}
	rows := []map[string]any{}
	var current map[string]any
	var live tracing.StateDB
	system := false
	hooks := &tracing.Hooks{OnSystemTxStart: func() { system = true }, OnSystemTxEnd: func() { system = false }}
	hooks.OnTxStart = func(ctx *tracing.VMContext, tx *types.Transaction, from common.Address) {
		live = ctx.StateDB
		wire, err := tx.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		fields, err := tx.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		current = map[string]any{"block": ctx.BlockNumber.Uint64(), "time": ctx.Time, "system": system, "hash": tx.Hash(), "type": tx.Type(), "from": from, "to": tx.To(), "value": tx.Value().String(), "nonce": tx.Nonce(), "gas_price": tx.EffectiveGasPriceForBSC().String(), "data": hexOf(tx.Data()), "wire": hexOf(wire), "fields": json.RawMessage(fields), "before": modernState(live, addresses, validator)}
	}
	hooks.OnTxEnd = func(receipt *types.Receipt, err error) {
		if current == nil {
			t.Fatal("end without start")
		}
		current["error"] = errText(err)
		current["after"] = modernState(live, addresses, validator)
		current["receipt"] = receipt
		rows = append(rows, current)
		current = nil
	}
	tr := newTracer(t, filepath.Join(work, "trace-import.jsonl"))
	n, err := modernNode(t, fx, filepath.Join(work, "home"), hooks, tr.emit)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.hook.Recover(n.chain); err != nil {
		n.close()
		t.Fatal(err)
	}
	count, importErr := n.chain.InsertChain(fx.blocks)
	bridge := stateOf(n)
	if importErr == nil {
		byHash := map[common.Hash]*types.Receipt{}
		for _, block := range fx.blocks {
			for _, receipt := range n.chain.GetReceiptsByHash(block.Hash()) {
				byHash[receipt.TxHash] = receipt
			}
		}
		for _, row := range rows {
			observed := row["receipt"].(*types.Receipt)
			stored := byHash[observed.TxHash]
			if stored == nil {
				t.Fatal("missing stored receipt")
			}
			a, err := observed.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			b, err := stored.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(a, b) {
				t.Fatal("observed receipt differs from stored receipt")
			}
			row["observed_receipt_wire"], row["stored_receipt_wire"] = hexOf(a), hexOf(b)
		}
	}
	writeJSON(t, filepath.Join(work, "import.json"), map[string]any{"count": count, "error": errText(importErr), "bridge": bridge, "transactions": rows})
	n.close()
	if importErr != nil {
		t.Fatal(importErr)
	}
	tr2 := newTracer(t, filepath.Join(work, "trace-reimport.jsonl"))
	n, err = modernNode(t, fx, filepath.Join(work, "home"), nil, tr2.emit)
	if err != nil {
		t.Fatal(err)
	}
	defer n.close()
	recoverErr := n.hook.Recover(n.chain)
	before := stateOf(n)
	_, again := n.chain.InsertChain(fx.blocks)
	after := stateOf(n)
	writeJSON(t, filepath.Join(work, "reimport.json"), map[string]any{"recover_error": errText(recoverErr), "error": errText(again), "before": before, "after": after})
	if recoverErr != nil || again != nil {
		t.Fatalf("reimport: %v %v", recoverErr, again)
	}
	genesisWire, err := rlp.EncodeToBytes(fx.genesis.ToBlock())
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(work, "report.json"), map[string]any{"schema_version": 1, "scope": "private-current-fork-parlia-import", "public_profiles": "NOT_RUN", "activation_import_controls": "NOT_RUN", "unknown_import_control": "NOT_RUN", "config": fx.genesis.Config, "genesis_wire": hexOf(genesisWire), "blocks": len(fx.blocks), "transactions": rows, "bridge": bridge, "reimport_before": before, "reimport_after": after})
}
