#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${ROOT_DIR}/.env" 2>/dev/null || true

: "${VM_HOST:?VM_HOST missing}"
: "${VM_USER:?VM_USER missing}"
: "${IMAGE_NAME:=txparser-local}"
: "${REMOTE_ARCHIVE:=txparser-amd64.tar}"

TAG=$(date +%Y%m%d-%H%M%S)

echo "==> Build linux/amd64 image"
docker buildx create --use >/dev/null 2>&1 || true
docker buildx build --platform linux/amd64 -t ${IMAGE_NAME}:${TAG} --load "${ROOT_DIR}"

echo "==> Save image to tar"
docker save ${IMAGE_NAME}:${TAG} -o "${ROOT_DIR}/${REMOTE_ARCHIVE}"

echo "==> Copy tar to VM"
scp "${ROOT_DIR}/${REMOTE_ARCHIVE}" "${VM_USER}@${VM_HOST}:/home/${VM_USER}/${REMOTE_ARCHIVE}"

# print tag for next step
echo "${TAG}"
