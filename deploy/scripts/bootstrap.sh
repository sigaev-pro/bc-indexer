#!/usr/bin/env bash
# Bootstrap the deployment environment from a clean checkout (E06-13):
# generate or verify the binding descriptors, generate the trust
# material and the NATS accounts, start NATS alone, and create the
# per-network streams with the D77 identity metadata.
#
# The command is idempotent: bindings and streams are verified, never
# silently replaced, and existing trust material is never regenerated.
set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG_DIR="${DEPLOY_DIR}/config"
SECRETS_DIR="${DEPLOY_DIR}/secrets"
BUILD_DIR="${DEPLOY_DIR}/.build"
SERVER="tls://127.0.0.1:14222"

# The base stack publishes no host ports (NETWORK_PORTS); the
# diagnostic override carries the loopback NATS ports this host-side
# bootstrap connects to.
compose() {
    docker compose -f "${DEPLOY_DIR}/docker-compose.yml" -f "${DEPLOY_DIR}/docker-compose.diagnostics.yml" "$@"
}

log() { printf 'bootstrap: %s\n' "$*"; }

command -v docker >/dev/null 2>&1 || { log "docker is required"; exit 1; }
command -v go >/dev/null 2>&1 || { log "go is required for the bootstrap tool"; exit 1; }

mkdir -p "${BUILD_DIR}"
log "building the bootstrap tool"
(cd "${DEPLOY_DIR}/bootstrap" && go build -o "${BUILD_DIR}/bootstrap" .)
BOOTSTRAP="${BUILD_DIR}/bootstrap"

log "verifying the binding descriptors against the authoritative contract reader"
"${BOOTSTRAP}" -config-dir "${CONFIG_DIR}" bindings

if [ ! -f "${SECRETS_DIR}/nats/nats.conf" ]; then
    log "generating the CA, the service certificates and the NATS accounts"
    "${BOOTSTRAP}" -config-dir "${CONFIG_DIR}" -secrets-dir "${SECRETS_DIR}" secrets
else
    log "trust material already present, keeping it"
fi

log "starting NATS"
compose up -d --wait nats

log "creating or verifying the per-network streams with the D77 metadata"
"${BOOTSTRAP}" -secrets-dir "${SECRETS_DIR}" -server "${SERVER}" streams

log "bootstrap complete"
