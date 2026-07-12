#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
EXPERIMENT_DIR="$SCRIPT_DIR/kata-direct-volume-ab"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

required_files=(
  README.md
  run-experiment.sh
  setup-device.sh
  format-device.sh
  create-raw-block-resources.sh
  run-raw-block-test.sh
  register-direct-volume.sh
  run-direct-volume-test.sh
  collect-runtime-state.sh
  collect-guest-state.sh
  verify-backing-device.sh
  cleanup.sh
  validate-node-resource-topology.jq
  manifests/raw-block-pv.yaml
  manifests/raw-block-pvc.yaml
  manifests/raw-block-probe.yaml
  manifests/direct-volume-probe.yaml
  manifests/optional-debug-probe.yaml
  results/.gitkeep
)

for relative_path in "${required_files[@]}"; do
  [[ -f "$EXPERIMENT_DIR/$relative_path" ]] || fail "missing $relative_path"
done

for script in "$EXPERIMENT_DIR"/*.sh; do
  bash -n "$script"
  [[ -x $script ]] || fail "script is not executable: ${script##*/}"
done
[[ -x $0 ]] || fail 'contract test is not executable'

grep -q 'volumeMode: Block' "$EXPERIMENT_DIR/manifests/raw-block-pv.yaml" || fail 'raw PV is not block mode'
grep -q 'volumeMode: Block' "$EXPERIMENT_DIR/manifests/raw-block-pvc.yaml" || fail 'raw PVC is not block mode'
grep -q 'ReadWriteOncePod' "$EXPERIMENT_DIR/manifests/raw-block-pvc.yaml" || fail 'raw PVC is not single-pod access'
grep -q 'devicePath: /dev/testdisk' "$EXPERIMENT_DIR/manifests/raw-block-probe.yaml" || fail 'raw probe lacks /dev/testdisk'
grep -q 'runtimeClassName: kata-vm-isolation' "$EXPERIMENT_DIR/manifests/raw-block-probe.yaml" || fail 'raw probe lacks Kata runtime'
grep -q 'runtimeClassName: kata-vm-isolation' "$EXPERIMENT_DIR/manifests/direct-volume-probe.yaml" || fail 'direct probe lacks Kata runtime'
grep -q 'mountPath: /workspace' "$EXPERIMENT_DIR/manifests/direct-volume-probe.yaml" || fail 'direct probe lacks /workspace'

grep -q 'DEPLOYMENT_ID' "$EXPERIMENT_DIR/provision.sh" || fail 'provisioning lacks opaque deployment identity'
grep -q 'az group exists' "$EXPERIMENT_DIR/provision.sh" || fail 'provisioning lacks resource-group collision check'
grep -q 'purpose=kata-direct-volume-ab' "$EXPERIMENT_DIR/provision.sh" || fail 'provisioning lacks experiment ownership tag'
grep -Eq 'RESOURCE_GROUP=.*DEPLOYMENT' "$EXPERIMENT_DIR/provision.sh" || fail 'resource group does not include deployment-derived uniqueness'
grep -q 'az acr check-name' "$EXPERIMENT_DIR/provision.sh" || fail 'provisioning lacks global ACR collision check'
grep -q 'az resource list' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'destruction does not inventory the exact resource group'
grep -q 'deployment-suffix' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'destruction does not validate deployment suffix ownership'
grep -q -- '--volume-path "$WORKSPACE" --mount-info "$json"' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct-volume add does not use the supported named arguments'
grep -q -- 'direct-volume remove --volume-path "$WORKSPACE"' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct-volume remove does not use the supported named argument'
if grep -q 'extent_length=.*\$5' "$EXPERIMENT_DIR/device-manager.sh"; then
  fail 'reserved raw blocks rely on unsafe filefrag extent-end arithmetic'
