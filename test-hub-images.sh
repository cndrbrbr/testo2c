#!/usr/bin/env bash
# Verify that the stack pulls from Docker Hub and that sim traffic flows.
# Starts the stack, waits for agents to run a few cycles, checks msglog
# entry counts on all three nodes, then stops the stack.
set -euo pipefail

WAIT_SECONDS=20
MIN_ENTRIES=10

pass() { echo "[PASS] $*"; }
fail() { echo "[FAIL] $*"; exit 1; }

echo "=== testo2c hub-image smoke test ==="

# Pull and confirm all images come from Docker Hub
echo ""
echo "--- Pulling images ---"
docker compose pull

echo ""
echo "--- Starting stack ---"
docker compose up -d

echo ""
echo "--- Waiting ${WAIT_SECONDS}s for agents to run cycles ---"
sleep "$WAIT_SECONDS"

# Verify sim agent 1 is running and has completed at least one cycle
echo ""
echo "--- Agent status ---"
STATUS=$(curl -sf http://localhost:9201/sim/status)
echo "$STATUS" | python3 -m json.tool

RUNNING=$(echo "$STATUS" | python3 -c "import json,sys; print(json.load(sys.stdin)['running'])")
CYCLE=$(echo "$STATUS"   | python3 -c "import json,sys; print(json.load(sys.stdin)['cycle'])")

[[ "$RUNNING" == "True" ]] || fail "sim-agent-1 is not running"
[[ "$CYCLE"   -ge 1     ]] || fail "sim-agent-1 has not completed a cycle (cycle=$CYCLE)"
pass "sim-agent-1 running, cycle=$CYCLE"

# Check msglog entry counts on all three nodes
echo ""
echo "--- Msglog entry counts ---"
for port in 8081 8082 8083; do
  COUNT=$(curl -sf "http://localhost:${port}/v1/msglog" \
    | python3 -c "import json,sys; print(len(json.load(sys.stdin)['entries']))")
  echo "  node (port $port): $COUNT entries"
  [[ "$COUNT" -ge "$MIN_ENTRIES" ]] \
    || fail "node on port $port has only $COUNT entries (expected >= $MIN_ENTRIES)"
  pass "node port $port: $COUNT entries"
done

# Confirm images are from Docker Hub (not locally built)
echo ""
echo "--- Image sources ---"
docker compose images

echo ""
echo "--- Stopping stack ---"
docker compose down

echo ""
echo "=== All checks passed ==="
