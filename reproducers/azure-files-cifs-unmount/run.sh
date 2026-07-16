#!/usr/bin/env bash
set -euo pipefail

KUBE_CONTEXT="${KUBE_CONTEXT:-aks-cifs-unmount-repro}"
WATCH_SECONDS="${WATCH_SECONDS:-180}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-1200}"
OUTPUT_DIR="${OUTPUT_DIR:-cifs-unmount-evidence-$(date -u +%Y%m%dt%H%M%S)}"
NAMESPACE="cifs-unmount-repro"
SELECTOR="repro.azure.com/target=cifs-unmount"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
KUBECTL=(kubectl --context "$KUBE_CONTEXT")

command -v kubectl >/dev/null 2>&1 || { printf 'error: kubectl is required\n' >&2; exit 1; }
[[ "$WATCH_SECONDS" =~ ^[1-9][0-9]*$ ]] || { printf 'error: WATCH_SECONDS must be a positive integer\n' >&2; exit 1; }
[[ "$TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ ]] || { printf 'error: TIMEOUT_SECONDS must be a positive integer\n' >&2; exit 1; }

read -r node_name extra_node <<< "$("${KUBECTL[@]}" get nodes -l "$SELECTOR" -o jsonpath='{.items[*].metadata.name}')"
[[ -n "$node_name" && -z "$extra_node" ]] || {
  printf 'error: expected exactly one node matching %s\n' "$SELECTOR" >&2
  exit 1
}

ready_status() {
  "${KUBECTL[@]}" get node "$node_name" \
    -o go-template='{{range .status.conditions}}{{if eq .type "Ready"}}{{.status}}{{end}}{{end}}'
}

kernel_bug_total() {
  local count
  local total=0
  while read -r count; do
    [[ "$count" =~ ^[1-9][0-9]*$ ]] || count=1
    total=$((total + count))
  done < <("${KUBECTL[@]}" get events --all-namespaces \
    --field-selector "involvedObject.kind=Node,involvedObject.name=${node_name}" \
    -o go-template='{{range .items}}{{if eq .reason "KernelBug"}}{{if .count}}{{.count}}{{else}}1{{end}}{{"\n"}}{{end}}{{end}}')
  printf '%s' "$total"
}

capture() {
  local prefix="$1"
  mkdir -p "$OUTPUT_DIR"
  "${KUBECTL[@]}" get node "$node_name" -o yaml > "$OUTPUT_DIR/${prefix}-node.yaml" 2>&1 || true
  "${KUBECTL[@]}" get events --all-namespaces \
    --field-selector "involvedObject.kind=Node,involvedObject.name=${node_name}" \
    --sort-by=.metadata.creationTimestamp -o yaml > "$OUTPUT_DIR/${prefix}-node-events.yaml" 2>&1 || true
  "${KUBECTL[@]}" -n kube-node-lease get lease "$node_name" -o yaml \
    > "$OUTPUT_DIR/${prefix}-node-lease.yaml" 2>&1 || true
  "${KUBECTL[@]}" -n "$NAMESPACE" get jobs,pods,persistentvolumeclaims -o yaml \
    > "$OUTPUT_DIR/${prefix}-workload.yaml" 2>&1 || true
  for job in fio-randread-0 fio-randread-1; do
    "${KUBECTL[@]}" --request-timeout=20s -n "$NAMESPACE" logs "job/${job}" \
      > "$OUTPUT_DIR/${prefix}-${job}.log" 2>&1 || true
  done
}

if "${KUBECTL[@]}" get namespace "$NAMESPACE" >/dev/null 2>&1; then
  printf 'error: namespace %s already exists; preserve it or delete it before another run\n' "$NAMESPACE" >&2
  exit 1
fi

[[ "$(ready_status)" == "True" ]] || {
  printf 'error: target node %s is not Ready\n' "$node_name" >&2
  exit 1
}

os_sku="$("${KUBECTL[@]}" get node "$node_name" -o jsonpath='{.metadata.labels.kubernetes\.azure\.com/os-sku}')"
kata_capable="$("${KUBECTL[@]}" get node "$node_name" -o jsonpath='{.metadata.labels.kubernetes\.azure\.com/kata-vm-isolation}')"
if [[ "$os_sku" != "AzureLinux" || "$kata_capable" != "true" ]]; then
  printf 'error: target node %s is not the expected Azure Linux Kata-capable disposable node\n' \
    "$node_name" >&2
  exit 1
fi

provisioner="$("${KUBECTL[@]}" get storageclass azurefile-csi -o jsonpath='{.provisioner}')"
[[ "$provisioner" == "file.csi.azure.com" ]] || {
  printf 'error: azurefile-csi uses unexpected provisioner %s\n' "$provisioner" >&2
  exit 1
}

baseline_bugs="$(kernel_bug_total)"
mkdir -p "$OUTPUT_DIR"
"${KUBECTL[@]}" get node "$node_name" -o yaml > "$OUTPUT_DIR/before-node.yaml"
"${KUBECTL[@]}" get storageclass azurefile-csi -o yaml > "$OUTPUT_DIR/storageclass.yaml"
"${KUBECTL[@]}" get runtimeclass -o yaml > "$OUTPUT_DIR/runtimeclasses.yaml"

printf 'Context:      %s\n' "$KUBE_CONTEXT"
printf 'Target node:  %s\n' "$node_name"
printf 'OS image:     %s\n' "$("${KUBECTL[@]}" get node "$node_name" -o jsonpath='{.status.nodeInfo.osImage}')"
printf 'Kernel:       %s\n' "$("${KUBECTL[@]}" get node "$node_name" -o jsonpath='{.status.nodeInfo.kernelVersion}')"
printf 'RuntimeClass: <unset>; containerd default runtime expected\n'
printf 'Evidence:     %s\n' "$OUTPUT_DIR"

for ((attempt = 1; attempt <= 10; attempt++)); do
  printf 'Starting attempt %d of 10.\n' "$attempt"
  "${KUBECTL[@]}" apply -f "$SCRIPT_DIR/workload.yaml"

  deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
  while true; do
    ready="$(ready_status)"
    current_bugs="$(kernel_bug_total)"
    if [[ "$ready" != "True" ]] || (( current_bugs > baseline_bugs )); then
      capture "attempt-${attempt}-trigger"
      printf 'trigger observed on attempt %d: Ready=%s, KernelBug events=%d -> %d\n' \
        "$attempt" "$ready" "$baseline_bugs" "$current_bugs" >&2
      exit 2
    fi

    complete="$("${KUBECTL[@]}" -n "$NAMESPACE" get jobs -l app=cifs-unmount-repro \
      -o go-template='{{range .items}}{{range .status.conditions}}{{if and (eq .type "Complete") (eq .status "True")}}x{{"\n"}}{{end}}{{end}}{{end}}' \
      | wc -l | tr -d '[:space:]')"
    failed="$("${KUBECTL[@]}" -n "$NAMESPACE" get jobs -l app=cifs-unmount-repro \
      -o go-template='{{range .items}}{{range .status.conditions}}{{if and (eq .type "Failed") (eq .status "True")}}x{{"\n"}}{{end}}{{end}}{{end}}' \
      | wc -l | tr -d '[:space:]')"
    (( failed == 0 )) || { capture "attempt-${attempt}-job-failure"; printf 'error: an FIO Job failed\n' >&2; exit 1; }
    (( complete == 2 )) && break
    (( $(date +%s) < deadline )) || { capture "attempt-${attempt}-timeout"; printf 'error: timed out waiting for FIO Jobs\n' >&2; exit 1; }
    sleep 5
  done

  printf 'Both FIO containers exited successfully; watching CIFS teardown for %s seconds.\n' "$WATCH_SECONDS"
  for ((elapsed = 0; elapsed < WATCH_SECONDS; elapsed += 5)); do
    ready="$(ready_status)"
    current_bugs="$(kernel_bug_total)"
    if [[ "$ready" != "True" ]] || (( current_bugs > baseline_bugs )); then
      capture "attempt-${attempt}-trigger"
      printf 'trigger observed after FIO exit on attempt %d: Ready=%s, KernelBug events=%d -> %d\n' \
        "$attempt" "$ready" "$baseline_bugs" "$current_bugs" >&2
      exit 2
    fi
    sleep 5
  done

  capture "attempt-${attempt}-healthy"
  if (( attempt < 10 )); then
    "${KUBECTL[@]}" delete namespace "$NAMESPACE"
    "${KUBECTL[@]}" wait --for=delete "namespace/${NAMESPACE}" --timeout="${TIMEOUT_SECONDS}s"
  fi
done

printf 'No trigger observed after 10 attempts. Preserve %s for comparison or delete it before another run.\n' "$NAMESPACE"
