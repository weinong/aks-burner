#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/../../../.." && pwd -P)
SUBSCRIPTION_ID="${SUBSCRIPTION_ID:?Set SUBSCRIPTION_ID to the approved Azure subscription GUID}"
RUN_ID="${RUN_ID:?Set RUN_ID to the exact provisioned run ID}"
DEPLOYMENT_ID="${DEPLOYMENT_ID:?Set DEPLOYMENT_ID to the exact opaque deployment UUID}"
DELETE_AZURE_RESOURCES="${DELETE_AZURE_RESOURCES:-}"
KUBECONFIG_PATH="${KUBECONFIG_PATH:?Set KUBECONFIG_PATH to the isolated kubeconfig created by provision.sh}"
RESOURCE_GROUP="rg-aks-burner-kata-vbw-${RUN_ID}"
RESULT_ROOT="${RESULT_ROOT:-$REPO_ROOT/results/virtio-blk-write-trace/destroy}"
RESULT_DIR="$RESULT_ROOT/$RUN_ID"

[[ $SUBSCRIPTION_ID =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$ ]] || { printf 'SUBSCRIPTION_ID must be a GUID.\n' >&2; exit 1; }
[[ $DEPLOYMENT_ID =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[1-5][[:xdigit:]]{3}-[89abAB][[:xdigit:]]{3}-[[:xdigit:]]{12}$ ]] || { printf 'DEPLOYMENT_ID must be an opaque UUID.\n' >&2; exit 1; }
[[ $RUN_ID =~ ^[a-z0-9]{4,12}$ ]] || { printf 'RUN_ID must contain 4-12 lowercase letters or digits.\n' >&2; exit 1; }
[[ $DELETE_AZURE_RESOURCES == yes ]] || { printf 'Set DELETE_AZURE_RESOURCES=yes to delete %s.\n' "$RESOURCE_GROUP" >&2; exit 1; }
command -v az >/dev/null || { printf 'az is required.\n' >&2; exit 1; }
command -v kubectl >/dev/null || { printf 'kubectl is required.\n' >&2; exit 1; }
[[ -f $KUBECONFIG_PATH && ! -L $KUBECONFIG_PATH ]] || { printf 'KUBECONFIG_PATH must be an existing regular file.\n' >&2; exit 1; }
[[ $(az account show --query id -o tsv) == "$SUBSCRIPTION_ID" ]] || { printf 'Active subscription does not match SUBSCRIPTION_ID.\n' >&2; exit 1; }
if [[ $(az group exists --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP") != true ]]; then
  printf 'Resource group already absent: %s\n' "$RESOURCE_GROUP"
  exit 0
fi
GROUP_DEPLOYMENT_ID=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query 'tags."deployment-id"' -o tsv)
GROUP_PURPOSE=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query 'tags.purpose' -o tsv)
GROUP_RUN_ID=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query 'tags."run-id"' -o tsv)
[[ $GROUP_DEPLOYMENT_ID == "$DEPLOYMENT_ID" && $GROUP_PURPOSE == kata-virtio-blk-write-trace && $GROUP_RUN_ID == "$RUN_ID" ]] || {
  printf 'Deployment identity tags do not match; refusing deletion.\n' >&2; exit 1;
}
CLUSTER_FQDN=$(az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "aksvbw${RUN_ID}" --query fqdn -o tsv)
KUBECONFIG_SERVER=$(kubectl --kubeconfig "$KUBECONFIG_PATH" config view --minify -o jsonpath='{.clusters[0].cluster.server}')
KUBECONFIG_HOST=${KUBECONFIG_SERVER#https://}
KUBECONFIG_HOST=${KUBECONFIG_HOST%%:*}
[[ -n $CLUSTER_FQDN && $KUBECONFIG_HOST == "$CLUSTER_FQDN" ]] || { printf 'Kubeconfig does not target the run cluster; refusing deletion.\n' >&2; exit 1; }
[[ ! -e $RESULT_DIR ]] || { printf 'Result directory exists: %s\n' "$RESULT_DIR" >&2; exit 1; }
mkdir -p "$RESULT_DIR"; chmod 0700 "$RESULT_DIR"
{
  printf 'COMMAND_START time=%s command=az-group-delete subscription=[REDACTED] resource-group=%q deployment-id=%q\n' "$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)" "$RESOURCE_GROUP" "$DEPLOYMENT_ID"
  if az group delete --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --yes --no-wait && \
    az group wait --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --deleted --interval 15 --timeout 1800; then rc=0; else rc=$?; fi
  printf 'COMMAND_END time=%s status=%d\n' "$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)" "$rc"
} >"$RESULT_DIR/delete.log" 2>&1
if [[ $rc -eq 0 ]]; then rm -f -- "$KUBECONFIG_PATH"; fi
exit "$rc"
