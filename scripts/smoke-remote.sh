#!/usr/bin/env bash
set -euo pipefail
HOST="${1:?host}"
PORT="${2:-8080}"
CASES_FILE="${3:-tests/cases.json}"
TIMEOUT="${4:-15}"

echo "==> Remote health"
curl -sf "http://${HOST}:${PORT}/healthz" >/dev/null

echo "==> Remote cases"
fail=0
while IFS= read -r row; do
  sig=$(echo "$row" | jq -r '.signature')
  label=$(echo "$row" | jq -r '.label')
  printf "  - %s ... " "$label"
  resp=$(curl -sS --max-time "${TIMEOUT}" -X POST "http://${HOST}:${PORT}/parse" \
    -H 'Content-Type: application/json' -d "{\"signature\":\"$sig\"}")
  if echo "$resp" | jq -e 'has("transaction") and .swapInfo != null' >/dev/null; then
    echo "OK"
  else
    echo "FAIL"
    fail=1
  fi
done < <(jq -c '.[]' "${CASES_FILE}")

[ "$fail" -eq 0 ] && echo "smoke OK" || { echo "smoke FAILED"; exit 1; }
