#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
SUBSCRIPTION_ID="${SUBSCRIPTION_ID:?Set SUBSCRIPTION_ID to the approved Azure subscription GUID}"
RUN_ID="${RUN_ID:?Set RUN_ID to the provisioned run ID}"
ACR_NAME="${ACR_NAME:-aksvbw${RUN_ID}acr}"
IMAGE_REPOSITORY=kata-virtio-blk-write-trace/probe
IMAGE_TAG="${IMAGE_TAG:-$RUN_ID}"

[[ $RUN_ID =~ ^[a-z0-9]{4,12}$ ]] || { printf 'invalid RUN_ID\n' >&2; exit 1; }
[[ $(az account show --query id -o tsv) == "$SUBSCRIPTION_ID" ]] || { printf 'active subscription mismatch\n' >&2; exit 1; }

az acr show --subscription "$SUBSCRIPTION_ID" --name "$ACR_NAME" --query name -o tsv >/dev/null
az acr build --subscription "$SUBSCRIPTION_ID" --registry "$ACR_NAME" \
  --image "$IMAGE_REPOSITORY:$IMAGE_TAG" \
  --file "$SCRIPT_DIR/Dockerfile.probe" "$SCRIPT_DIR"

LOGIN_SERVER=$(az acr show --subscription "$SUBSCRIPTION_ID" --name "$ACR_NAME" --query loginServer -o tsv)
DIGEST=$(az acr repository show --name "$ACR_NAME" --image "$IMAGE_REPOSITORY:$IMAGE_TAG" --query digest -o tsv)
[[ $DIGEST =~ ^sha256:[0-9a-f]{64}$ ]] || { printf 'failed to resolve pushed image digest\n' >&2; exit 1; }
printf 'PROBE_IMAGE=%q\nHOST_IMAGE=%q\n' "$LOGIN_SERVER/$IMAGE_REPOSITORY@$DIGEST" "$LOGIN_SERVER/$IMAGE_REPOSITORY@$DIGEST"
