#!/usr/bin/env bash
set -euo pipefail
IMAGE="$1"                  # e.g. ghcr.io/yourorg/txparser:<sha>
PORT_OUT="${2:-8080}"

echo "==> Pull image"
docker pull "$IMAGE"

echo "==> Stop old"
docker rm -f txparser >/dev/null 2>&1 || true

echo "==> Run new"
docker run -d --name txparser \
  -p 0.0.0.0:${PORT_OUT}:8080 \
  --restart unless-stopped \
  "$IMAGE"

echo "==> Wait for health"
for i in {1..40}; do
  if curl -sf "http://127.0.0.1:${PORT_OUT}/healthz" >/dev/null; then break; fi
  sleep 0.25
done

echo "OK"
