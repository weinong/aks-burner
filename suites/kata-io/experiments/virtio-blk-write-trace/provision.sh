#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/../../../.." && pwd -P)
SUBSCRIPTION_ID="${SUBSCRIPTION_ID:?Set SUBSCRIPTION_ID to the approved Azure subscription GUID}"
RUN_ID="${RUN_ID:?Set RUN_ID to a unique lowercase alphanumeric value}"
DEPLOYMENT_ID="${DEPLOYMENT_ID:?Set DEPLOYMENT_ID to a new opaque UUID}"
CREATE_AZURE_RESOURCES="${CREATE_AZURE_RESOURCES:-}"
LOCATION="${LOCATION:-westus2}"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-1.36}"
SYSTEM_VM_SIZE="${SYSTEM_VM_SIZE:-Standard_D4s_v5}"
KATA_VM_SIZE="${KATA_VM_SIZE:-Standard_L8s_v3}"
RESOURCE_GROUP="rg-aks-burner-kata-vbw-${RUN_ID}"
CLUSTER_NAME="aksvbw${RUN_ID}"
ACR_NAME="aksvbw${RUN_ID}acr"
KUBECONFIG_PATH="${KUBECONFIG_PATH:?Set KUBECONFIG_PATH to a new caller-controlled path}"
RESULT_ROOT="${RESULT_ROOT:-$REPO_ROOT/results/virtio-blk-write-trace/provision}"
RESULT_DIR="$RESULT_ROOT/$RUN_ID"
METADATA_PATH="${METADATA_PATH:-$RESULT_DIR/cluster-metadata.json}"
STATE_PATH="$RESULT_DIR/provision-state.env"
GROUP_CREATED=false
PROVISION_SUCCEEDED=false

timestamp() { date -u +%Y-%m-%dT%H:%M:%S.%NZ; }
redact() {
  sed -E \
    -e 's#(/subscriptions/)[0-9a-fA-F-]{36}#\1[REDACTED]#g' \
    -e 's/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}/[REDACTED-GUID]/g' \
    -e 's/(Bearer[[:space:]]+)[A-Za-z0-9._~+\/-]+/\1[REDACTED]/Ig' \
    -e 's/((access_?token|refresh_?token|client_?secret|password)["=: ]+)[^ ,"}]+/\1[REDACTED]/Ig' \
    -e 's/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/[REDACTED-JWT]/g'
}
save_state() {
  local status=$1
  {
    printf 'STATUS=%q\n' "$status"
    printf 'RUN_ID=%q\nDEPLOYMENT_ID=%q\nRESOURCE_GROUP=%q\nCLUSTER_NAME=%q\n' \
      "$RUN_ID" "$DEPLOYMENT_ID" "$RESOURCE_GROUP" "$CLUSTER_NAME"
    printf 'KUBECONFIG_REMOVED=%q\nRESULT_DIR=%q\n' "$([[ ! -e $KUBECONFIG_PATH ]] && printf true || printf false)" "$RESULT_DIR"
  } >"$STATE_PATH"
}
run_logged() {
  local label=$1 start rc out err log
  shift
  start=$(timestamp); out=$(mktemp); err=$(mktemp); log="$RESULT_DIR/${label//[^A-Za-z0-9_.-]/_}.log"
  { printf 'COMMAND_START label=%q time=%s command=' "$label" "$start"; printf '%q ' "$@"; printf '\n'; } | redact >"$log"
  if "$@" >"$out" 2>"$err"; then rc=0; else rc=$?; fi
  printf '%s\n' '--- stdout ---' >>"$log"; redact <"$out" >>"$log"
  printf '%s\n' '--- stderr ---' >>"$log"; redact <"$err" >>"$log"
  printf 'COMMAND_END label=%q time=%s status=%d\n' "$label" "$(timestamp)" "$rc" >>"$log"
  [[ $rc -ne 0 ]] || cat "$out"
  rm -f "$out" "$err"
  return "$rc"
}
on_exit() {
  local rc=$?
  trap - EXIT INT TERM
  if [[ $PROVISION_SUCCEEDED != true ]]; then
    rm -f "$KUBECONFIG_PATH"
    save_state failed
    if [[ $GROUP_CREATED == true ]]; then
      printf 'Provisioning failed. Resource group %s was preserved for inspection.\n' "$RESOURCE_GROUP" >&2
      printf 'After review, run RUN_ID=%q DEPLOYMENT_ID=%q DELETE_AZURE_RESOURCES=yes ./destroy-cluster.sh\n' "$RUN_ID" "$DEPLOYMENT_ID" >&2
    fi
  fi
  exit "$rc"
}
version_ge() { [[ $(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1) == "$2" ]]; }
require_sku() {
  local sku=$1 available
  available=$(az vm list-skus --subscription "$SUBSCRIPTION_ID" --location "$LOCATION" --resource-type virtualMachines \
    --query "[?name=='$sku' && (restrictions==null || length(restrictions)==\`0\`)].name | [0]" -o tsv)
  [[ $available == "$sku" ]] || { printf 'VM SKU %s is unavailable without restrictions in %s.\n' "$sku" "$LOCATION" >&2; exit 1; }
}
wait_for_aks_state() {
  local description=$1 query=$2 state
  for _ in {1..60}; do
    state=$(az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --query "$query" -o tsv)
    case $state in Succeeded) return 0 ;; Failed|Canceled) printf '%s entered %s.\n' "$description" "$state" >&2; return 1 ;; esac
    sleep 30
  done
  printf 'Timed out waiting for %s.\n' "$description" >&2
  return 1
}
wait_for_kubernetes_resource() {
  local description=$1; shift
  for _ in {1..60}; do "$@" >/dev/null 2>&1 && return 0; sleep 10; done
  printf 'Timed out waiting for %s.\n' "$description" >&2
  return 1
}

