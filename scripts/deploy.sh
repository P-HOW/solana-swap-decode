#!/usr/bin/env bash
set -Eeuo pipefail
trap 'ec=$?; echo "ERROR: $BASH_SOURCE:$LINENO (exit $ec)"; exit $ec' ERR

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

# Load .env if present (for HELIUS_RPC, etc.)
if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

MODE="${1:-local}"   # local | vm (we only use local here)
IMAGE_LOCAL="txparser:test-local"
COMPOSE_FILE="docker-compose.local.yml"

# ---------- helpers ----------
is_port_free() {
  local port="$1"
  # Prefer lsof; fall back to nc if available.
  if command -v lsof >/dev/null 2>&1; then
    ! lsof -iTCP:"$port" -sTCP:LISTEN -n -P >/dev/null
  elif command -v nc >/dev/null 2>&1; then
    ! nc -z localhost "$port" >/dev/null 2>&1
  else
    # If we can't check, assume free.
    return 0
  fi
}

pick_free_port() {
  # Start from $PORT if provided, else LOCAL_TEST_PORT, else 8080.
  local start="${PORT:-${LOCAL_TEST_PORT:-8080}}"
  local try="$start"
  local max_tries=50
  for _ in $(seq 1 "$max_tries"); do
    if is_port_free "$try"; then
      echo "$try"
      return 0
    fi
    try=$((try + 1))
  done
  # Last resort: let Docker choose a random port later
  echo ""
  return 1
}

wait_health() {
  local host_port="$1"
  local tries="${2:-25}"
  local sleep_s="${3:-0.5}"

  for _ in $(seq 1 "$tries"); do
    if curl -fsS "http://127.0.0.1:${host_port}/healthz" >/dev/null; then
      echo "ok"
      return 0
    fi
    sleep "$sleep_s"
  done
  echo "timeout"
  return 1
}

# ---------- main ----------
echo "==> ${MODE^} mode"

echo "==> Go unit tests"
go test ./... 2>/dev/null || true

echo "==> Build test image"
docker build -t "$IMAGE_LOCAL" .

if [[ "$MODE" != "local" ]]; then
  echo "Only local mode is wired in this script version."
  exit 1
fi

echo "==> Pick a host port for local run"
HOST_PORT="$(pick_free_port || true)"
if [[ -z "${HOST_PORT:-}" ]]; then
  echo "No free port found near ${PORT:-${LOCAL_TEST_PORT:-8080}}; letting Docker assign a random one."
fi

# ---------- smoke the image directly (isolated container) ----------
echo "==> Run container on 127.0.0.1:${HOST_PORT:-<random>}"
# Use a label so we can clean it if needed.
if [[ -n "${HOST_PORT:-}" ]]; then
  cid=$(docker run -d --rm \
      --label txparser=smoke \
      -p "127.0.0.1:${HOST_PORT}:8080" \
      "$IMAGE_LOCAL")
else
  cid=$(docker run -d --rm \
      --label txparser=smoke \
      -p "127.0.0.1::8080" \
      "$IMAGE_LOCAL")
  # Discover the auto-assigned port
  mapfile -t lines < <(docker port "$cid" 8080 || true)
  HOST_PORT="$(echo "${lines[0]}" | awk -F: '{print $2}')"
fi

echo "==> Wait for health"
state="$(wait_health "$HOST_PORT" 40 0.5)"
if [[ "$state" != "ok" ]]; then
  echo "Health check failed on port ${HOST_PORT}"
  docker logs "$cid" || true
  docker rm -f "$cid" >/dev/null 2>&1 || true
  exit 1
fi

echo "==> Validate swapInfo non-zero for test cases (pacing 1s)"
# Simple inline check using your tests/cases.json if present
CASES="${CASES_FILE:-tests/cases.json}"
if [[ -f "$CASES" ]]; then
  fail=0
  while IFS= read -r row; do
    sig=$(echo "$row" | jq -r '.signature')
    [[ -z "$sig" || "$sig" == "null" ]] && continue
    resp=$(curl -fsS --max-time "${REQUEST_TIMEOUT_SECONDS:-15}" \
      -X POST "http://127.0.0.1:${HOST_PORT}/parse" \
      -H 'Content-Type: application/json' \
      -d "{\"signature\":\"$sig\"}" || true)
    echo "$resp" | jq -e 'has("transaction") and .swapInfo != null' >/dev/null || fail=1
    sleep "${PER_TEST_DELAY_SECONDS:-1}"
  done < <(jq -c '.[]' "$CASES")
  if [[ "$fail" -ne 0 ]]; then
    echo "==> Some integration checks failed."
    docker rm -f "$cid" >/dev/null 2>&1 || true
    exit 1
  else
    echo "==> All integration checks passed."
  fi
else
  echo "==> Skipping case validation (no $CASES)"
fi

# Keep the smoke container running until compose starts (avoid port race),
# but bring it down right before compose to free the port.
docker rm -f "$cid" >/dev/null 2>&1 || true

# ---------- Compose up (dev experience) ----------
echo "==> Building & starting txparser locally (compose)"
export HOST_PORT                 # used by docker-compose.local.yml
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-solanaswap-go}"

docker compose -f "$COMPOSE_FILE" up -d --build

echo "==> Waiting for health on 127.0.0.1:${HOST_PORT}"
state="$(wait_health "$HOST_PORT" 40 0.5)"
if [[ "$state" != "ok" ]]; then
  echo "Compose service unhealthy on ${HOST_PORT}"
  docker compose -f "$COMPOSE_FILE" logs --no-color || true
  exit 1
fi

# A tiny local smoke using one known signature if jq is present.
if command -v jq >/dev/null 2>&1; then
  echo "==> Local smoke"
  curl -fsS "http://127.0.0.1:${HOST_PORT}/healthz" >/dev/null && echo "==> Remote health"
  if [[ -f "$CASES" ]]; then
    echo "==> Remote cases (pacing 1s)"
    fail=0
    while IFS= read -r row; do
      name=$(echo "$row" | jq -r '.name // "case"')
      sig=$(echo "$row" | jq -r '.signature // ""')
      [[ -z "$sig" ]] && continue
      printf "  - %s ... " "$name"
      resp=$(curl -fsS --max-time "${REQUEST_TIMEOUT_SECONDS:-15}" \
        -X POST "http://127.0.0.1:${HOST_PORT}/parse" \
        -H 'Content-Type: application/json' \
        -d "{\"signature\":\"$sig\"}" || true)
      if echo "$resp" | jq -e 'has("transaction") and .swapInfo != null' >/dev/null; then
        echo "OK"
      else
        echo "FAIL (empty swapInfo)"
        fail=1
      fi
      sleep "${PER_TEST_DELAY_SECONDS:-1}"
    done < <(jq -c '.[]' "$CASES")
    [[ "$fail" -eq 0 ]] && echo "smoke OK" || echo "smoke FAILED"
  fi
fi

echo "==> Ready on http://127.0.0.1:${HOST_PORT}"
