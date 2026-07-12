def lower: ascii_downcase;
def resource_id: .id | lower;
def resource_type: .type | lower;
def property_references:
  [.properties | .. | strings | select(test("^/subscriptions/"; "i")) | lower];
def exact_vmss:
  resource_type == "microsoft.compute/virtualmachinescalesets" and
  .tags["aks-managed-cluster-name"] == $aks and .tags["aks-managed-cluster-rg"] == $aks_rg and
  (.tags["aks-managed-poolName"] == "systempool" or .tags["aks-managed-poolName"] == "katapool");
def referenced_ids($resources; $owned):
  [$resources[] | select((resource_id as $id | $owned | index($id)) != null) | property_references[]] | unique;
def topology_related($owned; $references):
  . as $resource | resource_id as $id | resource_type as $type |
  if $type == "microsoft.compute/virtualmachinescalesets" then exact_vmss
  elif $type == "microsoft.managedidentity/userassignedidentities" then $kubelet != "" and $id == $kubelet
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
($resources | map(select(exact_vmss) | resource_id) | unique) as $roots |
if ($resources | map(select(resource_type == "microsoft.compute/virtualmachinescalesets")) | length) != ($roots | length) then
  error("one or more VMSS resources lack exact AKS cluster/pool tags")
elif ($roots | length) != 2 or
  ($resources | map(select(exact_vmss and .tags["aks-managed-poolName"] == "systempool")) | length) != 1 or
  ($resources | map(select(exact_vmss and .tags["aks-managed-poolName"] == "katapool")) | length) != 1 then
  error("expected exactly one systempool and one katapool VMSS root")
else
  {owned: $roots, references: []} |
  until(
    . as $state |
    (referenced_ids($resources; $state.owned)) as $refs |
    ($resources | map(select(topology_related($state.owned; $refs)) | resource_id) | unique) as $next |
    ($next | length) == ($state.owned | length);
    . as $state |
    (referenced_ids($resources; $state.owned)) as $refs |
    .references = $refs |
    .owned = ($resources | map(select(topology_related($state.owned; $refs)) | resource_id) | unique)
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
