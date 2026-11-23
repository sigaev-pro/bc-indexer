package eth

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sigaev-pro/bc-indexer/protocol/balance"
)

// The import wire is JSON over capture fields: one TagBlockEvent field per
// block, one TagTxEvent field per body transaction, TagBytes fields for
// large blobs. Small byte values travel inline as hex; the producer moves
// values at or above BlobInlineMax into the byte store and references them
// by their SHA-256 content key, which keeps the structured events small
// and the collector's event buffer bounded.

// BlobInlineMax is the inline threshold of the import wire.
const BlobInlineMax = 256

// wireBytes is one byte value: inline hex, or a store reference with the
// content key and size.
type wireBytes struct {
	Hex  string `json:"hex,omitempty"`
	Key  string `json:"key,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// blobStorer moves one byte value into the import path and returns its
// wire reference: the producer implementation streams the value as a
// TagBytes capture field.
type blobStorer func(data []byte) (wireBytes, error)

// blobResolver reads one wire byte value back: inline hex directly, a
// store reference through the byte store with a content-key check.
type blobResolver func(w wireBytes) ([]byte, error)

// wireBlock is the block event.
type wireBlock struct {
	Hash    string        `json:"hash"`
	Number  uint64        `json:"number"`
	Header  wireHeader    `json:"header"`
	TxCount uint64        `json:"tx_count"`
	Outside []wireOutside `json:"outside,omitempty"`
}

// wireOutside is one outside-tx observation of a block.
type wireOutside struct {
	Kind        uint8              `json:"kind"`
	Effects     []wireSystemEffect `json:"effects"`
	NativeOrder uint64             `json:"native_order"`
}

// wireSystemEffect is one observed effect of an outside-tx operation.
type wireSystemEffect struct {
	Address string `json:"address"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

// wireHeader mirrors NativeHeader with text encodings; pointer fields keep
// presence.
type wireHeader struct {
	ParentHash       string    `json:"parent_hash"`
	UncleHash        string    `json:"uncle_hash"`
	Beneficiary      string    `json:"beneficiary"`
	StateRoot        string    `json:"state_root"`
	TransactionsRoot string    `json:"transactions_root"`
	ReceiptsRoot     string    `json:"receipts_root"`
	LogsBloom        string    `json:"logs_bloom"`
	Difficulty       string    `json:"difficulty"`
	Number           uint64    `json:"number"`
	GasLimit         uint64    `json:"gas_limit"`
	GasUsed          uint64    `json:"gas_used"`
	Time             uint64    `json:"time"`
	ExtraData        wireBytes `json:"extra_data"`
	MixDigest        string    `json:"mix_digest"`
	Nonce            string    `json:"nonce"`
	BaseFee          *string   `json:"base_fee,omitempty"`
	WithdrawalsRoot  *string   `json:"withdrawals_root,omitempty"`
	BlobGasUsed      *uint64   `json:"blob_gas_used,omitempty"`
	ExcessBlobGas    *uint64   `json:"excess_blob_gas,omitempty"`
	ParentBeaconRoot *string   `json:"parent_beacon_block_root,omitempty"`
	RequestsHash     *string   `json:"requests_hash,omitempty"`
	BlockAccessHash  *string   `json:"block_access_list_hash,omitempty"`
	SlotNumber       *uint64   `json:"slot_number,omitempty"`
	SourceRLP        wireBytes `json:"source_rlp"`
}

// wireTx is the transaction event.
type wireTx struct {
	Position       uint64              `json:"position"`
	Hash           string              `json:"hash"`
	Type           uint32              `json:"type"`
	Raw            []wireRaw           `json:"raw"`
	Projection     wireProjection      `json:"projection"`
	Receipt        wireReceipt         `json:"receipt"`
	SourceKey      *wireBytes          `json:"source_key,omitempty"`
	ScopeOps       []wireScopeOp       `json:"scope_ops"`
	Frames         []wireFrame         `json:"frames"`
	Transfers      []wireTransfer      `json:"transfers"`
	SelfDestruct   []wireSelfDestruct  `json:"selfdestructs"`
	Fee            wireFee             `json:"fee"`
	Ops            []wireOp            `json:"ops"`
	State          []wireAccount       `json:"state"`
	Authorizations []wireAuthorization `json:"authorizations,omitempty"`
	AuthOutcomes   []wireAuthOutcome   `json:"auth_outcomes,omitempty"`
}

// wireRaw is one retained raw encoding.
type wireRaw struct {
	Kind uint8     `json:"kind"`
	Data wireBytes `json:"data"`
}

// wireProjection is the execution projection.
type wireProjection struct {
	Sender         string    `json:"sender"`
	SenderProv     uint8     `json:"sender_prov"`
	Target         *string   `json:"target,omitempty"`
	TargetProv     uint8     `json:"target_prov,omitempty"`
	Value          string    `json:"value"`
	ValueProv      uint8     `json:"value_prov"`
	Input          wireBytes `json:"input"`
	InputProv      uint8     `json:"input_prov"`
	Nonce          uint64    `json:"nonce"`
	NonceProv      uint8     `json:"nonce_prov"`
	GasLimit       uint64    `json:"gas_limit"`
	EffectivePrice string    `json:"effective_gas_price"`
}

// wireReceipt is the typed receipt without the source bytes.
type wireReceipt struct {
	Status         *uint8    `json:"status,omitempty"`
	PostRoot       *string   `json:"post_state_root,omitempty"`
	Result         uint8     `json:"result"`
	Cumulative     uint64    `json:"cumulative_gas_used"`
	GasUsed        *uint64   `json:"gas_used,omitempty"`
	EffectivePrice *string   `json:"effective_gas_price,omitempty"`
	BlobGasUsed    *uint64   `json:"blob_gas_used,omitempty"`
	BlobGasPrice   *string   `json:"blob_gas_price,omitempty"`
	Logs           []wireLog `json:"logs"`
	Bloom          string    `json:"logs_bloom"`
	Attempted      *string   `json:"attempted_create_address,omitempty"`
	Created        *string   `json:"created_contract,omitempty"`
}

// wireLog is one committed log.
type wireLog struct {
	Address    string    `json:"address"`
	Topics     []string  `json:"topics"`
	Data       wireBytes `json:"data"`
	TxIndex    uint64    `json:"transaction_log_index"`
	BlockIndex uint64    `json:"block_log_index"`
}

// wireScopeOp is one scope operation.
type wireScopeOp struct {
	Kind   uint8   `json:"kind"`
	Parent uint64  `json:"parent"`
	Owner  *uint64 `json:"owner_frame,omitempty"`
	Scope  uint64  `json:"scope"`
}

// wireFrame is one frame entry input.
type wireFrame struct {
	Parent    int       `json:"parent"`
	Kind      uint8     `json:"kind"`
	Engine    uint8     `json:"engine"`
	Outcome   uint8     `json:"outcome"`
	Caller    *string   `json:"caller,omitempty"`
	Context   *string   `json:"context,omitempty"`
	Code      *string   `json:"code,omitempty"`
	MsgSender *string   `json:"msg_sender,omitempty"`
	Target    *string   `json:"target,omitempty"`
	Value     *string   `json:"value,omitempty"`
	Gas       *uint64   `json:"gas,omitempty"`
	GasUsed   *uint64   `json:"gas_used,omitempty"`
	Input     wireBytes `json:"input"`
	Output    wireBytes `json:"output"`
	Error     *string   `json:"error,omitempty"`
	Revert    wireBytes `json:"revert_data"`
	Attempted *string   `json:"attempted_create,omitempty"`
	Created   *string   `json:"created_create,omitempty"`
}

// wireTransfer is one transfer observation.
type wireTransfer struct {
	Frame     uint64       `json:"frame"`
	From      string       `json:"from"`
	To        string       `json:"to"`
	Amount    string       `json:"amount"`
	Scope     uint64       `json:"scope"`
	Committed bool         `json:"committed"`
	Effects   []wireEffect `json:"effects"`
}

// wireEffect is one observed balance mutation.
type wireEffect struct {
	Address string `json:"address"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

// wireSelfDestruct is one selfdestruct observation.
type wireSelfDestruct struct {
	Frame       uint64       `json:"frame"`
	Contract    string       `json:"contract"`
	Beneficiary string       `json:"beneficiary"`
	Balance     string       `json:"balance_at_entry"`
	Eligible    bool         `json:"deletion_eligible"`
	Scope       uint64       `json:"scope"`
	RolledBack  bool         `json:"rolled_back"`
	Deleted     bool         `json:"account_deleted"`
	Position    uint8        `json:"movement_position"`
	Effects     []wireEffect `json:"movement_effects"`
}

// wireFee is the fee observation.
type wireFee struct {
	Payer           string  `json:"payer"`
	GasLimit        uint64  `json:"gas_limit"`
	GasUsed         uint64  `json:"gas_used"`
	Price           string  `json:"effective_gas_price"`
	BlobUsed        *uint64 `json:"blob_gas_used,omitempty"`
	BlobPrice       *string `json:"blob_gas_price,omitempty"`
	Recipient       string  `json:"recipient"`
	BaseFee         string  `json:"base_fee"`
	PayerBefore     string  `json:"payer_balance_before"`
	PayerAtReturn   *string `json:"payer_balance_at_return,omitempty"`
	RecipientBefore string  `json:"recipient_balance_before"`
}

// wireOp is one observed operation position.
type wireOp struct {
	Kind         uint8  `json:"kind"`
	Frame        uint64 `json:"frame"`
	Transfer     uint64 `json:"transfer"`
	SelfDestruct uint64 `json:"selfdestruct"`
}

// wirePoint is one account boundary point.
type wirePoint struct {
	Exists   bool              `json:"exists"`
	Balance  string            `json:"balance"`
	Nonce    uint64            `json:"nonce"`
	CodeHash string            `json:"code_hash"`
	Code     wireBytes         `json:"code"`
	Storage  map[string]string `json:"storage"`
}

// wireAccount is one account observation.
type wireAccount struct {
	Address string    `json:"address"`
	Before  wirePoint `json:"before"`
	After   wirePoint `json:"after"`
	Reset   bool      `json:"storage_reset"`
}

// wireAuthorization is one source tuple.
type wireAuthorization struct {
	ChainID *string `json:"chain_id,omitempty"`
	Address string  `json:"address"`
	Nonce   uint64  `json:"nonce"`
	Parity  uint64  `json:"y_parity"`
	R       string  `json:"r"`
	S       string  `json:"s"`
}

// wireAuthOutcome is one observed tuple outcome.
type wireAuthOutcome struct {
	Tuple      uint64 `json:"tuple_index"`
	Applied    bool   `json:"applied"`
	Authority  string `json:"authority"`
	NonceAfter uint64 `json:"nonce_after"`
}

// jsonUnmarshalStrict decodes JSON rejecting unknown fields: the wire is
// closed, and an unknown field is an import error, not ignored data.
func jsonUnmarshalStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing JSON content")
	}
	return nil
}

// encodeHex renders bytes as lowercase hex.
func encodeHex(b []byte) string { return hex.EncodeToString(b) }

// decodeFixedHex decodes exactly n bytes of hex, with or without 0x.
func decodeFixedHex(s string, n int) ([]byte, error) {
	t := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(t) != 2*n {
		return nil, fmt.Errorf("hex length %d, want %d", len(t), 2*n)
	}
	return hex.DecodeString(t)
}

func addrHex(a Address20) string { return encodeHex(a[:]) }
func hashHex(h Hash32) string    { return encodeHex(h[:]) }
func u256Hex(u UInt256) string   { return u.Decimal() }

func parseAddr(s string) (Address20, error) {
	b, err := decodeFixedHex(s, 20)
	if err != nil {
		return Address20{}, err
	}
	var out Address20
	copy(out[:], b)
	return out, nil
}

func parseHash(s string) (Hash32, error) {
	b, err := decodeFixedHex(s, 32)
	if err != nil {
		return Hash32{}, err
	}
	var out Hash32
	copy(out[:], b)
	return out, nil
}

func parseU256(s string) (UInt256, error) {
	v, err := balance.ParseUInt256(s)
	if err != nil {
		return UInt256{}, fmt.Errorf("invalid UInt256 %q", s)
	}
	return v, nil
}
