#!/usr/bin/env bash

set -Eeuo pipefail

EXPERIMENT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
PURPOSE=kata-direct-volume-ab
TAINT_VALUE=kata-direct-volume-ab
BACKING_SIZE_BYTES=4294967296
RAW_OFFSETS=(4026531840 4043309056)

die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
safety_die() { printf 'SAFETY_FAILURE: %s\n' "$*" >&2; exit 1; }
timestamp() { date -u +%Y-%m-%dT%H:%M:%S.%NZ; }

validate_run_identity() {
  [[ ${RUN_ID:-} =~ ^[a-z0-9]{4,20}$ ]] || die 'RUN_ID must contain 4-20 lowercase letters or digits'
  [[ ${DEPLOYMENT_ID:-} =~ ^[[:xdigit:]]{8}-[[:xdigit:]]{4}-[1-5][[:xdigit:]]{3}-[89abAB][[:xdigit:]]{3}-[[:xdigit:]]{12}$ ]] ||
    die 'DEPLOYMENT_ID must be an opaque RFC 4122 UUID'
  DEPLOYMENT_SUFFIX=$(printf '%s' "$DEPLOYMENT_ID" | tr '[:upper:]' '[:lower:]' | tr -d '-' | cut -c1-10)
  [[ $DEPLOYMENT_SUFFIX =~ ^[0-9a-f]{10}$ ]] || die 'failed to derive compact deployment suffix'
}

