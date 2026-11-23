# verify-streams

The read-back verifier of the multi-network verification: a small
command-line tool that inspects the JetStream streams the extractor-rpc
adapter publishes to. With -purge it empties one stream so a capture
run's counts are exact; without it, the tool reads every message of the
stream, checks the network identity each message claims, and counts the
block and transaction events plus the distinct block numbers. The result
is one JSON line on stdout.

## How to run

    cd scripts/verify-streams && go build .
    ./verify-streams -nats nats://localhost:4222 \
        -stream EXTRACTOR_ETH_SEPOLIA \
        -prefix extractor.eth-sepolia -network eth-sepolia

Flags:

- -nats: URL of the NATS server (default nats://localhost:4222).
- -stream: the JetStream stream to purge or verify (required).
- -prefix: the subject prefix to read, e.g. extractor.eth-sepolia.
- -network: the network name every message must claim.
- -purge: empty the stream and exit instead of verifying.
- -timeout: how long to wait for the stream messages (default 30s).

Exit codes: 0 verified, 2 verification failed, 3 the stream is missing.

The tool only reads; no chain data is stored anywhere. The JSON result
carries stream_messages, blocks, distinct_blocks, transactions,
network_mismatches, first_block, last_block and ok. distinct_blocks
counted against the intended window is what proves the adapter's no-gap
capture: every height of the window exactly once. The wrapper script
scripts/verify-all-networks.sh at the repository root drives this tool
for all three public networks and asserts distinct_blocks equals the
configured max_blocks.

The message bodies it checks follow the envelope contract documented in
the publish function comment of cmd/extractor-rpc in this repository.
