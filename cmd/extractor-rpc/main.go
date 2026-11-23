// The real-network extractor adapter: a demonstration that the merged
// delivery pipeline carries live blockchain data, not a production
// service. It connects to an unpatched Geth node over JSON-RPC, follows
// new block headers, normalizes every block into one block lifecycle
// event and one transaction event per body transaction, and publishes
// JSON envelopes to NATS JetStream through the repository seams:
// protocol/semantic, protocol/codec, gateway/destination and
// gateway/publisher. The mock wire and mock gateway of deploy/ are
// never used.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/nats-io/nats.go"

	"github.com/sigaev-pro/bc-indexer/gateway/destination"
	"github.com/sigaev-pro/bc-indexer/gateway/publisher"
	"github.com/sigaev-pro/bc-indexer/protocol/codec"
	"github.com/sigaev-pro/bc-indexer/protocol/proto"
	"github.com/sigaev-pro/bc-indexer/protocol/semantic"
)

// jsonRootType is the demo type key of the one-field message wrapping the
// JSON event inside the D73 record; the schema registry has not merged yet.
const jsonRootType uint32 = 9001

// demoDestinationID is the fixed destination identity of the contour.
var demoDestinationID = proto.ID16{0x7e, 0x0a, 0x5c, 0x93, 0xd1, 0xb4,
	0xf8, 0xa2, 0xc6, 0xe0, 0x9d, 0x43, 0xb5, 0x17, 0x2f, 0xa8}

func main() {
	rpcURL := env("RPC_URL", "ws://localhost:8546")
	natsURL := env("NATS_URL", "nats://localhost:4222")
	network := env("NETWORK", "eth-sepolia")
	fromBlock := env("FROM_BLOCK", "latest")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logf("ADAPTER_STARTED", map[string]any{"rpc": rpcURL, "nats": natsURL,
		"network": network, "from_block": fromBlock})

	// The chain identity comes from the live node, never from config.
	ec, err := ethclient.Dial(rpcURL)
	must("RPC_CONNECT_FAILED", err)
	chainID, err := ec.ChainID(ctx)
	must("RPC_CHAIN_ID_FAILED", err)
	genesis, err := ec.HeaderByNumber(ctx, big.NewInt(0))
	must("RPC_GENESIS_FAILED", err)
	var semNet semantic.Network
	semNet.ChainID = semantic.UInt256(chainID.FillBytes(make([]byte, 32)))
	copy(semNet.GenesisHash[:], genesis.Hash().Bytes())
	logf("NETWORK_IDENTIFIED", map[string]any{"chain_id": chainID.Uint64(),
		"genesis_hash": genesis.Hash().Hex()})

	nc, err := nats.Connect(natsURL, nats.MaxReconnects(-1), nats.ReconnectWait(2*time.Second))
	must("NATS_CONNECT_FAILED", err)
	defer nc.Close()
	js, err := nc.JetStream(nats.Context(ctx))
	must("NATS_JETSTREAM_FAILED", err)

	// The binding of the demo contour, derived from the live chain
	// identity and verified against the live server below.
	prefix := "extractor." + network
	binding := destination.Binding{
		DestinationID: demoDestinationID, Generation: 1, Network: protoNet(semNet),
		ExpectedAccount: "$G", // the demo NATS runs one plain default account
		Stream:          "EXTRACTOR_" + strings.ToUpper(strings.ReplaceAll(network, "-", "_")),
		SubjectPrefix:   prefix,
	}
	binding.DescriptorDigest, err = binding.DescriptorDigestComputed()
	must("BINDING_INVALID", err)
	subjects := []string{prefix + ".block", prefix + ".transaction"}
	must("STREAM_SETUP_FAILED", ensureStream(ctx, js, binding))
	report := destination.Check(ctx, jetstreamAPI{js}, binding, subjects, 1024*1024)
	if !report.OK() {
		fatal("DESTINATION_"+report.Status.String(), errors.New(strings.Join(report.Reasons, "; ")))
	}
	networkDigest, err := protoNet(semNet).Digest()
	must("NETWORK_DIGEST_FAILED", err)
	logf("DESTINATION_VERIFIED", map[string]any{"stream": binding.Stream, "subjects": subjects})

	a := &adapter{ec: ec, js: js, binding: binding, semNet: semNet,
		protoNet: protoNet(semNet), networkDigest: networkDigest, network: network,
		blockSubject: subjects[0], txSubject: subjects[1]}
	var identities [3][16]byte // producer, run, writer epoch of this run
	for i := range identities {
		_, err = rand.Read(identities[i][:])
		must("IDENTITY_FAILED", err)
	}
	a.producer, a.run, a.epoch = identities[0], identities[1], identities[2]

	head, err := ec.BlockNumber(ctx)
	must("RPC_HEAD_FAILED", err)
	headers := make(chan *types.Header, 64)
	if sub, err := ec.SubscribeNewHead(ctx, headers); err != nil {
		fatal("RPC_SUBSCRIBE_FAILED", fmt.Errorf("new-head subscription needs a ws:// endpoint: %w", err))
	} else {
		defer sub.Unsubscribe()
	}

	// A numeric FROM_BLOCK replays that history first; "latest" waits for
	// the blocks that arrive after startup.
	start := head + 1
	if fromBlock != "latest" {
		n, ok := new(big.Int).SetString(fromBlock, 0)
		if !ok {
			fatal("CONFIG_INVALID", fmt.Errorf("FROM_BLOCK %q is not a number or \"latest\"", fromBlock))
		}
		start = n.Uint64()
	}
	for i := start; i <= head && ctx.Err() == nil; i++ {
		a.processBlock(ctx, new(big.Int).SetUint64(i))
	}
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case h, ok := <-headers:
			if !ok { // the RPC connection dropped the subscription
				fatal("RPC_SUBSCRIPTION_CLOSED", errors.New("new-head subscription ended"))
			}
			if h.Number.Uint64() >= start && h.Number.Uint64() > a.processed {
				a.processBlock(ctx, h.Number)
			}
		}
	}
}

