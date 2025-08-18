#!/usr/bin/env bash
set -euo pipefail

echo "==> Go unit tests"
go test ./...

echo "==> Build test image"
docker build -t txparser:test-local .

echo "==> Run container on 127.0.0.1:8080"
CID=$(docker run -d --rm -p 127.0.0.1:8080:8080 txparser:test-local)
trap 'docker stop "$CID" >/dev/null 2>&1 || true' EXIT

echo "==> Wait for health"
for i in {1..40}; do
  if curl -sf http://127.0.0.1:8080/healthz >/dev/null; then break; fi
  sleep 0.25
done

echo "==> Check signatures; require non-empty swapInfo with non-zero amounts"
fail=0
while IFS= read -r row; do
  sig=$(echo "$row" | jq -r '.signature')
  label=$(echo "$row" | jq -r '.label')
  printf "  - %s ... " "$label"

  # 15s per request (your service also times out at 10s internally)
  resp=$(curl -sS --max-time 15 -X POST http://127.0.0.1:8080/parse \
    -H 'Content-Type: application/json' \
    -d "{\"signature\":\"$sig\"}")

  # Basic structure must exist
  echo "$resp" | jq -e 'has("transaction") and has("swapInfo")' >/dev/null || { echo "FAIL (bad shape)"; fail=1; continue; }

  # swapInfo must be non-null and “non-zero”: at least one of these > 0 or AMMs non-empty
  if echo "$resp" | jq -e '
      .swapInfo != null and (
        (.swapInfo.TokenInAmount // 0) > 0 or
        (.swapInfo.TokenOutAmount // 0) > 0 or
        ((.swapInfo.AMMs | length) // 0) > 0
      )
    ' >/dev/null; then
    echo "OK"
  else
    echo "FAIL (zero/empty swapInfo)"
    fail=1
  fi
done < <(jq -c '.[]' tests/cases.json)

if [ "$fail" -ne 0 ]; then
  echo "==> Some integration checks failed."
  exit 1
fi

echo "==> All integration checks passed."
