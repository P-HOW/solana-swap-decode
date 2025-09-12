#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
set -a; [ -f "${ROOT_DIR}/.env" ] && . "${ROOT_DIR}/.env"; set +a

TARGET="${1:-${DEPLOY_TARGET:-local}}"
case "$TARGET" in
  local)
    echo "==> Local mode"
    "${ROOT_DIR}/scripts/run-tests.sh"
    "${ROOT_DIR}/scripts/up-local.sh"
    echo "==> Local smoke"
    "${ROOT_DIR}/scripts/smoke-remote.sh" "127.0.0.1" "${LOCAL_TEST_PORT:-8080}" "${ROOT_DIR}/tests/cases.json" "${REQUEST_TIMEOUT_SECONDS:-15}"
    ;;
  vm)
    echo "==> VM mode"
    "${ROOT_DIR}/scripts/local-cicd.sh"
    ;;
  *)
    echo "Unknown target: $TARGET (use: local | vm)" >&2
    exit 1
    ;;
esac
