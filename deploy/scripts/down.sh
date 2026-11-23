#!/usr/bin/env bash
# Stop the deployment stack gracefully. Volumes are kept: the WAL of
# every adapter, the journal of every gateway and the JetStream store
# survive the stop, and the next start recovers pending delivery from
# them. Data is destroyed only by the explicit --volumes flag.
set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [ "${1:-}" = "--volumes" ]; then
    echo "deploy: stopping the stack and REMOVING ALL DATA VOLUMES"
    echo "deploy: this deletes every WAL, journal and the JetStream store"
    docker compose -f "${DEPLOY_DIR}/docker-compose.yml" down -v --remove-orphans
    echo "deploy: stack removed"
    exit 0
fi

echo "deploy: stopping the stack, keeping all volumes"
docker compose -f "${DEPLOY_DIR}/docker-compose.yml" down --remove-orphans
echo "deploy: stack stopped"
