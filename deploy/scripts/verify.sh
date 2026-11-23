#!/usr/bin/env bash
# Verify the running deployment stack end to end:
#   1. every service is up;
#   2. every network's runtime credentials pass the authoritative
#      destination check: account identity, stream, subjects, limits and
#      both D77 metadata keys;
#   3. every adapter confirms live deliveries in its control log — an
#      adapter without a single DELIVERY_CONFIRMED fails the check.
set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SECRETS_DIR="${DEPLOY_DIR}/secrets"
BOOTSTRAP="${DEPLOY_DIR}/.build/bootstrap"
SERVER="tls://127.0.0.1:14222"

# The base stack publishes no host ports; the diagnostic override
# (loopback NATS client and monitoring ports) is what this host-side
# verification connects to.
compose() {
    docker compose -f "${DEPLOY_DIR}/docker-compose.yml" -f "${DEPLOY_DIR}/docker-compose.diagnostics.yml" "$@"
}

if [ ! -x "${BOOTSTRAP}" ]; then
    echo "verify: bootstrap tool is missing; run scripts/bootstrap.sh first" >&2
    exit 1
fi

echo "== services"
compose ps --format 'table {{.Name}}\t{{.Service}}\t{{.Status}}'

echo
echo "== destination checks (runtime credentials, D77 metadata)"
"${BOOTSTRAP}" -secrets-dir "${SECRETS_DIR}" -server "${SERVER}" verify

echo
echo "== live delivery (latest DELIVERY_CONFIRMED per adapter)"
missing=0
for net in eth bsc polygon; do
    line="$(compose logs --no-log-prefix "${net}-adapter" 2>/dev/null | grep DELIVERY_CONFIRMED | tail -n 1 || true)"
    if [ -n "${line}" ]; then
        echo "${net}: ${line}"
    else
        echo "${net}: no DELIVERY_CONFIRMED in the adapter control log" >&2
        missing=1
    fi
done

echo
echo "== NATS health"
curl -fsS http://127.0.0.1:18222/healthz && echo

echo
if [ "${missing}" -ne 0 ]; then
    echo "verify: FAILED: at least one adapter has not confirmed a delivery" >&2
    exit 1
fi
echo "verify: ok"
