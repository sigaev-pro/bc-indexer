#!/usr/bin/env bash
#
# verify-all-networks.sh — verifies all three test networks through the
# real-network adapter using ONLY public RPC endpoints: Ethereum Sepolia,
# BSC Chapel testnet and Polygon Amoy. No local blockchain node is
# started, no chain data is stored on disk: the adapters capture a small
# window (max_blocks per config) of JSON events into the demo NATS
# JetStream, where every stream expires after 5 minutes.
#
# The script
#   1. uses the existing demo NATS on localhost:4222 (starts the stopped
#      demo-nats-1 container if needed),
#   2. builds the adapter and the stream verifier,
#   3. purges the three verification streams so the run counts are exact,
#   4. launches one adapter per configs/*.yml in parallel, each capturing
#      the last max_blocks blocks from its public endpoint,
#   5. reads every published message back — each network right after its
#      own capture, within the 5-minute retention — and checks the
#      network identity it claims, the chain identity the adapter derived
#      from the live endpoint, and the 5-minute retention,
#   6. prints a summary table and fails if any network did not verify.
#
# Usage: scripts/verify-all-networks.sh
# Environment: NATS_URL (default nats://localhost:4222),
#              VERIFY_TIMEOUT (seconds per run, default 300).

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ADAPTER_DIR="$ROOT/cmd/extractor-rpc"
VERIFIER_DIR="$ROOT/scripts/verify-streams"
CONFIG_DIR="$ROOT/configs"
NATS_URL="${NATS_URL:-nats://localhost:4222}"
VERIFY_TIMEOUT="${VERIFY_TIMEOUT:-300}"
NETWORKS=(eth-sepolia bsc-testnet poly-testnet)

WORK="$(mktemp -d "${TMPDIR:-/tmp}/verify-all-networks.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

