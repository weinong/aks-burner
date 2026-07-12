#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/../../../.." && pwd -P)
NAMESPACE=${NAMESPACE:-kata-io}
TARGET_NODE=${TARGET_NODE:-}
PROBE_IMAGE=${PROBE_IMAGE:-}
HOST_IMAGE=${HOST_IMAGE:-}
EXPECTED_NVME_DEVICE=${EXPECTED_NVME_DEVICE:-}
FORMAT_NVME=${FORMAT_NVME:-no}
KUBECTL_CONTEXT=${KUBECTL_CONTEXT:-}
KUBECONFIG_PATH=${KUBECONFIG_PATH:-}
SUBSCRIPTION_ID=${SUBSCRIPTION_ID:-}
EXPECTED_RESOURCE_GROUP=${EXPECTED_RESOURCE_GROUP:-}
EXPECTED_CLUSTER_NAME=${EXPECTED_CLUSTER_NAME:-}
UTC_RUN=$(date -u +%Y%m%dT%H%M%SZ)
RUN_LABEL=$(printf '%s-%s' "$UTC_RUN" "$$" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9-' | cut -c1-50)
RESULT_ROOT=${RESULT_ROOT:-$REPO_ROOT/results/virtio-blk-write-trace}
RESULT_DIR="$RESULT_ROOT/$UTC_RUN"
PV_NAME="kata-blk-$RUN_LABEL"
PVC_NAME="kata-blk-$RUN_LABEL"
DIAG_POD="kata-blk-host-$RUN_LABEL"
PREFLIGHT_POD="kata-blk-preflight-$RUN_LABEL"
HOST_ARTIFACT_DIR="/var/log/kata-virtio-blk-write-trace/$RUN_LABEL"
DEVICE=
ACTIVE_POD=
CREATED_NAMESPACE=false
DIAG_READY=false
DIAG_CREATED=false
PREFLIGHT_CREATED=false
HOST_PREP_STARTED=false
STORAGE_CREATED=false
SAFE_DETACHED=true
TRACE_HOST_PID=
TRACE_CASE_LOG=
TRACE_STATUS=not-run
FINAL_STATUS=0
K=(kubectl --kubeconfig "$KUBECONFIG_PATH")
[[ -z $KUBECTL_CONTEXT ]] || K+=(--context "$KUBECTL_CONTEXT")

usage() {
  cat <<'EOF'
Usage: KUBECONFIG_PATH=<file> SUBSCRIPTION_ID=<guid> \
  EXPECTED_RESOURCE_GROUP=<group> EXPECTED_CLUSTER_NAME=<cluster> \
  TARGET_NODE=<node> PROBE_IMAGE=<image>@sha256:<digest> \
  HOST_IMAGE=<image>@sha256:<digest> EXPECTED_NVME_DEVICE=/dev/nvmeXnY \
  [FORMAT_NVME=yes] ./run-experiment.sh

The exact whole NVMe namespace must be supplied. An existing ext4 filesystem is
used without formatting. FORMAT_NVME=yes permits formatting only after host-side
proof that the device is separate from root, kubelet, and containerd and has no
mounts, children, holders, slaves, swap, or users.
EOF
}

timestamp() { date -u +%Y-%m-%dT%H:%M:%S.%NZ; }

redact() {
  sed -E \
    -e 's#(/subscriptions/)[0-9a-fA-F-]{36}#\1[REDACTED]#g' \
    -e 's/[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}/[REDACTED-GUID]/g' \
    -e 's/(Bearer[[:space:]]+)[A-Za-z0-9._~+\/-]+/\1[REDACTED]/Ig' \
    -e 's/((access_?token|refresh_?token|client_?secret|password)["=: ]+)[^ ,"}]+/\1[REDACTED]/Ig' \
    -e 's/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/[REDACTED-JWT]/g'
}

run_logged() {
  local label=$1
  shift
  local start end rc raw_out raw_err log
  start=$(timestamp)
  raw_out=$(mktemp)
  raw_err=$(mktemp)
  log="$RESULT_DIR/${label//[^A-Za-z0-9_.-]/_}.log"
  {
    printf 'COMMAND_START label=%q time=%s command=' "$label" "$start"
    printf '%q ' "$@"
    printf '\n'
  } | redact >"$log"
  if "$@" >"$raw_out" 2>"$raw_err"; then rc=0; else rc=$?; fi
  printf '%s\n' '--- stdout ---' >>"$log"
  redact <"$raw_out" >>"$log"
  printf '%s\n' '--- stderr ---' >>"$log"
  redact <"$raw_err" >>"$log"
  end=$(timestamp)
  printf 'COMMAND_END label=%q time=%s status=%d\n' "$label" "$end" "$rc" >>"$log"
  rm -f "$raw_out" "$raw_err"
  return "$rc"
}

host_action() {
  local label=$1 action=$2 case_name=${3:-}
  local start end rc raw_out raw_err log
  start=$(timestamp)
  raw_out=$(mktemp)
  raw_err=$(mktemp)
  log="$RESULT_DIR/${label//[^A-Za-z0-9_.-]/_}.log"
  printf 'COMMAND_START label=%q time=%s command=host-action-%q\n' "$label" "$start" "$action" >"$log"
  if "${K[@]}" exec -i -n "$NAMESPACE" "$DIAG_POD" -- \
    chroot /host /usr/bin/nsenter --target 1 --mount --pid --wd=/ -- \
    /bin/bash -s -- "$action" "$RUN_LABEL" "$EXPECTED_NVME_DEVICE" "$FORMAT_NVME" "$case_name" \
    <"$SCRIPT_DIR/register-direct-volume.sh" >"$raw_out" 2>"$raw_err"; then rc=0; else rc=$?; fi
  printf '%s\n' '--- stdout ---' >>"$log"
  redact <"$raw_out" >>"$log"
  printf '%s\n' '--- stderr ---' >>"$log"
  redact <"$raw_err" >>"$log"
  end=$(timestamp)
  printf 'COMMAND_END label=%q time=%s status=%d\n' "$label" "$end" "$rc" >>"$log"
  rm -f "$raw_out" "$raw_err"
  return "$rc"
}

delete_test_pod() {
  [[ -n $ACTIVE_POD ]] || return 0
  run_logged "delete-$ACTIVE_POD" "${K[@]}" delete pod -n "$NAMESPACE" "$ACTIVE_POD" \
    --ignore-not-found --wait=true --timeout=120s || return 1
  ACTIVE_POD=
}

stop_trace_host_action() {
  [[ -n $TRACE_HOST_PID ]] || return 0
  run_logged trace-stop-marker "${K[@]}" exec -n "$NAMESPACE" "$DIAG_POD" -- \
    touch "/host$HOST_ARTIFACT_DIR/tracing/stop" || TRACE_STATUS=insufficient
  if wait "$TRACE_HOST_PID"; then
    [[ $TRACE_STATUS == insufficient ]] || TRACE_STATUS=sufficient
  else
    TRACE_STATUS=insufficient
  fi
  TRACE_HOST_PID=
}

copy_host_artifacts() {
  local archive="$RESULT_DIR/host-artifacts.tar" rc=0
  [[ $DIAG_READY == true && $HOST_PREP_STARTED == true ]] || return 0
  mkdir -p "$RESULT_DIR/host-artifacts"
  run_logged copy-container-tar-check "${K[@]}" exec -n "$NAMESPACE" "$DIAG_POD" -- tar --version || return 1
  run_logged copy-host-tar-check "${K[@]}" exec -n "$NAMESPACE" "$DIAG_POD" -- \
    chroot /host tar --version || return 1
  {
    printf 'COMMAND_START label=copy-host-artifacts time=%s command=kubectl-exec-tar\n' "$(timestamp)"
    if "${K[@]}" exec -n "$NAMESPACE" "$DIAG_POD" -- \
      tar -C "/host$HOST_ARTIFACT_DIR" -cf - . >"$archive"; then rc=0; else rc=$?; fi
    printf 'COMMAND_END label=copy-host-artifacts time=%s status=%d\n' "$(timestamp)" "$rc"
  } >"$RESULT_DIR/copy-host-artifacts.log" 2>&1
  [[ $rc -eq 0 ]] || return "$rc"
  tar -C "$RESULT_DIR/host-artifacts" -xf "$archive" || return 1
  rm -f "$archive"
  while IFS= read -r -d '' file; do
    if file "$file" | grep -q text; then
      redact <"$file" >"$file.redacted"
      mv -f "$file.redacted" "$file"
    fi
  done < <(find "$RESULT_DIR/host-artifacts" -type f -print0)
}

cleanup() {
  local rc=$? detach_rc=0 cleanup_rc=0 copy_rc=0 pvc_rc=0 pv_rc=0 pod_rc=0 diag_rc=0 namespace_rc=0
  trap - EXIT INT TERM
  set +e
  stop_trace_host_action
  delete_test_pod || pod_rc=$?
  if [[ $HOST_PREP_STARTED == true && $DIAG_READY == true ]]; then
    host_action cleanup-wait-detached wait-detached || detach_rc=$?
    if [[ $detach_rc -ne 0 ]] && host_action cleanup-already-complete cleanup; then
      detach_rc=0
      STORAGE_CREATED=false
    fi
    if [[ $detach_rc -eq 0 ]]; then
      SAFE_DETACHED=true
      if [[ $STORAGE_CREATED == true ]]; then
        run_logged cleanup-pvc "${K[@]}" delete pvc -n "$NAMESPACE" "$PVC_NAME" --ignore-not-found --wait=true --timeout=120s || pvc_rc=$?
        run_logged cleanup-pv "${K[@]}" delete pv "$PV_NAME" --ignore-not-found --wait=true --timeout=120s || pv_rc=$?
        if [[ $pvc_rc -eq 0 && $pv_rc -eq 0 ]]; then STORAGE_CREATED=false; else cleanup_rc=1; fi
      fi
      if [[ $STORAGE_CREATED == false ]]; then host_action cleanup-host cleanup || cleanup_rc=$?; fi
    else
      SAFE_DETACHED=false
      printf 'CLEANUP_BLOCKED: device still attached; PVC/PV, config, loop, backing, NVMe mount, and owner were preserved.\n' \
        >"$RESULT_DIR/cleanup-blocked.txt"
    fi
    copy_host_artifacts || copy_rc=$?
  fi
  if [[ $PREFLIGHT_CREATED == true ]]; then
    run_logged cleanup-preflight "${K[@]}" delete pod -n "$NAMESPACE" "$PREFLIGHT_POD" --ignore-not-found --wait=true --timeout=120s || pod_rc=$?
  fi
  if [[ $DIAG_CREATED == true && $SAFE_DETACHED == true ]]; then
    run_logged cleanup-diag "${K[@]}" delete pod -n "$NAMESPACE" "$DIAG_POD" --ignore-not-found --wait=true --timeout=120s || diag_rc=$?
  fi
  if [[ $CREATED_NAMESPACE == true && $SAFE_DETACHED == true && $STORAGE_CREATED == false ]]; then
    run_logged cleanup-namespace "${K[@]}" delete namespace "$NAMESPACE" --wait=true --timeout=120s || namespace_rc=$?
  fi
  (( detach_rc == 0 && cleanup_rc == 0 && copy_rc == 0 && pvc_rc == 0 && pv_rc == 0 && pod_rc == 0 && diag_rc == 0 && namespace_rc == 0 )) || rc=1
  (( FINAL_STATUS == 0 )) || rc=$FINAL_STATUS
  printf 'results=%s status=%d\n' "$RESULT_DIR" "$rc"
  exit "$rc"
}

render_manifest() {
  local case_name=$1 pod_name=$2 output=$3
  sed \
    -e "s|REPLACE_PV_NAME|$PV_NAME|g" \
    -e "s|REPLACE_PVC_NAME|$PVC_NAME|g" \
    -e "s|REPLACE_RUN_LABEL|$RUN_LABEL|g" \
    -e "s|REPLACE_HOST_DEVICE|$DEVICE|g" \
    -e "s|REPLACE_NAMESPACE|$NAMESPACE|g" \
    -e "s|REPLACE_NODE|$TARGET_NODE|g" \
    -e "s|REPLACE_POD_NAME|$pod_name|g" \
    -e "s|REPLACE_CASE|$case_name|g" \
    -e "s|REPLACE_PROBE_IMAGE|$PROBE_IMAGE|g" \
    "$SCRIPT_DIR/raw-block-probe.yml" >"$output"
}

extract_manifest_doc() {
  local document=$1 input=$2 output=$3
  awk -v wanted="$document" 'BEGIN {doc=1} /^---$/ {doc++; next} doc == wanted {print}' "$input" >"$output"
}

create_diag_pod() {
  local manifest="$RESULT_DIR/host-diagnostic.yml"
  cat >"$manifest" <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $DIAG_POD
  namespace: $NAMESPACE
  labels:
    app.kubernetes.io/name: kata-virtio-blk-write-trace-host
    kata-io-run: $RUN_LABEL
spec:
  nodeName: $TARGET_NODE
  tolerations:
    - key: dedicated
      operator: Equal
      value: kata-virtio-blk-write-trace
      effect: NoSchedule
  hostPID: true
  restartPolicy: Never
  terminationGracePeriodSeconds: 5
  containers:
    - name: host
      image: $HOST_IMAGE
      imagePullPolicy: IfNotPresent
      command: [/bin/sh, -c, "trap : TERM INT; sleep infinity & wait"]
      securityContext:
        privileged: true
      volumeMounts:
        - name: host-root
          mountPath: /host
          mountPropagation: Bidirectional
  volumes:
    - name: host-root
      hostPath:
        path: /
        type: Directory
EOF
  run_logged diag-client-dry-run "${K[@]}" apply --dry-run=client -f "$manifest"
  run_logged create-diag "${K[@]}" apply -f "$manifest"
  DIAG_CREATED=true
  run_logged wait-diag "${K[@]}" wait -n "$NAMESPACE" --for=condition=Ready "pod/$DIAG_POD" --timeout=120s
  DIAG_READY=true
}

run_image_preflight() {
  local manifest="$RESULT_DIR/image-preflight.yml"
  cat >"$manifest" <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $PREFLIGHT_POD
  namespace: $NAMESPACE
spec:
  runtimeClassName: kata-vm-isolation
  nodeName: $TARGET_NODE
  restartPolicy: Never
  tolerations:
    - key: dedicated
      operator: Equal
      value: kata-virtio-blk-write-trace
      effect: NoSchedule
  containers:
    - name: preflight
      image: $PROBE_IMAGE
      imagePullPolicy: IfNotPresent
      command: [/bin/bash, -ceu, "for c in bash blockdev date dd fio head lsblk sha256sum stat tail tar tr; do command -v \"\$c\"; done"]
EOF
  run_logged preflight-client-dry-run "${K[@]}" apply --dry-run=client -f "$manifest"
  run_logged create-preflight "${K[@]}" apply -f "$manifest"
  PREFLIGHT_CREATED=true
  run_logged wait-preflight "${K[@]}" wait -n "$NAMESPACE" --for=jsonpath='{.status.phase}'=Succeeded "pod/$PREFLIGHT_POD" --timeout=180s
  run_logged preflight-logs "${K[@]}" logs -n "$NAMESPACE" "$PREFLIGHT_POD"
  run_logged delete-preflight "${K[@]}" delete pod -n "$NAMESPACE" "$PREFLIGHT_POD" --wait=true --timeout=120s
  PREFLIGHT_CREATED=false
}

check_node_dedicated() {
  local occupants
  run_logged node-pods "${K[@]}" get pods --all-namespaces --field-selector "spec.nodeName=$TARGET_NODE" \
    -o go-template='{{range .items}}{{.metadata.namespace}}{{"\t"}}{{.metadata.name}}{{"\t"}}{{if .metadata.ownerReferences}}{{range .metadata.ownerReferences}}{{.kind}}{{" "}}{{end}}{{else}}NO_OWNER{{end}}{{"\n"}}{{end}}'
  occupants=$(sed -n '/^COMMAND_START/d; /^COMMAND_END/d; /^--- /d; /^[[:space:]]*$/d; /[[:space:]]DaemonSet[[:space:]]*$/d; /kube-system[[:space:]].*stale-pod-cleanup-.*[[:space:]]Job[[:space:]]*$/d; /[[:space:]]'"$DIAG_POD"'[[:space:]]/d; /[[:space:]]'"$PREFLIGHT_POD"'[[:space:]]/d; p' "$RESULT_DIR/node-pods.log")
  [[ -z $occupants ]] || { printf 'SAFETY_FAILURE: target node has non-DaemonSet workloads\n' >&2; exit 1; }
  run_logged node-dedicated-taint "${K[@]}" get node "$TARGET_NODE" \
    -o jsonpath='{range .spec.taints[*]}{.key}={.value}:{.effect}{"\n"}{end}'
  grep -q '^dedicated=kata-virtio-blk-write-trace:NoSchedule$' "$RESULT_DIR/node-dedicated-taint.log" || {
    printf 'SAFETY_FAILURE: target node lacks the dedicated experiment taint\n' >&2
    exit 1
  }
}

collect_pod_evidence() {
  local case_name=$1 pod_name=$2
  run_logged "case-$case_name-pod-yaml" "${K[@]}" get pod -n "$NAMESPACE" "$pod_name" -o yaml || true
  run_logged "case-$case_name-describe" "${K[@]}" describe pod -n "$NAMESPACE" "$pod_name" || true
  run_logged "case-$case_name-events" "${K[@]}" get events -n "$NAMESPACE" \
    --field-selector "involvedObject.name=$pod_name" --sort-by=.lastTimestamp || true
  run_logged "case-$case_name-logs" "${K[@]}" logs -n "$NAMESPACE" "$pod_name" || true
}

wait_for_ready_marker() {
  local case_name=$1 pod_name=$2 deadline=$((SECONDS + 300)) poll=0 phase
  while (( SECONDS < deadline )); do
    poll=$((poll + 1))
    run_logged "case-$case_name-ready-poll-$poll" "${K[@]}" logs -n "$NAMESPACE" "$pod_name" || true
    grep -q "READY_FOR_WRITE case=$case_name" "$RESULT_DIR/case-$case_name-ready-poll-$poll.log" && return 0
    run_logged "case-$case_name-phase-poll-$poll" "${K[@]}" get pod -n "$NAMESPACE" "$pod_name" -o jsonpath='{.status.phase}' || true
    phase=$(sed -n '/^--- stdout ---$/,/^--- stderr ---$/p' "$RESULT_DIR/case-$case_name-phase-poll-$poll.log")
    if [[ $phase == *Failed* || $phase == *Succeeded* ]]; then
      return 1
    fi
    sleep 2
  done
  return 1
}

wait_for_trace_ready() {
  local deadline=$((SECONDS + 60)) poll=0
  while (( SECONDS < deadline )); do
    poll=$((poll + 1))
    if run_logged "case-D-trace-ready-$poll" "${K[@]}" exec -n "$NAMESPACE" "$DIAG_POD" -- \
      test -e "/host$HOST_ARTIFACT_DIR/tracing/ready"; then return 0; fi
    sleep 1
  done
  return 1
}

run_case() {
  local case_name=$1 pod_name full_manifest pod_manifest
  pod_name="kata-blk-${case_name,,}-$RUN_LABEL"
  full_manifest="$RESULT_DIR/case-$case_name.yml"
  pod_manifest="$RESULT_DIR/case-$case_name-pod.yml"
  local ready_rc=0 write_rc=0 detach_rc=0 verify_rc=0 collect_rc=0 delete_rc=0 trace_status=not-run
  ACTIVE_POD=$pod_name
  SAFE_DETACHED=false
  host_action "case-$case_name-start" case-start "$case_name"
  render_manifest "$case_name" "$pod_name" "$full_manifest"
  extract_manifest_doc 3 "$full_manifest" "$pod_manifest"
  run_logged "case-$case_name-client-dry-run" "${K[@]}" apply --dry-run=client -f "$pod_manifest"
  run_logged "case-$case_name-create" "${K[@]}" apply -f "$pod_manifest"
  wait_for_ready_marker "$case_name" "$pod_name" || ready_rc=$?
  if [[ $ready_rc -eq 0 ]]; then
    if host_action "case-$case_name-collect" collect "$case_name"; then collect_rc=0; else collect_rc=$?; fi
    [[ $collect_rc -eq 0 ]] || write_rc=125
    if [[ $case_name == D ]]; then
      TRACE_CASE_LOG="$RESULT_DIR/case-D-trace-run.log"
      host_action case-D-trace-run trace-run D &
      TRACE_HOST_PID=$!
      if wait_for_trace_ready; then
        trace_status=armed
      else
        printf 'case D tracer did not publish a ready marker\n' >&2
        trace_status=insufficient
      fi
    fi
    if [[ $collect_rc -eq 0 ]]; then
      run_logged "case-$case_name-release" "${K[@]}" exec -n "$NAMESPACE" "$pod_name" -- touch /tmp/release-write
      if run_logged "case-$case_name-complete" "${K[@]}" wait -n "$NAMESPACE" \
        --for=jsonpath='{.status.phase}'=Succeeded "pod/$pod_name" --timeout=180s; then write_rc=0; else write_rc=$?; fi
    fi
  fi
  collect_pod_evidence "$case_name" "$pod_name"
  stop_trace_host_action
  [[ $case_name != D ]] || trace_status=$TRACE_STATUS
  if delete_test_pod; then delete_rc=0; else delete_rc=$?; fi
  if host_action "case-$case_name-wait-detached" wait-detached; then detach_rc=0; else detach_rc=$?; fi
  if [[ $detach_rc -ne 0 ]]; then
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$case_name" "$ready_rc" "$write_rc" "$collect_rc" "$delete_rc" "$detach_rc" not-run "$trace_status" >>"$RESULT_DIR/case-summary.tsv"
    FINAL_STATUS=1
    return 1
  fi
  SAFE_DETACHED=true
  if host_action "case-$case_name-host-verify" verify "$case_name"; then verify_rc=0; else verify_rc=$?; fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$case_name" "$ready_rc" "$write_rc" "$collect_rc" "$delete_rc" "$detach_rc" "$verify_rc" "$trace_status" >>"$RESULT_DIR/case-summary.tsv"
  if [[ $case_name == D && $trace_status != sufficient ]]; then
    FINAL_STATUS=1
  fi
  if [[ $verify_rc -ne 0 || $collect_rc -ne 0 || $delete_rc -ne 0 ]]; then
    FINAL_STATUS=1
    return 1
  fi
  # READY/write failures are expected test outcomes; identity, detach, and seed integrity failures are not.
  return 0
}

[[ ${1:-} != -h && ${1:-} != --help ]] || { usage; exit 0; }
[[ -n $KUBECONFIG_PATH && -f $KUBECONFIG_PATH ]] || { printf 'KUBECONFIG_PATH must name an existing explicit kubeconfig\n' >&2; exit 2; }
[[ -n $SUBSCRIPTION_ID && -n $EXPECTED_RESOURCE_GROUP && -n $EXPECTED_CLUSTER_NAME ]] || { usage >&2; exit 2; }
[[ -n $TARGET_NODE && -n $EXPECTED_NVME_DEVICE ]] || { usage >&2; exit 2; }
[[ $TARGET_NODE =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]] || { printf 'invalid TARGET_NODE\n' >&2; exit 2; }
[[ $NAMESPACE =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || { printf 'invalid NAMESPACE\n' >&2; exit 2; }
[[ $EXPECTED_NVME_DEVICE =~ ^/dev/nvme[0-9]+n[0-9]+$ ]] || { printf 'invalid EXPECTED_NVME_DEVICE\n' >&2; exit 2; }
[[ $FORMAT_NVME == yes || $FORMAT_NVME == no ]] || { printf 'FORMAT_NVME must be yes or no\n' >&2; exit 2; }
[[ $PROBE_IMAGE =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || { printf 'PROBE_IMAGE must be immutable\n' >&2; exit 2; }
[[ $HOST_IMAGE =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || { printf 'HOST_IMAGE must be immutable\n' >&2; exit 2; }
for command_name in az kubectl sed awk find file tar; do command -v "$command_name" >/dev/null || { printf '%s is required\n' "$command_name" >&2; exit 2; }; done
[[ ! -e $RESULT_DIR ]] || { printf 'refusing to overwrite %s\n' "$RESULT_DIR" >&2; exit 1; }
mkdir -p "$RESULT_DIR"
chmod 0700 "$RESULT_DIR"
printf 'case\tready_rc\twrite_rc\tcollect_rc\tdelete_rc\tdetach_rc\tverify_rc\ttrace_status\n' >"$RESULT_DIR/case-summary.tsv"
trap cleanup EXIT INT TERM

run_logged client-version "${K[@]}" version --client=true
run_logged context "${K[@]}" config current-context
CURRENT_CONTEXT=$("${K[@]}" config current-context)
[[ -n $CURRENT_CONTEXT ]] || { printf 'explicit kubeconfig has no current context\n' >&2; exit 1; }
CURRENT_SERVER=$("${K[@]}" config view --minify -o jsonpath='{.clusters[0].cluster.server}')
CURRENT_SERVER_HOST=${CURRENT_SERVER#https://}
CURRENT_SERVER_HOST=${CURRENT_SERVER_HOST%%:*}
AKS_FQDN=$(az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$EXPECTED_RESOURCE_GROUP" --name "$EXPECTED_CLUSTER_NAME" --query fqdn -o tsv)
AKS_PRIVATE_FQDN=$(az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$EXPECTED_RESOURCE_GROUP" --name "$EXPECTED_CLUSTER_NAME" --query privateFqdn -o tsv)
[[ $CURRENT_SERVER_HOST == "$AKS_FQDN" || ( -n $AKS_PRIVATE_FQDN && $AKS_PRIVATE_FQDN != null && $CURRENT_SERVER_HOST == "$AKS_PRIVATE_FQDN" ) ]] || {
  printf 'SAFETY_FAILURE: kubeconfig context %s server does not target expected AKS cluster %s/%s\n' "$CURRENT_CONTEXT" "$EXPECTED_RESOURCE_GROUP" "$EXPECTED_CLUSTER_NAME" >&2
  exit 1
}
run_logged target-node "${K[@]}" get node "$TARGET_NODE" -o wide
run_logged target-node-pool "${K[@]}" get node "$TARGET_NODE" -o jsonpath='{.metadata.labels.kubernetes\.azure\.com/agentpool}{"\n"}{.metadata.labels.kubernetes\.azure\.com/storageprofile}{"\n"}'
grep -q '^katapool$' "$RESULT_DIR/target-node-pool.log" || { printf 'SAFETY_FAILURE: target node is not in katapool\n' >&2; exit 1; }
grep -q '^managed$' "$RESULT_DIR/target-node-pool.log" || { printf 'SAFETY_FAILURE: target node does not use a managed OS disk\n' >&2; exit 1; }
run_logged runtime-class "${K[@]}" get runtimeclass kata-vm-isolation -o yaml
run_logged node-ready "${K[@]}" wait --for=condition=Ready "node/$TARGET_NODE" --timeout=60s
check_node_dedicated
if ! run_logged namespace-get "${K[@]}" get namespace "$NAMESPACE"; then
  run_logged create-namespace "${K[@]}" create namespace "$NAMESPACE"
  CREATED_NAMESPACE=true
fi
create_diag_pod
run_image_preflight
check_node_dedicated
HOST_PREP_STARTED=true
host_action host-prepare prepare
run_logged host-state "${K[@]}" exec -n "$NAMESPACE" "$DIAG_POD" -- cat "/host$HOST_ARTIFACT_DIR/state.env"
DEVICE=$(sed -n 's/^LOOP_DEVICE=//p' "$RESULT_DIR/host-state.log" | tail -n1)
[[ $DEVICE =~ ^/dev/loop[0-9]+$ ]] || { printf 'failed to obtain loop device\n' >&2; exit 1; }
host_action host-seed seed

render_manifest A "kata-blk-a-$RUN_LABEL" "$RESULT_DIR/storage.yml"
extract_manifest_doc 1 "$RESULT_DIR/storage.yml" "$RESULT_DIR/pv.yml"
extract_manifest_doc 2 "$RESULT_DIR/storage.yml" "$RESULT_DIR/pvc.yml"
run_logged storage-client-dry-run "${K[@]}" apply --dry-run=client -f "$RESULT_DIR/pv.yml" -f "$RESULT_DIR/pvc.yml"
STORAGE_CREATED=true
run_logged create-storage "${K[@]}" apply -f "$RESULT_DIR/pv.yml" -f "$RESULT_DIR/pvc.yml"
run_logged wait-pvc "${K[@]}" wait -n "$NAMESPACE" --for=jsonpath='{.status.phase}'=Bound "pvc/$PVC_NAME" --timeout=120s

for case_name in A B C D E; do run_case "$case_name"; done

printf 'experiment cases finished; results: %s\n' "$RESULT_DIR"
