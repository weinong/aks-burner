def lower: ascii_downcase;
def resource_id: .id | lower;
def resource_type: .type | lower;
def property_references:
  ([.properties | .. | strings | select(test("^/subscriptions/"; "i"))] +
   [(.identity.userAssignedIdentities // {}) | keys[]]) | map(lower);
def cluster_anchor:
  resource_type == "microsoft.network/loadbalancers" and
  .name == "kubernetes" and
  .tags["aks-managed-cluster-name"] == $aks and .tags["aks-managed-cluster-rg"] == $aks_rg;
def cluster_anchor_references:
  [.properties.backendAddressPools[]?.properties.backendIPConfigurations[]?.id,
   .properties.backendAddressPools[]?.properties.loadBalancerBackendAddresses[]?.properties.networkInterfaceIPConfiguration.id]
  | map(select(type == "string") | lower) | unique;
def exact_vmss($anchor_references):
  resource_type == "microsoft.compute/virtualmachinescalesets" and
  (.tags["aks-managed-poolName"] == "systempool" or .tags["aks-managed-poolName"] == "katapool") and
  (resource_id as $id |
   [$anchor_references[] |
    select(startswith($id + "/virtualmachines/")) |
    ltrimstr($id + "/virtualmachines/") |
    select(test("^[0-9]+/networkinterfaces/[^/]+/ipconfigurations/[^/]+$"))] | length > 0);
def referenced_ids($resources; $owned):
  [$resources[] | select((resource_id as $id | $owned | index($id)) != null) | property_references[]] | unique;
def topology_related($owned; $references; $anchor_references):
  . as $resource | resource_id as $id | resource_type as $type |
  if cluster_anchor then true
  elif $type == "microsoft.compute/virtualmachinescalesets" then exact_vmss($anchor_references)
  elif $type == "microsoft.managedidentity/userassignedidentities" then
    ($kubelet != "" and $id == $kubelet) or ($references | index($id) != null)
  elif $type == "microsoft.kubernetesconfiguration/privatelinkscopes" then
    (.properties.clusterResourceId // "" | lower) == $aks_id
  elif $type == "microsoft.network/networkinterfaces" then
    (.properties.virtualMachine.id // "" | lower) as $vm |
    [$owned[] | . as $owner | select($vm | startswith($owner + "/virtualmachines/"))] | length > 0
  elif $type == "microsoft.compute/disks" then
    ($resource.managedBy // $resource.properties.managedBy // "" | lower) as $manager |
    [$owned[] | . as $owner | select($manager | startswith($owner + "/virtualmachines/"))] | length > 0
  else
    [$references[] | . as $reference | select($reference == $id or ($reference | startswith($id + "/")))] | length > 0
  end;

. as $resources |
($resources | map(select(cluster_anchor))) as $anchors |
([$anchors[] | cluster_anchor_references[]] | unique) as $anchor_references |
($resources | map(select(exact_vmss($anchor_references)) | resource_id) | unique) as $roots |
if ($anchors | length) != 1 then
  error("expected exactly one cluster-tagged kubernetes load balancer anchor")
elif ($resources | map(select(resource_type == "microsoft.compute/virtualmachinescalesets")) | length) != ($roots | length) then
  error("one or more VMSS resources lack exact cluster-anchor references or pool tags")
elif ($roots | length) != 2 or
  ($resources | map(select(exact_vmss($anchor_references) and .tags["aks-managed-poolName"] == "systempool")) | length) != 1 or
  ($resources | map(select(exact_vmss($anchor_references) and .tags["aks-managed-poolName"] == "katapool")) | length) != 1 then
  error("expected exactly one systempool and one katapool VMSS root")
else
  {owned: $roots, references: []} |
  until(
    . as $state |
    (referenced_ids($resources; $state.owned)) as $refs |
    ($resources | map(select(topology_related($state.owned; $refs; $anchor_references)) | resource_id) | unique) as $next |
    ($next | length) == ($state.owned | length);
    . as $state |
    (referenced_ids($resources; $state.owned)) as $refs |
    .references = $refs |
    .owned = ($resources | map(select(topology_related($state.owned; $refs; $anchor_references)) | resource_id) | unique)
  ) |
  .references = referenced_ids($resources; .owned) |
  . as $closure |
  ($resources | map(select((resource_id as $id | $closure.owned | index($id)) == null) | [.type,.name,.id])) as $foreign |
  if ($foreign | length) == 0 then
    {owned_ids:$closure.owned, referenced_ids:$closure.references}
  else
    error("resources lack exact VMSS/AKS topology proof: " + ($foreign | tostring))
  end
end
