#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

: "${RESULT_DIR:?}" "${KUBECONFIG_PATH:?}" "${NAMESPACE:?}" "${RUN_LABEL:?}" "${TARGET_NODE:?}" "${PROBE_IMAGE:?}"
: "${DEVICE_ID:?}" "${CASE_NAME:?}" "${WORKSPACE:?}"
: "${OWN_MARKER:?OWN_MARKER is required}" "${MARKER_VALUE:?MARKER_VALUE is required}"
POD_NAME="kdva-${RUN_LABEL}-${CASE_NAME,,}"
CASE_DIR="$RESULT_DIR/$CASE_NAME"; mkdir -p "$CASE_DIR"
POD_UID=
POD_RESOURCE_VERSION=
POD_RESPONSE="$CASE_DIR/pod-created.json"
POD_OWNERSHIP_FILE="$CASE_DIR/pod-ownership.env"
K=(kubectl --kubeconfig "$KUBECONFIG_PATH")
persist_pod_ownership() {
  local tmp="$POD_OWNERSHIP_FILE.tmp.$$"
  printf 'WORKLOAD_CASE_NAME=%q\nWORKLOAD_POD_NAME=%q\nWORKLOAD_POD_UID=%q\nWORKLOAD_POD_RESOURCE_VERSION=%q\n' \
    "$CASE_NAME" "$POD_NAME" "$POD_UID" "$POD_RESOURCE_VERSION" >"$tmp"
  chmod 0600 "$tmp"; mv -f "$tmp" "$POD_OWNERSHIP_FILE"
  cat "$POD_OWNERSHIP_FILE"
}
verify_uncaptured_pod_absent() {
  local json
  json=$("${K[@]}" get pod -n "$NAMESPACE" "$POD_NAME" --ignore-not-found -o json) || return 1
  [[ -z $json ]]
}
handle_create_failure() {
  local rc=$?
  trap - EXIT INT TERM
  if [[ $rc -ne 0 && -z $POD_UID ]] && ! verify_uncaptured_pod_absent; then
    printf 'SAFETY_FAILURE: pod create failed before UID capture and exact-name absence is unproven; refusing to delete unknown object\n' >&2
  fi
  exit "$rc"
}
WORKLOAD=$(filesystem_workload_command /workspace "$CASE_NAME" "${EXPECTED_MARKER:-none}" "$OWN_MARKER" "$MARKER_VALUE")
COMMAND="set -o pipefail; source=\$(findmnt -T /workspace -n -o SOURCE); fstype=\$(findmnt -T /workspace -n -o FSTYPE); options=\$(findmnt -T /workspace -n -o OPTIONS); case \"\$source\" in /dev/vda) echo 'refusing guest root' >&2; exit 1;; /dev/vd*) :;; *) echo \"expected direct /dev/vd* source, got \$source\" >&2; exit 1;; esac; test \"\$fstype\" = ext4; printf 'PATH_METADATA\\tguest_path=%s\\tfstype=%s\\toptions=%s\\tread_only=%s\\n' \"\$source\" \"\$fstype\" \"\$options\" \"\$(printf '%s' \"\$options\" | grep -Eq '(^|,)ro(,|$)' && echo true || echo false)\"; echo EXT4_MOUNT_OK; test -f /workspace/.kata-raw-reserved; test \"\$(dd if=/workspace/.kata-raw-reserved bs=1 count=1 status=none)\" = M; test \"\$(dd if=/workspace/.kata-raw-reserved bs=1 count=1 skip=4096 status=none)\" = N; echo READY_FOR_TEST; until test -e /tmp/release-test; do sleep 1; done; $WORKLOAD; echo FS_MATRIX_OK"
render_template "$EXPERIMENT_DIR/manifests/direct-volume-probe.yaml" "$CASE_DIR/rendered-pod.yaml" \
  REPLACE_POD_NAME "$POD_NAME" REPLACE_NAMESPACE "$NAMESPACE" REPLACE_RUN_LABEL "$RUN_LABEL" REPLACE_CASE "$CASE_NAME" \
  REPLACE_NODE "$TARGET_NODE" REPLACE_PROBE_IMAGE "$PROBE_IMAGE" REPLACE_WORKSPACE "$WORKSPACE" REPLACE_COMMAND_B64 "$(printf '%s' "$COMMAND" | base64 -w0)"
existing=$("${K[@]}" get pod -n "$NAMESPACE" "$POD_NAME" --ignore-not-found -o name) || safety_die 'exact workload pod collision check failed'
[[ -z $existing ]] || safety_die "pre-existing exact workload pod: $NAMESPACE/$POD_NAME"
"${K[@]}" create --dry-run=client -f "$CASE_DIR/rendered-pod.yaml" >/dev/null
printf 'pod-create-attempted\t%s\n' "$POD_NAME" >"$CASE_DIR/pod-create-attempted.tsv"
trap handle_create_failure EXIT INT TERM
"${K[@]}" create -f "$CASE_DIR/rendered-pod.yaml" -o json >"$POD_RESPONSE"
POD_UID=$(jq -r '.metadata.uid // empty' "$POD_RESPONSE")
POD_RESOURCE_VERSION=$(jq -r '.metadata.resourceVersion // empty' "$POD_RESPONSE")
[[ $POD_UID =~ ^[0-9a-f-]{16,64}$ && -n $POD_RESOURCE_VERSION ]] || safety_die 'created workload pod identity is absent'
persist_pod_ownership
printf 'pod-created\t%s\n' "$POD_NAME" >"$CASE_DIR/pod-created.tsv"
deadline=$((SECONDS + 300))
until kubectl --kubeconfig "$KUBECONFIG_PATH" logs -n "$NAMESPACE" "$POD_NAME" 2>/dev/null | grep -q '^READY_FOR_TEST$'; do
  phase=$(kubectl --kubeconfig "$KUBECONFIG_PATH" get pod -n "$NAMESPACE" "$POD_NAME" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  if [[ $phase == Failed || $phase == Succeeded || $SECONDS -ge $deadline ]]; then
    OUTPUT_DIR="$CASE_DIR" POD_NAME="$POD_NAME" "$EXPERIMENT_DIR/collect-guest-state.sh"
    if grep -Eqi 'Input/output error|EIO' "$CASE_DIR"/*; then printf 'mount-evidence\tEIO\n' >"$CASE_DIR/classification.tsv"
    elif grep -Eqi 'No such file|ENOENT' "$CASE_DIR"/*; then printf 'mount-evidence\tENOENT\n' >"$CASE_DIR/classification.tsv"
    elif grep -Eqi 'permission denied|EPERM|EACCES' "$CASE_DIR"/*; then printf 'mount-evidence\tPERMISSION\n' >"$CASE_DIR/classification.tsv"
    else printf 'mount-evidence\tUNKNOWN_OR_INSUFFICIENT\n' >"$CASE_DIR/classification.tsv"; fi
    exit 1
  fi
  sleep 2
done
trap - EXIT INT TERM
