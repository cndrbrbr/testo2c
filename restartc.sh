#!/usr/bin/env bash
# restartc.sh — restart the testo2c cluster (external Caddy mode) and refresh
# the shared caddy-proxy so it picks up the recreated containers.
#
# Why this is needed:
#   testo2c runs behind the shared `caddy-proxy` reverse proxy (../caddy-proxy),
#   which reaches orbital-node1..3 / sim-agent-1..3 by container name over the
#   external `proxy` Docker network. That network attachment is only added via
#   the docker-compose.caddy-external.yml override. A plain `docker compose
#   down && up` (without the override) drops the containers from `proxy`, and
#   even after rejoining, Caddy's resolver can keep a stale negative DNS cache
#   for the old container names/IPs — both leave the cluster unreachable from
#   outside until Caddy is restarted.
#
# Usage:
#   bash restartc.sh
#
# Requires the caddy-proxy stack (cndrbrbr/caddy-proxy) to already be running.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CADDY_PROXY_CONTAINER="caddy-proxy-caddy-1"

cd "${SCRIPT_DIR}"

echo "=== restarting testo2c (external Caddy mode) ==="
docker compose -f docker-compose.yml -f docker-compose.caddy-external.yml down --remove-orphans
docker compose -f docker-compose.yml -f docker-compose.caddy-external.yml up -d

echo
echo "Waiting for orbital nodes to become healthy..."
for i in $(seq 1 60); do
    healthy=$(docker compose -f docker-compose.yml -f docker-compose.caddy-external.yml ps \
        --format '{{.Service}} {{.Health}}' | grep -c '^orbital-node[0-9] healthy' || true)
    [ "${healthy}" = "3" ] && break
    sleep 2
done

echo
echo "=== restarting shared caddy-proxy to refresh DNS/upstream connections ==="
if docker ps --format '{{.Names}}' | grep -qx "${CADDY_PROXY_CONTAINER}"; then
    docker restart "${CADDY_PROXY_CONTAINER}"
else
    echo "WARNING: ${CADDY_PROXY_CONTAINER} is not running — start the caddy-proxy stack first." >&2
    exit 1
fi

echo
echo "Waiting for caddy-proxy to come back up..."
for i in $(seq 1 30); do
    if docker exec "${CADDY_PROXY_CONTAINER}" wget -qO- http://orbital-node1:8080/healthz 2>/dev/null | grep -q ok; then
        break
    fi
    sleep 2
done

echo
echo "=== done ==="
echo "Check from outside with e.g.:"
echo "  curl -sk https://o2c.codefield.de:35581/healthz"
echo "  curl -sk https://o2c.codefield.de:35582/healthz"
echo "  curl -sk https://o2c.codefield.de:35583/healthz"
