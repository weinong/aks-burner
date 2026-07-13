#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

SUBSCRIPTION_ID=${SUBSCRIPTION_ID:?}
RUN_ID=${RUN_ID:?}
DEPLOYMENT_ID=${DEPLOYMENT_ID:?}
KUBECONFIG_PATH=${KUBECONFIG_PATH:?}
TARGET_NODE=${TARGET_NODE:?}
EXPECTED_NVME_DEVICE=${EXPECTED_NVME_DEVICE:?}
PROBE_IMAGE=${PROBE_IMAGE:?}
HOST_IMAGE=${HOST_IMAGE:?}
FORMAT_NVME=${FORMAT_NVME:-no}
RUN_EXPERIMENT=${RUN_EXPERIMENT:-}
derive_names
UTC=$(date -u +%Y%m%dT%H%M%SZ)
RUN_LABEL="${RUN_ID}-${DEPLOYMENT_SUFFIX}-${UTC,,}"
RESULT_ROOT=${RESULT_ROOT:-$EXPERIMENT_DIR/results}
RESULT_DIR="$RESULT_ROOT/$UTC"
DIAG_POD="kdva-host-${DEPLOYMENT_SUFFIX}"
K=(kubectl --kubeconfig "$KUBECONFIG_PATH")
ACTIVE_POD=
SAFE_TO_CLEAN=true
DIAG_CREATED=false
NAMESPACE_UID=
NAMESPACE_RESOURCE_VERSION=
DIAG_POD_UID=
DIAG_POD_RESOURCE_VERSION=
IN_CLEANUP=false
TRACE_CLEANUP_FAILED=false
declare -a DEVICES=()
declare -a RAW_ACTIVE_KEYS=()
declare -a WORKLOAD_POD_KEYS=()
declare -A RAW_CLEANUP_BLOCKED=()
declare -A RAW_KEY_ACTIVE=() RAW_DEVICE=() RAW_CASE=() RAW_PV_NAME=() RAW_PV_UID=() RAW_PVC_NAME=() RAW_PVC_UID=() RAW_LOOP_DEVICE=()
declare -A DEVICE_LOOP=()
declare -A POD_KEY_ACTIVE=() POD_NAME=() POD_UID=() POD_RESOURCE_VERSION=() POD_CASE=()
TRACE_CASES=(A B B2 A2 RAW DIRECT)

host_action() {
  local device=$1 action=$2 label case_id marker_name marker_value out rc
  label=${3:-$action}; case_id=${4:-$label}; marker_name=${5:-}; marker_value=${6:-}; out="$RESULT_DIR/${device}-${label}.log"
  if "${K[@]}" exec -i -n "$NAMESPACE" "$DIAG_POD" -- chroot /host /usr/bin/nsenter --target 1 --mount --pid --wd=/ -- /bin/bash -s -- \
    "$action" "$RUN_ID" "$device" "$EXPECTED_NVME_DEVICE" "$FORMAT_NVME" "$case_id" "$marker_name" "$marker_value" \
    <"$EXPERIMENT_DIR/device-manager.sh" >"$out" 2>&1; then
    return 0
  else
    rc=$?
  fi
  grep -q '^EXPERIMENT_FAILURE:' "$out" && return 2
  [[ $IN_CLEANUP == false ]] || return 1
  safety_die "host action $action failed for $device; see $out (status $rc)"
}
stop_case_trace() {
  local device=$1 case_name=$2 rc
  IN_CLEANUP=true
  if host_action "$device" trace-stop "$case_name-trace-stop" "$case_name"; then rc=0; else rc=$?; fi
  IN_CLEANUP=false
  return "$rc"
}
wait_for_workload_completion() {
  local pod=$1 deadline=$((SECONDS + 300)) phase
  while (( SECONDS < deadline )); do
    phase=$("${K[@]}" get pod -n "$NAMESPACE" "$pod" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    case "$phase" in
      Succeeded) return 0 ;;
      Failed) return 1 ;;
    esac
    sleep 2
  done
  return 1
}
state_value() { sed -n "s/^$2=//p" "$RESULT_DIR/$1-$3.log" | tail -n1; }

persist_run_kubernetes_ownership() {
  local file="$RESULT_DIR/kubernetes-ownership.env" tmp key
  tmp="$file.tmp.$$"
  {
    printf 'NAMESPACE=%q\nNAMESPACE_UID=%q\nNAMESPACE_RESOURCE_VERSION=%q\n' "$NAMESPACE" "$NAMESPACE_UID" "$NAMESPACE_RESOURCE_VERSION"
    printf 'DIAG_POD=%q\nDIAG_POD_UID=%q\nDIAG_POD_RESOURCE_VERSION=%q\n' "$DIAG_POD" "$DIAG_POD_UID" "$DIAG_POD_RESOURCE_VERSION"
    for key in "${WORKLOAD_POD_KEYS[@]}"; do
      printf 'WORKLOAD_POD_OWNERSHIP=%q\n' "${POD_CASE[$key]}:${POD_NAME[$key]}:${POD_UID[$key]}:${POD_RESOURCE_VERSION[$key]}"
    done
  } >"$tmp"
  chmod 0600 "$tmp"; mv -f "$tmp" "$file"
}

kubernetes_object_json() {
  local kind=$1 name=$2 namespace=${3:-} namespace_args=()
  [[ -z $namespace ]] || namespace_args=(-n "$namespace")
  "${K[@]}" get "$kind" "${namespace_args[@]}" "$name" --ignore-not-found -o json
}

wait_for_kubernetes_uid_absent() {
  local kind=$1 name=$2 namespace=$3 uid=$4 json current_uid
  [[ -n $uid ]] || return 1
  for _ in {1..120}; do
    json=$(kubernetes_object_json "$kind" "$name" "$namespace") || return 1
    [[ -n $json ]] || return 0
    current_uid=$(jq -r '.metadata.uid // empty' <<<"$json")
    if [[ $current_uid != "$uid" ]]; then
      printf 'STOP_REASON: exact %s %s was replaced by foreign UID %s; refusing replacement cleanup.\n' "$kind" "$name" "$current_uid" >>"$RESULT_DIR/cleanup-blocked.txt"
      return 1
    fi
    sleep 1
  done
  return 1
}

delete_kubernetes_object_with_preconditions() {
  local kind=$1 name=$2 namespace=$3 uid=$4 expected_app=$5 expected_case=${6:-}
  local json current_resource_version body path
  [[ $kind == pod && -n $namespace ]] || return 1
  json=$(kubernetes_object_json "$kind" "$name" "$namespace") || return 1
  if [[ -z $json ]]; then
    wait_for_kubernetes_uid_absent "$kind" "$name" "$namespace" "$uid"
    return
  fi
  jq -e --arg uid "$uid" --arg run "$RUN_LABEL" --arg app "$expected_app" --arg case_id "$expected_case" \
    '.metadata.uid == $uid and .metadata.labels["kata-direct-volume-ab/run"] == $run and
     .metadata.labels["app.kubernetes.io/name"] == $app and
     ($case_id == "" or .metadata.labels["kata-direct-volume-ab/case"] == $case_id)' <<<"$json" >/dev/null || return 1
  path="/api/v1/namespaces/$namespace/pods/$name"
  current_resource_version=$(jq -r '.metadata.resourceVersion // empty' <<<"$json")
  [[ -n $current_resource_version ]] || return 1
  body=$(jq -cn --arg uid "$uid" --arg rv "$current_resource_version" \
    '{apiVersion:"v1",kind:"DeleteOptions",preconditions:{uid:$uid,resourceVersion:$rv},propagationPolicy:"Foreground"}')
  "${K[@]}" delete --raw "$path" -f - <<<"$body" >/dev/null || return 1
  wait_for_kubernetes_uid_absent "$kind" "$name" "$namespace" "$uid"
}

