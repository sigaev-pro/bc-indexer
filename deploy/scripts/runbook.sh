#!/usr/bin/env bash
# The operator runbook of the deployment stack: the observation and
# recovery drills of E06-09 (live start) as reproducible commands.
# Readiness and recovery are observed in the control logs (D52); no
# product API or probe is introduced by these commands.
#
# Usage: runbook.sh COMMAND [ARGS]
#   status              service table and the latest control event of every process
#   logs NETWORK        follow the control logs of one network (eth|bsc|polygon)
#   events SERVICE      print the last control events of one service
#   restart-network NET restart one circuit (gateway and adapter) and
#                       show that the other circuits keep delivering
#   stop-nats           stop NATS: capture continues into the WALs
#   start-nats          start NATS again: the backlog drains, nothing is lost
#   drain NETWORK       stop a network's adapter, then run it once in drain
#                       mode: pending events deliver, then the process exits
#   tail NETWORK        print the last message of every event route of a network
set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BOOTSTRAP="${DEPLOY_DIR}/.build/bootstrap"
SERVER="tls://127.0.0.1:14222"

# The base stack publishes no host ports; the diagnostics override
# carries the loopback NATS ports the runbook observes.
compose() {
    docker compose -f "${DEPLOY_DIR}/docker-compose.yml" -f "${DEPLOY_DIR}/docker-compose.diagnostics.yml" "$@"
}

usage() { grep '^#   ' "${BASH_SOURCE[0]}" | sed 's/^#   //'; exit 2; }

latest_event() {
    compose logs --no-log-prefix "$1" 2>/dev/null | grep '"event_code"' | tail -n "${2:-1}" || true
}

latest_named_event() {
    compose logs --no-log-prefix "$1" 2>/dev/null | grep "\"$2\"" | tail -n "${3:-1}" || true
}

case "${1:-}" in
status)
    compose ps --format 'table {{.Name}}\t{{.Service}}\t{{.Status}}'
    echo
    for svc in nats eth-gateway eth-adapter bsc-gateway bsc-adapter polygon-gateway polygon-adapter; do
        printf '%-18s %s\n' "${svc}" "$(latest_event "${svc}" | head -c 400 || true)"
    done
    ;;
logs)
    [ $# -eq 2 ] || usage
    compose logs -f "${2}-gateway" "${2}-adapter"
    ;;
events)
    [ $# -ge 2 ] || usage
    latest_event "$2" "${3:-10}"
    ;;
restart-network)
    [ $# -eq 2 ] || usage
    net="$2"
    case "${net}" in eth|bsc|polygon) ;; *) usage ;; esac
    echo "runbook: restarting the ${net} circuit; the other circuits are untouched"
    compose restart "${net}-gateway" "${net}-adapter"
    echo
    echo "runbook: the ${net} adapter recovery events"
    sleep 8
    latest_event "${net}-adapter" 12
    echo
    echo "runbook: the untouched circuits still confirm deliveries"
    for other in eth bsc polygon; do
        [ "${other}" = "${net}" ] && continue
        printf '%s: %s\n' "${other}" "$(latest_event "${other}-adapter" | head -c 300 || true)"
    done
    ;;
stop-nats)
    echo "runbook: stopping NATS; every adapter keeps capturing into its WAL"
    compose stop nats
    sleep 6
    for net in eth bsc polygon; do
        printf '%s WAL_SAVED: %s\n' "${net}" "$(latest_named_event "${net}-adapter" WAL_SAVED 1 || echo none yet)"
    done
    echo
    echo "runbook: run scripts/runbook.sh start-nats to drain the backlog"
    ;;
start-nats)
    echo "runbook: starting NATS; the gateways reconnect and the WALs drain"
    compose up -d --wait nats
    sleep 10
    for net in eth bsc polygon; do
        printf '%s DELIVERY_CONFIRMED: %s\n' "${net}" "$(latest_named_event "${net}-adapter" DELIVERY_CONFIRMED 1 || echo none yet)"
    done
    ;;
drain)
    [ $# -eq 2 ] || usage
    net="$2"
    case "${net}" in eth|bsc|polygon) ;; *) usage ;; esac
    echo "runbook: ${net} pending-recovery drill"
    echo "runbook: 1. stopping the ${net} gateway; the adapter keeps capturing into its WAL"
    compose stop "${net}-gateway"
    sleep 12
    echo "runbook: 2. latest WAL state while the gateway is down"
    latest_named_event "${net}-adapter" WAL_SAVED 1 || true
    echo "runbook: 3. stopping the adapter and starting the gateway again"
    compose stop "${net}-adapter"
    compose up -d "${net}-gateway"
    echo "runbook: 4. running the adapter once in drain mode: pending events deliver, then the process exits"
    compose run --rm --no-deps -T "${net}-adapter" --extractor-mode=drain | grep -E 'DRAIN_|RECOVERY_|DELIVERY_CONFIRMED' | tail -n 6 || true
    echo "runbook: 5. drain finished; restart live capture"
    compose up -d "${net}-adapter"
    ;;
tail)
    [ $# -eq 2 ] || usage
    [ -x "${BOOTSTRAP}" ] || { echo "runbook: bootstrap tool is missing; run scripts/bootstrap.sh" >&2; exit 1; }
    "${BOOTSTRAP}" -secrets-dir "${DEPLOY_DIR}/secrets" -server "${SERVER}" peek | grep "^$2 " || true
    ;;
*)
    usage
    ;;
esac
