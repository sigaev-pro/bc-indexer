// The stream verifier of the multi-network verification: a small
// read-back tool for the demo JetStream server. With -purge it empties
// one stream before a capture run so the run's counts are exact; without
// it, the tool reads every message of the stream, checks the network
// identity each message claims, and counts the block and transaction
// events. The result is one JSON line that the verification script
// consumes. No chain data is stored anywhere: the tool only reads.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
)

// exit codes of the tool: 0 verified, 2 verification failed, 3 the stream
// is missing. The verification script maps them to the summary table.
const (
	exitOK       = 0
	exitFailed   = 2
	exitNoStream = 3
)

func main() {
	natsURL := flag.String("nats", "nats://localhost:4222", "URL of the demo NATS server")
	stream := flag.String("stream", "", "the stream to purge or verify, e.g. EXTRACTOR_ETH_SEPOLIA")
	prefix := flag.String("prefix", "", "the subject prefix to read, e.g. extractor.eth-sepolia")
	network := flag.String("network", "", "the network name every message must claim")
	purge := flag.Bool("purge", false, "purge the stream and exit instead of verifying")
	timeout := flag.Duration("timeout", 30*time.Second, "how long to wait for the stream messages")
	flag.Parse()

	if *stream == "" {
		fmt.Fprintln(os.Stderr, "-stream is required")
		os.Exit(exitFailed)
	}
	nc, err := nats.Connect(*natsURL, nats.Timeout(10*time.Second))
	if err != nil {
		emit(map[string]any{"stream": *stream, "ok": false, "error": "nats connect: " + err.Error()})
		os.Exit(exitFailed)
	}
	defer nc.Close()
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(1 << 20))
	if err != nil {
		emit(map[string]any{"stream": *stream, "ok": false, "error": "jetstream: " + err.Error()})
		os.Exit(exitFailed)
	}
	if *purge {
		purgeStream(js, *stream)
		return
	}
	os.Exit(verify(js, *stream, *prefix, *network, *timeout))
}

// purgeStream empties the stream when it exists; a missing stream is fine
// and reports as such, because the first run has not created it yet.
func purgeStream(js nats.JetStreamContext, stream string) {
	info, err := js.StreamInfo(stream)
	if errors.Is(err, nats.ErrStreamNotFound) {
		emit(map[string]any{"stream": stream, "purged": false, "existed": false})
		return
	}
	if err != nil {
		emit(map[string]any{"stream": stream, "purged": false, "error": err.Error()})
		os.Exit(exitFailed)
	}
	if err := js.PurgeStream(stream); err != nil {
		emit(map[string]any{"stream": stream, "purged": false, "error": err.Error()})
		os.Exit(exitFailed)
	}
	emit(map[string]any{"stream": stream, "purged": true, "existed": true,
		"removed": info.State.Msgs})
}

// verify reads the whole stream through an ephemeral ordered consumer and
// checks the network identity of every message.
func verify(js nats.JetStreamContext, stream, prefix, network string,
	timeout time.Duration) int {
	info, err := js.StreamInfo(stream)
	if errors.Is(err, nats.ErrStreamNotFound) {
		emit(map[string]any{"stream": stream, "exists": false, "ok": false,
			"error": "stream not found"})
		return exitNoStream
	}
	if err != nil {
		emit(map[string]any{"stream": stream, "ok": false, "error": err.Error()})
		return exitFailed
	}
	total := info.State.Msgs
	result := map[string]any{"stream": stream, "expected_network": network,
		"exists": true, "stream_messages": total,
		"max_age_seconds": info.Config.MaxAge.Seconds()}
	if prefix == "" || network == "" {
		result["error"] = "-prefix and -network are required to verify"
		emit(result)
		return exitFailed
	}
	if total == 0 {
		result["ok"] = false
		result["error"] = "stream is empty"
		emit(result)
		return exitFailed
	}

	msgs := make(chan *nats.Msg, 256)
	sub, err := js.ChanSubscribe(prefix+".>", msgs, nats.OrderedConsumer())
	if err != nil {
		result["ok"] = false
		result["error"] = "subscribe: " + err.Error()
		emit(result)
		return exitFailed
	}
	defer sub.Unsubscribe()

	var read, mismatches, blocks, transactions int
	var firstBlock, lastBlock float64
	blockNumbers := make(map[float64]bool)
	deadline := time.Now().Add(timeout)
	for read < int(total) {
		var msg *nats.Msg
		select {
		case msg = <-msgs:
		case <-time.After(2 * time.Second):
			if time.Now().After(deadline) {
				result["ok"] = false
				result["error"] = fmt.Sprintf("timeout: read %d of %d messages", read, total)
				emit(result)
				return exitFailed
			}
			continue
		}
		read++
		var event map[string]any
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			mismatches++ // an unreadable body cannot prove its identity
			continue
		}
		if name, _ := event["network"].(string); name != network {
			mismatches++
			continue
		}
		switch family, _ := event["family"].(string); family {
		case "block":
			blocks++
			number, _ := event["number"].(float64)
			blockNumbers[number] = true
			if firstBlock == 0 || number < firstBlock {
				firstBlock = number
			}
			if number > lastBlock {
				lastBlock = number
			}
		case "tx":
			transactions++
		}
	}
	distinct := make([]float64, 0, len(blockNumbers))
	for number := range blockNumbers {
		distinct = append(distinct, number)
	}
	// A run proves the network identity when the stream carries exactly
	// the messages that were published, every one of them claims the
	// configured network, and at least one block was captured.
	ok := read == int(total) && mismatches == 0 && blocks > 0
	result["read"] = read
	result["blocks"] = blocks
	result["distinct_blocks"] = len(distinct)
	result["transactions"] = transactions
	result["network_mismatches"] = mismatches
	if blocks > 0 {
		result["first_block"] = firstBlock
		result["last_block"] = lastBlock
	}
	result["ok"] = ok
	emit(result)
	if ok {
		return exitOK
	}
	return exitFailed
}

// emit writes one JSON result line to stdout.
func emit(fields map[string]any) {
	line, err := json.Marshal(fields)
	if err != nil {
		fmt.Println(`{"ok":false,"error":"result encode failed"}`)
		return
	}
	fmt.Println(string(line))
}
