package parlia

// Initialization prerequisite only: no header validation, InsertChain, extractor
// observation, or complete modern system-state claim is made by this probe.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/systemcontracts/lorentz"
	"github.com/ethereum/go-ethereum/core/systemcontracts/pascal"
	"github.com/ethereum/go-ethereum/core/systemcontracts/pasteur"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

type modernInitContext struct {
	p *Parlia
	h *types.Header
}

func (c modernInitContext) Config() *params.ChainConfig  { return c.p.chainConfig }
func (c modernInitContext) Engine() consensus.Engine     { return c.p }
func (c modernInitContext) CurrentHeader() *types.Header { return c.h }
func (c modernInitContext) GetHeader(hash common.Hash, number uint64) *types.Header {
	if hash == c.h.Hash() && number == c.h.Number.Uint64() {
		return c.h
	}
	return nil
}
func (c modernInitContext) GetHeaderByNumber(number uint64) *types.Header {
	if number == c.h.Number.Uint64() {
		return c.h
	}
	return nil
}
func (c modernInitContext) GetHeaderByHash(hash common.Hash) *types.Header {
	if hash == c.h.Hash() {
		return c.h
	}
	return nil
}

func TestExtractorBSCModernInit(t *testing.T) {
	config := params.BSCChainConfig
	if err := config.CheckConfigForkOrder(); err != nil {
		t.Fatal(err)
	}
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	coinbase := crypto.PubkeyToAddress(key.PublicKey)
	header := &types.Header{Number: big.NewInt(100000001), Time: 1795046401,
		Coinbase: coinbase, Difficulty: big.NewInt(2), GasLimit: 40000000, BaseFee: big.NewInt(0)}
	if !config.IsPasteur(header.Number, header.Time) || config.IsJenner(header.Number, header.Time) {
		t.Fatal("wrong modern fork interval")
	}
	db := rawdb.NewMemoryDatabase()
	defer db.Close()
	p := New(config, db, nil, common.Hash{})
	defer p.Close()
	st, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	// Every modern system account uses the latest applicable code from the
	// native mainnet upgrade table through Pasteur. This initializes code only;
	// the complete system's storage is deliberately not claimed initialized.
	codes := map[string]string{
		"0x1000": lorentz.MainnetValidatorContract,
		"0x1001": pascal.MainnetSlashContract,
		"0x1002": pascal.MainnetSystemRewardContract,
		"0x1003": pascal.MainnetLightClientContract,
		"0x1004": pascal.MainnetTokenHubContract,
		"0x1005": pascal.MainnetRelayerIncentivizeContract,
		"0x1006": pascal.MainnetRelayerHubContract,
		"0x1007": pascal.MainnetGovHubContract,
		"0x1008": pascal.MainnetTokenManagerContract,
		"0x2000": pascal.MainnetCrossChainContract,
		"0x2001": pascal.MainnetStakingContract,
		"0x2002": pasteur.MainnetStakeHubContract,
		"0x2003": pascal.MainnetStakeCreditContract,
		"0x2004": pasteur.MainnetGovernorContract,
		"0x2005": pascal.MainnetGovTokenContract,
		"0x2006": pascal.MainnetTimelockContract,
		"0x3000": pascal.MainnetTokenRecoverPortalContract,
	}
	codeHashes := map[string]string{}
	for address, encoded := range codes {
		code, err := hex.DecodeString(strings.TrimSpace(encoded))
		if err != nil || len(code) == 0 {
			t.Fatalf("decode %s: %v", address, err)
		}
		addr := common.HexToAddress(address)
		st.SetCode(addr, code, tracing.CodeChangeUnspecified)
		digest := sha256.Sum256(code)
		codeHashes[strings.ToLower(addr.Hex())] = hex.EncodeToString(digest[:])
	}
	st.SetBalance(coinbase, uint256.NewInt(10000), tracing.BalanceChangeUnspecified)
	validator, reward, burn := common.HexToAddress("0x1000"), common.HexToAddress("0x1002"), common.HexToAddress("0xdead")
	view := func() map[string]string {
		out := map[string]string{"coinbase": st.GetBalance(coinbase).String(), "validator": st.GetBalance(validator).String(),
			"reward": st.GetBalance(reward).String(), "burn": st.GetBalance(burn).String(),
			"nonce": new(big.Int).SetUint64(st.GetNonce(coinbase)).String()}
		for _, n := range []int64{0, 1, 3, 6, 15, 20, 23, 24} {
			out["slot_"+big.NewInt(n).String()] = st.GetState(validator, common.BigToHash(big.NewInt(n))).Big().String()
		}
		// Solidity mapping(address => uint256), slot 4. This public fixture
		// signer is NOT an initialized mainnet validator: cover the actual
		// deprecated-validator deposit branch, never claim active income.
		mapKey := crypto.Keccak256Hash(common.LeftPadBytes(coinbase.Bytes(), 32), common.LeftPadBytes([]byte{4}, 32))
		out["validator_index"] = st.GetState(validator, mapKey).Big().String()
		return out
	}
	rows := []map[string]any{}
	txs, receipts, used := []*types.Transaction{}, []*types.Receipt{}, uint64(0)
	for _, method := range []string{"init", "deposit"} {
		var data []byte
		value := big.NewInt(0)
		if method == "init" {
			data, err = p.validatorSetABI.Pack(method)
		} else {
			data, err = p.validatorSetABI.Pack(method, coinbase)
			value = big.NewInt(10000)
		}
		if err != nil {
			t.Fatal(err)
		}
		msg := p.getSystemMessage(coinbase, validator, data, value)
		tx, err := types.SignTx(types.NewTransaction(st.GetNonce(coinbase), validator, value, msg.GasLimit, big.NewInt(0), data), p.signer, key)
		if err != nil {
			t.Fatal(err)
		}
		received := []*types.Transaction{tx}
		before := view()
		err = p.applyTransaction(msg, st, header, modernInitContext{p, header}, &txs, &receipts, &received, &used, systemTxImporting, nil)
		if err != nil {
			t.Fatalf("%s failed: %v", method, err)
		}
		if len(received) != 0 || receipts[len(receipts)-1].Status != types.ReceiptStatusSuccessful {
			t.Fatal("missing native successful receipt")
		}
		rows = append(rows, map[string]any{"method": method, "before": before, "after": view(), "hash": tx.Hash().Hex()})
	}
	// Independent Solidity layout/transition oracle; do not derive expected
	// numbers from another execution or from the output state root.
	want := map[string]string{"coinbase": "0", "validator": "8375", "reward": "625", "burn": "1000", "nonce": "2",
		"slot_0": "1", "slot_1": "21", "slot_3": "0", "slot_6": "1000", "slot_15": "625", "slot_20": "1",
		"slot_23": "0", "slot_24": "0", "validator_index": "0"}
	for k, v := range want {
		if view()[k] != v {
			t.Fatalf("%s: got %s want %s", k, view()[k], v)
		}
	}
	report := map[string]any{"schema_version": 1, "scope": "modern-validator-init-deposit-prerequisite",
		"import_observation": "NOT_RUN", "system_transaction_state": "NOT_RUN", "public_profiles": "NOT_RUN",
		"complete_system_initialization": "NOT_RUN", "config": config, "header_number": header.Number.String(),
		"header_time": header.Time, "code_hashes": codeHashes, "rows": rows}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("EXTRACTOR_BSC_TYPE_REPORT"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}
