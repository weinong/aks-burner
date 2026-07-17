#!/bin/sh
set -eu

job="$1"
timeout_seconds="${AKS_BURNER_STORAGE_CLEANUP_TIMEOUT:-900}"
request_timeout="${AKS_BURNER_STORAGE_API_TIMEOUT:-10s}"
state_file="../raw/storage-cleanup-${job}.pvs"
kubectl_run() {
  if [ -n "${AKS_BURNER_KUBE_CONTEXT:-}" ]; then
    kubectl --context "$AKS_BURNER_KUBE_CONTEXT" --request-timeout="$request_timeout" "$@"
  else
    kubectl --request-timeout="$request_timeout" "$@"
  fi
}

if ! kubectl_run get persistentvolumeclaims --all-namespaces -l "kube-burner.io/job=${job}" -o 'jsonpath={range .items[*]}{.spec.volumeName}{"\n"}{end}' > "$state_file"; then
  echo "storage cleanup capture failed for ${job}; verify persistentvolumeclaim list RBAC and API availability (deadline ${timeout_seconds}s)" >&2
  exit 1
fi