register_workload_pod_identity() {
  local case_name=$1 expected_name=$2 state_file=$3 key=$1
  local WORKLOAD_CASE_NAME= WORKLOAD_POD_NAME= WORKLOAD_POD_UID= WORKLOAD_POD_RESOURCE_VERSION= json
  if [[ ! -s $state_file ]]; then
    json=$(kubernetes_object_json pod "$expected_name" "$NAMESPACE") || return 1
    [[ -z $json ]] || return 1
    return 0
  fi
  # Workload helpers emit only shell-escaped assignments into this run-owned 0700 result directory.
  source "$state_file"
  [[ $WORKLOAD_CASE_NAME == "$case_name" && $WORKLOAD_POD_NAME == "$expected_name" ]] || return 1
  [[ $WORKLOAD_POD_UID =~ ^[0-9a-f-]{16,64}$ && -n $WORKLOAD_POD_RESOURCE_VERSION ]] || return 1
  [[ -z ${POD_KEY_ACTIVE[$key]:-} ]] || return 1
  WORKLOAD_POD_KEYS+=("$key")
  POD_KEY_ACTIVE[$key]=true
  POD_CASE[$key]=$case_name
  POD_NAME[$key]=$WORKLOAD_POD_NAME
  POD_UID[$key]=$WORKLOAD_POD_UID
  POD_RESOURCE_VERSION[$key]=$WORKLOAD_POD_RESOURCE_VERSION
  persist_run_kubernetes_ownership
}

delete_owned_workload_pod() {
  local key=$1 expected_name=$2 json
  if [[ ${POD_KEY_ACTIVE[$key]:-false} != true ]]; then
    json=$(kubernetes_object_json pod "$expected_name" "$NAMESPACE") || return 1
    [[ -z $json ]]
    return
  fi
  delete_kubernetes_object_with_preconditions pod "${POD_NAME[$key]}" "$NAMESPACE" "${POD_UID[$key]}" "$PURPOSE" "${POD_CASE[$key]}" || return 1
  POD_KEY_ACTIVE[$key]=false
}
register_raw_identity() {
  local device=$1 case_name=$2 state_file=$3 key="$1:$2"
  local PV_NAME= PVC_NAME= PV_UID= PVC_UID= LOOP_DEVICE=
  # The create helper emits only shell-escaped assignments into this run-owned 0700 result directory.
  source "$state_file"
  [[ $PV_NAME == "kdva-${RUN_LABEL}-${device}" && $PVC_NAME == "$PV_NAME" ]] || safety_die 'created raw resource names differ from the exact case identity'
  [[ $PV_UID =~ ^[0-9a-f-]{16,64}$ && $PVC_UID =~ ^[0-9a-f-]{16,64}$ ]] || safety_die 'created raw resource UID is invalid'
  [[ $LOOP_DEVICE == "${DEVICE_LOOP[$device]}" ]] || safety_die 'created raw resource loop identity is invalid'
  [[ -z ${RAW_KEY_ACTIVE[$key]:-} ]] || safety_die 'raw identity was already active for this device/case'
  RAW_ACTIVE_KEYS+=("$key")
  RAW_KEY_ACTIVE[$key]=true; RAW_DEVICE[$key]=$device; RAW_CASE[$key]=$case_name
  RAW_PV_NAME[$key]=$PV_NAME; RAW_PV_UID[$key]=$PV_UID; RAW_PVC_NAME[$key]=$PVC_NAME; RAW_PVC_UID[$key]=$PVC_UID; RAW_LOOP_DEVICE[$key]=$LOOP_DEVICE
}

register_partial_raw_identity() {
  local device=$1 case_name=$2 state_file=$3 key="$1:$2"
  local PV_NAME= PVC_NAME= PV_UID= PVC_UID= LOOP_DEVICE=
  [[ -s $state_file ]] || return 0
  source "$state_file"
  [[ -n $PV_UID || -n $PVC_UID ]] || return 0
  [[ $PV_NAME == "kdva-${RUN_LABEL}-${device}" && $PVC_NAME == "$PV_NAME" && $LOOP_DEVICE == "${DEVICE_LOOP[$device]}" ]] || return 1
  [[ -z $PV_UID || $PV_UID =~ ^[0-9a-f-]{16,64}$ ]] || return 1
  [[ -z $PVC_UID || $PVC_UID =~ ^[0-9a-f-]{16,64}$ ]] || return 1
  RAW_ACTIVE_KEYS+=("$key")
  RAW_KEY_ACTIVE[$key]=true; RAW_DEVICE[$key]=$device; RAW_CASE[$key]=$case_name
  RAW_PV_NAME[$key]=$PV_NAME; RAW_PV_UID[$key]=$PV_UID; RAW_PVC_NAME[$key]=$PVC_NAME; RAW_PVC_UID[$key]=$PVC_UID; RAW_LOOP_DEVICE[$key]=$LOOP_DEVICE
}

raw_resource_json() {
  local kind=$1 name=$2 namespace_args=()
  [[ $kind != pvc ]] || namespace_args=(-n "$NAMESPACE")
  "${K[@]}" get "$kind" "${namespace_args[@]}" "$name" --ignore-not-found -o json
}

raw_identity_is_owned() {
  local key=$1 kind=$2 json=$3
  if [[ $kind == pv ]]; then
    [[ -n ${RAW_PV_UID[$key]} ]] || return 1
    jq -e --arg uid "${RAW_PV_UID[$key]}" --arg run "$RUN_LABEL" --arg purpose "$PURPOSE" --arg path "${RAW_LOOP_DEVICE[$key]}" \
      '.metadata.uid == $uid and .metadata.labels["kata-direct-volume-ab/run"] == $run and
       .metadata.labels["app.kubernetes.io/name"] == $purpose and .spec.local.path == $path' <<<"$json" >/dev/null
  else
    [[ -n ${RAW_PVC_UID[$key]} ]] || return 1
    jq -e --arg uid "${RAW_PVC_UID[$key]}" --arg run "$RUN_LABEL" --arg purpose "$PURPOSE" --arg pv "${RAW_PV_NAME[$key]}" \
      '.metadata.uid == $uid and .metadata.labels["kata-direct-volume-ab/run"] == $run and
       .metadata.labels["app.kubernetes.io/name"] == $purpose and .spec.volumeName == $pv' <<<"$json" >/dev/null
  fi
}

delete_with_uid_precondition() {
  local path=$1 uid=$2 resource_version=$3 body
  body=$(jq -cn --arg uid "$uid" --arg rv "$resource_version" \
    '{apiVersion:"v1",kind:"DeleteOptions",preconditions:{uid:$uid,resourceVersion:$rv},propagationPolicy:"Foreground"}')
  "${K[@]}" delete --raw "$path" -f - <<<"$body" >/dev/null
}

wait_for_uid_absent() {
  local kind=$1 name=$2 uid=$3 json current_uid namespace_args=()
  [[ $kind != pvc ]] || namespace_args=(-n "$NAMESPACE")
  [[ -n $uid ]] || return 0
  for _ in {1..120}; do
    json=$("${K[@]}" get "$kind" "${namespace_args[@]}" "$name" --ignore-not-found -o json) || return 1
    [[ -n $json ]] || return 0
    current_uid=$(jq -r '.metadata.uid // empty' <<<"$json")
    if [[ $current_uid != "$uid" ]]; then
      printf 'STOP_REASON: exact %s %s was replaced by foreign UID %s; preserving device state.\n' "$kind" "$name" "$current_uid" >>"$RESULT_DIR/cleanup-blocked.txt"
      return 1
    fi
    sleep 1
  done
  return 1
}

