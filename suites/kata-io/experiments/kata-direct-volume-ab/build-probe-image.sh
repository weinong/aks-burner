#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

SUBSCRIPTION_ID=${SUBSCRIPTION_ID:?}
RUN_ID=${RUN_ID:?}
DEPLOYMENT_ID=${DEPLOYMENT_ID:?}
derive_names
[[ $(az account show --query id -o tsv) == "$SUBSCRIPTION_ID" ]] || die 'active subscription mismatch'
[[ $(az acr show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$ACR_NAME" --query 'tags."deployment-id"' -o tsv) == "$DEPLOYMENT_ID" ]] ||
  safety_die 'ACR deployment identity mismatch'
REPOSITORY=kata-direct-volume-ab/probe
TAG="${RUN_ID}-${DEPLOYMENT_SUFFIX}"
az acr build --subscription "$SUBSCRIPTION_ID" --registry "$ACR_NAME" --image "$REPOSITORY:$TAG" --file "$EXPERIMENT_DIR/Dockerfile.probe" "$EXPERIMENT_DIR"
LOGIN_SERVER=$(az acr show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$ACR_NAME" --query loginServer -o tsv)
DIGEST=$(az acr repository show --name "$ACR_NAME" --image "$REPOSITORY:$TAG" --query digest -o tsv)
[[ $DIGEST =~ ^sha256:[0-9a-f]{64}$ ]] || die 'failed to resolve immutable image digest'
printf 'PROBE_IMAGE=%q\nHOST_IMAGE=%q\n' "$LOGIN_SERVER/$REPOSITORY@$DIGEST" "$LOGIN_SERVER/$REPOSITORY@$DIGEST"
