#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

SUBSCRIPTION_ID=${SUBSCRIPTION_ID:?Set the approved subscription GUID}
RUN_ID=${RUN_ID:?Set a unique run ID}
DEPLOYMENT_ID=${DEPLOYMENT_ID:?Set a new opaque UUID}
KUBECONFIG_PATH=${KUBECONFIG_PATH:?Set a new explicit kubeconfig path}
CREATE_AZURE_RESOURCES=${CREATE_AZURE_RESOURCES:-}
LOCATION=${LOCATION:-westus2}
KUBERNETES_VERSION=${KUBERNETES_VERSION:-1.36}
SYSTEM_VM_SIZE=${SYSTEM_VM_SIZE:-Standard_D4s_v5}
KATA_VM_SIZE=${KATA_VM_SIZE:-Standard_L8s_v3}
derive_names
# Keep both deployment-derived collision domains explicit at their destructive boundaries.
RESOURCE_GROUP="rg-kdva-${RUN_ID}-${DEPLOYMENT_SUFFIX}"
NODE_RESOURCE_GROUP="rg-kdva-nodes-${RUN_ID}-${DEPLOYMENT_SUFFIX}"
RESULT_ROOT=${RESULT_ROOT:-$EXPERIMENT_DIR/results}
UTC=$(date -u +%Y%m%dT%H%M%SZ)
RESULT_DIR="$RESULT_ROOT/${UTC}-provision-${DEPLOYMENT_SUFFIX}"
GROUP_CREATED=false
AKS_RESOURCE_ID="/subscriptions/$SUBSCRIPTION_ID/resourceGroups/$RESOURCE_GROUP/providers/Microsoft.ContainerService/managedClusters/$CLUSTER_NAME"
NODE_RESOURCE_GROUP_ID="/subscriptions/$SUBSCRIPTION_ID/resourceGroups/$NODE_RESOURCE_GROUP"
NODE_RESOURCE_GROUP_MANAGED_BY="$AKS_RESOURCE_ID"

save_state() {
  printf 'RUN_ID=%q\nDEPLOYMENT_ID=%q\nDEPLOYMENT_SUFFIX=%q\nRESOURCE_GROUP=%q\nCLUSTER_NAME=%q\nAKS_RESOURCE_ID=%q\nNODE_RESOURCE_GROUP=%q\nNODE_RESOURCE_GROUP_ID=%q\nNODE_RESOURCE_GROUP_MANAGED_BY=%q\nACR_NAME=%q\nNAMESPACE=%q\nKUBECONFIG_PATH=%q\n' \
    "$RUN_ID" "$DEPLOYMENT_ID" "$DEPLOYMENT_SUFFIX" "$RESOURCE_GROUP" "$CLUSTER_NAME" "$AKS_RESOURCE_ID" \
    "$NODE_RESOURCE_GROUP" "$NODE_RESOURCE_GROUP_ID" "$NODE_RESOURCE_GROUP_MANAGED_BY" "$ACR_NAME" "$NAMESPACE" "$KUBECONFIG_PATH" >"$RESULT_DIR/provision-state.env"
}
on_exit() {
  local rc=$?; trap - EXIT INT TERM
  if [[ $rc -ne 0 ]]; then
    rm -f -- "$KUBECONFIG_PATH"
    [[ $GROUP_CREATED == false ]] || printf 'Provisioning failed; preserving exact resource group %s for inspected destruction.\n' "$RESOURCE_GROUP" >&2
  fi
  exit "$rc"
}