// adapter carries the state of one capture run: the clients, the verified
// binding, the run identities and the monotone counters.
type adapter struct {
	ec                               *ethclient.Client
	js                               nats.JetStreamContext
	binding                          destination.Binding
	semNet                           semantic.Network
	protoNet                         proto.NetworkIdentity
	networkDigest                    proto.Hash32
	network, blockSubject, txSubject string
	producer, run, epoch             [16]byte
	sourceSeq, eventSeq, processed   uint64
}

// processBlock normalizes one full block: one block lifecycle event plus
// one transaction event per body transaction, each with a validated
// semantic descriptor, and publishes them through the seam.
func (a *adapter) processBlock(ctx context.Context, number *big.Int) {
	blk, err := a.ec.BlockByNumber(ctx, number)
	if err != nil {
		logf("RPC_BLOCK_FAILED", map[string]any{"number": number.Uint64(), "error": err.Error()})
		return
	}
	height := blk.NumberU64()
	source := func() semantic.SourceRef {
		a.sourceSeq++
		return semantic.SourceRef{NodeInstanceID: semantic.ID16(a.producer),
			WriterEpoch: semantic.ID16(a.epoch), SourceSequence: a.sourceSeq}
	}
	// The block entity: family block, entity ordinal 0.
	block, err := semantic.NewBlockDescriptor(semantic.KindApplied, a.semNet,
		semantic.ID16(a.producer), semantic.ID16(a.run), height,
		semantic.Hash32(blk.Hash()), 0, source())
	if err != nil {
		logf("DESCRIPTOR_INVALID", map[string]any{"error": err.Error()})
		return
	}
	digest, err := block.Digest()
	if err != nil {
		logf("DESCRIPTOR_INVALID", map[string]any{"error": err.Error()})
		return
	}
	a.publish(ctx, a.blockSubject, block, digest, map[string]any{
		"number": height, "hash": blk.Hash().Hex(), "parent_hash": blk.ParentHash().Hex(),
		"timestamp": blk.Time(), "gas_used": blk.GasUsed(), "tx_count": blk.Transactions().Len()})

	// The transaction entities: family tx, ordinal the body position plus
	// one, with the receipt status of the inclusion.
	signer := types.LatestSignerForChainID(new(big.Int).SetBytes(a.semNet.ChainID[:]))
	for i, tx := range blk.Transactions() {
		transaction, err := semantic.NewTransactionDescriptor(semantic.KindApplied,
			a.semNet, semantic.ID16(a.producer), semantic.ID16(a.run), height,
			semantic.Hash32(blk.Hash()), semantic.Hash32(tx.Hash()), uint64(i), 0, source())
		if err != nil {
			logf("DESCRIPTOR_INVALID", map[string]any{"error": err.Error()})
			continue
		}
		txDigest, err := transaction.Digest()
		if err != nil {
			logf("DESCRIPTOR_INVALID", map[string]any{"error": err.Error()})
			continue
		}
		from, _ := types.Sender(signer, tx)
		var status any
		if receipt, err := a.ec.TransactionReceipt(ctx, tx.Hash()); err == nil {
			status = receipt.Status
		} else {
			logf("RPC_RECEIPT_FAILED", map[string]any{"hash": tx.Hash().Hex(), "error": err.Error()})
		}
		a.publish(ctx, a.txSubject, transaction, txDigest, map[string]any{
			"block_number": height, "block_hash": blk.Hash().Hex(), "tx_index": i,
			"hash": tx.Hash().Hex(), "from": from.Hex(), "to": nullableAddr(tx.To()),
			"value": tx.Value().String(), "gas": tx.Gas(), "status": status})
	}
	a.processed = height
	logf("BLOCK_PUBLISHED", map[string]any{"number": height, "transactions": blk.Transactions().Len()})
}

