#!/usr/bin/env bash
# Bring up the full deployment stack: bootstrap the environment, then
# start every service of the three network circuits.
set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

"${DEPLOY_DIR}/scripts/bootstrap.sh"

echo "deploy: starting all services"
# The base stack publishes no host ports; the diagnostics override
# carries the loopback NATS ports the bootstrap and verification use.
docker compose -f "${DEPLOY_DIR}/docker-compose.yml" -f "${DEPLOY_DIR}/docker-compose.diagnostics.yml" up -d --build --wait

echo "deploy: all services are up"
echo "deploy: verify with ${DEPLOY_DIR}/scripts/verify.sh"
