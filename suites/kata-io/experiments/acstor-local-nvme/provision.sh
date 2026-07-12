#!/usr/bin/env bash
set -euo pipefail

SUBSCRIPTION_ID="${SUBSCRIPTION_ID:?Set SUBSCRIPTION_ID to the approved Azure subscription GUID}"
RUN_ID="${RUN_ID:?Set RUN_ID to a unique lowercase alphanumeric value, for example wng0711}"
CREATE_AZURE_RESOURCES="${CREATE_AZURE_RESOURCES:-}"
LOCATION="${LOCATION:-westus2}"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-1.35.5}"
SYSTEM_NODE_COUNT="1"
NVME_NODE_COUNT="1"
SYSTEM_VM_SIZE="${SYSTEM_VM_SIZE:-Standard_D4s_v5}"
NVME_VM_SIZE="${NVME_VM_SIZE:-Standard_L8s_v3}"
RESOURCE_GROUP="rg-aks-burner-kata-acstor-${RUN_ID}"
CLUSTER_NAME="aksacsnvme${RUN_ID}"
KUBECONFIG_PATH="${KUBECONFIG_PATH:-/tmp/${CLUSTER_NAME}.kubeconfig}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

if [[ ! "$RUN_ID" =~ ^[a-z0-9]{4,12}$ ]]; then
  printf 'RUN_ID must contain 4-12 lowercase letters or digits.\n' >&2
  exit 1
fi
if [[ "$CREATE_AZURE_RESOURCES" != "yes" ]]; then
  printf 'Set CREATE_AZURE_RESOURCES=yes after reviewing subscription, names, region, and cost.\n' >&2
  exit 1
fi

ACTIVE_SUBSCRIPTION="$(az account show --query id --output tsv)"
if [[ "$ACTIVE_SUBSCRIPTION" != "$SUBSCRIPTION_ID" ]]; then
  printf 'Active subscription %s does not match SUBSCRIPTION_ID %s.\n' "$ACTIVE_SUBSCRIPTION" "$SUBSCRIPTION_ID" >&2
  exit 1
fi
OWNER="$(az account show --query user.name --output tsv)"

wait_for_cluster_ready() {
  local state
  for _ in {1..60}; do
    state="$(az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --query provisioningState --output tsv)"
    case "$state" in
      Succeeded) return 0 ;;
      Failed|Canceled) printf 'AKS entered terminal state %s.\n' "$state" >&2; return 1 ;;
    esac
    sleep 30
  done
  printf 'Timed out waiting for AKS to finish updating.\n' >&2
  return 1
}

wait_for_local_capacity() {
  local capacity_output nvme_node
  nvme_node="$(kubectl get nodes -l kubernetes.azure.com/agentpool=nvmepool -o jsonpath='{.items[0].metadata.name}')"
  for _ in {1..60}; do
    capacity_output="$(kubectl get csistoragecapacities.storage.k8s.io -n kube-system -o go-template='{{range .items}}{{if eq .storageClassName "local-csi"}}{{.capacity}} {{index .nodeTopology.matchLabels "topology.localdisk.csi.acstor.io/node"}}{{"\n"}}{{end}}{{end}}')"
    if grep -Eq "^[1-9][^ ]* ${nvme_node}$" <<<"$capacity_output"; then
      return 0
    fi
    sleep 10
  done
  printf 'No nonzero local-csi capacity appeared on %s.\n' "$nvme_node" >&2
  kubectl get pods -n kube-system -o wide >&2
  kubectl get csistoragecapacities.storage.k8s.io -n kube-system -o wide >&2
  return 1
}

wait_for_kubernetes_resource() {
  local description="$1"
  shift
  for _ in {1..60}; do
    if "$@" >/dev/null 2>&1; then
      return 0
    fi
    sleep 10
  done
  printf 'Timed out waiting for %s.\n' "$description" >&2
  return 1
}

if az group exists --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" | tr '[:upper:]' '[:lower:]' | grep -q '^true$'; then
  printf 'Resource group %s already exists; choose another RUN_ID.\n' "$RESOURCE_GROUP" >&2
  exit 1
fi

