#!/bin/sh
set -eu

job="$1"
timeout_seconds="${AKS_BURNER_STORAGE_CLEANUP_TIMEOUT:-900}"
request_timeout="${AKS_BURNER_STORAGE_API_TIMEOUT:-10s}"
max_api_failures="${AKS_BURNER_STORAGE_API_RETRIES:-5}"
state_file="../raw/storage-cleanup-${job}.pvs"
kubectl_run() {
  if [ -n "${AKS_BURNER_KUBE_CONTEXT:-}" ]; then
    kubectl --context "$AKS_BURNER_KUBE_CONTEXT" --request-timeout="$request_timeout" "$@"
  else
    kubectl --request-timeout="$request_timeout" "$@"
  fi
}
deadline=$(( $(date +%s) + timeout_seconds ))
api_failures=0

while :; do
  stuck=""
  api_error=""
  while IFS= read -r pv; do
    [ -n "$pv" ] || continue
    if pv_output=$(kubectl_run get persistentvolumes "$pv" 2>&1); then
      stuck="${stuck} persistentvolumes/${pv}"
    else
      case "$pv_output" in
        *NotFound*|*not\ found*) ;;
        *) api_error="persistentvolumes/${pv}: ${pv_output}" ;;
      esac
    fi
    if ! attachments=$(kubectl_run get volumeattachments.storage.k8s.io -o "jsonpath={range .items[?(@.spec.source.persistentVolumeName=='${pv}')]}{.metadata.name}{' '}{end}" 2>&1); then
      api_error="volumeattachments for ${pv}: ${attachments}"
      attachments=""
    fi
    [ -z "$attachments" ] || stuck="${stuck} volumeattachments/${attachments}"
  done < "$state_file"

  if [ -n "$api_error" ]; then
    api_failures=$((api_failures + 1))
    if [ "$api_failures" -ge "$max_api_failures" ]; then
      echo "storage cleanup API checks failed ${api_failures} times at ${api_error}; verify persistentvolumes/volumeattachments cluster RBAC and API availability" >&2
      exit 1
    fi
  elif [ -z "$stuck" ]; then
    exit 0
  else
    api_failures=0
  fi

  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "storage cleanup timed out after ${timeout_seconds}s for ${job}; stuck resources:${stuck:- unavailable due to API errors}; inspect PV finalizers, CSI controller health, and VolumeAttachment detach state" >&2
    exit 1
  fi
  sleep 2
done
