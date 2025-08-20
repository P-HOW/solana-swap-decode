#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${ROOT_DIR}/.env" 2>/dev/null || true

: "${LOCAL_TEST_PORT:=8080}"
: "${REQUEST_TIMEOUT_SECONDS:=15}"
: "${CASES_FILE:=tests/cases.json}"

echo "==> Go unit tests"
go test ./...

echo "==> Build test image"
docker build -t txparser:test-local "${ROOT_DIR}"

echo "==> Run container on 127.0.0.1:${LOCAL_TEST_PORT}"
CID=$(docker run -d --rm -p 127.0.0.1:${LOCAL_TEST_PORT}:8080 txparser:test-local)
trap 'docker stop "$CID" >/dev/null 2>&1 || true' EXIT

echo "==> Wait for health"
for i in {1..40}; do
  curl -sf "http://127.0.0.1:${LOCAL_TEST_PORT}/healthz" >/dev/null && break
  sleep 0.25
done

echo "==> Validate swapInfo non-zero for test cases"
fail=0
while IFS= read -r row; do
  sig=$(echo "$row" | jq -r '.signature')
  label=$(echo "$row" | jq -r '.label')
  printf "  - %s ... " "$label"

  resp=$(curl -sS --max-time "${REQUEST_TIMEOUT_SECONDS}" -X POST "http://127.0.0.1:${LOCAL_TEST_PORT}/parse" \
    -H 'Content-Type: application/json' -d "{\"signature\":\"$sig\"}")

  echo "$resp" | jq -e 'has("transaction") and has("swapInfo")' >/dev/null || { echo "FAIL (shape)"; fail=1; continue; }
  if echo "$resp" | jq -e '
    .swapInfo != null and (
      (.swapInfo.TokenInAmount // 0) > 0 or
      (.swapInfo.TokenOutAmount // 0) > 0 or
      ((.swapInfo.AMMs | length) // 0) > 0
    )' >/dev/null; then
    echo "OK"
  else
    echo "FAIL (empty swapInfo)"
    fail=1
  fi
done < <(jq -c '.[]' "${ROOT_DIR}/${CASES_FILE}")

[ "$fail" -eq 0 ] && echo "==> All integration checks passed." || { echo "==> Some checks failed."; exit 1; }
