#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/lib.sh"

: "${RESULT_DIR:?RESULT_DIR is required}" "${KUBECONFIG_PATH:?KUBECONFIG_PATH is required}" "${NAMESPACE:?NAMESPACE is required}"
: "${RUN_LABEL:?RUN_LABEL is required}" "${TARGET_NODE:?TARGET_NODE is required}" "${LOOP_DEVICE:?LOOP_DEVICE is required}"
PV_NAME="kdva-${RUN_LABEL}-${DEVICE_ID}"
PVC_NAME="$PV_NAME"
K=(kubectl --kubeconfig "$KUBECONFIG_PATH")
PV_UID=
PVC_UID=
PV_RESPONSE="$RESULT_DIR/$DEVICE_ID-pv-created.json"
PVC_RESPONSE="$RESULT_DIR/$DEVICE_ID-pvc-created.json"

emit_state() {
  printf 'PV_NAME=%q\nPVC_NAME=%q\nPV_UID=%q\nPVC_UID=%q\nLOOP_DEVICE=%q\n' \
    "$PV_NAME" "$PVC_NAME" "$PV_UID" "$PVC_UID" "$LOOP_DEVICE"
}

delete_created_uid() {
  local kind=$1 name=$2 uid=$3 current_uid body namespace_args=() path
  [[ -n $uid ]] || return 0
  [[ $kind != pvc ]] || namespace_args=(-n "$NAMESPACE")
  current_uid=$("${K[@]}" get "$kind" "${namespace_args[@]}" "$name" --ignore-not-found -o json | jq -r '.metadata.uid // empty') || return 1
  [[ -n $current_uid ]] || return 0
  [[ $current_uid == "$uid" ]] || return 1
  if [[ $kind == pvc ]]; then path="/api/v1/namespaces/$NAMESPACE/persistentvolumeclaims/$name"; else path="/api/v1/persistentvolumes/$name"; fi
  body=$(jq -cn --arg uid "$uid" '{apiVersion:"v1",kind:"DeleteOptions",preconditions:{uid:$uid},propagationPolicy:"Foreground"}')
  "${K[@]}" delete --raw "$path" -f - <<<"$body" >/dev/null || return 1
  for _ in {1..120}; do
    current_uid=$("${K[@]}" get "$kind" "${namespace_args[@]}" "$name" --ignore-not-found -o json | jq -r '.metadata.uid // empty') || return 1
    [[ $current_uid != "$uid" ]] && return 0
    sleep 1
  done
  return 1
}

rollback_partial_creation() {
  local rc=$? pvc_removed=true
  trap - EXIT INT TERM
  if [[ $rc -ne 0 ]]; then
    [[ -n $PVC_UID || ! -s $PVC_RESPONSE ]] || PVC_UID=$(jq -r '.metadata.uid // empty' "$PVC_RESPONSE")
    [[ -n $PV_UID || ! -s $PV_RESPONSE ]] || PV_UID=$(jq -r '.metadata.uid // empty' "$PV_RESPONSE")
    emit_state
    delete_created_uid pvc "$PVC_NAME" "$PVC_UID" || {
      pvc_removed=false
      printf 'SAFETY_FAILURE: failed to remove partially created PVC UID %s; preserving PV and device state\n' "$PVC_UID" >&2
    }
    if [[ $pvc_removed == true ]]; then
      delete_created_uid pv "$PV_NAME" "$PV_UID" ||
        printf 'SAFETY_FAILURE: failed to remove partially created PV UID %s; preserving device state\n' "$PV_UID" >&2
    fi
  fi
  exit "$rc"
}

require_tools jq kubectl
emit_state
existing=$("${K[@]}" get pv "$PV_NAME" --ignore-not-found -o name) || safety_die 'exact PV collision check failed'
[[ -z $existing ]] || safety_die "pre-existing exact PV: $PV_NAME"
existing=$("${K[@]}" get pvc -n "$NAMESPACE" "$PVC_NAME" --ignore-not-found -o name) || safety_die 'exact PVC collision check failed'
[[ -z $existing ]] || safety_die "pre-existing exact PVC: $NAMESPACE/$PVC_NAME"
render_template "$EXPERIMENT_DIR/manifests/raw-block-pv.yaml" "$RESULT_DIR/$DEVICE_ID-pv.yaml" \
  REPLACE_PV_NAME "$PV_NAME" REPLACE_PVC_NAME "$PVC_NAME" REPLACE_NAMESPACE "$NAMESPACE" REPLACE_RUN_LABEL "$RUN_LABEL" \
  REPLACE_LOOP_DEVICE "$LOOP_DEVICE" REPLACE_NODE "$TARGET_NODE"
render_template "$EXPERIMENT_DIR/manifests/raw-block-pvc.yaml" "$RESULT_DIR/$DEVICE_ID-pvc.yaml" \
  REPLACE_PV_NAME "$PV_NAME" REPLACE_PVC_NAME "$PVC_NAME" REPLACE_NAMESPACE "$NAMESPACE" REPLACE_RUN_LABEL "$RUN_LABEL"
"${K[@]}" create --dry-run=client -f "$RESULT_DIR/$DEVICE_ID-pv.yaml" -f "$RESULT_DIR/$DEVICE_ID-pvc.yaml" >/dev/null
trap rollback_partial_creation EXIT INT TERM
"${K[@]}" create -f "$RESULT_DIR/$DEVICE_ID-pv.yaml" -o json >"$PV_RESPONSE"
PV_UID=$(jq -r '.metadata.uid // empty' "$PV_RESPONSE")
[[ -n $PV_UID ]] || safety_die 'created PV UID is absent'
emit_state
"${K[@]}" create -f "$RESULT_DIR/$DEVICE_ID-pvc.yaml" -o json >"$PVC_RESPONSE"
PVC_UID=$(jq -r '.metadata.uid // empty' "$PVC_RESPONSE")
[[ -n $PVC_UID ]] || safety_die 'created PVC UID is absent'
emit_state
"${K[@]}" wait -n "$NAMESPACE" --for=jsonpath='{.status.phase}'=Bound "pvc/$PVC_NAME" --timeout=120s >/dev/null
trap - EXIT INT TERM