[[ $SUBSCRIPTION_ID =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{4}-[[:xdigit:]]{12}$ ]] || { printf 'SUBSCRIPTION_ID must be a GUID.\n' >&2; exit 1; }
[[ $DEPLOYMENT_ID =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[1-5][[:xdigit:]]{3}-[89abAB][[:xdigit:]]{3}-[[:xdigit:]]{12}$ ]] || { printf 'DEPLOYMENT_ID must be an opaque UUID.\n' >&2; exit 1; }
[[ $RUN_ID =~ ^[a-z0-9]{4,12}$ ]] || { printf 'RUN_ID must contain 4-12 lowercase letters or digits.\n' >&2; exit 1; }
[[ $CREATE_AZURE_RESOURCES == yes ]] || { printf 'Set CREATE_AZURE_RESOURCES=yes after review.\n' >&2; exit 1; }
for tool in az kubectl sort; do command -v "$tool" >/dev/null || { printf '%s is required.\n' "$tool" >&2; exit 1; }; done
[[ ! -e $KUBECONFIG_PATH ]] || { printf 'KUBECONFIG_PATH already exists: %s\n' "$KUBECONFIG_PATH" >&2; exit 1; }
[[ ! -e $RESULT_DIR ]] || { printf 'Result directory already exists: %s\n' "$RESULT_DIR" >&2; exit 1; }
mkdir -p "$RESULT_DIR"; chmod 0700 "$RESULT_DIR"; trap on_exit EXIT INT TERM

CLI_VERSION=$(az version --query '"azure-cli"' -o tsv)
version_ge "$CLI_VERSION" 2.80.0 || { printf 'Azure CLI >=2.80.0 is required; found %s.\n' "$CLI_VERSION" >&2; exit 1; }
ACTIVE_SUBSCRIPTION=$(az account show --query id -o tsv)
[[ $ACTIVE_SUBSCRIPTION == "$SUBSCRIPTION_ID" ]] || { printf 'Active subscription does not match SUBSCRIPTION_ID.\n' >&2; exit 1; }
AVAILABLE_VERSION=$(az aks get-versions --subscription "$SUBSCRIPTION_ID" --location "$LOCATION" \
  --query "values[?version=='$KUBERNETES_VERSION'].version | [0]" -o tsv)
[[ $AVAILABLE_VERSION == "$KUBERNETES_VERSION" ]] || { printf 'AKS version %s is unavailable in %s.\n' "$KUBERNETES_VERSION" "$LOCATION" >&2; exit 1; }
require_sku "$SYSTEM_VM_SIZE"; require_sku "$KATA_VM_SIZE"
run_logged quota-snapshot az vm list-usage --subscription "$SUBSCRIPTION_ID" --location "$LOCATION" -o json >/dev/null
SYSTEM_VCPUS=$(az vm list-skus --subscription "$SUBSCRIPTION_ID" --location "$LOCATION" --resource-type virtualMachines \
  --query "[?name=='$SYSTEM_VM_SIZE'] | [0].capabilities[?name=='vCPUs'] | [0].value" -o tsv)
KATA_VCPUS=$(az vm list-skus --subscription "$SUBSCRIPTION_ID" --location "$LOCATION" --resource-type virtualMachines \
  --query "[?name=='$KATA_VM_SIZE'] | [0].capabilities[?name=='vCPUs'] | [0].value" -o tsv)
[[ $SYSTEM_VCPUS =~ ^[0-9]+$ && $KATA_VCPUS =~ ^[0-9]+$ ]] || { printf 'Unable to resolve VM SKU vCPU counts.\n' >&2; exit 1; }
TOTAL_VCPUS=$((SYSTEM_VCPUS + KATA_VCPUS))
mapfile -t REGIONAL_USAGE < <(az vm list-usage --subscription "$SUBSCRIPTION_ID" --location "$LOCATION" \
  --query "[?name.value=='cores'].[currentValue,limit] | [0]" -o tsv)