fi
grep -q 'raw restore verification failed' "$EXPERIMENT_DIR/device-manager.sh" || fail 'raw restoration is not verified byte-for-byte'
grep -q 'backing file already exists' "$EXPERIMENT_DIR/device-manager.sh" || fail 'device setup can reuse another run backing file'
grep -q 'owned NVMe mount marker is absent' "$EXPERIMENT_DIR/device-manager.sh" || fail 'device setup can reuse an unowned NVMe mount'
grep -q 'all devices must detach before cleanup' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'cleanup can mutate shared state before all devices detach'
grep -q 'case-scoped workload directory' "$EXPERIMENT_DIR/lib.sh" || fail 'sequential filesystem cases can collide'
grep -q 'sort -u' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'Cloud Hypervisor access-mode comparison is nondeterministic'
grep -q 'TRACE_STATUS=' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'result matrix does not consume trace sufficiency status'
grep -q 'MARKER_OFFSET_ONE=.*state_value' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'raw diagnostic does not receive marker offset one'
grep -q 'MARKER_OFFSET_TWO=.*state_value' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'raw diagnostic does not receive marker offset two'
grep -q 'DIRECT_REGISTRATION_PENDING=1' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct-volume registration is not persisted before validation'
grep -q 'direct registration precondition failed' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct registration does not reject stale metadata before add/rollback'
grep -q 'DIRECT_METADATA_ROOT.*LOOP_DEVICE\|LOOP_DEVICE.*DIRECT_METADATA_ROOT' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct registration does not scan the full metadata root for reused loop identity'
grep -q 'post-removal full metadata scan failed' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct removal does not re-scan the full metadata root'
grep -q 'DIRECT_REGISTRATION_PENDING=0' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct-volume pending state is never resolved'
grep -q 'state_value DIRECT_REGISTRATION_PENDING' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct-volume pending state is not persisted'
grep -q 'state_value DIRECT_PRECONDITION_PROVEN' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct-volume precondition proof is not persisted'
grep -q 'state_value DIRECT_ADD_BOUNDARY_CROSSED' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct-volume add ownership boundary is not persisted'
grep -q 'remove_direct_registration' "$EXPERIMENT_DIR/device-manager.sh" || fail 'direct-volume rollback and cleanup do not share exact removal verification'
grep -Eq 'DIRECT_REGISTERED.*DIRECT_REGISTRATION_PENDING|DIRECT_REGISTRATION_PENDING.*DIRECT_REGISTERED' "$EXPERIMENT_DIR/device-manager.sh" || fail 'cleanup does not treat pending registration as potentially registered'
registration_body=$(awk '/^register_direct\(\)/,/^}/' "$EXPERIMENT_DIR/device-manager.sh")
precondition_body=$(awk '/^prove_direct_registration_precondition\(\)/,/^}/' "$EXPERIMENT_DIR/device-manager.sh")
removal_body=$(awk '/^remove_direct_registration\(\)/,/^}/' "$EXPERIMENT_DIR/device-manager.sh")
absence_body=$(awk '/^direct_registration_absent\(\)/,/^}/' "$EXPERIMENT_DIR/device-manager.sh")
cleanup_device_body=$(awk '/^cleanup_device\(\)/,/^}/' "$EXPERIMENT_DIR/device-manager.sh")
line_of() {
  local body=$1 pattern=$2
  awk -v pattern="$pattern" 'index($0, pattern) { print NR; exit }' <<<"$body"
}
precondition_line=$(line_of "$registration_body" 'prove_direct_registration_precondition')
proof_line=$(line_of "$registration_body" 'DIRECT_PRECONDITION_PROVEN=1')
proof_save_line=$(awk -v start="$proof_line" 'NR > start && /save_state/ { print NR; exit }' <<<"$registration_body")
detect_line=$(line_of "$registration_body" 'detect_direct_cli')
create_line=$(line_of "$registration_body" 'install -d -m 0700 "$DIRECT_ROOT" "$WORKSPACE"')
pending_line=$(line_of "$registration_body" 'DIRECT_REGISTRATION_PENDING=1')
boundary_line=$(line_of "$registration_body" 'DIRECT_ADD_BOUNDARY_CROSSED=1')
boundary_save_line=$(awk -v start="$boundary_line" 'NR > start && /save_state/ { print NR; exit }' <<<"$registration_body")
add_line=$(line_of "$registration_body" 'direct-volume add --volume-path "$WORKSPACE"')
rollback_line=$(line_of "$registration_body" 'remove_direct_registration direct-volume-add-rollback')
for line in "$precondition_line" "$proof_line" "$proof_save_line" "$detect_line" "$create_line" "$pending_line" "$boundary_line" "$boundary_save_line" "$add_line" "$rollback_line"; do
  [[ $line =~ ^[0-9]+$ ]] || fail 'direct registration ordering evidence is incomplete'
