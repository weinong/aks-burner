#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

SUBSCRIPTION_ID=${SUBSCRIPTION_ID:?Set the exact provisioned subscription GUID}
RUN_ID=${RUN_ID:?Set the exact run ID}
DEPLOYMENT_ID=${DEPLOYMENT_ID:?Set the exact opaque deployment UUID}
KUBECONFIG_PATH=${KUBECONFIG_PATH:-}
DELETE_AZURE_RESOURCES=${DELETE_AZURE_RESOURCES:-}
derive_names
RESULT_ROOT=${RESULT_ROOT:-$EXPERIMENT_DIR/results}
UTC=$(date -u +%Y%m%dT%H%M%SZ)
RESULT_DIR="$RESULT_ROOT/${UTC}-destroy-${DEPLOYMENT_SUFFIX}"
KUBECONFIG_VALIDATED=false
EXPECTED_AKS_ID="/subscriptions/$SUBSCRIPTION_ID/resourceGroups/$RESOURCE_GROUP/providers/Microsoft.ContainerService/managedClusters/$CLUSTER_NAME"
EXPECTED_NODE_RESOURCE_GROUP_ID="/subscriptions/$SUBSCRIPTION_ID/resourceGroups/$NODE_RESOURCE_GROUP"
NODE_RESOURCE_GROUP_EXISTS=false
KUBELET_ID=
ALLOWED_NODE_RESOURCE_TYPES=(
  microsoft.compute/disks
  microsoft.compute/virtualmachinescalesets
  microsoft.managedidentity/userassignedidentities
  microsoft.network/loadbalancers
  microsoft.network/networkinterfaces
  microsoft.network/networksecuritygroups
  microsoft.network/publicipaddresses
  microsoft.network/virtualnetworks
)

[[ $DELETE_AZURE_RESOURCES == yes ]] || die "Set DELETE_AZURE_RESOURCES=yes to delete exact group $RESOURCE_GROUP"
require_tools az jq kubectl
[[ $(az account show --query id -o tsv) == "$SUBSCRIPTION_ID" ]] || safety_die 'active subscription mismatch'
[[ -z $KUBECONFIG_PATH || ( -f $KUBECONFIG_PATH && ! -L $KUBECONFIG_PATH ) ]] || safety_die 'provided isolated kubeconfig is unsafe'
[[ $(az group exists --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP") == true ]] || die "exact resource group is absent: $RESOURCE_GROUP"
GROUP_NAME=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query name -o tsv)
GROUP_DEPLOYMENT=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query 'tags."deployment-id"' -o tsv)
GROUP_PURPOSE=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query tags.purpose -o tsv)
GROUP_RUN=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query 'tags."run-id"' -o tsv)
GROUP_SUFFIX=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query 'tags."deployment-suffix"' -o tsv)
[[ $GROUP_NAME == "$RESOURCE_GROUP" && $GROUP_DEPLOYMENT == "$DEPLOYMENT_ID" && $GROUP_PURPOSE == "$PURPOSE" && $GROUP_RUN == "$RUN_ID" && $GROUP_SUFFIX == "$DEPLOYMENT_SUFFIX" ]] ||
  safety_die 'exact group name or deployment-id/purpose/run-id/deployment-suffix tags do not match'
