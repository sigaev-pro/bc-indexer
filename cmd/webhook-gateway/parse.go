// Event parsing and notification shaping. The envelope contract is the one
// the extractor adapter publishes (cmd/extractor-rpc): every NATS message
// is a JSON object with a native body plus nine envelope keys (schema,
// network, family, kind, decision_sequence, entity_ordinal,
// source_sequence, descriptor_digest, record_digest). Block bodies carry
// number/hash/parent_hash/timestamp/gas_used/tx_count; transaction bodies
// carry block_number/block_hash/tx_index/hash/from/to(null)/value/gas/
// status(1|0|null). The field mapping mirrors cmd/clickhouse-sink so both
// consumers read the stream the same way.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// subjectFilter is the fixed subject namespace this gateway consumes:
// extractor.<network>.<block|transaction>.
const subjectFilter = "extractor.>"

// subjectPrefix is the literal tokens of subjectFilter before the >
// wildcard: ["extractor"].
var subjectPrefix = []string{"extractor"}

// splitSubject derives the network and event type tokens from
// extractor.<network>.<type>. Extra trailing tokens are tolerated and
// ignored; "" , false means the subject is not in the namespace.
func splitSubject(subject string) (network, eventType string, ok bool) {
	tokens := strings.Split(subject, ".")
	if len(tokens) < len(subjectPrefix)+2 {
		return "", "", false
	}
	for i, want := range subjectPrefix {
		if tokens[i] != want {
			return "", "", false
		}
	}
	network, eventType = tokens[len(subjectPrefix)], tokens[len(subjectPrefix)+1]
	if network == "" || eventType == "" {
		return "", "", false
	}
	return network, eventType, true
}

// subjectsIntersect reports whether two NATS subject filters can match at
// least one common subject, honoring the token wildcards ("*" exactly one
// token, ">" the non-empty tail). Used to discover every stream that
// carries extractor events.
func subjectsIntersect(a, b []string) bool {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		aEnd, bEnd := i >= len(a), i >= len(b)
		switch {
		case aEnd && bEnd:
			return true
		case aEnd || bEnd:
			return false
		}
		switch ta, tb := a[i], b[i]; {
		case ta == ">" || tb == ">":
			return true
		case ta == "*" || tb == "*" || ta == tb:
			continue
		default:
			return false
		}
	}
	return true
}

// chainIDs maps the known network subject tokens to their EIP-155 chain
// ids, the same table the sink uses; unknown networks keep 0.
var chainIDs = map[string]uint64{
	"eth-sepolia":  11155111,
	"eth-holesky":  17000,
	"eth-mainnet":  1,
	"bsc-testnet":  97,
	"bsc-mainnet":  56,
	"poly-testnet": 80002,
	"poly-mainnet": 137,
}

// chainID prefers an explicit payload value, then the known table.
func chainID(network string, fromPayload flexNumber) uint64 {
	if v, ok := fromPayload.u64(); ok {
		return v
	}
	return chainIDs[network]
}

// flexNumber accepts a JSON number or a numeric string and keeps the
// literal; empty means null/absent.
type flexNumber string

func (f *flexNumber) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if len(s) > 0 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*f = flexNumber(str)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexNumber(n)
	return nil
}

// u64 parses the literal as an unsigned integer; false means absent or
// unparsable.
func (f flexNumber) u64() (uint64, bool) {
	if f == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSpace(string(f)), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// flexString accepts a JSON string, number or null (null decodes to the
// empty string, so a null "to" of a contract creation lands as empty).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if len(s) > 0 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*f = flexString(str)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n)
	return nil
}

// payload is the union of the block-shaped and transaction-shaped event
// bodies plus the adapter envelope present on every event.
type payload struct {
	// Block shape.
	Number     flexNumber `json:"number"`
	Hash       flexString `json:"hash"`
	ParentHash flexString `json:"parent_hash"`
	Timestamp  flexNumber `json:"timestamp"`
	GasUsed    flexNumber `json:"gas_used"`
	TxCount    flexNumber `json:"tx_count"`

	// Transaction shape.
	BlockNumber flexNumber `json:"block_number"`
	BlockHash   flexString `json:"block_hash"`
	TxIndex     flexNumber `json:"tx_index"`
	From        flexString `json:"from"`
	To          flexString `json:"to"`
	Value       flexString `json:"value"`
	Gas         flexNumber `json:"gas"`
	Status      flexNumber `json:"status"`

	// Envelope on every event (network comes from the subject).
	Family           flexString `json:"family"`
	Kind             flexString `json:"kind"`
	ChainID          flexNumber `json:"chain_id"`
	EntityOrdinal    flexNumber `json:"entity_ordinal"`
	SourceSequence   flexNumber `json:"source_sequence"`
	DescriptorDigest flexString `json:"descriptor_digest"`
	RecordDigest     flexString `json:"record_digest"`
}

// parsedEvent is one normalized extractor event, ready for the engine.
type parsedEvent struct {
	network   string
	eventType string // block | transaction (from the subject)
	kind      string // applied | removed (from the envelope)
	raw       string // the original message bytes, verbatim

	blockNumber uint64
	blockHash   string
	parentHash  string
	timestamp   uint64
	gasUsed     uint64
	txCount     uint64

	txIndex uint64
	txHash  string
	from    string
	to      string // "" means null (a contract creation)
	value   string
	gas     uint64
	status  string // success | reverted | unknown

	capturedAt time.Time
}

