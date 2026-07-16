#!/usr/bin/env bash
set -euo pipefail

SUBSCRIPTION="${SUBSCRIPTION:-}"
LOCATION="${LOCATION:-southcentralus}"
RESOURCE_GROUP="${RESOURCE_GROUP:-rg-cifs-unmount-repro}"
CLUSTER_NAME="${CLUSTER_NAME:-aks-cifs-unmount-repro}"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-1.36}"
SYSTEM_VM_SIZE="${SYSTEM_VM_SIZE:-Standard_D4s_v5}"
WORKLOAD_VM_SIZE="${WORKLOAD_VM_SIZE:-Standard_D8s_v5}"
KUBE_CONTEXT="${KUBE_CONTEXT:-${CLUSTER_NAME}}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

command -v az >/dev/null 2>&1 || { printf 'error: az is required\n' >&2; exit 1; }
command -v kubectl >/dev/null 2>&1 || { printf 'error: kubectl is required\n' >&2; exit 1; }
nodepool_help="$(az aks nodepool add --help 2>/dev/null)"
if [[ "$nodepool_help" != *KataMshvVmIsolation* ]]; then
  printf '%s\n' \
    'error: this Azure CLI does not support --workload-runtime KataMshvVmIsolation' \
    'Update Azure CLI and install or update the aks-preview extension.' >&2
  exit 1
fi

if [[ -n "$SUBSCRIPTION" ]]; then
  az account set --subscription "$SUBSCRIPTION"
fi

subscription_id="$(az account show --query id -o tsv)"
printf 'Subscription:   %s\n' "$subscription_id"
printf 'Resource group: %s\n' "$RESOURCE_GROUP"
printf 'Cluster:        %s\n' "$CLUSTER_NAME"
printf 'Location:       %s\n' "$LOCATION"

if [[ "$(az group exists --name "$RESOURCE_GROUP" --output tsv)" == "true" ]]; then
  printf 'error: resource group %s already exists; choose a new group or delete it with destroy.sh\n' \
    "$RESOURCE_GROUP" >&2
  exit 1
fi

az group create \
  --name "$RESOURCE_GROUP" \
  --location "$LOCATION" \
  --output none

az aks create \
  --resource-group "$RESOURCE_GROUP" \
  --name "$CLUSTER_NAME" \
  --location "$LOCATION" \
  --kubernetes-version "$KUBERNETES_VERSION" \
  --nodepool-name systempool \
  --node-count 1 \
  --node-vm-size "$SYSTEM_VM_SIZE" \
  --node-osdisk-type Managed \
  --os-sku Ubuntu \
  --vm-set-type VirtualMachineScaleSets \
  --network-plugin azure \
  --network-plugin-mode overlay \
  --network-dataplane cilium \
  --network-policy cilium \
  --load-balancer-sku standard \
  --outbound-type loadBalancer \
  --enable-managed-identity \
  --node-os-upgrade-channel None \
  --generate-ssh-keys \
  --tier free \
  --yes \
  --output none

az aks update \
  --resource-group "$RESOURCE_GROUP" \
  --name "$CLUSTER_NAME" \
  --enable-file-driver \
  --output none

az aks nodepool add \
  --resource-group "$RESOURCE_GROUP" \
  --cluster-name "$CLUSTER_NAME" \
  --name repropool \
  --mode User \
  --node-count 1 \
  --node-vm-size "$WORKLOAD_VM_SIZE" \
  --node-osdisk-type Managed \
  --node-osdisk-size 256 \
  --max-pods 250 \
  --os-type Linux \
  --os-sku AzureLinux \
  --workload-runtime KataMshvVmIsolation \
  --labels repro.azure.com/target=cifs-unmount \
  --output none

az aks get-credentials \
  --resource-group "$RESOURCE_GROUP" \
  --name "$CLUSTER_NAME" \
  --context "$KUBE_CONTEXT" \
  --overwrite-existing \
  --output none

kubectl --context "$KUBE_CONTEXT" wait \
  --for=condition=Ready node \
  --selector='repro.azure.com/target=cifs-unmount' \
  --timeout=15m

printf '\nCluster is ready. Run:\n'
printf 'KUBE_CONTEXT=%q %q\n' "$KUBE_CONTEXT" "$SCRIPT_DIR/run.sh"
