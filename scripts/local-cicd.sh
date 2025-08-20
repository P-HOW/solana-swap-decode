#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${ROOT_DIR}/.env" 2>/dev/null || true

: "${VM_HOST:?VM_HOST missing}"
: "${VM_USER:?VM_USER missing}"
: "${LIVE_PORT:=8080}"
: "${STAGE_PORT:=8081}"
: "${DEPLOY_MODE:=simple}"
: "${REMOTE_ARCHIVE:=txparser-amd64.tar}"

echo "==> 1) run local tests"
"${ROOT_DIR}/scripts/run-tests.sh"

echo "==> 2) build & ship image tar"
TAG="$("${ROOT_DIR}/scripts/build-and-ship.sh")"
echo "TAG=${TAG}"

echo "==> 3) deploy on VM (${DEPLOY_MODE})"
if [ "${DEPLOY_MODE}" = "bluegreen" ]; then
  ssh "${VM_USER}@${VM_HOST}" "bash -s" <<EOF
set -e
mkdir -p ~/txparser/tests
# copy your cases file if not present
[ -f ~/txparser/tests/cases.json ] || true
sudo bash ~/txparser/deploy-bluegreen.sh "/home/${VM_USER}/${REMOTE_ARCHIVE}" "${LIVE_PORT}" "${STAGE_PORT}" || exit 1
EOF
else
  ssh "${VM_USER}@${VM_HOST}" "bash -s" <<EOF
set -e
sudo bash ~/txparser/deploy-simple.sh "/home/${VM_USER}/${REMOTE_ARCHIVE}" "${LIVE_PORT}"
EOF
fi

echo "==> 4) remote smoke"
"${ROOT_DIR}/scripts/smoke-remote.sh" "${VM_HOST}" "${LIVE_PORT}" "${ROOT_DIR}/tests/cases.json" 15

echo "==> Done. Deployed tag ${TAG}"