done
(( precondition_line < proof_line && proof_line < proof_save_line && proof_save_line < detect_line && detect_line < create_line &&
   create_line < pending_line && pending_line < boundary_line && boundary_line < boundary_save_line &&
   boundary_save_line < add_line && add_line < rollback_line )) || fail 'direct registration precondition/proof/pending/add/rollback ordering is unsafe'
for needle in '"$WORKSPACE"' '"$metadata_dir"' '"$LOOP_DEVICE"' '"$BACKING_FILE"' '"$LOOP_MAJMIN"'; do
  grep -Fq "$needle" <<<"$precondition_body" || fail "direct registration precondition omits $needle"
done
grep -Fq '[[ -d $DIRECT_ROOT && ! -L $DIRECT_ROOT' <<<"$precondition_body" || fail 'direct registration precondition permits a foreign direct root'
grep -Fq 'direct_owner=$(timeout 10 stat -c %u -- "$DIRECT_ROOT"' <<<"$precondition_body" || fail 'direct registration precondition does not fail closed while checking direct-root ownership'
grep -Fq 'find "$DIRECT_ROOT" -mindepth 1 -maxdepth 1 -print -quit' <<<"$precondition_body" || fail 'direct registration precondition permits a nonempty direct root'
if grep -Eq 'direct-volume (add|remove)' <<<"$precondition_body"; then
  fail 'direct registration precondition mutates direct-volume metadata'
fi
grep -Fq 'DIRECT_ADD_BOUNDARY_CROSSED:-0} == 1' <<<"$removal_body" || fail 'direct-volume removal is not guarded by the persisted add ownership boundary'
for needle in '"$WORKSPACE"' '"$metadata_dir"' '"$LOOP_DEVICE"' '"$BACKING_FILE"' '"$LOOP_MAJMIN"'; do
  grep -Fq "$needle" <<<"$absence_body" || fail "post-removal full metadata scan omits $needle"
done
grep -Fq 'metadata_root_references_value "$DIRECT_METADATA_ROOT" "$needle"' <<<"$absence_body" || fail 'post-removal absence verification does not scan the full direct metadata root'
grep -Fq "die 'post-removal full metadata scan failed'" <<<"$removal_body" || fail 'post-removal full metadata scan failure does not preserve state'
grep -Fq 'direct_registration_absent' <<<"$cleanup_device_body" || fail 'device cleanup does not repeat full direct metadata absence verification'
cleanup_absence_line=$(line_of "$cleanup_device_body" 'direct_registration_absent')
cleanup_detach_line=$(line_of "$cleanup_device_body" 'losetup -d "$LOOP_DEVICE"')
[[ $cleanup_absence_line =~ ^[0-9]+$ && $cleanup_detach_line =~ ^[0-9]+$ && $cleanup_absence_line -lt $cleanup_detach_line ]] ||
  fail 'device cleanup does not verify full direct metadata absence immediately before loop detach'
grep -Fq "die 'post-removal full metadata scan failed'" <<<"$cleanup_device_body" || fail 'cleanup metadata scan failure does not preserve state'
grep -q 'metadata scan failed' "$EXPERIMENT_DIR/device-manager.sh" || fail 'metadata scan errors do not fail closed'
grep -Fq '[[ $scan_rc -eq 1 ]] || die '\''direct registration precondition failed'\''' <<<"$precondition_body" || fail 'direct registration metadata scan errors do not use the fail-closed precondition error'
grep -q 'trace cleanup failed; preserving all device state' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'cleanup mutates devices after trace cleanup failure'
grep -q 'raw resources must be absent before device cleanup' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'device cleanup can precede raw resource deletion'
grep -q 'PV_UID=' "$EXPERIMENT_DIR/create-raw-block-resources.sh" || fail 'raw PV identity is not retained'
grep -q 'PVC_UID=' "$EXPERIMENT_DIR/create-raw-block-resources.sh" || fail 'raw PVC identity is not retained'
grep -q 'pre-existing exact PV' "$EXPERIMENT_DIR/create-raw-block-resources.sh" || fail 'raw creation does not reject an exact-name PV collision'
grep -q 'pre-existing exact PVC' "$EXPERIMENT_DIR/create-raw-block-resources.sh" || fail 'raw creation does not reject an exact-name PVC collision'
grep -q 'create --dry-run=client' "$EXPERIMENT_DIR/create-raw-block-resources.sh" || fail 'raw manifests are not validated with create semantics'
if grep -Eq 'kubectl.* apply|"\$\{K\[@\]\}" apply' "$EXPERIMENT_DIR/create-raw-block-resources.sh"; then
  fail 'raw resources use update-capable apply semantics'
