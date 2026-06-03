#!/usr/bin/env bash
# Smoke test: pull fresh images, start the 3-node cluster, wait for agents
# to run cycles, then verify health, sim traffic, layers, and xcommand.
# Tears the stack down on exit (pass or fail).
set -euo pipefail

MIN_CYCLES=3
MIN_ENTRIES=10
MIN_LAYERS=3
CYCLE_TIMEOUT=120  # seconds to wait for agents to reach MIN_CYCLES

PASS=0; FAIL=0
pass() { echo "[PASS] $*"; (( PASS++ )) || true; }
fail() { echo "[FAIL] $*"; (( FAIL++ )) || true; }

cleanup() {
  echo ""
  echo "--- Tearing down stack ---"
  docker compose down -v --timeout 10 2>/dev/null || true
}
trap cleanup EXIT

echo "=== testo2c smoke test ==="
echo ""

# ── Pull ──────────────────────────────────────────────────────────────────────
echo "--- Pulling images ---"
docker compose pull
echo ""

# ── Start ─────────────────────────────────────────────────────────────────────
echo "--- Starting stack ---"
docker compose up -d
echo ""

# ── Wait for agents ───────────────────────────────────────────────────────────
echo "--- Waiting for agents to reach ${MIN_CYCLES} cycles (timeout ${CYCLE_TIMEOUT}s) ---"
deadline=$(( $(date +%s) + CYCLE_TIMEOUT ))
while true; do
  cycle=$(curl -sf http://localhost:9201/sim/status 2>/dev/null \
    | python3 -c "import json,sys; print(json.load(sys.stdin).get('cycle',0))" 2>/dev/null || echo 0)
  [[ "$cycle" -ge "$MIN_CYCLES" ]] && break
  [[ $(date +%s) -lt $deadline ]] || { fail "timed out waiting for agent-1 to reach cycle $MIN_CYCLES"; break; }
  sleep 2
done
echo ""

# ── Node health ───────────────────────────────────────────────────────────────
echo "--- Node health ---"
for port in 8081 8082 8083; do
  STATUS=$(curl -sf "http://localhost:${port}/healthz" \
    | python3 -c "import json,sys; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")
  if [[ "$STATUS" == "ok" ]]; then
    pass "node:$port healthy"
  else
    fail "node:$port health check failed (status='$STATUS')"
  fi
done
echo ""

# ── Sim agent status ──────────────────────────────────────────────────────────
echo "--- Sim agent status ---"
for port in 9201 9202 9203; do
  STATUS=$(curl -sf "http://localhost:${port}/sim/status" 2>/dev/null || echo "{}")
  RUNNING=$(echo "$STATUS" | python3 -c "import json,sys; print(json.load(sys.stdin).get('running',False))")
  CYCLE=$(echo "$STATUS"   | python3 -c "import json,sys; print(json.load(sys.stdin).get('cycle',0))")
  LASTERR=$(echo "$STATUS" | python3 -c "import json,sys; print(json.load(sys.stdin).get('lastErr',''))")

  if [[ "$RUNNING" == "True" ]]; then
    pass "agent:$port running (cycle=$CYCLE)"
  else
    fail "agent:$port not running"
  fi

  if [[ "$CYCLE" -ge "$MIN_CYCLES" ]]; then
    pass "agent:$port cycle=$CYCLE >= $MIN_CYCLES"
  else
    fail "agent:$port cycle=$CYCLE < $MIN_CYCLES"
  fi

  if [[ -z "$LASTERR" ]]; then
    pass "agent:$port no errors"
  else
    # Transient timeouts are retried — warn but don't fail
    echo "[WARN] agent:$port lastErr: ${LASTERR:0:80}"
  fi
done
echo ""

# ── Msglog entry counts ───────────────────────────────────────────────────────
echo "--- Msglog entry counts ---"
for port in 8081 8082 8083; do
  COUNT=$(curl -sf "http://localhost:${port}/v1/msglog" \
    | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('entries',[])))" 2>/dev/null || echo 0)
  if [[ "$COUNT" -ge "$MIN_ENTRIES" ]]; then
    pass "node:$port msglog $COUNT entries"
  else
    fail "node:$port msglog $COUNT entries (expected >= $MIN_ENTRIES)"
  fi
done
echo ""

# ── Feature layers ────────────────────────────────────────────────────────────
echo "--- Feature layers ---"
for port in 8081 8082 8083; do
  COUNT=$(curl -sf "http://localhost:${port}/v1/layers" \
    | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('layers',[])))" 2>/dev/null || echo 0)
  if [[ "$COUNT" -ge "$MIN_LAYERS" ]]; then
    pass "node:$port $COUNT layers"
  else
    fail "node:$port $COUNT layers (expected >= $MIN_LAYERS)"
  fi
done
echo ""

# ── xcommand endpoint ─────────────────────────────────────────────────────────
echo "--- xcommand endpoint ---"
for port in 8081 8082 8083; do
  CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:${port}/v1/commands")
  if [[ "$CODE" == "200" ]]; then
    pass "node:$port GET /v1/commands → HTTP 200"
  else
    fail "node:$port GET /v1/commands → HTTP $CODE"
  fi
done
echo ""

# ── Image sources ─────────────────────────────────────────────────────────────
echo "--- Image sources ---"
docker compose images
echo ""

# ── Summary ───────────────────────────────────────────────────────────────────
echo "=== Results: $PASS passed, $FAIL failed ==="
[[ "$FAIL" -eq 0 ]]
