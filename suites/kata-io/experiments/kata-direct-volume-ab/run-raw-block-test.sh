#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

: "${RESULT_DIR:?}" "${KUBECONFIG_PATH:?}" "${NAMESPACE:?}" "${RUN_LABEL:?}" "${TARGET_NODE:?}" "${PROBE_IMAGE:?}"
: "${DEVICE_ID:?}" "${PVC_NAME:?}" "${CASE_NAME:?}" "${TEST_MODE:?}"
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
case "$TEST_MODE" in
  filesystem)
    WORKLOAD=$(filesystem_workload_command /tmp/fs "$CASE_NAME" "${EXPECTED_MARKER:-none}" "$OWN_MARKER" "$MARKER_VALUE")
    COMMAND="dev=/dev/testdisk; test -b \"\$dev\"; test \"\$(blockdev --getsize64 \"\$dev\")\" -eq 4294967296; dd if=\"\$dev\" of=/tmp/raw-read bs=4096 count=1 iflag=direct status=none; echo GUEST_RAW_READ_OK; echo READY_FOR_TEST; until test -e /tmp/release-test; do sleep 1; done; mkdir /tmp/fs; mount -t ext4 \"\$dev\" /tmp/fs; echo EXT4_MOUNT_OK; printf 'PATH_METADATA\\tguest_path=%s\\tfstype=%s\\toptions=%s\\tread_only=%s\\n' \"\$dev\" \"\$(findmnt -T /tmp/fs -n -o FSTYPE)\" \"\$(findmnt -T /tmp/fs -n -o OPTIONS)\" \"\$(findmnt -T /tmp/fs -n -o OPTIONS | grep -Eq '(^|,)ro(,|$)' && echo true || echo false)\"; test -f /tmp/fs/.kata-raw-reserved; test \"\$(dd if=/tmp/fs/.kata-raw-reserved bs=1 count=1 status=none)\" = M; test \"\$(dd if=/tmp/fs/.kata-raw-reserved bs=1 count=1 skip=4096 status=none)\" = N; $WORKLOAD; echo FS_MATRIX_OK; umount /tmp/fs"
    ;;
  raw-diagnostic)
    COMMAND="dev=/dev/testdisk; test -b \"\$dev\"; for pair in ${MARKER_OFFSET_ONE:?}:M ${MARKER_OFFSET_TWO:?}:N; do off=\${pair%%:*}; byte=\${pair##*:}; expected=\$(head -c 4096 /dev/zero | tr '\\0' \"\$byte\" | sha256sum | cut -d' ' -f1); actual=\$(dd if=\"\$dev\" bs=4096 count=1 skip=\$((off/4096)) iflag=direct status=none | sha256sum | cut -d' ' -f1); test \"\$actual\" = \"\$expected\"; done; echo GUEST_RAW_READ_OK; echo READY_FOR_TEST; until test -e /tmp/release-test; do sleep 1; done; for pair in ${RAW_OFFSET_ONE:?}:R ${RAW_OFFSET_TWO:?}:S; do off=\${pair%%:*}; byte=\${pair##*:}; dd if=\"\$dev\" of=/tmp/before bs=4096 count=1 skip=\$((off/4096)) iflag=direct status=none; head -c 4096 /dev/zero | tr '\\0' \"\$byte\" | dd of=\"\$dev\" bs=4096 count=1 seek=\$((off/4096)) oflag=direct conv=notrunc,fsync status=none; dd if=\"\$dev\" of=/tmp/after bs=4096 count=1 skip=\$((off/4096)) iflag=direct status=none; sha256sum /tmp/before /tmp/after; done; echo GUEST_RAW_WRITE_OK"
    ;;
  *) die "unknown TEST_MODE: $TEST_MODE" ;;
esac
render_template "$EXPERIMENT_DIR/manifests/raw-block-probe.yaml" "$CASE_DIR/rendered-pod.yaml" \
  REPLACE_POD_NAME "$POD_NAME" REPLACE_NAMESPACE "$NAMESPACE" REPLACE_RUN_LABEL "$RUN_LABEL" REPLACE_CASE "$CASE_NAME" \
  REPLACE_NODE "$TARGET_NODE" REPLACE_PROBE_IMAGE "$PROBE_IMAGE" REPLACE_PVC_NAME "$PVC_NAME" REPLACE_COMMAND_B64 "$(printf '%s' "$COMMAND" | base64 -w0)"
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
while true; do
  logs=$(kubectl --kubeconfig "$KUBECONFIG_PATH" logs -n "$NAMESPACE" "$POD_NAME" 2>/dev/null || true)
  grep -q '^READY_FOR_TEST$' <<<"$logs" && break
  phase=$(kubectl --kubeconfig "$KUBECONFIG_PATH" get pod -n "$NAMESPACE" "$POD_NAME" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  if [[ $phase == Failed || $phase == Succeeded || $SECONDS -ge $deadline ]]; then
    OUTPUT_DIR="$CASE_DIR" POD_NAME="$POD_NAME" NAMESPACE="$NAMESPACE" KUBECONFIG_PATH="$KUBECONFIG_PATH" "$EXPERIMENT_DIR/collect-guest-state.sh"; exit 1
  fi
  sleep 2
done
trap - EXIT INT TERM