fi
grep -q 'preconditions.*uid\|uid.*preconditions' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'raw resources are not deleted with UID preconditions'
grep -q 'foreign PV references loop device' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'cleanup does not inventory loop-device PV references'
grep -q 'delete_owned_raw_resources' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'raw resource cleanup does not share one fail-closed ownership gate'
grep -q 'kata-direct-volume-ab/run' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'raw resource cleanup does not validate the exact run ownership label'
grep -q 'spec.local.path' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'raw PV cleanup does not validate the exact loop path'
grep -q 'spec.volumeName' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'raw PVC cleanup does not validate the exact PV binding'
grep -q 'RAW_ACTIVE_KEYS' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'raw identities are not retained per device/case'
grep -q 'wait_for_uid_absent' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'raw cleanup does not verify exact UID absence'
grep -q 'NODE_RESOURCE_GROUP' "$EXPERIMENT_DIR/provision.sh" || fail 'provisioning does not retain the AKS node resource group'
grep -q -- '--node-resource-group "$NODE_RESOURCE_GROUP"' "$EXPERIMENT_DIR/provision.sh" || fail 'AKS creation does not use the explicit derived node resource group'
grep -Eq 'NODE_RESOURCE_GROUP=.*DEPLOYMENT_SUFFIX' "$EXPERIMENT_DIR/lib.sh" || fail 'node resource group does not include deployment-derived uniqueness'
grep -q 'NODE_RESOURCE_GROUP_ID' "$EXPERIMENT_DIR/provision.sh" || fail 'provisioning state does not retain the exact node resource group ID'
grep -q 'nodeResourceGroup' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'destruction does not validate the AKS node resource group'
grep -q 'node-resource-group inventory' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'destruction does not fail closed on node resource group inventory'
grep -q 'ALLOWED_NODE_RESOURCE_TYPES' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'node resource group inventory lacks an explicit type allowlist'
grep -q 'unexpected node resource type' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'node resource group inventory does not reject unrecognized types first'
grep -q 'node-resource-topology.json' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'node resource group inventory does not retain topology evidence'
grep -q 'referenced_ids' "$EXPERIMENT_DIR/validate-node-resource-topology.jq" || fail 'node resource group inventory does not require relationship evidence'
grep -q 'resourceGroup != \$rg' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'node resource group inventory does not validate exact resource-group membership'
grep -q 'expected exactly one systempool and one katapool VMSS root' "$EXPERIMENT_DIR/validate-node-resource-topology.jq" || fail 'node resource group inventory does not prove both exact pool roots'
if grep -Eq '^[[:space:]]+microsoft\.compute/snapshots|^[[:space:]]+microsoft\.network/(publicipprefixes|routetables)' "$EXPERIMENT_DIR/destroy-cluster.sh"; then
  fail 'node resource allowlist includes types not requested by the exact provision configuration'
fi
topology_args=(--arg aks aks --arg aks_rg rg --arg kubelet '')
if ! jq -e "${topology_args[@]}" -f "$EXPERIMENT_DIR/validate-node-resource-topology.jq" >/dev/null <<'JSON'
[
  {"id":"/subscriptions/s/resourceGroups/nodes/providers/Microsoft.Compute/virtualMachineScaleSets/system","type":"Microsoft.Compute/virtualMachineScaleSets","tags":{"aks-managed-cluster-name":"aks","aks-managed-cluster-rg":"rg","aks-managed-poolName":"systempool"},"properties":{"virtualMachineProfile":{"networkProfile":{"networkInterfaceConfigurations":[{"properties":{"ipConfigurations":[{"properties":{"subnet":{"id":"/subscriptions/s/resourceGroups/nodes/providers/Microsoft.Network/virtualNetworks/vnet/subnets/nodes"}}}]}}]}}}},
  {"id":"/subscriptions/s/resourceGroups/nodes/providers/Microsoft.Compute/virtualMachineScaleSets/kata","type":"Microsoft.Compute/virtualMachineScaleSets","tags":{"aks-managed-cluster-name":"aks","aks-managed-cluster-rg":"rg","aks-managed-poolName":"katapool"},"properties":{}},
  {"id":"/subscriptions/s/resourceGroups/nodes/providers/Microsoft.Network/virtualNetworks/vnet","type":"Microsoft.Network/virtualNetworks","properties":{}}
]
JSON
then
  fail 'valid exact-pool topology is rejected'