// publish frames one normalized event — the semantic identity in the JSON
// envelope, the canonical record digest of the D73 encoding, the exact v2
// message id and header bytes — and drives it to a durable JetStream
// publish, retrying transient failures under the immutable id so the
// stream dedups the retries.
func (a *adapter) publish(ctx context.Context, subject string,
	d semantic.SemanticDescriptor, digest semantic.Hash32, event map[string]any) {
	a.eventSeq++
	eventID := proto.NewEventID(proto.ID16(a.producer), proto.ID16(a.run), a.eventSeq)
	body, err := json.Marshal(event)
	var digestHex string
	if err == nil {
		if digestHex, err = recordDigest(eventID, a.protoNet,
			proto.Family(d.Family), proto.Kind(d.Kind), body); err == nil {
			event["schema"], event["network"] = "extractor.rpc.v1", a.network
			event["family"], event["kind"] = d.Family.String(), d.Kind.String()
			event["decision_sequence"] = d.Activation.ActivationID.DecisionSequence
			event["entity_ordinal"] = d.Activation.ActivationID.EntityOrdinal
			event["source_sequence"] = d.Source.SourceSequence
			event["descriptor_digest"] = digest.String()
			event["record_digest"] = digestHex
			body, err = json.Marshal(event)
		}
	}
	if err != nil {
		logf("RECORD_ENCODE_FAILED", map[string]any{"error": err.Error()})
		return
	}
	msgID, err := proto.NatsMsgID(a.networkDigest, eventID, 0, proto.FormComplete, 0)
	must("MSG_ID_FAILED", err)
	header, err := proto.NatsHeader(msgID, a.binding.Stream)
	must("NATS_HEADER_FAILED", err)
	for backoff := time.Second; ; {
		ack, err := jetstreamAPI{a.js}.Publish(subject, header, body)
		if err == nil {
			logf("RECORD_PUBLISHED", map[string]any{"subject": subject, "msg_id": msgID,
				"stream": ack.Stream, "sequence": ack.Sequence, "bytes": len(body)})
			return
		}
		if !errors.Is(err, publisher.ErrTransient) {
			logf("PUBLISH_FAILED", map[string]any{"subject": subject, "error": err.Error()})
			return
		}
		logf("PUBLISH_RETRY", map[string]any{"subject": subject, "error": err.Error(), "in": backoff.String()})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
			if backoff < 15*time.Second {
				backoff *= 2
			}
		}
	}
}

// jetstreamAPI adapts the pinned client to the two repository seams: the
// publisher Conn surface and the destination Remote surface.
type jetstreamAPI struct{ js nats.JetStreamContext }

// Publish implements publisher.Conn with the exact header bytes.
func (j jetstreamAPI) Publish(subject string, header, payload []byte) (publisher.PubAck, error) {
	const idPrefix, streamPrefix = "Nats-Msg-Id: ", "Nats-Expected-Stream: "
	lines := strings.Split(string(header), "\r\n")
	// The exact layout is NATS/1.0, the two header lines, then the blank
	// terminator: the trailing \r\n\r\n splits into two empty strings.
	if len(lines) != 5 || lines[0] != "NATS/1.0" || lines[3] != "" || lines[4] != "" ||
		!strings.HasPrefix(lines[1], idPrefix) || !strings.HasPrefix(lines[2], streamPrefix) {
		return publisher.PubAck{}, fmt.Errorf("%w: unexpected header layout", publisher.ErrBadHeader)
	}
	msg := nats.NewMsg(subject)
	msg.Data = payload
	msg.Header.Set("Nats-Msg-Id", strings.TrimPrefix(lines[1], idPrefix))
	stream := strings.TrimPrefix(lines[2], streamPrefix)
	msg.Header.Set("Nats-Expected-Stream", stream)
	ack, err := j.js.PublishMsg(msg)
	if err != nil {
		return publisher.PubAck{}, fmt.Errorf("%w: %v", publisher.ErrTransient, err)
	}
	if ack == nil || ack.Stream != stream {
		return publisher.PubAck{}, fmt.Errorf("%w: ack %v differs from the plan stream %q",
			publisher.ErrUnexpectedStream, ack, stream)
	}
	return publisher.PubAck{Stream: ack.Stream, Sequence: ack.Sequence,
		Duplicate: ack.Duplicate}, nil
}

