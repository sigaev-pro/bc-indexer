// The real-network extractor adapter: a demonstration that the merged
// delivery pipeline carries live blockchain data, not a production
// service. It connects to an unpatched blockchain node — a local Geth
// or a public RPC endpoint such as the Sepolia, BSC Chapel and Polygon
// Amoy testnets — over JSON-RPC, follows new block headers, normalizes
// every block into one block lifecycle event and one transaction event
// per body transaction, and publishes JSON envelopes to NATS JetStream
// through the repository seams: protocol/semantic, protocol/codec,
// gateway/destination and gateway/publisher. The mock wire and mock
// gateway of deploy/ are never used. A --config YAML file selects the
// network, pins the expected chain_id against the one the live endpoint
// serves, and bounds the run with max_blocks; the streams the adapter
// creates keep their messages for five minutes so no chain data
// accumulates on disk, and a pre-existing stream is never rewritten.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
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

// streamRetention bounds the demo footprint: every capture run expires
// from the JetStream file store five minutes after it landed, so repeated
// verification runs never accumulate chain data on disk.
const streamRetention = 5 * time.Minute

// settings is the resolved configuration of one adapter run. Values come
// from --config YAML when given, else from the environment, else from the
// defaults; the YAML wins over the environment key by key.
type settings struct {
	rpcURL, natsURL, network, fromBlock string
	chainID, maxBlocks                  uint64 // 0 disables the check / the limit
}

