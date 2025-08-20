#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${ROOT_DIR}/.env" 2>/dev/null || true

HOST="${1:?host}"
PORT="${2:-8080}"
CASES_FILE="${3:-tests/cases.json}"
TIMEOUT="${4:-15}"
: "${PER_TEST_DELAY_SECONDS:=1}"

echo "==> Remote health"
curl -sf "http://${HOST}:${PORT}/healthz" >/dev/null

echo "==> Remote cases (pacing ${PER_TEST_DELAY_SECONDS}s)"
fail=0
while IFS= read -r row; do
  sig=$(echo "$row" | jq -r '.signature')
  label=$(echo "$row" | jq -r '.label')
  printf "  - %s ... " "$label"

  sleep "${PER_TEST_DELAY_SECONDS}"

  tries=0; max_tries=3
  while : ; do
    tries=$((tries+1))
    http=$(curl -sS -o /tmp/remote_resp.json -w "%{http_code}" --max-time "${TIMEOUT}" \
      -X POST "http://${HOST}:${PORT}/parse" \
      -H 'Content-Type: application/json' \
      -d "{\"signature\":\"$sig\"}") || http=000

    if [[ "$http" =~ ^(429|5..)$ ]] && [[ $tries -lt $max_tries ]]; then
      sleep "${PER_TEST_DELAY_SECONDS}"
      continue
    fi
    break
  done

  resp="$(cat /tmp/remote_resp.json 2>/dev/null || echo '{}')"
  if echo "$resp" | jq -e 'has("transaction") and .swapInfo != null' >/dev/null; then
    echo "OK"
  else
    echo "FAIL"
    fail=1
  fi
done < <(jq -c '.[]' "${CASES_FILE}")

[ "$fail" -eq 0 ] && echo "smoke OK" || { echo "smoke FAILED"; exit 1; }