fi
if jq -e "${topology_args[@]}" -f "$EXPERIMENT_DIR/validate-node-resource-topology.jq" >/dev/null 2>&1 <<'JSON'
[
  {"id":"/subscriptions/s/resourceGroups/nodes/providers/Microsoft.Compute/virtualMachineScaleSets/system","type":"Microsoft.Compute/virtualMachineScaleSets","tags":{"aks-managed-cluster-name":"aks","aks-managed-cluster-rg":"rg","aks-managed-poolName":"systempool"},"properties":{}},
  {"id":"/subscriptions/s/resourceGroups/nodes/providers/Microsoft.Compute/virtualMachineScaleSets/kata","type":"Microsoft.Compute/virtualMachineScaleSets","tags":{"aks-managed-cluster-name":"aks","aks-managed-cluster-rg":"rg","aks-managed-poolName":"katapool"},"properties":{}},
  {"id":"/subscriptions/s/resourceGroups/nodes/providers/Microsoft.Network/networkSecurityGroups/foreign","type":"Microsoft.Network/networkSecurityGroups","tags":{"aks-managed-cluster-name":"aks","aks-managed-cluster-rg":"rg","aks-managed-poolName":"systempool"},"properties":{}}
]
JSON
then
  fail 'unreferenced allowed-type node resource is accepted'
fi
grep -q 'managedBy' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'destruction does not validate node resource group managedBy ownership'
grep -q 'az lock list' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'destruction does not reject node resource group locks'
grep -q 'aks-managed-cluster-name' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'node resource group inventory is not tied to the exact AKS cluster'
grep -q 'aks-managed-poolName' "$EXPERIMENT_DIR/destroy-cluster.sh" || fail 'node resource group inventory is not tied to exact node pools'
grep -q 'cleanup_epilogue' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'planned result rows are not finalized from a cleanup epilogue'
grep -q 'CASE_REGISTRATION.*unsupported' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'direct CLI/schema unsupported status is not propagated to case classification'
grep -q 'namespace already exists' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'runner can reuse another run namespace'
grep -q 'NAMESPACE_UID=' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'namespace UID ownership is not retained'
grep -q 'POD_UID' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'pod UID ownership is not retained'
grep -q 'delete_kubernetes_object_with_preconditions' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'pod cleanup lacks UID preconditions'
grep -q 'preserving unique namespace for cluster destruction' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'normal cleanup can cascade-delete unowned namespaced resources'
if grep -Eq 'delete_kubernetes_object_with_preconditions[[:space:]]+namespace|/api/v1/namespaces/\$name"' "$EXPERIMENT_DIR/run-experiment.sh"; then
  fail 'normal cleanup contains a namespace deletion path'
fi
grep -q 'Definition of Done.*destroy-cluster.sh' "$EXPERIMENT_DIR/README.md" || fail 'README does not assign Definition of Done cleanup to cluster destruction'
if grep -q 'require_case_success' "$EXPERIMENT_DIR/run-experiment.sh"; then
  fail 'an expected case failure prevents later discriminator cases'
fi
grep -q 'refusing to overwrite' "$EXPERIMENT_DIR/run-experiment.sh" || fail 'runner does not protect prior evidence'
grep -q 'results/.*UTC' "$EXPERIMENT_DIR/README.md" || fail 'README does not document timestamped evidence'
grep -q 'results/\*' "$EXPERIMENT_DIR/.gitignore" || fail 'generated evidence is not ignored'

printf 'kata-direct-volume-ab contract checks passed\n'