[[ $SUBSCRIPTION_ID =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$ ]] || die 'SUBSCRIPTION_ID must be a GUID'
[[ $CREATE_AZURE_RESOURCES == yes ]] || die 'Set CREATE_AZURE_RESOURCES=yes after review'
require_tools az kubectl sed sort
az aks create --help | grep -F -- '--node-resource-group' >/dev/null || die 'installed Azure CLI does not support az aks create --node-resource-group'
[[ ! -e $KUBECONFIG_PATH ]] || die "KUBECONFIG_PATH exists; refusing to overwrite: $KUBECONFIG_PATH"
[[ ! -e $RESULT_DIR ]] || die "result target exists; refusing to overwrite: $RESULT_DIR"
[[ $(az account show --query id -o tsv) == "$SUBSCRIPTION_ID" ]] || die 'active subscription mismatch'
[[ $(az group exists --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP") == false ]] || die 'resource group already exists'
[[ $(az group exists --subscription "$SUBSCRIPTION_ID" --name "$NODE_RESOURCE_GROUP") == false ]] || die 'node resource group already exists'
[[ $(az acr check-name --subscription "$SUBSCRIPTION_ID" --name "$ACR_NAME" --query nameAvailable -o tsv) == true ]] ||
  die 'globally unique ACR name is unavailable'
mkdir -p "$RESULT_DIR"; chmod 0700 "$RESULT_DIR"; save_state; trap on_exit EXIT INT TERM
run_logged group-create az group create --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --location "$LOCATION" \
  --tags deployment-id="$DEPLOYMENT_ID" purpose=kata-direct-volume-ab run-id="$RUN_ID" deployment-suffix="$DEPLOYMENT_SUFFIX" --output none
GROUP_CREATED=true
run_logged acr-create az acr create --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$ACR_NAME" --sku Basic --admin-enabled false \
  --tags deployment-id="$DEPLOYMENT_ID" purpose=kata-direct-volume-ab run-id="$RUN_ID" deployment-suffix="$DEPLOYMENT_SUFFIX" --output none
run_logged aks-create az aks create --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --location "$LOCATION" \
  --node-resource-group "$NODE_RESOURCE_GROUP" \
  --kubernetes-version "$KUBERNETES_VERSION" --nodepool-name systempool --node-count 1 --node-vm-size "$SYSTEM_VM_SIZE" --node-osdisk-type Managed \
  --os-sku Ubuntu --network-plugin azure --network-plugin-mode overlay --network-dataplane cilium --ssh-access disabled \
  --tags deployment-id="$DEPLOYMENT_ID" purpose=kata-direct-volume-ab run-id="$RUN_ID" deployment-suffix="$DEPLOYMENT_SUFFIX" --yes --output none
AKS_RESOURCE_ID=$(az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --query id -o tsv)
AKS_NODE_RESOURCE_GROUP=$(az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --query nodeResourceGroup -o tsv)
[[ $AKS_NODE_RESOURCE_GROUP == "$NODE_RESOURCE_GROUP" ]] || safety_die 'AKS returned an unexpected node resource group'
NODE_RESOURCE_GROUP_ID=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$NODE_RESOURCE_GROUP" --query id -o tsv)
NODE_RESOURCE_GROUP_MANAGED_BY=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$NODE_RESOURCE_GROUP" --query managedBy -o tsv)
[[ ${AKS_RESOURCE_ID,,} == "/subscriptions/${SUBSCRIPTION_ID,,}/resourcegroups/${RESOURCE_GROUP,,}/providers/microsoft.containerservice/managedclusters/${CLUSTER_NAME,,}" ]] ||
  safety_die 'AKS resource ID does not match the exact derived cluster identity'
[[ ${NODE_RESOURCE_GROUP_ID,,} == "/subscriptions/${SUBSCRIPTION_ID,,}/resourcegroups/${NODE_RESOURCE_GROUP,,}" ]] ||
  safety_die 'node resource group ID does not match the exact derived identity'
[[ ${NODE_RESOURCE_GROUP_MANAGED_BY,,} == "${AKS_RESOURCE_ID,,}" ]] || safety_die 'node resource group managedBy does not point to the exact AKS resource'
save_state
az group show --subscription "$SUBSCRIPTION_ID" --name "$NODE_RESOURCE_GROUP" -o json | redact >"$RESULT_DIR/node-resource-group.json"
for attempt in {1..20}; do
  kata_pool_label="kata-pool-create-attempt-$attempt"
  if run_logged "$kata_pool_label" az aks nodepool add --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --cluster-name "$CLUSTER_NAME" \
    --name katapool --mode User --node-count 1 --node-vm-size "$KATA_VM_SIZE" --node-osdisk-type Managed --os-sku AzureLinux \
    --workload-runtime KataVmIsolation --node-taints dedicated=kata-direct-volume-ab:NoSchedule \
    --labels purpose=kata-direct-volume-ab deployment-suffix="$DEPLOYMENT_SUFFIX" --ssh-access disabled \
    --tags deployment-id="$DEPLOYMENT_ID" purpose=kata-direct-volume-ab run-id="$RUN_ID" deployment-suffix="$DEPLOYMENT_SUFFIX" --output none; then
    break
  else
    kata_pool_rc=$?
  fi
  kata_pool_log="$RESULT_DIR/$kata_pool_label.log"
  if ! grep -Fq 'Code: OperationNotAllowed' "$kata_pool_log" ||
     ! grep -Fq 'in-progress PutExtensionAddonHandler.PUT operation' "$kata_pool_log" ||
     (( attempt == 20 )); then
    exit "$kata_pool_rc"
  fi
  sleep 30
done
for attempt in {1..20}; do
  reconcile_label="aks-reconcile-attempt-$attempt"
  if run_logged "$reconcile_label" az aks update --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --attach-acr "$ACR_NAME" --output none; then
    break
  else
    reconcile_rc=$?
  fi
  reconcile_log="$RESULT_DIR/$reconcile_label.log"
  if ! grep -Fq 'Code: OperationNotAllowed' "$reconcile_log" ||
     ! grep -Fq 'in-progress PutExtensionAddonHandler.PUT operation' "$reconcile_log" ||
     (( attempt == 20 )); then
    exit "$reconcile_rc"
  fi
  sleep 30
done
run_logged get-credentials az aks get-credentials --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --file "$KUBECONFIG_PATH" --output none
assert_kubeconfig_targets_cluster
run_logged nodes kubectl --kubeconfig "$KUBECONFIG_PATH" get nodes -o wide
run_logged runtime-class kubectl --kubeconfig "$KUBECONFIG_PATH" get runtimeclass kata-vm-isolation -o yaml
az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" -o json | redact >"$RESULT_DIR/cluster.json"
trap - EXIT INT TERM
printf 'RESOURCE_GROUP=%s\nNODE_RESOURCE_GROUP=%s\nNODE_RESOURCE_GROUP_ID=%s\nCLUSTER_NAME=%s\nACR_NAME=%s\nNAMESPACE=%s\nKUBECONFIG_PATH=%s\nRESULT_DIR=%s\n' \
  "$RESOURCE_GROUP" "$NODE_RESOURCE_GROUP" "$NODE_RESOURCE_GROUP_ID" "$CLUSTER_NAME" "$ACR_NAME" "$NAMESPACE" "$KUBECONFIG_PATH" "$RESULT_DIR"
