# extractor-rpc

The real-network extractor adapter: a demonstration that the delivery
pipeline of this repository carries live blockchain data. It connects to
an unpatched blockchain node over WebSocket JSON-RPC — a local Geth or a
public testnet endpoint — identifies the chain from the live node, builds
and verifies the destination binding, provisions the JetStream stream,
then follows new heads and publishes one block event plus one transaction
event per body transaction as JSON envelopes.

This is a demonstration adapter, not a production service. Every run is
bounded by max_blocks; the streams it creates expire their messages after
five minutes so no chain data accumulates on disk.

## How to run

    cd cmd/extractor-rpc && go build .
    NATS_URL=nats://localhost:4222 \
    RPC_URL=wss://ethereum-sepolia-rpc.publicnode.com \
    NETWORK=eth-sepolia MAX_BLOCKS=20 FROM_BLOCK=latest \
    ./extractor-rpc

Configuration comes from the environment or a flat YAML file passed with
--config (the YAML wins key by key). The configs/ directory at the
repository root holds one file per public network with the pinned
chain_id. Keys:

- RPC_URL: WebSocket endpoint of the node (ws:// or wss://).
- NATS_URL: NATS server with JetStream enabled.
- NETWORK: contour name; selects the subject prefix and stream name.
- FROM_BLOCK: "latest" to capture from startup, or a non-negative
  decimal height to replay history first.
- CHAIN_ID: optional expected chain id, checked against the live node.
- MAX_BLOCKS: capture window; 0 follows the chain forever.

docker-compose.sepolia.yml at the repository root runs the full Sepolia
stack: an upstream Geth node, the NATS JetStream server and this adapter.

## Public endpoints used by the verification runs

- Ethereum Sepolia: wss://ethereum-sepolia-rpc.publicnode.com
- BSC Chapel testnet: wss://bsc-testnet-rpc.publicnode.com
- Polygon Amoy: wss://polygon-amoy-bor-rpc.publicnode.com

scripts/verify-all-networks.sh at the repository root captures a bounded
window from all three in one go.

## Subjects and the envelope contract

The adapter publishes to extractor.<network>.block and
extractor.<network>.transaction inside the stream
EXTRACTOR_<NETWORK_UPPER>. Each message body is the JSON event wrapped in
the envelope contract: record_digest is the canonical D73 record digest
of the pre-envelope body, then nine keys are added — schema, network,
family, kind, decision_sequence, entity_ordinal, source_sequence,
descriptor_digest, record_digest. The exact contract, including the
strip-nine-keys verification rule, is documented in the comment block on
the publish function in main.go of this directory.

## Capture guarantees

The subscription is established before the head is sampled and every
live head is reconciled against the capture frontier by fetching missing
heights by number, so every block height from start to the final head
the run processes is captured exactly once — with one sanctioned hole: a
block fetch that still fails after the bounded retry is logged as
RPC_BLOCK_FAILED and the height is skipped (live on Sepolia while the
pinned go-ethereum v1.14.12 cannot decode a recent transaction type; a
PASS there means max_blocks distinct heights with logged holes, not a
consecutive window). A failed block-event publish skips the block's
transactions (no orphaned tx events) and counts the block as skipped,
logged as BLOCK_SKIPPED.
