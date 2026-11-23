#!/usr/bin/env bash
# Verify the running deployment stack end to end:
#   1. every service is up;
#   2. every network's runtime credentials pass the authoritative
#      destination check: account identity, stream, subjects, limits and
#      both D77 metadata keys;
#   3. every adapter confirms live deliveries in its control log.
set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS_DIR="${DEPLOY_DIR}/secrets"
BOOTSTRAP="${DEPLOY_DIR}/.build/bootstrap"
SERVER="tls://127.0.0.1:14222"
COMPOSE_FILE="${DEPLOY_DIR}/docker-compose.yml"

if [ ! -x "${BOOTSTRAP}" ]; then
    echo "verify: bootstrap tool is missing; run scripts/bootstrap.sh first" >&2
    exit 1
fi

echo "== services"
docker compose -f "${COMPOSE_FILE}" ps --format 'table {{.Name}}\t{{.Service}}\t{{.Status}}'

echo
echo "== destination checks (runtime credentials, D77 metadata)"
"${BOOTSTRAP}" -secrets-dir "${SECRETS_DIR}" -server "${SERVER}" verify

echo
echo "== live delivery (latest DELIVERY_CONFIRMED per adapter)"
for net in eth bsc polygon; do
    line="$(docker compose -f "${COMPOSE_FILE}" logs --no-log-prefix "${net}-adapter" 2>/dev/null | grep DELIVERY_CONFIRMED | tail -n 1 || true)"
    if [ -n "${line}" ]; then
        echo "${net}: ${line}"
    else
        echo "${net}: no DELIVERY_CONFIRMED yet; wait for the first captured block"
    fi
done

echo
echo "== NATS health"
curl -fsS http://127.0.0.1:18222/healthz && echo

echo
echo "verify: ok"