// parseMessage maps one NATS message onto a parsedEvent. The network and
// event type come from the subject; the body is the adapter envelope.
func parseMessage(subject string, data []byte, now time.Time) (*parsedEvent, error) {
	network, eventType, ok := splitSubject(subject)
	if !ok {
		return nil, fmt.Errorf("subject %q does not match %s<network>.<type>",
			subject, strings.Join(subjectPrefix, ".")+".")
	}
	var p payload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode json body: %w", err)
	}
	kind := string(p.Kind)
	if kind == "" {
		kind = "applied"
	}
	ev := &parsedEvent{
		network:    network,
		eventType:  eventType,
		kind:       kind,
		raw:        string(data),
		capturedAt: now,
	}
	switch eventType {
	case "block":
		ev.blockNumber = firstU64(p.Number, p.BlockNumber)
		ev.blockHash = firstString(p.Hash, p.BlockHash)
		ev.parentHash = string(p.ParentHash)
		ev.timestamp = firstU64(p.Timestamp)
		ev.gasUsed = firstU64(p.GasUsed)
		ev.txCount = firstU64(p.TxCount)
		if ev.blockHash == "" {
			return nil, fmt.Errorf("block event without a hash (hash/block_hash both absent)")
		}
	case "transaction":
		ev.blockNumber = firstU64(p.BlockNumber, p.Number)
		ev.blockHash = string(p.BlockHash)
		ev.txIndex = firstU64(p.TxIndex)
		ev.txHash = string(p.Hash)
		ev.from = string(p.From)
		ev.to = string(p.To)
		ev.value = string(p.Value)
		ev.gas = firstU64(p.Gas)
		ev.status = txStatus(p.Status)
		if ev.txHash == "" {
			return nil, fmt.Errorf("transaction event without a hash (hash absent)")
		}
	default:
		return nil, fmt.Errorf("event type %q is not block or transaction", eventType)
	}
	return ev, nil
}

// txStatus maps the numeric receipt status: 1 success, 0 reverted,
// null/absent/other stays unknown.
func txStatus(v flexNumber) string {
	switch v {
	case "1":
		return "success"
	case "0":
		return "reverted"
	default:
		return "unknown"
	}
}

// firstU64 returns the first literal that parses, 0 otherwise.
func firstU64(vals ...flexNumber) uint64 {
	for _, v := range vals {
		if n, ok := v.u64(); ok {
			return n
		}
	}
	return 0
}

// firstString returns the first non-empty value.
func firstString(vals ...flexString) string {
	for _, v := range vals {
		if v != "" {
			return string(v)
		}
	}
	return ""
}

// eventKey is the stable identity of one canonical event occurrence:
// network:block:entity, where entity is tx_hash:tx_index for transactions
// and the block hash for blocks. A reorganized replacement at the same
// height gets a different key whenever the block hash (and thus the
// inclusion) changed.
func eventKey(network string, blockNumber uint64, entity string) string {
	return fmt.Sprintf("%s:%d:%s", network, blockNumber, entity)
}

// txEntity is the events-table entity of a transaction event: the tx hash
// plus the body index, so two occurrences of the same hash at different
// positions never collapse.
func txEntity(txHash string, txIndex uint64) string {
	return fmt.Sprintf("%s:%d", txHash, txIndex)
}

// notificationEvent builds the JSON body of a transaction or block
// webhook notification for one subscription. confirmations is the
// subscription's configured N (what the client asked to wait for, not the
// observed depth).
func notificationEvent(p *payload, network, eventType, capturedAt string, confirmations int64) map[string]any {
	kind := string(p.Kind)
	if kind == "" {
		kind = "applied"
	}
	base := map[string]any{
		"type":          eventType,
		"network":       network,
		"chain_id":      chainID(network, p.ChainID),
		"confirmations": confirmations,
		"kind":          kind,
		"captured_at":   capturedAt,
	}
	if eventType == "block" {
		base["block_number"] = firstU64(p.Number, p.BlockNumber)
		base["block_hash"] = firstString(p.Hash, p.BlockHash)
		base["parent_hash"] = string(p.ParentHash)
		base["timestamp"] = firstU64(p.Timestamp)
		base["gas_used"] = firstU64(p.GasUsed)
		base["tx_count"] = firstU64(p.TxCount)
		return base
	}
	var to any
	if p.To != "" {
		to = string(p.To)
	}
	base["block_number"] = firstU64(p.BlockNumber, p.Number)
	base["block_hash"] = string(p.BlockHash)
	base["tx_hash"] = string(p.Hash)
	base["tx_index"] = firstU64(p.TxIndex)
	base["from"] = string(p.From)
	base["to"] = to
	base["value"] = string(p.Value)
	base["gas"] = firstU64(p.Gas)
	base["status"] = txStatus(p.Status)
	return base
}

// notificationReorg builds the body of the reorg webhook: the replaced
// canonical range [from_height..to_height] and the last height that stayed
// canonical on both sides of the fork.
func notificationReorg(network string, from, to, common uint64) map[string]any {
	return map[string]any{
		"type":                   "reorg",
		"network":                network,
		"from_height":            from,
		"to_height":              to,
		"replaced_common_height": common,
	}
}

// notificationRemoved builds the body of the event_removed webhook: a
// pending event whose entity never re-appeared in the rebuilt chain.
func notificationRemoved(network string, original json.RawMessage) map[string]any {
	return map[string]any{
		"type":    "event_removed",
		"network": network,
		"reason":  "reorg",
		"event":   original,
	}
}

// notificationInvalidated builds the body of the event_invalidated
// webhook — the "event became bad" notification — for an event that was
// already delivered and whose block was replaced by a reorg.
func notificationInvalidated(network string, replacedByHeight uint64,
	originalWebhookID string, original json.RawMessage) map[string]any {
	return map[string]any{
		"type":               "event_invalidated",
		"network":            network,
		"reason":             "reorg",
		"replaced_by_height": replacedByHeight,
		"webhook_id":         originalWebhookID,
		"event":              original,
	}
}
