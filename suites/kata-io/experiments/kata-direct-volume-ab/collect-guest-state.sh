#!/usr/bin/env bash
set -Eeuo pipefail

: "${KUBECONFIG_PATH:?KUBECONFIG_PATH is required}" "${NAMESPACE:?NAMESPACE is required}" "${POD_NAME:?POD_NAME is required}" "${OUTPUT_DIR:?OUTPUT_DIR is required}"
mkdir -p "$OUTPUT_DIR"
collected=0
if ! kubectl --kubeconfig "$KUBECONFIG_PATH" get pod -n "$NAMESPACE" "$POD_NAME" >/dev/null 2>&1; then
  if [[ ! -e $OUTPUT_DIR/pod-apply-attempted.tsv ]]; then
    printf 'pod-never-created\t%s\n' "$POD_NAME" >"$OUTPUT_DIR/pod-never-created.tsv"
    printf 'status\tcomplete\treason\tpod-never-created\n' >"$OUTPUT_DIR/collection-status.tsv"
    exit 0
  fi
  printf 'status\tinsufficient\treason\tpod-absent-after-create-attempt\n' >"$OUTPUT_DIR/collection-status.tsv"
  exit 1
fi
if kubectl --kubeconfig "$KUBECONFIG_PATH" get pod -n "$NAMESPACE" "$POD_NAME" -o yaml >"$OUTPUT_DIR/pod.yaml" 2>"$OUTPUT_DIR/pod-yaml.stderr"; then collected=$((collected + 1)); fi
if kubectl --kubeconfig "$KUBECONFIG_PATH" describe pod -n "$NAMESPACE" "$POD_NAME" >"$OUTPUT_DIR/describe.txt" 2>&1; then collected=$((collected + 1)); fi
if kubectl --kubeconfig "$KUBECONFIG_PATH" get events -n "$NAMESPACE" --field-selector "involvedObject.name=$POD_NAME" --sort-by=.lastTimestamp >"$OUTPUT_DIR/events.txt" 2>&1; then collected=$((collected + 1)); fi
if kubectl --kubeconfig "$KUBECONFIG_PATH" logs -n "$NAMESPACE" "$POD_NAME" >"$OUTPUT_DIR/guest.log" 2>"$OUTPUT_DIR/guest-log.stderr"; then collected=$((collected + 1)); fi
if [[ $collected -eq 4 && -s $OUTPUT_DIR/pod.yaml && -s $OUTPUT_DIR/describe.txt && -s $OUTPUT_DIR/events.txt && -s $OUTPUT_DIR/guest.log ]]; then
  printf 'status\tcomplete\treason\trequired-guest-artifacts-present\n' >"$OUTPUT_DIR/collection-status.tsv"
  exit 0
fi
printf 'status\tinsufficient\treason\tmissing-or-empty-required-guest-artifact\n' >"$OUTPUT_DIR/collection-status.tsv"
exit 1
