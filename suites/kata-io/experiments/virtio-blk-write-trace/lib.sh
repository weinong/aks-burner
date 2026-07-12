#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)

canonical_host_action() {
  local action=$1 case_name=${2:-}
  exec "$SCRIPT_DIR/register-direct-volume.sh" "$action" \
    "${RUN_ID:?RUN_ID is required}" \
    "${EXPECTED_NVME_DEVICE:?EXPECTED_NVME_DEVICE is required}" \
    "${FORMAT_NVME:-no}" "$case_name"
}