// AccountInfo implements destination.Remote. The pinned client's account
// answer carries no account name, and the demo contour runs one plain
// server whose single account is the default "$G": a reachable JetStream
// API proves liveness, and the binding fixes the compared account.
func (j jetstreamAPI) AccountInfo(ctx context.Context) (destination.AccountInfo, error) {
	if _, err := j.js.AccountInfo(nats.Context(ctx)); err != nil {
		return destination.AccountInfo{}, err
	}
	return destination.AccountInfo{Account: "$G"}, nil
}

// StreamInfo implements destination.Remote; a negative max message size
// means unlimited on the server and reads as zero here.
func (j jetstreamAPI) StreamInfo(ctx context.Context, stream string) (destination.StreamInfo, error) {
	info, err := j.js.StreamInfo(stream, nats.Context(ctx))
	if err != nil {
		return destination.StreamInfo{}, err
	}
	return destination.StreamInfo{Name: info.Config.Name, Subjects: info.Config.Subjects,
		Metadata:   info.Config.Metadata,
		MaxMsgSize: uint64(max(info.Config.MaxMsgSize, int32(0)))}, nil
}

// ensureStream creates the demo stream with its D77 identity metadata when
// it is missing; an existing stream is left untouched for the check.
func ensureStream(ctx context.Context, js nats.JetStreamContext, b destination.Binding) error {
	if _, err := js.StreamInfo(b.Stream, nats.Context(ctx)); err == nil {
		return nil
	} else if !errors.Is(err, nats.ErrStreamNotFound) {
		return err
	}
	networkDigest, err := b.NetworkDigest()
	if err == nil {
		_, err = js.AddStream(&nats.StreamConfig{Name: b.Stream,
			Subjects: []string{b.SubjectPrefix + ".>"},
			Metadata: map[string]string{
				destination.MetadataNetworkDigest: hex.EncodeToString(networkDigest[:]),
				destination.MetadataDestinationID: hex.EncodeToString(b.DestinationID[:]),
			}}, nats.Context(ctx))
	}
	return err
}

// recordDigest encodes the JSON event as a one-field D73 record and returns
// its canonical record digest: the identity the binary codec transport
// will carry when it replaces the JSON body.
func recordDigest(eventID proto.EventID, net proto.NetworkIdentity,
	family proto.Family, kind proto.Kind, body []byte) (string, error) {
	nodes := []codec.TreeNode{
		{Kind: codec.ValueMessage, TypeKey: jsonRootType, ChildCount: 1},
		{Kind: codec.ValueBytes, HasFieldTag: true, FieldTag: 1, Value: body},
	}
	result, err := codec.BuildRecord(eventID, net, family, kind, jsonRootType, nodes,
		func(uint64, []byte) error { return nil })
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(result.RecordDigest[:]), nil
}

// protoNet converts the semantic network identity to the wire form.
func protoNet(n semantic.Network) proto.NetworkIdentity {
	return proto.NetworkIdentity{Family: proto.NetworkFamilyEVM,
		ChainID: proto.Hash32(n.ChainID), GenesisHash: proto.Hash32(n.GenesisHash)}
}

// nullableAddr returns the 0x text of addr, or nil for a creation. The
// parameter is the concrete pointer: a typed nil in an interface would
// not compare equal to nil and would panic on the dereference.
func nullableAddr(addr *common.Address) any {
	if addr == nil {
		return nil
	}
	return addr.Hex()
}

// env reads one configuration variable with its default.
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// logf writes one JSON event line to stdout.
func logf(code string, fields map[string]any) {
	fields["event_code"] = code
	fields["ts"] = time.Now().UTC().Format(time.RFC3339)
	if line, err := json.Marshal(fields); err == nil {
		fmt.Println(string(line))
	}
}

// fatal logs one terminal event and exits.
func fatal(code string, err error) {
	logf(code, map[string]any{"error": err.Error()})
	os.Exit(2)
}

// must fatals when err is not nil.
func must(code string, err error) {
	if err != nil {
		fatal(code, err)
	}
}
