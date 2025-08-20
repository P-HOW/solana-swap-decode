#!/usr/bin/env bash
set -euo pipefail

ARCHIVE="$1"          # /home/azureuser/txparser-amd64.tar
LIVE_PORT="${2:-8080}"

echo "==> docker load"
docker load -i "$ARCHIVE" >/dev/null

# get image name:tag from archive
REF=$(docker image ls --format '{{.Repository}}:{{.Tag}}' | grep txparser-local | head -n1)
echo "Using image: $REF"

echo "==> Stop & remove old"
docker rm -f txparser >/dev/null 2>&1 || true

echo "==> Run new"
docker run -d --name txparser \
  -p 0.0.0.0:${LIVE_PORT}:8080 \
  --restart unless-stopped \
  "$REF"

echo "==> Health"
for i in {1..40}; do
  curl -sf "http://127.0.0.1:${LIVE_PORT}/healthz" >/dev/null && break
  sleep 0.25
done
echo "OK"