[[ ${#REGIONAL_USAGE[@]} -eq 2 ]] || { printf 'Unable to resolve regional vCPU quota.\n' >&2; exit 1; }
REGIONAL_CURRENT=${REGIONAL_USAGE[0]}
REGIONAL_LIMIT=${REGIONAL_USAGE[1]}
REGIONAL_REMAINING=$((REGIONAL_LIMIT - REGIONAL_CURRENT))
[[ $REGIONAL_REMAINING =~ ^[0-9]+$ && $REGIONAL_REMAINING -ge $TOTAL_VCPUS ]] || { printf 'Insufficient regional vCPU quota: need %s, remaining %s.\n' "$TOTAL_VCPUS" "${REGIONAL_REMAINING:-unknown}" >&2; exit 1; }
[[ $(az group exists --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP") == false ]] || { printf 'Resource group exists; choose another RUN_ID.\n' >&2; exit 1; }

run_logged group-create az group create --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --location "$LOCATION" \
  --tags deployment-id="$DEPLOYMENT_ID" purpose=kata-virtio-blk-write-trace run-id="$RUN_ID" --output none
GROUP_CREATED=true; save_state group-created
run_logged acr-create az acr create --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" \
  --name "$ACR_NAME" --sku Basic --admin-enabled false --output none
run_logged aks-create az aks create --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" \
  --location "$LOCATION" --kubernetes-version "$KUBERNETES_VERSION" --nodepool-name systempool --node-count 1 \
  --node-vm-size "$SYSTEM_VM_SIZE" --node-osdisk-type Managed --os-sku Ubuntu --network-plugin azure \
  --network-plugin-mode overlay --network-dataplane cilium --ssh-access disabled \
  --tags deployment-id="$DEPLOYMENT_ID" purpose=kata-virtio-blk-write-trace run-id="$RUN_ID" --yes --output none
wait_for_aks_state 'AKS cluster' provisioningState
run_logged katapool-add az aks nodepool add --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" \
  --cluster-name "$CLUSTER_NAME" --name katapool --mode User --node-count 1 --node-vm-size "$KATA_VM_SIZE" \
  --node-osdisk-type Managed --os-sku AzureLinux --workload-runtime KataVmIsolation \
  --node-taints dedicated=kata-virtio-blk-write-trace:NoSchedule --labels perf.azure.com/node-role=workload \
  --ssh-access disabled --tags deployment-id="$DEPLOYMENT_ID" purpose=kata-virtio-blk-write-trace run-id="$RUN_ID" --output none
wait_for_aks_state 'Kata node pool' 'agentPoolProfiles[?name==`katapool`] | [0].provisioningState'
run_logged reconcile-pod-sandboxing az aks update --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" \
  --name "$CLUSTER_NAME" --attach-acr "$ACR_NAME" --output none
wait_for_aks_state 'pod sandboxing update' provisioningState

run_logged get-credentials az aks get-credentials --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" \
  --name "$CLUSTER_NAME" --file "$KUBECONFIG_PATH" --output none
wait_for_kubernetes_resource 'Ready system node' kubectl --kubeconfig "$KUBECONFIG_PATH" wait --for=condition=Ready node -l kubernetes.azure.com/agentpool=systempool --timeout=10s
wait_for_kubernetes_resource 'Ready Kata node' kubectl --kubeconfig "$KUBECONFIG_PATH" wait --for=condition=Ready node -l kubernetes.azure.com/agentpool=katapool --timeout=10s
wait_for_kubernetes_resource 'kata-vm-isolation RuntimeClass' kubectl --kubeconfig "$KUBECONFIG_PATH" get runtimeclass kata-vm-isolation
run_logged node-images kubectl --kubeconfig "$KUBECONFIG_PATH" get nodes -o custom-columns=NAME:.metadata.name,POOL:.metadata.labels.agentpool,IMAGE:.status.nodeInfo.osImage,KERNEL:.status.nodeInfo.kernelVersion
az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" \
  --query '{resourceGroup:resourceGroup,name:name,location:location,kubernetesVersion:kubernetesVersion,provisioningState:provisioningState,agentPools:agentPoolProfiles[].{name:name,count:count,vmSize:vmSize,osSku:osSku,osDiskType:osDiskType,workloadRuntime:workloadRuntime,nodeImageVersion:nodeImageVersion,nodeTaints:nodeTaints},tags:tags}' \
  --output json | redact >"$METADATA_PATH"
PROVISION_SUCCEEDED=true; save_state succeeded
printf 'Isolated kubeconfig: %s\nCluster metadata: %s\nLifecycle logs: %s\nACR: %s\n' \
  "$KUBECONFIG_PATH" "$METADATA_PATH" "$RESULT_DIR" "$ACR_NAME"