[[ ! -e $RESULT_DIR ]] || die "result target exists: $RESULT_DIR"
mkdir -p "$RESULT_DIR"; chmod 0700 "$RESULT_DIR"
az resource list --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" -o json >"$RESULT_DIR/resource-inventory.json"
az lock list --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" -o json >"$RESULT_DIR/resource-locks.json"
[[ $(jq 'length' "$RESULT_DIR/resource-locks.json") -eq 0 ]] || safety_die 'exact resource group contains a management lock'
mapfile -t unowned_resources < <(jq -r --arg deployment "$DEPLOYMENT_ID" --arg purpose "$PURPOSE" --arg run "$RUN_ID" --arg suffix "$DEPLOYMENT_SUFFIX" '
  .[] | select(
    .tags["deployment-id"] != $deployment or .tags.purpose != $purpose or
    .tags["run-id"] != $run or .tags["deployment-suffix"] != $suffix
  ) | [.type,.name] | @tsv' "$RESULT_DIR/resource-inventory.json")
[[ ${#unowned_resources[@]} -eq 0 ]] || safety_die "resource group contains a resource without exact deployment-id/purpose/run-id/suffix ownership: ${unowned_resources[*]}"
mapfile -t unexpected_resources < <(jq -r --arg aks "$CLUSTER_NAME" --arg acr "$ACR_NAME" '
  .[] | select(
    ((.type | ascii_downcase) != "microsoft.containerservice/managedclusters" or .name != $aks) and
    ((.type | ascii_downcase) != "microsoft.containerregistry/registries" or .name != $acr)
  ) | [.type,.name] | @tsv' "$RESULT_DIR/resource-inventory.json")
[[ ${#unexpected_resources[@]} -eq 0 ]] || safety_die "resource group contains resources outside exact owned AKS+ACR set: ${unexpected_resources[*]}"
AKS_COUNT=$(jq -r --arg aks "$CLUSTER_NAME" '[.[] | select((.type | ascii_downcase) == "microsoft.containerservice/managedclusters" and .name == $aks)] | length' "$RESULT_DIR/resource-inventory.json")
ACR_COUNT=$(jq -r --arg acr "$ACR_NAME" '[.[] | select((.type | ascii_downcase) == "microsoft.containerregistry/registries" and .name == $acr)] | length' "$RESULT_DIR/resource-inventory.json")
[[ $AKS_COUNT -le 1 && $ACR_COUNT -le 1 ]] || safety_die 'exact resource inventory contains duplicate expected resources'
if [[ $AKS_COUNT -eq 1 ]]; then
  az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" -o json >"$RESULT_DIR/aks.json"
  jq -e --arg id "${EXPECTED_AKS_ID,,}" --arg aks "$CLUSTER_NAME" --arg nrg "$NODE_RESOURCE_GROUP" \
    --arg deployment "$DEPLOYMENT_ID" --arg purpose "$PURPOSE" --arg run "$RUN_ID" --arg suffix "$DEPLOYMENT_SUFFIX" '
      (.id | ascii_downcase) == $id and .name == $aks and .nodeResourceGroup == $nrg and
      .tags["deployment-id"] == $deployment and .tags.purpose == $purpose and
      .tags["run-id"] == $run and .tags["deployment-suffix"] == $suffix
    ' "$RESULT_DIR/aks.json" >/dev/null || safety_die 'exact tag-validated AKS object identity or nodeResourceGroup does not match'
  KUBELET_ID=$(jq -r '.identityProfile.kubeletidentity.resourceId // empty | ascii_downcase' "$RESULT_DIR/aks.json")
  if [[ -n $KUBECONFIG_PATH ]]; then assert_kubeconfig_targets_cluster; KUBECONFIG_VALIDATED=true; fi
fi
if [[ $ACR_COUNT -eq 1 ]]; then
  ACR_ACTUAL=$(az acr show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$ACR_NAME" --query name -o tsv)
  [[ $ACR_ACTUAL == "$ACR_NAME" ]] || safety_die 'exact ACR name does not match'
fi
if [[ $(az group exists --subscription "$SUBSCRIPTION_ID" --name "$NODE_RESOURCE_GROUP") == true ]]; then
  NODE_RESOURCE_GROUP_EXISTS=true
  az group show --subscription "$SUBSCRIPTION_ID" --name "$NODE_RESOURCE_GROUP" -o json >"$RESULT_DIR/node-resource-group.json"
  jq -e --arg name "$NODE_RESOURCE_GROUP" --arg id "${EXPECTED_NODE_RESOURCE_GROUP_ID,,}" --arg managed_by "${EXPECTED_AKS_ID,,}" '
    .name == $name and (.id | ascii_downcase) == $id and (.managedBy | ascii_downcase) == $managed_by
    ' "$RESULT_DIR/node-resource-group.json" >/dev/null ||
    safety_die 'node-resource-group inventory: exact node resource group ID or managedBy does not point to the exact AKS resource'
  az lock list --subscription "$SUBSCRIPTION_ID" --resource-group "$NODE_RESOURCE_GROUP" -o json >"$RESULT_DIR/node-resource-group-locks.json"
  [[ $(jq 'length' "$RESULT_DIR/node-resource-group-locks.json") -eq 0 ]] ||
    safety_die 'node-resource-group inventory: foreign locks are present'
  az resource list --subscription "$SUBSCRIPTION_ID" --resource-group "$NODE_RESOURCE_GROUP" -o json >"$RESULT_DIR/node-resource-group-inventory.json"
  ALLOWED_NODE_RESOURCE_TYPES_JSON=$(printf '%s\n' "${ALLOWED_NODE_RESOURCE_TYPES[@]}" | jq -Rsc 'split("\n") | map(select(length > 0))')
  mapfile -t unexpected_node_types < <(jq -r --argjson allowed "$ALLOWED_NODE_RESOURCE_TYPES_JSON" '
    .[] | (.type | ascii_downcase) as $type | select($allowed | index($type) | not) | [.type,.name,.id] | @tsv
    ' "$RESULT_DIR/node-resource-group-inventory.json")
  [[ ${#unexpected_node_types[@]} -eq 0 ]] ||
    safety_die "node-resource-group inventory: unexpected node resource type: ${unexpected_node_types[*]}"
  mapfile -t misplaced_node_resources < <(jq -r --arg rg "$NODE_RESOURCE_GROUP" --arg rg_id "${EXPECTED_NODE_RESOURCE_GROUP_ID,,}" '
    .[] | select(.resourceGroup != $rg or ((.id | ascii_downcase) | startswith($rg_id + "/providers/") | not)) |
      [.type,.name,.id] | @tsv
    ' "$RESULT_DIR/node-resource-group-inventory.json")
  [[ ${#misplaced_node_resources[@]} -eq 0 ]] ||
    safety_die "node-resource-group inventory: resourceGroup or exact node resource group ID prefix differs: ${misplaced_node_resources[*]}"
  mapfile -t contradictory_node_tags < <(jq -r --arg aks "$CLUSTER_NAME" --arg aks_rg "$RESOURCE_GROUP" '
    .[] | select(
      (.tags["aks-managed-cluster-name"] != null and .tags["aks-managed-cluster-name"] != $aks) or
      (.tags["aks-managed-cluster-rg"] != null and .tags["aks-managed-cluster-rg"] != $aks_rg) or
      (.tags["aks-managed-poolName"] != null and .tags["aks-managed-poolName"] != "systempool" and .tags["aks-managed-poolName"] != "katapool")
    ) | [.type,.name,.id] | @tsv
    ' "$RESULT_DIR/node-resource-group-inventory.json")
  [[ ${#contradictory_node_tags[@]} -eq 0 ]] ||
    safety_die "node-resource-group inventory: supplied AKS system tags contradict the exact cluster or pools: ${contradictory_node_tags[*]}"

  : >"$RESULT_DIR/node-resource-topology.jsonl"
  mapfile -t node_resource_ids < <(jq -r '.[].id' "$RESULT_DIR/node-resource-group-inventory.json")
  for resource_id in "${node_resource_ids[@]}"; do
    az resource show --subscription "$SUBSCRIPTION_ID" --ids "$resource_id" -o json | jq -c . >>"$RESULT_DIR/node-resource-topology.jsonl" ||
      safety_die "node-resource-group inventory: cannot retrieve relationship topology for $resource_id"
  done
  jq -s . "$RESULT_DIR/node-resource-topology.jsonl" >"$RESULT_DIR/node-resource-topology.json"
  rm -f "$RESULT_DIR/node-resource-topology.jsonl"
  jq -e --arg aks "$CLUSTER_NAME" --arg aks_rg "$RESOURCE_GROUP" --arg kubelet "$KUBELET_ID" \
    -f "$EXPERIMENT_DIR/validate-node-resource-topology.jq" "$RESULT_DIR/node-resource-topology.json" \
    >"$RESULT_DIR/node-resource-topology-proof.json" ||
    safety_die 'node-resource-group inventory: a resource is not tied to the exact AKS cluster/pools by allowed topology; preserving the node resource group'
elif [[ $AKS_COUNT -eq 1 ]]; then
  safety_die 'node-resource-group inventory: exact tag-validated AKS object exists but its expected node resource group is absent'
fi
if [[ $AKS_COUNT -eq 0 && $NODE_RESOURCE_GROUP_EXISTS == true ]]; then
  run_logged exact-node-group-delete az group delete --subscription "$SUBSCRIPTION_ID" --name "$NODE_RESOURCE_GROUP" --yes --no-wait
  run_logged exact-node-group-wait az group wait --subscription "$SUBSCRIPTION_ID" --name "$NODE_RESOURCE_GROUP" --deleted --interval 15 --timeout 1800
  NODE_RESOURCE_GROUP_EXISTS=false
fi
run_logged exact-group-delete az group delete --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --yes --no-wait
run_logged exact-group-wait az group wait --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --deleted --interval 15 --timeout 1800
if [[ $NODE_RESOURCE_GROUP_EXISTS == true ]]; then
  run_logged exact-node-group-wait az group wait --subscription "$SUBSCRIPTION_ID" --name "$NODE_RESOURCE_GROUP" --deleted --interval 15 --timeout 1800
fi
[[ $KUBECONFIG_VALIDATED == false ]] || rm -f -- "$KUBECONFIG_PATH"
printf 'Deleted exact resource group %s; no prefix or wildcard deletion was used.\n' "$RESOURCE_GROUP"