func main() {
	configPath := flag.String("config", "",
		"path to a YAML config (rpc_url, nats_url, network, chain_id, max_blocks)")
	flag.Parse()

	s := settings{
		rpcURL:    env("RPC_URL", "ws://localhost:8546"),
		natsURL:   env("NATS_URL", "nats://localhost:4222"),
		network:   env("NETWORK", "eth-sepolia"),
		fromBlock: env("FROM_BLOCK", "latest"),
		chainID:   envUint("CHAIN_ID", 0),
		// The default test window keeps a run bounded even without a
		// config file; 0 would mean "follow the chain forever".
		maxBlocks: envUint("MAX_BLOCKS", 20),
	}
	if *configPath != "" {
		if err := applyConfig(*configPath, &s); err != nil {
			fatal("CONFIG_INVALID", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logf("ADAPTER_STARTED", map[string]any{"rpc": s.rpcURL, "nats": s.natsURL,
		"network": s.network, "from_block": s.fromBlock,
		"chain_id": s.chainID, "max_blocks": s.maxBlocks, "config": *configPath})

	// The chain identity comes from the live node, never from config.
	ec, err := ethclient.Dial(s.rpcURL)
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
	// The config claims a network identity; the live endpoint just proved
	// one. A mismatch means the config points at the wrong RPC, so the run
	// must not publish under the wrong name.
	if s.chainID != 0 && s.chainID != chainID.Uint64() {
		fatal("CONFIG_CHAIN_ID_MISMATCH", fmt.Errorf(
			"config expects chain %d, the RPC serves chain %d", s.chainID, chainID.Uint64()))
	}

	nc, err := nats.Connect(s.natsURL, nats.MaxReconnects(-1), nats.ReconnectWait(2*time.Second))
	must("NATS_CONNECT_FAILED", err)
	defer nc.Close()
	js, err := nc.JetStream(nats.Context(ctx))
	must("NATS_JETSTREAM_FAILED", err)

	// The binding of the demo contour, derived from the live chain
	// identity and verified against the live server below.
	prefix := "extractor." + s.network
	binding := destination.Binding{
		DestinationID: demoDestinationID, Generation: 1, Network: protoNet(semNet),
		ExpectedAccount: "$G", // the demo NATS runs one plain default account
		Stream:          "EXTRACTOR_" + strings.ToUpper(strings.ReplaceAll(s.network, "-", "_")),
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
		protoNet: protoNet(semNet), networkDigest: networkDigest, network: s.network,
		blockSubject: subjects[0], txSubject: subjects[1], maxBlocks: s.maxBlocks}
	var identities [3][16]byte // producer, run, writer epoch of this run
	for i := range identities {
		_, err = rand.Read(identities[i][:])
		must("IDENTITY_FAILED", err)
	}
	a.producer, a.run, a.epoch = identities[0], identities[1], identities[2]

	// The subscription is established BEFORE the head is sampled: the
	// go-ethereum client retains up to 20000 notifications per
	// subscription after the subscribe call confirms, so every head from
	// the moment the subscription exists is held even while the replay
	// loop below runs. Sampling the head first, as the natural order
	// suggests, would leave any block that lands between the sample and
	// the subscription permanently uncaptured — with "latest" that gap is
	// never healed. The channel mirrors the client's own buffer size.
	headers := make(chan *types.Header, 20000)
	sub, err := ec.SubscribeNewHead(ctx, headers)
	if err != nil {
		fatal("RPC_SUBSCRIBE_FAILED", fmt.Errorf("new-head subscription needs a ws:// endpoint: %w", err))
	}
	defer sub.Unsubscribe()
	head, err := ec.BlockNumber(ctx)
	must("RPC_HEAD_FAILED", err)

	// A numeric FROM_BLOCK replays that history first; "latest" captures
	// the blocks that arrive after startup, beginning at head+1. Only a
	// plain non-negative decimal is accepted: a negative or overflowing
	// value must fail fast, not idle forever on a wrapped height.
	start := head + 1
	if s.fromBlock != "latest" {
		n, ok := new(big.Int).SetString(s.fromBlock, 10)
		if !ok || n.Sign() < 0 || !n.IsUint64() {
			fatal("CONFIG_INVALID", fmt.Errorf(
				"FROM_BLOCK %q is not a non-negative decimal block number or \"latest\"", s.fromBlock))
		}
		start = n.Uint64()
	}
	a.next = start
	for i := start; i <= head && ctx.Err() == nil; i++ {
		if a.captureDone() {
			break
		}
		a.processBlock(ctx, new(big.Int).SetUint64(i))
	}
	// The replay covered start..head, so the capture frontier begins at
	// the height after it — never below the configured start, which a
	// FROM_BLOCK above the current head leaves waiting for the chain.
	if head+1 > start {
		a.next = head + 1
	}
	// No-gap invariant: every block height from start to the final head
	// the run processes is captured exactly once. The replay loop covers
	// start..head by number; the head loop below reconciles every live
	// head against the frontier — a head at or below the frontier is an
	// already-captured duplicate and is dropped, and a head above
	// frontier+1 pulls the missing heights in by number (a public RPC
	// serves BlockByNumber of any height), so out-of-order or even lost
	// head notifications cannot open a hole. The one sanctioned hole is a
	// block fetch that still fails after processBlock's bounded retry,
	// logged as RPC_BLOCK_FAILED.
	for ctx.Err() == nil && !a.captureDone() {
		select {
		case <-ctx.Done():
		case err := <-sub.Err():
			// The pinned client (go-ethereum v1.14.12) never closes the
			// caller's head channel: on a WebSocket drop or a
			// notification-queue overflow it sends exactly one error here
			// (the rpc.Subscription contract) and the channel goes silent.
			// Without this case the select would block forever — no log,
			// no exit, restart: unless-stopped idling — while the contour
			// silently stalls.
			fatal("RPC_SUBSCRIPTION_CLOSED", err)
		case h, ok := <-headers:
			if !ok { // defensive: the pinned client never closes this
				reason := errors.New("new-head subscription channel closed without a cause")
				select {
				case err, ok := <-sub.Err():
					if ok && err != nil {
						reason = err
					}
				default:
				}
				fatal("RPC_SUBSCRIPTION_CLOSED", reason)
			}
			a.processHead(ctx, h.Number.Uint64())
		}
	}
	if a.captureDone() {
		logf("CAPTURE_COMPLETE", map[string]any{"network": a.network,
			"blocks": a.captured, "skipped": a.skipped, "events": a.eventSeq,
			"head_processed": a.processed})
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
	maxBlocks                        uint64 // 0 means follow the chain without a limit
	captured                         uint64 // blocks normalized and published by this run
	skipped                          uint64 // blocks whose block event failed to publish
	next                             uint64 // the capture frontier: the next height to process
}

// captureDone reports whether the run has its bounded window: with
// max_blocks set, the adapter captures exactly that many blocks and exits,
// so a verification run costs minutes instead of forever.
func (a *adapter) captureDone() bool { return a.maxBlocks != 0 && a.captured >= a.maxBlocks }

// processHead reconciles one live head against the capture frontier. A
// head at or below next-1 is an already-captured duplicate and is dropped;
// the frontier never regresses. A head above next means heads were missed
// or arrived out of order, so the missing heights are fetched by number
// too — a public RPC serves BlockByNumber of any height — which is what
// keeps the no-gap invariant of the capture loop in main.
func (a *adapter) processHead(ctx context.Context, height uint64) {
	if height < a.next {
		return // an already-captured height: a duplicate, not a regression
	}
	for i := a.next; i <= height; i++ {
		if a.captureDone() || ctx.Err() != nil {
			return
		}
		a.processBlock(ctx, new(big.Int).SetUint64(i))
	}
	a.next = height + 1
}

// processBlock normalizes one full block: one block lifecycle event plus
// one transaction event per body transaction, each with a validated
// semantic descriptor, and publishes them through the seam.
func (a *adapter) processBlock(ctx context.Context, number *big.Int) {
	blk, err := a.ec.BlockByNumber(ctx, number)
	if err != nil {
		// A head notification can beat its block becoming queryable by
		// number on a public endpoint, so one bounded retry after a short
		// wait closes that race without an unbounded retry loop.
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			blk, err = a.ec.BlockByNumber(ctx, number)
		}
	}
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
	if !a.publish(ctx, a.blockSubject, block, digest, map[string]any{
		"number": height, "hash": blk.Hash().Hex(), "parent_hash": blk.ParentHash().Hex(),
		"timestamp": blk.Time(), "gas_used": blk.GasUsed(), "tx_count": blk.Transactions().Len()}) {
		// The block event is the parent of every transaction event of this
		// block: publishing the transactions would leave orphaned tx
		// events in the stream without their block. The tx loop is
		// skipped, the height counts as skipped — not captured, so
		// max_blocks still demands its block from a later height — and
		// the frontier advances past it rather than retrying a publish
		// that already exhausted its transient retries.
		a.skipped++
		a.processed = height
		logf("BLOCK_SKIPPED", map[string]any{"number": height, "hash": blk.Hash().Hex(),
			"skipped": a.skipped, "reason": "block event publish failed"})
		return
	}

	// The transaction entities: family tx, ordinal the body position plus
	// one, with the receipt status of the inclusion. The receipts come
	// from one RPC call each, and a public endpoint answers slowly, so
	// they are fetched concurrently before any event is published; the
	// publish order stays the body order.
	statuses := a.receiptStatuses(ctx, blk.Transactions())
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
		from, err := types.Sender(signer, tx)
		if err != nil {
			// The event still publishes — with the zero address as
			// "from", because the field must exist — and the failure is
			// logged with the tx hash so the suspect transaction is
			// identifiable off-line.
			logf("SENDER_RECOVER_FAILED", map[string]any{"hash": tx.Hash().Hex(),
				"error": err.Error(), "note": "from stays the zero address"})
		}
		a.publish(ctx, a.txSubject, transaction, txDigest, map[string]any{
			"block_number": height, "block_hash": blk.Hash().Hex(), "tx_index": i,
			"hash": tx.Hash().Hex(), "from": from.Hex(), "to": nullableAddr(tx.To()),
			"value": tx.Value().String(), "gas": tx.Gas(), "status": statuses[i]})
	}
	a.processed = height
	a.captured++
	logf("BLOCK_PUBLISHED", map[string]any{"number": height,
		"transactions": blk.Transactions().Len(), "captured": a.captured})
}

// receiptStatuses fetches the inclusion status of one block's
// transactions with a small worker pool: a public endpoint answers one
// receipt call at a time slowly enough that a Sepolia block of two
// hundred transactions would blow the capture window sequentially. The
// statuses keep the body order; a failed fetch leaves a nil status and
// the event publishes without one.
func (a *adapter) receiptStatuses(ctx context.Context, txs types.Transactions) []any {
	statuses := make([]any, len(txs))
	if len(txs) == 0 {
		return statuses
	}
	const workers = 8
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i, tx := range txs {
		wg.Add(1)
		go func(i int, hash common.Hash) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if receipt, err := a.ec.TransactionReceipt(ctx, hash); err == nil {
				statuses[i] = receipt.Status
			} else {
				logf("RPC_RECEIPT_FAILED", map[string]any{"hash": hash.Hex(), "error": err.Error()})
			}
		}(i, tx.Hash())
	}
	wg.Wait()
	return statuses
}

// The envelope contract, stated here because nothing else names it:
// record_digest is the canonical D73 record digest of the PRE-envelope
// body — the JSON event exactly as it is marshaled before any envelope
// key exists. After the digest is taken, publish adds exactly nine keys
// to the same object:
//
//	schema, network, family, kind, decision_sequence, entity_ordinal,
//	source_sequence, descriptor_digest, record_digest
//
// The verification rule follows: strip exactly those nine keys from the
// published envelope, re-marshal the remainder and recompute the record
// digest over that body; it equals record_digest. A native event field
// whose name collides with one of the nine envelope keys is overwritten
// by the envelope value and the native value is lost — accepted and
// documented here because the native field names of this adapter
// (number, hash, parent_hash, from, to, ...) never collide with the
// envelope set.
//
// publish frames one normalized event — the semantic identity in the JSON
// envelope, the canonical record digest of the D73 encoding, the exact v2
// message id and header bytes — and drives it to a durable JetStream
// publish, retrying transient failures under the immutable id so the
// stream dedups the retries. It reports whether the event is durably
// published: false means encode failure, a permanent publish error, or a
// shutdown mid-retry.
func (a *adapter) publish(ctx context.Context, subject string,
	d semantic.SemanticDescriptor, digest semantic.Hash32, event map[string]any) bool {
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
		return false
	}
	msgID, err := proto.NatsMsgID(a.networkDigest, eventID, 0, proto.FormComplete, 0)
	must("MSG_ID_FAILED", err)
	header, err := proto.NatsHeader(msgID, a.binding.Stream)
	must("NATS_HEADER_FAILED", err)
	// The backoff doubles from 1s and caps at exactly 15s (1, 2, 4, 8,
	// 15, 15, ...); transient failures retry forever under the same
	// Nats-Msg-Id, so the stream dedups any double-stored copy.
	for backoff := time.Second; ; {
		ack, err := jetstreamAPI{a.js}.Publish(subject, header, body)
		if err == nil {
			logf("RECORD_PUBLISHED", map[string]any{"subject": subject, "msg_id": msgID,
				"stream": ack.Stream, "sequence": ack.Sequence, "bytes": len(body)})
			return true
		}
		if !errors.Is(err, publisher.ErrTransient) {
			logf("PUBLISH_FAILED", map[string]any{"subject": subject, "error": err.Error()})
			return false
		}
		logf("PUBLISH_RETRY", map[string]any{"subject": subject, "error": err.Error(), "in": backoff.String()})
		select {
		case <-ctx.Done():
			logf("PUBLISH_DROPPED", map[string]any{"subject": subject, "msg_id": msgID,
				"reason": "context canceled mid-retry"})
			return false
		case <-time.After(backoff):
			backoff = min(backoff*2, 15*time.Second)
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
	switch {
	case err == nil:
	case errors.Is(err, nats.ErrNoResponders):
		// No stream admits the subject: a routing or stream-configuration
		// error that no retry heals, so it carries the seam's permanent
		// class and the publish loop fails fast instead of retrying
		// forever.
		return publisher.PubAck{}, fmt.Errorf("%w: %v", publisher.ErrNoStream, err)
	case errors.Is(err, nats.ErrMaxPayload):
		// The frame exceeds the server's payload ceiling: permanent for
		// this message, classified with the seam's size sentinel.
		return publisher.PubAck{}, fmt.Errorf("%w: %v", publisher.ErrMsgTooLarge, err)
	default:
		// Timeouts, disconnects, reconnect-buffer overflow and JetStream
		// API errors: retryable, nothing was confirmed or lost.
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

// ensureStream creates the demo stream with its D77 identity metadata and
// the 5-minute retention when — and only when — it is missing. A
// pre-existing stream is never rewritten: if its retention differs from
// the demo retention, the mismatch is logged (RETENTION_MISMATCH, current
// versus wanted) and the stream is left exactly as its creator made it;
// changing another actor's stream configuration is not this adapter's
// call, and the destination check that follows still reports what the
// stream actually is.
func ensureStream(ctx context.Context, js nats.JetStreamContext, b destination.Binding) error {
	info, err := js.StreamInfo(b.Stream, nats.Context(ctx))
	if err == nil {
		if info.Config.MaxAge != streamRetention {
			logf("RETENTION_MISMATCH", map[string]any{"stream": b.Stream,
				"current_max_age": info.Config.MaxAge.String(),
				"wanted_max_age":  streamRetention.String(),
				"action":          "kept the existing configuration"})
		}
		return nil
	} else if !errors.Is(err, nats.ErrStreamNotFound) {
		return err
	}
	networkDigest, err := b.NetworkDigest()
	if err == nil {
		_, err = js.AddStream(&nats.StreamConfig{Name: b.Stream,
			Subjects: []string{b.SubjectPrefix + ".>"},
			MaxAge:   streamRetention,
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

// envUint reads one numeric configuration variable. An unset variable
// yields the default; a set but malformed value fails fast (CONFIG_INVALID,
// exit 2) instead of silently running with the default — MAX_BLOCKS=abc
// must not quietly mean 20.
func envUint(key string, def uint64) uint64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		fatal("CONFIG_INVALID", fmt.Errorf("%s=%q is not a decimal number: %v", key, v, err))
	}
	return n
}

// applyConfig loads a YAML config over the resolved settings: the YAML
// wins over the environment key by key; keys the file does not set keep
// their environment or default value.
func applyConfig(path string, s *settings) error {
	file, err := parseConfig(path)
	if err != nil {
		return err
	}
	if v := file["rpc_url"]; v != "" {
		s.rpcURL = v
	}
	if v := file["nats_url"]; v != "" {
		s.natsURL = v
	}
	if v := file["network"]; v != "" {
		s.network = v
	}
	if v := file["chain_id"]; v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return fmt.Errorf("chain_id %q is not a number: %w", v, err)
		}
		s.chainID = n
	}
	if v := file["max_blocks"]; v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return fmt.Errorf("max_blocks %q is not a number: %w", v, err)
		}
		s.maxBlocks = n
	}
	return nil
}

// parseConfig reads a flat "key: value" YAML file into a map. The configs
// of this contour are flat scalars, so no YAML library is pulled in:
// blank and comment lines are skipped, values may be quoted, and trailing
// comments are stripped.
func parseConfig(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%s:%d: not a key: value line: %q", path, i+1, line)
		}
		key = strings.TrimSpace(key)
		if cut := strings.Index(value, " #"); cut >= 0 {
			value = value[:cut]
		}
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		out[key] = value
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no settings found", path)
	}
	return out, nil
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