inventory_foreign_loop_pvs() {
  local device=$1 loop=$2 inventory foreign key allowed_json allowed_uids=()
  inventory=$("${K[@]}" get pv -o json) || return 1
  for key in "${RAW_ACTIVE_KEYS[@]}"; do
    [[ ${RAW_DEVICE[$key]:-} == "$device" && ${RAW_KEY_ACTIVE[$key]:-false} == true && -n ${RAW_PV_UID[$key]:-} ]] || continue
    allowed_uids+=("${RAW_PV_UID[$key]}")
  done
  if (( ${#allowed_uids[@]} )); then
    allowed_json=$(printf '%s\n' "${allowed_uids[@]}" | jq -Rsc 'split("\n") | map(select(length > 0))')
  else
    allowed_json='[]'
  fi
  foreign=$(jq -r --arg path "$loop" --argjson allowed "$allowed_json" \
    '.items[] | select(.spec.local.path == $path and (.metadata.uid as $uid | $allowed | index($uid) | not)) | [.metadata.name,.metadata.uid] | @tsv' <<<"$inventory") || return 1
  if [[ -n $foreign ]]; then
    printf 'STOP_REASON: foreign PV references loop device %s for device %s: %s\n' "$loop" "$device" "$foreign" >>"$RESULT_DIR/cleanup-blocked.txt"
    return 1
  fi
}

delete_raw_identity() {
  local key=$1 device pv_json pvc_json resource_version
  device=${RAW_DEVICE[$key]}
  pvc_json=$(raw_resource_json pvc "${RAW_PVC_NAME[$key]}") || return 1
  if [[ -n $pvc_json ]]; then
    raw_identity_is_owned "$key" pvc "$pvc_json" || return 1
  elif [[ -z ${RAW_PVC_UID[$key]} ]]; then
    :
  fi
  inventory_foreign_loop_pvs "$device" "${RAW_LOOP_DEVICE[$key]}" || return 1
  if [[ -n $pvc_json ]]; then
    resource_version=$(jq -r '.metadata.resourceVersion // empty' <<<"$pvc_json"); [[ -n $resource_version ]] || return 1
    delete_with_uid_precondition "/api/v1/namespaces/$NAMESPACE/persistentvolumeclaims/${RAW_PVC_NAME[$key]}" "${RAW_PVC_UID[$key]}" "$resource_version" || return 1
  fi
  wait_for_uid_absent pvc "${RAW_PVC_NAME[$key]}" "${RAW_PVC_UID[$key]}" || return 1
  pv_json=$(raw_resource_json pv "${RAW_PV_NAME[$key]}") || return 1
  if [[ -n $pv_json ]]; then
    raw_identity_is_owned "$key" pv "$pv_json" || return 1
  elif [[ -z ${RAW_PV_UID[$key]} ]]; then
    :
  fi
  inventory_foreign_loop_pvs "$device" "${RAW_LOOP_DEVICE[$key]}" || return 1
  if [[ -n $pv_json ]]; then
    resource_version=$(jq -r '.metadata.resourceVersion // empty' <<<"$pv_json"); [[ -n $resource_version ]] || return 1
    delete_with_uid_precondition "/api/v1/persistentvolumes/${RAW_PV_NAME[$key]}" "${RAW_PV_UID[$key]}" "$resource_version" || return 1
  fi
  wait_for_uid_absent pv "${RAW_PV_NAME[$key]}" "${RAW_PV_UID[$key]}" || return 1
  inventory_foreign_loop_pvs "$device" "${RAW_LOOP_DEVICE[$key]}" || return 1
  RAW_KEY_ACTIVE[$key]=false
}

delete_owned_raw_resources() {
  local device=$1 requested_case=${2:-} key failed=false found=false
  for key in "${RAW_ACTIVE_KEYS[@]}"; do
    [[ ${RAW_DEVICE[$key]:-} == "$device" && ${RAW_KEY_ACTIVE[$key]:-false} == true ]] || continue
    [[ -z $requested_case || ${RAW_CASE[$key]} == "$requested_case" ]] || continue
    found=true
    if ! delete_raw_identity "$key"; then failed=true; RAW_CLEANUP_BLOCKED[$device]=true; fi
  done
  [[ $failed == false ]] || {
    printf 'STOP_REASON: raw resources must be absent before device cleanup; exact UID ownership validation or deletion failed for device %s.\n' \
      "$device" >>"$RESULT_DIR/cleanup-blocked.txt"
    return 1
  }
  [[ -z $requested_case || $found == true ]] || return 1
  inventory_foreign_loop_pvs "$device" "${DEVICE_LOOP[$device]}" || { RAW_CLEANUP_BLOCKED[$device]=true; return 1; }
}
cleanup_epilogue() {
  local rc=$1
  if [[ -f $RESULT_DIR/result-matrix.tsv ]]; then
    awk -F '\t' 'BEGIN {OFS="\t"} NR == 1 {print; next} $15 == "planned" {$7="skipped"; $8="skipped"; $9="skipped"; $10="skipped"; $11="skipped"; $12="skipped"; $13="skipped"; $14="stopped"; $15="stopped"} {print}' \
      "$RESULT_DIR/result-matrix.tsv" >"$RESULT_DIR/result-matrix.tsv.tmp" && mv "$RESULT_DIR/result-matrix.tsv.tmp" "$RESULT_DIR/result-matrix.tsv"
  fi
  printf 'results=%s status=%s\n' "$RESULT_DIR" "$rc"
  exit "$rc"
}
cleanup() {
  local rc=$? device pod pod_json remaining_pods index case_id key device_safe detach_failed=false raw_cleanup_failed=false trace_cleanup_failed=false
  trap - EXIT INT TERM; set +e
  IN_CLEANUP=true
  printf '%s\n' 'preserving unique namespace for cluster destruction' >>"$RESULT_DIR/cleanup.log"
  printf '%s\n' 'preserving unique namespace for cluster destruction' >&2
  if [[ $TRACE_CLEANUP_FAILED == true ]]; then
    printf '%s\n' 'trace cleanup failed; preserving all device state' >>"$RESULT_DIR/cleanup-blocked.txt"
    printf '%s\n' 'trace cleanup failed; preserving all device state' >&2
    cleanup_epilogue 1
  fi
  # Trace processes and tracefs instances must be gone before any workload or device state is changed.
  for device in "${DEVICES[@]}"; do
    for case_id in "${TRACE_CASES[@]}"; do
      host_action "$device" trace-stop "cleanup-$case_id-trace-stop" "$case_id" || { trace_cleanup_failed=true; SAFE_TO_CLEAN=false; rc=1; }
    done
  done
  if [[ $trace_cleanup_failed == true ]]; then
    printf '%s\n' 'trace cleanup failed; preserving all device state' >>"$RESULT_DIR/cleanup-blocked.txt"
    printf '%s\n' 'trace cleanup failed; preserving all device state' >&2
    cleanup_epilogue 1
  fi
  # The next phase is non-destructive to storage: delete workload pods, then prove every run device detached.
  for key in "${WORKLOAD_POD_KEYS[@]}"; do
    [[ ${POD_KEY_ACTIVE[$key]:-false} == true ]] || continue
    delete_owned_workload_pod "$key" "${POD_NAME[$key]}" || SAFE_TO_CLEAN=false
  done
  pod_json=$("${K[@]}" get pods -n "$NAMESPACE" -l "kata-direct-volume-ab/run=$RUN_LABEL" -o json) || SAFE_TO_CLEAN=false
  if [[ -n ${pod_json:-} ]]; then
    remaining_pods=$(jq -r --arg diag "$DIAG_POD" '[.items[] | select(.metadata.name != $diag)] | length' <<<"$pod_json") || SAFE_TO_CLEAN=false
    [[ ${remaining_pods:-1} -eq 0 ]] || SAFE_TO_CLEAN=false
  fi
  if [[ $SAFE_TO_CLEAN != true ]]; then
    rc=1
    printf 'STOP_REASON: workload pods must be absent before device cleanup; deletion or absence verification failed. Preserving ALL device state.\n' \
      >>"$RESULT_DIR/cleanup-blocked.txt"
  fi
  for device in "${DEVICES[@]}"; do
    if ! host_action "$device" wait-detached cleanup-detach; then
      detach_failed=true; SAFE_TO_CLEAN=false; rc=1
      printf 'STOP_REASON: all devices must detach before cleanup; device %s detach is unproven. Preserving ALL PV/PVC/direct metadata/loop/backing/config/owner/diag/namespace.\n' \
        "$device" >>"$RESULT_DIR/cleanup-blocked.txt"
    fi
  done
  if [[ $detach_failed == true || $SAFE_TO_CLEAN != true ]]; then
    rc=1
    printf 'Cleanup stopped before shared config restoration or device cleanup.\n' >>"$RESULT_DIR/cleanup-blocked.txt"
    cleanup_epilogue "$rc"
  fi
  # Raw resources must be absent before device cleanup mutates raw bytes, metadata, loops, backing files, or shared configuration.
  for device in "${DEVICES[@]}"; do
    if [[ ${RAW_CLEANUP_BLOCKED[$device]:-false} == true ]] || ! delete_owned_raw_resources "$device"; then
      raw_cleanup_failed=true; SAFE_TO_CLEAN=false; rc=1
      printf 'STOP_REASON: raw resources must be absent before device cleanup; PVC/PV cleanup failed for device %s. Preserving ALL device state.\n' \
        "$device" >>"$RESULT_DIR/cleanup-blocked.txt"
    fi
  done
  if [[ $raw_cleanup_failed == true ]]; then cleanup_epilogue "$rc"; fi
  for ((index=${#DEVICES[@]}-1; index>=0; index--)); do
    device=${DEVICES[$index]}
    device_safe=true
    if [[ $device == isolated-raw ]]; then host_action "$device" restore-raw cleanup-restore-raw || { SAFE_TO_CLEAN=false; device_safe=false; rc=1; }; fi
    if [[ $device_safe == true ]]; then host_action "$device" unregister-direct cleanup-unregister || { SAFE_TO_CLEAN=false; device_safe=false; rc=1; }; fi
    if [[ $device_safe == true ]]; then host_action "$device" cleanup cleanup-device || { SAFE_TO_CLEAN=false; device_safe=false; rc=1; }; fi
    if [[ $device_safe != true ]]; then
      printf 'Device %s restoration/metadata cleanup failed; preserving its remaining owned state.\n' "$device" >>"$RESULT_DIR/cleanup-blocked.txt"
    fi
  done
  if [[ $SAFE_TO_CLEAN == true && $DIAG_CREATED == true ]]; then
    delete_kubernetes_object_with_preconditions pod "$DIAG_POD" "$NAMESPACE" "$DIAG_POD_UID" kata-direct-volume-ab-host || { SAFE_TO_CLEAN=false; rc=1; }
  fi
  if [[ $SAFE_TO_CLEAN != true ]]; then rc=1; printf 'Cleanup incomplete; preserving diagnostic pod and namespace for inspection.\n' >>"$RESULT_DIR/cleanup-blocked.txt"; fi
  cleanup_epilogue "$rc"
}

copy_host_device_artifacts() {
  local device=$1 case_name=$2 target file_path tmp
  target="$RESULT_DIR/$case_name/host-artifacts/$device"
  mkdir -p "$target"
  "${K[@]}" exec -n "$NAMESPACE" "$DIAG_POD" -- tar -C "/host/var/log/kata-direct-volume-ab/$RUN_ID/devices/$device" -cf - . | tar -C "$target" -xf -
  while IFS= read -r -d '' file_path; do
    if file --brief --mime "$file_path" | grep -q '^text/'; then
      tmp="$file_path.redacted"; redact <"$file_path" >"$tmp"; mv -f "$tmp" "$file_path"
    fi
  done < <(find "$target" -type f -print0)
}

derive_trace_status() {
  local case_name=$1 device=$2 trace_dir
  trace_dir="$RESULT_DIR/$case_name/host-artifacts/$device/traces/$case_name"
  local syscall_status syscall_reason block_status block_reason
  if [[ ! -s $trace_dir/syscall-status.tsv || ! -s $trace_dir/block-status.tsv ]]; then
    TRACE_STATUS=insufficient; TRACE_REASON=missing-case-scoped-trace-status; return
  fi
  syscall_status=$(awk -F '\t' '$1=="unsupported" {unsupported=1} $1!="sufficient" && $1!="unsupported" {insufficient=1} END {print unsupported ? "unsupported" : (insufficient ? "insufficient" : "sufficient")}' "$trace_dir/syscall-status.tsv")
  block_status=$(awk -F '\t' '$1=="unsupported" {unsupported=1} $1!="sufficient" && $1!="unsupported" {insufficient=1} END {print unsupported ? "unsupported" : (insufficient ? "insufficient" : "sufficient")}' "$trace_dir/block-status.tsv")
  syscall_reason=$(awk -F '\t' '{print $2}' "$trace_dir/syscall-status.tsv" | paste -sd, -)
  block_reason=$(awk -F '\t' '{print $2}' "$trace_dir/block-status.tsv" | paste -sd, -)
  TRACE_REASON="syscall=${syscall_reason:-missing};block=${block_reason:-missing}"
  if [[ $syscall_status == sufficient && $block_status == sufficient ]]; then TRACE_STATUS=collected
  elif [[ $syscall_status == unsupported || $block_status == unsupported ]]; then TRACE_STATUS=unsupported
  else TRACE_STATUS=insufficient
  fi
}

derive_evidence_status() {
  local case_name=$1 device=$2 started=$3 case_dir host_dir runtime_dir missing=() artifact
  case_dir="$RESULT_DIR/$case_name"; host_dir="$case_dir/host-artifacts/$device"; runtime_dir="$host_dir/runtime-$case_name"
  [[ -s $case_dir/rendered-pod.yaml ]] || missing+=(rendered-pod)
  if [[ -s $case_dir/pod-never-created.tsv ]]; then
    :
  else
    for artifact in pod.yaml describe.txt events.txt guest.log; do [[ -s $case_dir/$artifact ]] || missing+=("$artifact"); done
    [[ $started == false ]] || {
      [[ -s $runtime_dir/lsblk.txt && -s $runtime_dir/device-fds.tsv && -s $runtime_dir/runtime-metadata-index.tsv ]] || missing+=(case-runtime-capture)
      grep -q $'^sufficient\tmetadata-scan-complete$' "$runtime_dir/runtime-metadata-status.tsv" 2>/dev/null || missing+=(runtime-metadata-scan)
      grep -q $'^sufficient\t' "$runtime_dir/ch-classification.tsv" 2>/dev/null || missing+=(ch-classification)
      [[ -s $case_dir/guest.log ]] || missing+=(guest-state)
    }
  fi
  [[ -s $host_dir/state.env ]] || missing+=(host-artifacts)
  if (( ${#missing[@]} == 0 )); then
    CASE_EVIDENCE=complete; EVIDENCE_REASON=required-case-artifacts-present
  else
    CASE_EVIDENCE=insufficient; EVIDENCE_REASON=$(IFS=,; printf 'missing:%s' "${missing[*]}")
  fi
}

create_diag() {
  local response="$RESULT_DIR/host-diagnostic-created.json"
  if "${K[@]}" get pod -n "$NAMESPACE" "$DIAG_POD" >/dev/null 2>&1; then safety_die 'diagnostic pod already exists'; fi
  cat >"$RESULT_DIR/host-diagnostic.yaml" <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $DIAG_POD
  namespace: $NAMESPACE
  labels: {app.kubernetes.io/name: kata-direct-volume-ab-host, kata-direct-volume-ab/run: $RUN_LABEL}
spec:
  nodeName: $TARGET_NODE
  hostPID: true
  restartPolicy: Never
  tolerations: [{key: dedicated, operator: Equal, value: kata-direct-volume-ab, effect: NoSchedule}]
  containers:
    - name: host
      image: $HOST_IMAGE
      command: [/bin/sh, -c, "trap : TERM INT; sleep infinity & wait"]
      securityContext: {privileged: true}
      volumeMounts: [{name: host-root, mountPath: /host, mountPropagation: Bidirectional}]
  volumes: [{name: host-root, hostPath: {path: /, type: Directory}}]
EOF
  "${K[@]}" create --dry-run=client -f "$RESULT_DIR/host-diagnostic.yaml" >/dev/null
  if ! "${K[@]}" create -f "$RESULT_DIR/host-diagnostic.yaml" -o json >"$response"; then
    if [[ -n $(kubernetes_object_json pod "$DIAG_POD" "$NAMESPACE") ]]; then
      SAFE_TO_CLEAN=false
      safety_die 'diagnostic pod create failed before UID capture; refusing to delete unknown replacement'
    fi
    return 1
  fi
  DIAG_POD_UID=$(jq -r '.metadata.uid // empty' "$response")
  DIAG_POD_RESOURCE_VERSION=$(jq -r '.metadata.resourceVersion // empty' "$response")
  if [[ ! $DIAG_POD_UID =~ ^[0-9a-f-]{16,64}$ || -z $DIAG_POD_RESOURCE_VERSION ]]; then
    SAFE_TO_CLEAN=false
    safety_die 'created diagnostic pod identity is absent'
  fi
  DIAG_CREATED=true; persist_run_kubernetes_ownership
  "${K[@]}" wait -n "$NAMESPACE" --for=condition=Ready "pod/$DIAG_POD" --timeout=180s
}

assert_dedicated_node() {
  local taints occupants
  taints=$("${K[@]}" get node "$TARGET_NODE" -o jsonpath='{range .spec.taints[*]}{.key}={.value}:{.effect}{"\n"}{end}')
  grep -qx 'dedicated=kata-direct-volume-ab:NoSchedule' <<<"$taints" || safety_die 'target node lacks exact dedicated taint'
  [[ $("${K[@]}" get node "$TARGET_NODE" -o jsonpath='{.metadata.labels.kubernetes\.azure\.com/agentpool}') == katapool ]] || safety_die 'target node is not katapool'
  occupants=$("${K[@]}" get pods -A --field-selector "spec.nodeName=$TARGET_NODE" -o go-template='{{range .items}}{{.metadata.namespace}}{{"\t"}}{{.metadata.name}}{{"\t"}}{{range .metadata.ownerReferences}}{{.kind}}{{end}}{{"\n"}}{{end}}' | awk -v diag="$DIAG_POD" '$2 != diag && $3 != "DaemonSet" && !($1=="kube-system" && $2 ~ /^stale-pod-cleanup-/)')
  [[ -z $occupants ]] || safety_die 'target node has non-DaemonSet workloads'
}

prepare_device() {
  local device=$1
  DEVICES+=("$device")
  host_action "$device" prepare
  DEVICE_LOOP[$device]=$(state_value "$device" LOOP_DEVICE prepare)
  [[ ${DEVICE_LOOP[$device]} =~ ^/dev/loop[0-9]+$ ]] || die 'cannot retain prepared loop identity'
  host_action "$device" format
  host_action "$device" verify
  host_action "$device" enable-config "$device-enable-config"
}

run_raw() {
  local device=$1 case_name=$2 mode=$3 trace=${4:-no} expected_marker=${5:-none} own_marker=${6:?} marker_value
  marker_value="${RUN_ID}:${device}:${own_marker}"
  local loop pv="kdva-${RUN_LABEL}-${device}" pvc="kdva-${RUN_LABEL}-${device}" pod="kdva-${RUN_LABEL}-${case_name,,}" rc=0 trace_status=not-requested trace_reason=not-requested cleanup_status=complete host_status=skipped started=false runtime_collected=false
  [[ $trace != yes ]] || trace_status=insufficient
  loop=$(state_value "$device" LOOP_DEVICE prepare); [[ $loop =~ ^/dev/loop[0-9]+$ ]] || die 'cannot resolve loop device'
  if LOOP_DEVICE=$loop DEVICE_ID=$device RUN_LABEL=$RUN_LABEL TARGET_NODE=$TARGET_NODE RESULT_DIR=$RESULT_DIR NAMESPACE=$NAMESPACE KUBECONFIG_PATH=$KUBECONFIG_PATH \
    "$EXPERIMENT_DIR/create-raw-block-resources.sh" >"$RESULT_DIR/$case_name-storage.env"; then
    register_raw_identity "$device" "$case_name" "$RESULT_DIR/$case_name-storage.env"
    pv=${RAW_PV_NAME["$device:$case_name"]}; pvc=${RAW_PVC_NAME["$device:$case_name"]}
    host_action "$device" begin-guest "$case_name-begin"; started=true
  else
    rc=$?
    register_partial_raw_identity "$device" "$case_name" "$RESULT_DIR/$case_name-storage.env" || { RAW_CLEANUP_BLOCKED[$device]=true; SAFE_TO_CLEAN=false; }
  fi
  if [[ $rc -eq 0 ]] && DEVICE_ID=$device CASE_NAME=$case_name TEST_MODE=$mode PVC_NAME=$pvc OWN_MARKER=$own_marker MARKER_VALUE=$marker_value EXPECTED_MARKER=$expected_marker \
    MARKER_OFFSET_ONE=$(state_value "$device" MARKER_OFFSET_ONE format) \
    MARKER_OFFSET_TWO=$(state_value "$device" MARKER_OFFSET_TWO format) \
    RAW_OFFSET_ONE=$(state_value "$device" RAW_OFFSET_ONE format) \
    RAW_OFFSET_TWO=$(state_value "$device" RAW_OFFSET_TWO format) RUN_LABEL=$RUN_LABEL TARGET_NODE=$TARGET_NODE RESULT_DIR=$RESULT_DIR \
    NAMESPACE=$NAMESPACE KUBECONFIG_PATH=$KUBECONFIG_PATH PROBE_IMAGE=$PROBE_IMAGE "$EXPERIMENT_DIR/run-raw-block-test.sh" >"$RESULT_DIR/$case_name-pod.env"; then
    if host_action "$device" collect "$case_name-runtime-ready" "$case_name"; then
      runtime_collected=true
      [[ $trace != yes ]] || { host_action "$device" trace-start "$case_name-trace-start" "$case_name" && trace_status=started || trace_status=insufficient; }
      "${K[@]}" exec -n "$NAMESPACE" "$pod" -- touch /tmp/release-test || rc=$?
      [[ $rc -ne 0 ]] || wait_for_workload_completion "$pod" || rc=$?
    else rc=$?; fi
  else
    [[ $rc -ne 0 ]] || rc=$?
  fi
  register_workload_pod_identity "$case_name" "$pod" "$RESULT_DIR/$case_name/pod-ownership.env" || { SAFE_TO_CLEAN=false; cleanup_status=failed; rc=1; }
  mkdir -p "$RESULT_DIR/$case_name"
  if [[ $started == true && $runtime_collected == false ]]; then host_action "$device" collect "$case_name-runtime-failure" "$case_name" || true; fi
  OUTPUT_DIR="$RESULT_DIR/$case_name" POD_NAME=$pod NAMESPACE=$NAMESPACE KUBECONFIG_PATH=$KUBECONFIG_PATH "$EXPERIMENT_DIR/collect-guest-state.sh" || true
  if [[ $trace == yes ]] && ! stop_case_trace "$device" "$case_name"; then
    TRACE_CLEANUP_FAILED=true; SAFE_TO_CLEAN=false; cleanup_status=failed; rc=1
  fi
  if [[ $SAFE_TO_CLEAN != true ]]; then
    copy_host_device_artifacts "$device" "$case_name" || true
    derive_evidence_status "$case_name" "$device" "$started"
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$case_name" "$CASE_EVIDENCE" "$EVIDENCE_REASON" "$trace_status" "$trace_reason" "$cleanup_status" >"$RESULT_DIR/$case_name/evidence-status.tsv"
    CASE_TRACE=$trace_status CASE_CLEANUP=$cleanup_status CASE_HOST=$host_status CASE_REGISTRATION=not-applicable
    return "$rc"
  fi
  delete_owned_workload_pod "$case_name" "$pod" || { SAFE_TO_CLEAN=false; cleanup_status=failed; }
  if [[ $started == true ]]; then host_action "$device" end-guest "$case_name-detach" || { SAFE_TO_CLEAN=false; cleanup_status=failed; }; fi
  copy_host_device_artifacts "$device" "$case_name" || true
  if [[ $trace == yes ]]; then derive_trace_status "$case_name" "$device"; trace_status=$TRACE_STATUS; trace_reason=$TRACE_REASON; fi
  if [[ $SAFE_TO_CLEAN == true ]] && ! delete_owned_raw_resources "$device" "$case_name"; then
    SAFE_TO_CLEAN=false; cleanup_status=failed; rc=1
    printf 'STOP_REASON: raw resources must be absent before device cleanup; preserving device %s state.\n' "$device" >>"$RESULT_DIR/cleanup-blocked.txt"
  fi
  if [[ $SAFE_TO_CLEAN == true && $mode == raw-diagnostic ]]; then host_action "$device" restore-raw "$case_name-restore" || { SAFE_TO_CLEAN=false; cleanup_status=failed; }; fi
  if [[ $mode == filesystem && $started == true ]]; then
    if host_action "$device" verify-marker "$case_name-host-marker" "$case_name" "$own_marker" "$marker_value"; then host_status=passed; else [[ $rc -ne 0 ]] || rc=$?; host_status=failed; fi
  fi
  derive_evidence_status "$case_name" "$device" "$started"
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$case_name" "$CASE_EVIDENCE" "$EVIDENCE_REASON" "$trace_status" "$trace_reason" "$cleanup_status" >"$RESULT_DIR/$case_name/evidence-status.tsv"
  CASE_TRACE=$trace_status CASE_CLEANUP=$cleanup_status CASE_HOST=$host_status CASE_REGISTRATION=not-applicable
  return "$rc"
}

run_direct() {
  local device=$1 case_name=$2 trace=${3:-no} expected_marker=${4:-none} own_marker=${5:?} marker_value
  marker_value="${RUN_ID}:${device}:${own_marker}"
  local workspace= pod="kdva-${RUN_LABEL}-${case_name,,}" rc=0 trace_status=not-requested trace_reason=not-requested cleanup_status=complete host_status=skipped started=false registered=false runtime_collected=false registration_status=failed
  [[ $trace != yes ]] || trace_status=insufficient
  if host_action "$device" register-direct "$case_name-register"; then
    registered=true; registration_status=supported
  else
    rc=$?; [[ $rc -ne 2 ]] || registration_status=unsupported
  fi
  if [[ $registered == true ]]; then
    workspace=$(state_value "$device" WORKSPACE "$case_name-register"); [[ $workspace == "/run/kata-direct-volume-ab/$RUN_ID/workspace" ]] || die 'unexpected direct workspace'
  fi
  if [[ $registered == true ]]; then host_action "$device" begin-guest "$case_name-begin"; started=true; fi
  if [[ $rc -eq 0 ]] && DEVICE_ID=$device CASE_NAME=$case_name WORKSPACE=$workspace OWN_MARKER=$own_marker MARKER_VALUE=$marker_value EXPECTED_MARKER=$expected_marker \
    RUN_LABEL=$RUN_LABEL TARGET_NODE=$TARGET_NODE RESULT_DIR=$RESULT_DIR NAMESPACE=$NAMESPACE KUBECONFIG_PATH=$KUBECONFIG_PATH PROBE_IMAGE=$PROBE_IMAGE \
    "$EXPERIMENT_DIR/run-direct-volume-test.sh" >"$RESULT_DIR/$case_name-pod.env"; then
    if host_action "$device" collect "$case_name-runtime-ready" "$case_name"; then
      runtime_collected=true
      [[ $trace != yes ]] || { host_action "$device" trace-start "$case_name-trace-start" "$case_name" && trace_status=started || trace_status=insufficient; }
      "${K[@]}" exec -n "$NAMESPACE" "$pod" -- touch /tmp/release-test || rc=$?
      [[ $rc -ne 0 ]] || wait_for_workload_completion "$pod" || rc=$?
    else rc=$?; fi
  else
    [[ $rc -ne 0 ]] || rc=$?
  fi
  register_workload_pod_identity "$case_name" "$pod" "$RESULT_DIR/$case_name/pod-ownership.env" || { SAFE_TO_CLEAN=false; cleanup_status=failed; rc=1; }
  mkdir -p "$RESULT_DIR/$case_name"
  if [[ $started == true && $runtime_collected == false ]]; then host_action "$device" collect "$case_name-runtime-failure" "$case_name" || true; fi
  OUTPUT_DIR="$RESULT_DIR/$case_name" POD_NAME=$pod NAMESPACE=$NAMESPACE KUBECONFIG_PATH=$KUBECONFIG_PATH "$EXPERIMENT_DIR/collect-guest-state.sh" || true
  if [[ $trace == yes ]] && ! stop_case_trace "$device" "$case_name"; then
    TRACE_CLEANUP_FAILED=true; SAFE_TO_CLEAN=false; cleanup_status=failed; rc=1
  fi
  if [[ $SAFE_TO_CLEAN != true ]]; then
    copy_host_device_artifacts "$device" "$case_name" || true
    derive_evidence_status "$case_name" "$device" "$started"
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$case_name" "$CASE_EVIDENCE" "$EVIDENCE_REASON" "$trace_status" "$trace_reason" "$cleanup_status" >"$RESULT_DIR/$case_name/evidence-status.tsv"
    CASE_TRACE=$trace_status CASE_CLEANUP=$cleanup_status CASE_HOST=$host_status CASE_REGISTRATION=$registration_status
    return "$rc"
  fi
  delete_owned_workload_pod "$case_name" "$pod" || { SAFE_TO_CLEAN=false; cleanup_status=failed; }
  if [[ $started == true ]]; then host_action "$device" end-guest "$case_name-detach" || { SAFE_TO_CLEAN=false; cleanup_status=failed; }; fi
  copy_host_device_artifacts "$device" "$case_name" || true
  if [[ $trace == yes ]]; then derive_trace_status "$case_name" "$device"; trace_status=$TRACE_STATUS; trace_reason=$TRACE_REASON; fi
  if [[ $SAFE_TO_CLEAN == true ]] && ! delete_owned_raw_resources "$device"; then
    SAFE_TO_CLEAN=false; cleanup_status=failed; rc=1
    printf 'STOP_REASON: raw resources must be absent before device cleanup; preserving device %s state.\n' "$device" >>"$RESULT_DIR/cleanup-blocked.txt"
  fi
  if [[ $SAFE_TO_CLEAN == true && $registered == true ]]; then host_action "$device" unregister-direct "$case_name-unregister" || { SAFE_TO_CLEAN=false; cleanup_status=failed; }; fi
  if [[ $started == true ]]; then
    if host_action "$device" verify-marker "$case_name-host-marker" "$case_name" "$own_marker" "$marker_value"; then host_status=passed; else [[ $rc -ne 0 ]] || rc=$?; host_status=failed; fi
  fi
  derive_evidence_status "$case_name" "$device" "$started"
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$case_name" "$CASE_EVIDENCE" "$EVIDENCE_REASON" "$trace_status" "$trace_reason" "$cleanup_status" >"$RESULT_DIR/$case_name/evidence-status.tsv"
  CASE_TRACE=$trace_status CASE_CLEANUP=$cleanup_status CASE_HOST=$host_status CASE_REGISTRATION=$registration_status
  return "$rc"
}

record_case() {
  local case_name=$1 sequence=$2 device=$3 order=$4 path=$5 variation=$6 rc=$7 classification=success guest_raw_read=not-applicable guest_raw_write=not-applicable ext4_mount=failed fs_write=failed
  local log="$RESULT_DIR/$case_name/guest.log"
  if [[ $rc -ne 0 ]]; then
    if [[ ${CASE_EVIDENCE:-insufficient} == complete ]]; then classification=failure-with-evidence; else classification=insufficient; fi
  fi
  [[ ${CASE_EVIDENCE:-insufficient} == complete ]] || classification=insufficient
  if [[ $rc -eq 0 && ${CASE_TRACE:-not-requested} == unsupported ]]; then classification=unsupported; fi
  if [[ $rc -eq 0 && ${CASE_TRACE:-not-requested} == insufficient ]]; then classification=insufficient; fi
  if [[ ${CASE_REGISTRATION:-not-applicable} == unsupported ]]; then classification=unsupported; fi
  if [[ $path == direct-volume ]]; then guest_raw_read=unsupported; guest_raw_write=unsupported; fi
  if [[ ${CASE_CLEANUP:-failed} == failed ]]; then classification=cleanup-failure; fi
  [[ $path != raw-block ]] || { grep -q GUEST_RAW_READ_OK "$log" 2>/dev/null && guest_raw_read=passed || guest_raw_read=failed; }
  [[ $variation != reserved-raw-diagnostic ]] || { grep -q GUEST_RAW_WRITE_OK "$log" 2>/dev/null && guest_raw_write=passed || guest_raw_write=failed; ext4_mount=not-applicable; fs_write=not-applicable; }
  grep -q EXT4_MOUNT_OK "$log" 2>/dev/null && ext4_mount=passed
  grep -q FS_MATRIX_OK "$log" 2>/dev/null && fs_write=passed
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$RUN_ID" "$case_name" "$device" "$order" "$path" "$variation" "$guest_raw_read" "$guest_raw_write" "$ext4_mount" "$fs_write" \
    "${CASE_HOST:-skipped}" "${CASE_EVIDENCE:-insufficient}" "${CASE_TRACE:-not-requested}" "${CASE_CLEANUP:-failed}" "$classification" >"$RESULT_DIR/result-row.tmp"
  awk -F '\t' -v key="$case_name" -v replacement="$(<"$RESULT_DIR/result-row.tmp")" 'BEGIN {OFS="\t"} NR == 1 || $2 != key {print; next} {print replacement}' \
    "$RESULT_DIR/result-matrix.tsv" >"$RESULT_DIR/result-matrix.tsv.tmp"
  mv "$RESULT_DIR/result-matrix.tsv.tmp" "$RESULT_DIR/result-matrix.tsv"; rm -f "$RESULT_DIR/result-row.tmp"
  record_normalized_metadata "$case_name" "$device" "$path"
}

record_normalized_metadata() {
  local case_name=$1 device=$2 path=$3 case_dir host_dir runtime_dir state
  case_dir="$RESULT_DIR/$case_name"; host_dir="$case_dir/host-artifacts/$device"; runtime_dir="$host_dir/runtime-$case_name"; state="$host_dir/state.env"
  local fd_mode=insufficient host_device=insufficient host_majmin=insufficient fs_uuid=insufficient guest_path=insufficient fstype=insufficient options=insufficient read_only=insufficient
  local metadata_ref=insufficient storage_ref=insufficient path_line
  if [[ -s $runtime_dir/ch-fds.tsv ]]; then
    fd_mode=$(awk -F '\t' 'NR > 1 && $5 != "" {print $5}' "$runtime_dir/ch-fds.tsv" | sort -u | paste -sd, -)
    [[ -n $fd_mode ]] || fd_mode=insufficient
  fi
  if [[ -s $state ]]; then
    host_device=$(sed -n 's/^LOOP_DEVICE=//p' "$state" | tail -n1); [[ -n $host_device ]] || host_device=insufficient
    host_majmin=$(sed -n 's/^LOOP_MAJMIN=//p' "$state" | tail -n1); [[ -n $host_majmin ]] || host_majmin=insufficient
    fs_uuid=$(sed -n 's/^FILESYSTEM_UUID=//p' "$state" | tail -n1); [[ -n $fs_uuid ]] || fs_uuid=insufficient
  fi
  path_line=$(grep '^PATH_METADATA' "$case_dir/guest.log" 2>/dev/null | tail -n1 || true)
  if [[ -n $path_line ]]; then
    guest_path=$(printf '%s\n' "$path_line" | awk -F '\t' '{sub(/^guest_path=/,"",$2); print $2}')
    fstype=$(printf '%s\n' "$path_line" | awk -F '\t' '{sub(/^fstype=/,"",$3); print $3}')
    options=$(printf '%s\n' "$path_line" | awk -F '\t' '{sub(/^options=/,"",$4); print $4}')
    read_only=$(printf '%s\n' "$path_line" | awk -F '\t' '{sub(/^read_only=/,"",$5); print $5}')
  fi
  if [[ $path == raw-block ]]; then
    [[ -s $case_dir/rendered-pod.yaml ]] && metadata_ref="$case_name/rendered-pod.yaml" || metadata_ref=insufficient
    [[ -s $RESULT_DIR/$case_name-storage.env ]] && storage_ref="$case_name-storage.env" || storage_ref=insufficient
  else
    if [[ -s $host_dir/mountInfo.json ]]; then
      metadata_ref="$case_name/host-artifacts/$device/mountInfo.json"
      [[ $fstype != insufficient ]] || fstype=$(jq -r '.fstype // "insufficient"' "$host_dir/mountInfo.json")
      if [[ $options == insufficient ]]; then options=$(jq -r 'if (.options | type)=="array" then (.options | sort | unique | join(",")) else "insufficient" end' "$host_dir/mountInfo.json"); fi
      if [[ $read_only == insufficient && $options != insufficient ]]; then
        if grep -Eq '(^|,)ro(,|$)' <<<"$options"; then read_only=true; elif grep -Eq '(^|,)rw(,|$)' <<<"$options"; then read_only=false; fi
      fi
    fi
    [[ -s $host_dir/mount-info-path.txt ]] && storage_ref="$case_name/host-artifacts/$device/mount-info-path.txt" || storage_ref=insufficient
  fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$case_name" "$device" "$path" "$host_device" "$host_majmin" "$guest_path" "$fs_uuid" "$fstype" "$options" "$read_only" "$fd_mode" "$metadata_ref" "$storage_ref" >>"$RESULT_DIR/normalized-metadata-fd.tsv"
}

initialize_matrix() {
  printf 'run\tcase\tdevice\torder\tpath\tvariation\tguest_raw_read\tguest_raw_write\text4_mount\tfs_write\thost_persistence\tevidence\ttrace\tcleanup\tclassification\n' >"$RESULT_DIR/result-matrix.tsv"
  printf '%s\tA\tsame-ab\t1\traw-block\tfilesystem\tskipped\tskipped\tskipped\tskipped\tskipped\tskipped\tskipped\tskipped\tplanned\n' "$RUN_ID" >>"$RESULT_DIR/result-matrix.tsv"
  printf '%s\tB\tsame-ab\t2\tdirect-volume\tfilesystem\tskipped\tnot-applicable\tskipped\tskipped\tskipped\tskipped\tskipped\tskipped\tplanned\n' "$RUN_ID" >>"$RESULT_DIR/result-matrix.tsv"
  printf '%s\tB2\tfresh-ba\t1\tdirect-volume\tfilesystem\tskipped\tnot-applicable\tskipped\tskipped\tskipped\tskipped\tskipped\tskipped\tplanned\n' "$RUN_ID" >>"$RESULT_DIR/result-matrix.tsv"
  printf '%s\tA2\tfresh-ba\t2\traw-block\tfilesystem\tskipped\tskipped\tskipped\tskipped\tskipped\tskipped\tskipped\tskipped\tplanned\n' "$RUN_ID" >>"$RESULT_DIR/result-matrix.tsv"
  printf '%s\tRAW\tisolated-raw\t1\traw-block\treserved-raw-diagnostic\tskipped\tskipped\tnot-applicable\tnot-applicable\tskipped\tskipped\tskipped\tskipped\tplanned\n' "$RUN_ID" >>"$RESULT_DIR/result-matrix.tsv"
  printf '%s\tDIRECT\tisolated-direct\t1\tdirect-volume\tfilesystem\tskipped\tnot-applicable\tskipped\tskipped\tskipped\tskipped\tskipped\tskipped\tplanned\n' "$RUN_ID" >>"$RESULT_DIR/result-matrix.tsv"
  printf 'case\tdevice\tpath\thost_device\thost_major_minor\tguest_path\tfilesystem_uuid\tfstype\tmount_options\tread_only\tch_fd_access_modes\tpath_metadata_ref\tstorage_metadata_ref\n' >"$RESULT_DIR/normalized-metadata-fd.tsv"
  printf 'sequence\tfirst_case\tfirst_path_metadata\tsecond_case\tsecond_path_metadata\tcomparison\nA-to-B\tA\tinsufficient\tB\tinsufficient\tinsufficient\nB-to-A\tB2\tinsufficient\tA2\tinsufficient\tinsufficient\n' >"$RESULT_DIR/normalized-comparison.tsv"
}

abort_if_unsafe() {
  [[ $SAFE_TO_CLEAN == true ]] || safety_die 'case cleanup failed; stopping before another consumer and preserving diagnostic state'
}

[[ $RUN_EXPERIMENT == yes ]] || die 'Set RUN_EXPERIMENT=yes only after reviewing the harness; this script does not provision Azure'
[[ $FORMAT_NVME == yes || $FORMAT_NVME == no ]] || die 'FORMAT_NVME must be yes or no'
require_tools az awk base64 file find grep jq kubectl paste sed sort tar
require_immutable_image "$PROBE_IMAGE" PROBE_IMAGE; require_immutable_image "$HOST_IMAGE" HOST_IMAGE
[[ ! -e $RESULT_DIR ]] || die "refusing to overwrite $RESULT_DIR"
mkdir -p "$RESULT_DIR"; chmod 0700 "$RESULT_DIR"; trap cleanup EXIT INT TERM
initialize_matrix
assert_kubeconfig_targets_cluster
GROUP_TAGS=$(az group show --subscription "$SUBSCRIPTION_ID" --name "$RESOURCE_GROUP" --query '[[tags."deployment-id",tags.purpose,tags."run-id",tags."deployment-suffix"]]' -o tsv)
[[ $GROUP_TAGS == "$DEPLOYMENT_ID"$'\t'"$PURPOSE"$'\t'"$RUN_ID"$'\t'"$DEPLOYMENT_SUFFIX" ]] || safety_die 'resource group ownership tags do not match this run'
AKS_TAGS=$(az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --query '[[tags."deployment-id",tags.purpose,tags."run-id",tags."deployment-suffix"]]' -o tsv)
[[ $AKS_TAGS == "$DEPLOYMENT_ID"$'\t'"$PURPOSE"$'\t'"$RUN_ID"$'\t'"$DEPLOYMENT_SUFFIX" ]] || safety_die 'cluster ownership tags do not match this run'
if "${K[@]}" get namespace "$NAMESPACE" >/dev/null 2>&1; then safety_die 'namespace already exists'; fi
cat >"$RESULT_DIR/namespace.yaml" <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: $NAMESPACE
  labels:
    purpose: $PURPOSE
    run-id: $RUN_ID
    deployment-suffix: $DEPLOYMENT_SUFFIX
EOF
if ! "${K[@]}" create -f "$RESULT_DIR/namespace.yaml" -o json >"$RESULT_DIR/namespace-created.json"; then
  [[ -z $(kubernetes_object_json namespace "$NAMESPACE" '') ]] || safety_die 'namespace create failed before UID capture; refusing to delete unknown replacement'
  safety_die 'namespace create failed and exact-name absence was verified'
fi
NAMESPACE_UID=$(jq -r '.metadata.uid // empty' "$RESULT_DIR/namespace-created.json")
NAMESPACE_RESOURCE_VERSION=$(jq -r '.metadata.resourceVersion // empty' "$RESULT_DIR/namespace-created.json")
[[ $NAMESPACE_UID =~ ^[0-9a-f-]{16,64}$ && -n $NAMESPACE_RESOURCE_VERSION ]] || safety_die 'created namespace identity is absent'
persist_run_kubernetes_ownership
create_diag; assert_dedicated_node
run_logged environment "${K[@]}" get nodes -o wide
run_logged runtime-class "${K[@]}" get runtimeclass kata-vm-isolation -o yaml
printf 'case\tstatus\tnote\tevidence\n' >"$RESULT_DIR/diagnostic-matrix.tsv"

# Required same-device A -> B sequence.
prepare_device same-ab
if host_action same-ab direct-capabilities pre-mount-direct-capabilities; then
  PREMOUNT_STATUS=$(state_value same-ab PREMOUNT_RAW_STATUS pre-mount-direct-capabilities)
  PREMOUNT_REASON=$(state_value same-ab PREMOUNT_RAW_REASON pre-mount-direct-capabilities)
  PREMOUNT_EVIDENCE=$(state_value same-ab PREMOUNT_RAW_EVIDENCE pre-mount-direct-capabilities)
  printf 'pre-mount-direct-raw\t%s\t%s\t%s\n' "$PREMOUNT_STATUS" "$PREMOUNT_REASON" "same-ab-pre-mount-direct-capabilities.log" >>"$RESULT_DIR/diagnostic-matrix.tsv"
else
  printf 'pre-mount-direct-raw\tinsufficient\truntime-capability-detection-failed\t%s\n' "same-ab-pre-mount-direct-capabilities.log" >>"$RESULT_DIR/diagnostic-matrix.tsv"
fi
if run_raw same-ab A filesystem yes none marker-a; then rc=0; else rc=$?; fi; record_case A same-device-a-to-b same-ab 1 raw-block filesystem "$rc"
abort_if_unsafe
if run_direct same-ab B yes marker-a marker-b; then rc=0; else rc=$?; fi; record_case B same-device-a-to-b same-ab 2 direct-volume filesystem "$rc"
abort_if_unsafe

# Required fresh reversal B -> A.
prepare_device fresh-ba
if run_direct fresh-ba B2 no none marker-b2; then rc=0; else rc=$?; fi; record_case B2 fresh-device-b-to-a fresh-ba 1 direct-volume filesystem "$rc"
abort_if_unsafe
if run_raw fresh-ba A2 filesystem no marker-b2 marker-a2; then rc=0; else rc=$?; fi; record_case A2 fresh-device-b-to-a fresh-ba 2 raw-block filesystem "$rc"
abort_if_unsafe

# Isolated raw variation, using only reserved ext4-owned blocks and restoring them before cleanup.
prepare_device isolated-raw
if run_raw isolated-raw RAW raw-diagnostic yes none marker-raw; then rc=0; else rc=$?; fi; record_case RAW isolated isolated-raw 1 raw-block reserved-raw-diagnostic "$rc"
abort_if_unsafe
if [[ $rc -eq 0 ]]; then printf 'RAW\tsupported\treserved offsets written and restored\n' >>"$RESULT_DIR/diagnostic-matrix.tsv"; else printf 'RAW\tinsufficient\tsee case evidence\n' >>"$RESULT_DIR/diagnostic-matrix.tsv"; fi

# Isolated direct variation on another freshly formatted device.
prepare_device isolated-direct
if run_direct isolated-direct DIRECT yes none marker-direct; then rc=0; else rc=$?; fi; record_case DIRECT isolated isolated-direct 1 direct-volume filesystem "$rc"
abort_if_unsafe

awk -F '\t' 'BEGIN {OFS="\t"; print "sequence","first_case","first_path_metadata","second_case","second_path_metadata","comparison"}
  NR > 1 {
    fd[$1]=$11;
    metadata[$1]="host_device=" $4 ";host_major_minor=" $5 ";guest_path=" $6 ";filesystem_uuid=" $7 ";fstype=" $8 ";mount_options=" $9 ";read_only=" $10 ";ch_fd_access_modes=" $11 ";path_metadata_ref=" $12 ";storage_metadata_ref=" $13
  }
  END {
    comparison=(fd["A"] == "" || fd["B"] == "" || fd["A"] == "insufficient" || fd["B"] == "insufficient") ? "insufficient" : (fd["A"] == fd["B"] ? "equal" : "different");
    print "A-to-B","A",(metadata["A"]==""?"insufficient":metadata["A"]),"B",(metadata["B"]==""?"insufficient":metadata["B"]),comparison;
    comparison=(fd["B2"] == "" || fd["A2"] == "" || fd["B2"] == "insufficient" || fd["A2"] == "insufficient") ? "insufficient" : (fd["B2"] == fd["A2"] ? "equal" : "different");
    print "B-to-A","B2",(metadata["B2"]==""?"insufficient":metadata["B2"]),"A2",(metadata["A2"]==""?"insufficient":metadata["A2"]),comparison
  }' "$RESULT_DIR/normalized-metadata-fd.tsv" >"$RESULT_DIR/normalized-comparison.tsv"

printf 'Experiment completed; evidence remains observational and is not interpreted by this harness.\n'
