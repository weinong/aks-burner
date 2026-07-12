#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"
exec "$EXPERIMENT_DIR/device-manager.sh" cleanup "${RUN_ID:?RUN_ID is required}" \
  "${DEVICE_ID:?DEVICE_ID is required}" "${EXPECTED_NVME_DEVICE:?EXPECTED_NVME_DEVICE is required}" "${FORMAT_NVME:-no}"