printf 'Subscription and owner:\n'
az account show --query '{name:name,id:id,user:user.name}' --output table
printf 'Resource group: %s\nCluster: %s\nRegion: %s\n' "$RESOURCE_GROUP" "$CLUSTER_NAME" "$LOCATION"
printf '\nRelevant quota in %s:\n' "$LOCATION"
az vm list-usage --subscription "$SUBSCRIPTION_ID" --location "$LOCATION" \
  --query "[?name.localizedValue=='Standard LSv3 Family vCPUs' || name.localizedValue=='Total Regional vCPUs'].{name:name.localizedValue,current:currentValue,limit:limit}" \
  --output table

az group create \
  --subscription "$SUBSCRIPTION_ID" \
  --name "$RESOURCE_GROUP" \
  --location "$LOCATION" \
  --tags aks-burner-run-id="$RUN_ID" aks-burner-owner="$OWNER" purpose=kata-acstor-nvme-poc

GROUP_RUN_ID="$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query 'tags."aks-burner-run-id"' --output tsv)"
GROUP_OWNER="$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query 'tags."aks-burner-owner"' --output tsv)"
if [[ "$GROUP_RUN_ID" != "$RUN_ID" || "$GROUP_OWNER" != "$OWNER" ]]; then
  printf 'Resource group ownership tags do not match; refusing to provision into it.\n' >&2
  exit 1
fi

az aks create \
  --subscription "$SUBSCRIPTION_ID" \
  --resource-group "$RESOURCE_GROUP" \
  --name "$CLUSTER_NAME" \
  --location "$LOCATION" \
  --kubernetes-version "$KUBERNETES_VERSION" \
  --nodepool-name systempool \
  --node-count "$SYSTEM_NODE_COUNT" \
  --node-vm-size "$SYSTEM_VM_SIZE" \
  --node-osdisk-type Managed \
  --os-sku Ubuntu \
  --network-plugin azure \
  --network-plugin-mode overlay \
  --network-dataplane cilium \
  --ssh-access disabled

az aks nodepool add \
  --subscription "$SUBSCRIPTION_ID" \
  --resource-group "$RESOURCE_GROUP" \
  --cluster-name "$CLUSTER_NAME" \
  --name nvmepool \
  --mode User \
  --node-count "$NVME_NODE_COUNT" \
  --node-vm-size "$NVME_VM_SIZE" \
  --node-osdisk-type Managed \
  --os-sku AzureLinux \
  --workload-runtime KataMshvVmIsolation \
  --labels perf.azure.com/node-role=workload \
  --ssh-access disabled

wait_for_cluster_ready

az aks update \
  --subscription "$SUBSCRIPTION_ID" \
  --resource-group "$RESOURCE_GROUP" \
  --name "$CLUSTER_NAME" \
  --enable-azure-container-storage ephemeralDisk \
  --container-storage-version 2

if [[ -e "$KUBECONFIG_PATH" ]]; then
  printf 'KUBECONFIG_PATH %s already exists; choose another path.\n' "$KUBECONFIG_PATH" >&2
  exit 1
fi
az aks get-credentials \
  --subscription "$SUBSCRIPTION_ID" \
  --resource-group "$RESOURCE_GROUP" \
  --name "$CLUSTER_NAME" \
  --file "$KUBECONFIG_PATH"
export KUBECONFIG="$KUBECONFIG_PATH"

wait_for_kubernetes_resource "Ready nvmepool node" kubectl wait --for=condition=Ready node -l kubernetes.azure.com/agentpool=nvmepool --timeout=10s
wait_for_kubernetes_resource "kata-vm-isolation RuntimeClass" kubectl get runtimeclass kata-vm-isolation
wait_for_kubernetes_resource "local-csi StorageClass" kubectl get storageclass local-csi
kubectl apply -f "$SCRIPT_DIR/storageclass.yml"
kubectl apply -f "$SCRIPT_DIR/namespace.yml"
wait_for_kubernetes_resource "csi-local-node DaemonSet" kubectl get daemonset/csi-local-node -n kube-system
kubectl rollout status daemonset/csi-local-node -n kube-system --timeout=10m
wait_for_local_capacity

kubectl get nodes -o wide
kubectl get runtimeclass kata-vm-isolation
kubectl get storageclass local-csi
kubectl get pods -n kube-system -o wide
kubectl get csistoragecapacities.storage.k8s.io -n kube-system -o wide
printf 'Use this isolated kubeconfig for the experiment:\nexport KUBECONFIG=%q\n' "$KUBECONFIG_PATH"