say() { printf '%s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# config_value prints the value of one flat "key: value" YAML line.
config_value() {
	sed -n "s/^$2:[[:space:]]*//p" "$1" | head -1 | sed 's/[[:space:]]*#.*//; s/^"//; s/"$//'
}

# nats_alive reports whether the demo NATS accepts connections.
nats_alive() {
	local host="${NATS_URL#nats://}" port
	host="${host%%/*}"
	port="${host##*:}"
	host="${host%%:*}"
	port="${port:-4222}"
	(exec 3<>"/dev/tcp/$host/$port") 2>/dev/null
}

# stream_of maps the config network name to its stream, exactly like the
# adapter does: EXTRACTOR_ plus the upper-cased network with underscores.
stream_of() {
	printf 'EXTRACTOR_%s' "$(printf '%s' "$1" | tr '[:lower:]-' '[:upper:]_')"
}

say "== Multi-network verification via public RPC endpoints =="
say "Work dir: $WORK"

# 1. The demo NATS: use it as-is; start the stopped demo container when
#    it is not listening.
if nats_alive; then
	say "NATS: using the demo server at $NATS_URL"
else
	say "NATS: not reachable, starting the demo-nats-1 container"
	docker start demo-nats-1 >/dev/null 2>&1 || true
	for _ in $(seq 1 15); do
		nats_alive && break
		sleep 1
	done
	nats_alive || die "no NATS server at $NATS_URL; start the demo NATS first"
	say "NATS: demo server is up"
fi

# 2. Build the adapter and the verifier.
say "Building the adapter and the verifier"
(cd "$ADAPTER_DIR" && go build -o "$WORK/extractor-rpc" .) || die "adapter build failed"
(cd "$VERIFIER_DIR" && go build -o "$WORK/verify-streams" .) || die "verifier build failed"

# 3. Purge the verification streams: the counts of this run must be exact
#    even if an earlier run left messages in them.
for network in "${NETWORKS[@]}"; do
	"$WORK/verify-streams" -nats "$NATS_URL" -stream "$(stream_of "$network")" -purge
done

# 4. One adapter per config, in parallel. The FROM_BLOCK window targets
#    the last max_blocks blocks through the endpoint's HTTP twin, so the
#    run takes seconds; if the head query fails, the adapter falls back
#    to following live blocks, still bounded by max_blocks.
pids=()
for network in "${NETWORKS[@]}"; do
	config="$CONFIG_DIR/$network.yml"
	[ -f "$config" ] || die "missing config $config"
	rpc_url="$(config_value "$config" rpc_url)"
	max_blocks="$(config_value "$config" max_blocks)"
	max_blocks="${max_blocks:-20}"
	http_url="${rpc_url/wss:/https:}"
	from_block="latest"
	if latest_hex="$(curl -m 15 -s -X POST -H 'Content-Type: application/json' \
		--data '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}' \
		"$http_url" 2>/dev/null | jq -er '.result' 2>/dev/null)"; then
		latest=$((16#"${latest_hex#0x}"))
		if [ "$latest" -ge "$max_blocks" ]; then
			from_block=$((latest - max_blocks + 1))
		fi
		say "$network: public head $latest, capturing from block $from_block"
	else
		say "$network: head query failed on $http_url, following live blocks"
	fi
	(
		# The deadline watchdog stops the wrapper; the trap relays the
		# stop to the adapter, whose signal handler exits cleanly.
		trap 'kill "$adapter_pid" 2>/dev/null' TERM
		FROM_BLOCK="$from_block" "$WORK/extractor-rpc" --config "$config" \
			>"$WORK/$network.log" 2>&1 &
		adapter_pid=$!
		wait "$adapter_pid"
		printf '%s\n' "$?" >"$WORK/$network.exit"
		# The read-back starts the moment this capture ends: the stream
		# retention is 5 minutes, so the verifier of a finished network
		# must not queue behind the other networks.
		"$WORK/verify-streams" -nats "$NATS_URL" \
			-stream "$(stream_of "$network")" -prefix "extractor.$network" \
			-network "$network" >"$WORK/$network.verify.json" 2>/dev/null
	) &
	pids+=("$!")
done

# Wait for every adapter to finish its window, or stop it at the
# deadline: each run gets a watchdog, and an unfinished run then reports
# as a failure instead of hanging the verification.
watchdogs=()
for i in "${!NETWORKS[@]}"; do
	( sleep "$VERIFY_TIMEOUT" && kill "${pids[$i]}" 2>/dev/null ) &
	watchdogs+=("$!")
done
for i in "${!NETWORKS[@]}"; do
	# The wrapper has already written the adapter's exit code and this
	# network's read-back result.
	wait "${pids[$i]}" 2>/dev/null || true
done
for pid in "${watchdogs[@]}"; do
	kill "$pid" 2>/dev/null || true
done
wait 2>/dev/null || true

# 5. Read every stream back and verify identity, chain and retention.
say ""
overall=0
declare -a table failed_networks
for network in "${NETWORKS[@]}"; do
	config="$CONFIG_DIR/$network.yml"
	max_blocks="$(config_value "$config" max_blocks)"
	max_blocks="${max_blocks:-20}"
	exit_code="$(cat "$WORK/$network.exit")"

	# The chain identity the adapter derived from the live endpoint.
	chain_id="$(jq -r 'select(.event_code == "NETWORK_IDENTIFIED") | .chain_id' \
		"$WORK/$network.log" 2>/dev/null | tail -1)"
	chain_id="${chain_id:--}"

	# The read-back JSON the wrapper captured right after this capture.
	verify_json="$(cat "$WORK/$network.verify.json" 2>/dev/null)"
	messages="$(jq -r '.stream_messages // 0' <<<"$verify_json" 2>/dev/null)"
	blocks="$(jq -r '.blocks // 0' <<<"$verify_json" 2>/dev/null)"
	mismatches="$(jq -r '.network_mismatches // "?"' <<<"$verify_json" 2>/dev/null)"
	max_age="$(jq -r '.max_age_seconds // 0' <<<"$verify_json" 2>/dev/null)"

	status="PASS"
	reason=""
	[ "$exit_code" = "0" ] || { status="FAIL"; reason="adapter exit $exit_code"; }
	[ "$blocks" != "0" ] || { status="FAIL"; reason="$reason no block events in the stream"; }
	[ "$mismatches" = "0" ] || { status="FAIL"; reason="$reason $mismatches messages claim a wrong network"; }
	[ "$blocks" = "$max_blocks" ] || { status="FAIL"; reason="$reason captured $blocks of $max_blocks blocks"; }
	[ "$max_age" = "300" ] || { status="FAIL"; reason="$reason stream retention is ${max_age}s, not 300s"; }

	table+=("$network|$chain_id|$blocks/$max_blocks|$messages|$status")
	if [ "$status" = "FAIL" ]; then
		overall=1
		failed_networks+=("$network")
	fi
done

# 6. The summary table.
say "Verification results:"
printf '%-14s %-9s %-15s %-10s %s\n' NETWORK CHAIN_ID BLOCKS MESSAGES STATUS
for row in "${table[@]}"; do
	IFS='|' read -r network chain_id blocks messages status <<<"$row"
	printf '%-14s %-9s %-15s %-10s %s\n' "$network" "$chain_id" "$blocks" "$messages" "$status"
done

# The failure details: the last log lines of every failing run.
for network in "${failed_networks[@]:-}"; do
	[ -n "$network" ] || continue
	say ""
	say "Last log lines of $network:"
	tail -5 "$WORK/$network.log"
done

if [ "$overall" = "0" ]; then
	say ""
	say "All three networks verified through their public endpoints."
else
	say ""
	say "Some networks failed verification; see the details above."
fi
exit "$overall"