derive_names() {
  validate_run_identity
  RESOURCE_GROUP="rg-kdva-${RUN_ID}-${DEPLOYMENT_SUFFIX}"
  NODE_RESOURCE_GROUP="rg-kdva-nodes-${RUN_ID}-${DEPLOYMENT_SUFFIX}"
  CLUSTER_NAME="aks-kdva-${RUN_ID}-${DEPLOYMENT_SUFFIX}"
  ACR_NAME="kdva${RUN_ID}${DEPLOYMENT_SUFFIX}"
  ACR_NAME=${ACR_NAME:0:50}
  NAMESPACE="kdva-${RUN_ID}-${DEPLOYMENT_SUFFIX}"
  [[ ${#RESOURCE_GROUP} -le 90 && ${#NODE_RESOURCE_GROUP} -le 90 && ${#CLUSTER_NAME} -le 63 && ${#NAMESPACE} -le 63 ]] ||
    die 'derived name exceeds platform limits'
}

redact() {
  sed -E \
    -e 's#(/subscriptions/)[0-9a-fA-F-]{36}#\1[REDACTED]#g' \
    -e 's/(Bearer[[:space:]]+)[A-Za-z0-9._~+\/-]+/\1[REDACTED]/Ig' \
    -e 's/((access_?token|refresh_?token|client_?secret|password)["=: ]+)[^ ,"}]+/\1[REDACTED]/Ig' \
    -e 's/eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/[REDACTED-JWT]/g'
}

run_logged() {
  local label=$1 rc out err log
  shift
  out=$(mktemp); err=$(mktemp)
  log="$RESULT_DIR/${label//[^A-Za-z0-9_.-]/_}.log"
  { printf 'COMMAND_START label=%q time=%s command=' "$label" "$(timestamp)"; printf '%q ' "$@"; printf '\n'; } | redact >"$log"
  if "$@" >"$out" 2>"$err"; then rc=0; else rc=$?; fi
  printf '%s\n' '--- stdout ---' >>"$log"; redact <"$out" >>"$log"
  printf '%s\n' '--- stderr ---' >>"$log"; redact <"$err" >>"$log"
  printf 'COMMAND_END label=%q time=%s status=%d\n' "$label" "$(timestamp)" "$rc" >>"$log"
  rm -f "$out" "$err"
  return "$rc"
}

require_tools() {
  local tool
  for tool in "$@"; do command -v "$tool" >/dev/null || die "$tool is required"; done
}

require_immutable_image() {
  [[ $1 =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]] || die "$2 must be an image digest reference"
}

filesystem_workload_command() {
  local root=$1 case_id=$2 expected_marker=${3:-none} own_marker=$4 marker_value=$5
  # Keep workload output in a case-scoped workload directory; only handoff markers live at filesystem root.
  printf '%s' "root='$root'; case_id='$case_id'; expected_marker='$expected_marker'; own_marker='$own_marker'; marker_value='$marker_value'; case_root=\"\$root/.kdva-cases/\$case_id\";
if test \"\$expected_marker\" != none; then test -f \"\$root/.kdva-\$expected_marker\"; test \"\$(cat \"\$root/.kdva-\$expected_marker\")\" = \"${RUN_ID}:${DEVICE_ID}:\$expected_marker\"; fi;
mkdir -p \"\$root/.kdva-cases\"; test ! -e \"\$case_root\"; mkdir \"\$case_root\";
printf '%s' \"\$marker_value\" >\"\$root/.kdva-\$own_marker\"; sync; printf x >\"\$case_root/small-file\"; echo OP_small_file_OK;
dd if=/dev/zero of=\"\$case_root/close-4k\" bs=4096 count=1 status=none; echo OP_4k_close_OK;
dd if=/dev/zero of=\"\$case_root/fsync-4k\" bs=4096 count=1 conv=fsync status=none; echo OP_4k_fsync_OK;
dd if=/dev/zero of=\"\$case_root/file-64m\" bs=1M count=64 conv=fsync status=none; echo OP_64MiB_OK;
mkdir \"\$case_root/files-10k\"; for i in \$(seq 1 10000); do : >\"\$case_root/files-10k/\$i\"; done; sync; echo OP_10k_files_OK;
mv \"\$case_root/small-file\" \"\$case_root/renamed-file\"; rm \"\$case_root/renamed-file\"; echo OP_rename_delete_OK;
fio --name=sync-qd1 --filename=\"\$case_root/fio-sync-qd1\" --size=16M --bs=4k --rw=write --ioengine=sync --iodepth=1 --fsync=1 --output-format=json; echo OP_fio_sync_qd1_OK;
mkdir -p /tmp/tar-source/\$case_id/sub \"\$case_root/tar-extract\"; printf tar-payload >/tmp/tar-source/\$case_id/sub/file; tar -C /tmp/tar-source/\$case_id -cf /tmp/matrix-\$case_id.tar .; tar -C \"\$case_root/tar-extract\" -xf /tmp/matrix-\$case_id.tar; test \"\$(cat \"\$case_root/tar-extract/sub/file\")\" = tar-payload; sync; echo OP_tar_extraction_OK;
sha256sum \"\$root/.kdva-\$own_marker\"; stat -f \"\$root\""
}

assert_kubeconfig_targets_cluster() {
  local server host fqdn private_fqdn
  [[ -f $KUBECONFIG_PATH && ! -L $KUBECONFIG_PATH ]] || safety_die 'explicit kubeconfig must be a regular file'
  server=$(kubectl --kubeconfig "$KUBECONFIG_PATH" config view --minify -o jsonpath='{.clusters[0].cluster.server}')
  host=${server#https://}; host=${host%%:*}
  fqdn=$(az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --query fqdn -o tsv)
  private_fqdn=$(az aks show --subscription "$SUBSCRIPTION_ID" --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --query privateFqdn -o tsv)
  [[ -n $host && ( $host == "$fqdn" || ( $private_fqdn != null && -n $private_fqdn && $host == "$private_fqdn" ) ) ]] ||
    safety_die 'kubeconfig server does not match the exact run cluster FQDN'
}

render_template() {
  local input=$1 output=$2
  shift 2
  cp "$input" "$output"
  while (( $# )); do
    local key=$1 value=$2
    shift 2
    sed -i "s|${key}|${value}|g" "$output"
  done
}
