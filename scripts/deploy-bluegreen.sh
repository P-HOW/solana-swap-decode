#!/usr/bin/env bash
set -euo pipefail

ARCHIVE="$1"           # /home/azureuser/txparser-amd64.tar
LIVE_PORT="${2:-8080}"
STAGE_PORT="${3:-8081}"

echo "==> docker load"
docker load -i "$ARCHIVE" >/dev/null
REF=$(docker image ls --format '{{.Repository}}:{{.Tag}}' | grep txparser-local | head -n1)
echo "Using image: $REF"

NEW=txparser_new
OLD=txparser

echo "==> Start new on ${STAGE_PORT}"
docker rm -f "${NEW}" >/dev/null 2>&1 || true
docker run -d --name "${NEW}" \
  -p 0.0.0.0:${STAGE_PORT}:8080 \
  --restart unless-stopped \
  "$REF"

echo "==> Health (staging)"
for i in {1..40}; do
  curl -sf "http://127.0.0.1:${STAGE_PORT}/healthz" >/dev/null && break
  sleep 0.25
done

echo "==> Smoke (staging)"
cat > /tmp/cases.json <<'EOS'
REPLACED_BY_LOCAL
EOS
# If you already placed cases.json on VM under ~/txparser/tests/cases.json, use that instead:
CASES=~/txparser/tests/cases.json
if [ ! -f "$CASES" ]; then
  echo "cases.json not found at $CASES"; exit 1
fi

fail=0
while IFS= read -r row; do
  sig=$(echo "$row" | jq -r '.signature')
  resp=$(curl -sS --max-time 15 -X POST "http://127.0.0.1:${STAGE_PORT}/parse" \
    -H 'Content-Type: application/json' -d "{\"signature\":\"$sig\"}")
  echo "$resp" | jq -e 'has("transaction") and .swapInfo != null' >/dev/null || fail=1
done < <(jq -c '.[]' "$CASES")

if [ "$fail" -ne 0 ]; then
  echo "Staging smoke FAILED. Rolling back."
  docker rm -f "${NEW}" || true
  exit 1
fi

echo "==> Promote: replace old"
docker rm -f "${OLD}" >/dev/null 2>&1 || true
docker rename "${NEW}" "${OLD}"

# rebind on LIVE_PORT (quick restart to move port)
docker stop "${OLD}"
docker rm "${OLD}"
docker run -d --name "${OLD}" \
  -p 0.0.0.0:${LIVE_PORT}:8080 \
  --restart unless-stopped \
  "$REF"

echo "==> Health (live)"
for i in {1..40}; do
  curl -sf "http://127.0.0.1:${LIVE_PORT}/healthz" >/dev/null && break
  sleep 0.25
done
echo "OK"
