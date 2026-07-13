#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

ACTION=${1:?action is required}
RUN_ID=${2:?run ID is required}
DEVICE_ID=${3:?device ID is required}
EXPECTED_NVME_DEVICE=${4:-}
FORMAT_NVME=${5:-no}
CASE_ID=${6:-host}
MARKER_NAME=${7:-}
MARKER_VALUE=${8:-}
PURPOSE=kata-direct-volume-ab
BASE_DIR=/var/log/kata-direct-volume-ab
RUN_DIR="$BASE_DIR/$RUN_ID"
DEVICE_DIR="$RUN_DIR/devices/$DEVICE_ID"
STATE_FILE="$DEVICE_DIR/state.env"
OWNER_ROOT=/run/kata-direct-volume-ab
OWNER_FILE="$OWNER_ROOT/owner"
LOCK_FILE="$OWNER_ROOT/lock"
NVME_MOUNT=/mnt/kata-direct-volume-ab
BACKING_DIR="$NVME_MOUNT/$RUN_ID"
BACKING_FILE="$BACKING_DIR/$DEVICE_ID.raw"
MOUNT_OWNER_MARKER="$RUN_DIR/nvme-mounted-by-run"
BACKING_SIZE_BYTES=4294967296
DIRECT_ROOT="$OWNER_ROOT/$RUN_ID"
WORKSPACE="$DIRECT_ROOT/workspace"
DIRECT_METADATA_ROOT=/run/kata-containers/shared/direct-volumes
MOUNT_INFO_COPY="$DEVICE_DIR/mountInfo.json"
COMMAND_DIR="$DEVICE_DIR/commands"
RUN_CMD_ECHO=${RUN_CMD_ECHO:-0}
OCI_METADATA_FILTER=$(cat <<'JQ'
($needle | if test("^[0-9]+:[0-9]+$") then capture("^(?<major>[0-9]+):(?<minor>[0-9]+)$") else null end) as $mm |
def concrete_device_reference:
  if $mm != null then
    any(.linux.devices[]?;
      .type == "b" and (.major | tostring) == $mm.major and (.minor | tostring) == $mm.minor)
  else
    any(.linux.devices[]?; .path == $needle)
  end;
concrete_device_reference or
(del(.linux.devices, .linux.resources.devices, .linux.maskedPaths, .linux.readonlyPaths) |
 any(.. | strings; . == $needle))
JQ
)

die() { printf 'SAFETY_FAILURE: %s\n' "$*" >&2; exit 1; }
outcome_die() { printf 'EXPERIMENT_FAILURE: %s\n' "$*" >&2; exit 2; }
timestamp() { date -u +%Y-%m-%dT%H:%M:%S.%NZ; }

run_cmd() {
  local label=$1 rc stem
  shift
  stem="$COMMAND_DIR/$(date -u +%Y%m%dT%H%M%S)-$$-${label//[^A-Za-z0-9_.-]/_}"
  mkdir -p "$COMMAND_DIR"
  { printf 'COMMAND_START label=%q time=%s command=' "$label" "$(timestamp)"; printf '%q ' "$@"; printf '\n'; } >"$stem.meta"
  if "$@" >"$stem.stdout" 2>"$stem.stderr"; then rc=0; else rc=$?; fi
  [[ $RUN_CMD_ECHO != 1 ]] || cat "$stem.stdout"
  cat "$stem.stderr" >&2
  printf 'COMMAND_END label=%q time=%s status=%d\n' "$label" "$(timestamp)" "$rc" >>"$stem.meta"
  LAST_STDOUT="$stem.stdout"; LAST_STDERR="$stem.stderr"
  return "$rc"
}

state_value() { printf '%s=%q\n' "$1" "${2-}"; }
save_state() {
  local tmp="$STATE_FILE.tmp.$$"
  {
    state_value PHASE "${PHASE:-unknown}"
    state_value NVME_DEVICE "${NVME_DEVICE:-}"
    state_value NVME_MAJMIN "${NVME_MAJMIN:-}"
    state_value RUN_MOUNTED_NVME "${RUN_MOUNTED_NVME:-0}"
    state_value LOOP_DEVICE "${LOOP_DEVICE:-}"
    state_value LOOP_MAJMIN "${LOOP_MAJMIN:-}"
    state_value FILESYSTEM_UUID "${FILESYSTEM_UUID:-}"
    state_value MARKER_OFFSET_ONE "${MARKER_OFFSET_ONE:-}"
    state_value MARKER_OFFSET_TWO "${MARKER_OFFSET_TWO:-}"
    state_value RAW_OFFSET_ONE "${RAW_OFFSET_ONE:-}"
    state_value RAW_OFFSET_TWO "${RAW_OFFSET_TWO:-}"
    state_value KATA_RUNTIME "${KATA_RUNTIME:-}"
    state_value KATA_CONFIG "${KATA_CONFIG:-}"
    state_value CONFIG_BACKUP "${CONFIG_BACKUP:-}"
    state_value CONFIG_HASH_ORIGINAL "${CONFIG_HASH_ORIGINAL:-}"
    state_value CONFIG_HASH_MODIFIED "${CONFIG_HASH_MODIFIED:-}"
    state_value CONFIG_CHANGED "${CONFIG_CHANGED:-0}"
    state_value CONFIG_RESTART_REQUIRED "${CONFIG_RESTART_REQUIRED:-0}"
    state_value DIRECT_REGISTERED "${DIRECT_REGISTERED:-0}"
    state_value DIRECT_REGISTRATION_PENDING "${DIRECT_REGISTRATION_PENDING:-0}"
    state_value DIRECT_PRECONDITION_PROVEN "${DIRECT_PRECONDITION_PROVEN:-0}"
    state_value DIRECT_ADD_BOUNDARY_CROSSED "${DIRECT_ADD_BOUNDARY_CROSSED:-0}"
  } >"$tmp"
  chmod 0600 "$tmp"; mv -f "$tmp" "$STATE_FILE"
}
load_state() {
  [[ -f $STATE_FILE && ! -L $STATE_FILE && $(stat -c %u "$STATE_FILE") -eq 0 && $(stat -c %a "$STATE_FILE") == 600 ]] || die "trusted state is missing: $STATE_FILE"
  # save_state emits only shell-escaped values into a root-owned 0600 file.
  source "$STATE_FILE"
}
hash_file() { sha256sum "$1" | awk '{print $1}'; }

validate_common() {
  [[ $EUID -eq 0 ]] || die 'host action must run as root in the host mount namespace'
  [[ $RUN_ID =~ ^[a-z0-9]{4,20}$ && $DEVICE_ID =~ ^[a-z0-9][a-z0-9-]{0,30}$ ]] || die 'invalid run or device identity'
  [[ $FORMAT_NVME == yes || $FORMAT_NVME == no ]] || die 'FORMAT_NVME must be yes or no'
  local tool
  [[ $CASE_ID =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,80}$ ]] || die 'invalid case identity'
  for tool in awk base64 blkid blockdev cmp cp cut dd debugfs e2fsck fallocate filefrag find findmnt flock fuser grep head install jq journalctl losetup lsblk mkfs.ext4 mount mountpoint readlink setsid sha256sum sort stat sync systemctl timeout tr umount wipefs xargs; do
    command -v "$tool" >/dev/null || die "host tool is missing: $tool"
  done
  mkdir -p "$OWNER_ROOT"
  exec 9>"$LOCK_FILE"; flock -n 9 || die 'another host action owns the experiment lock'
}

claim_owner() {
  local expected="$RUN_ID" current tmp
  if [[ -f $OWNER_FILE ]]; then
    current=$(<"$OWNER_FILE"); [[ $current == "$expected" ]] || die "node is owned by another run: $current"
    return
  fi
  tmp="$OWNER_FILE.tmp.$$"; printf '%s\n' "$expected" >"$tmp"; chmod 0600 "$tmp"
  mv -n "$tmp" "$OWNER_FILE" || true; rm -f "$tmp"
  [[ -f $OWNER_FILE && $(<"$OWNER_FILE") == "$expected" ]] || die 'failed to acquire run owner'
}
require_owner() { [[ -f $OWNER_FILE && $(<"$OWNER_FILE") == "$RUN_ID" ]] || die 'run does not own this node'; }

parent_disk() {
  local current type parent
  current=$(readlink -f "$1")
  while :; do
    type=$(lsblk -ndo TYPE "$current" | xargs)
    [[ $type == disk ]] && { printf '%s\n' "$current"; return; }
    parent=$(lsblk -ndo PKNAME "$current" | awk 'NF {print $1; exit}')
    [[ -n $parent ]] || return 1; current="/dev/$parent"
  done
}
critical_disk() {
  local source majmin
  source=$(findmnt -T "$1" -n -o SOURCE); source=${source%%\[*}
  if [[ $source != /dev/* || ! -b $source ]]; then
    majmin=$(findmnt -T "$1" -n -o MAJ:MIN)
    source=$(lsblk -rno PATH,MAJ:MIN | awk -v mm="$majmin" '$2 == mm {print $1; exit}')
  fi
  parent_disk "$source"
}
assert_empty_sysfs() {
  local path
  for path in "/sys/class/block/${1##*/}/$2/"*; do [[ ! -e $path ]] || die "$1 has $2 ${path##*/}"; done
}

validate_nvme() {
  local critical disk type transport rota descendants findmnt_rc=0 users_rc=0 mounted_source users
  [[ $EXPECTED_NVME_DEVICE =~ ^/dev/nvme[0-9]+n[0-9]+$ && -b $EXPECTED_NVME_DEVICE ]] || die 'exact whole local NVMe device is required'
  NVME_DEVICE=$(readlink -f "$EXPECTED_NVME_DEVICE"); [[ $NVME_DEVICE == "$EXPECTED_NVME_DEVICE" ]] || die 'NVMe path must be canonical'
  type=$(lsblk -ndo TYPE "$NVME_DEVICE" | xargs); transport=$(lsblk -ndo TRAN "$NVME_DEVICE" | xargs); rota=$(lsblk -ndo ROTA "$NVME_DEVICE" | xargs)
  [[ $type == disk && $transport == nvme && $rota == 0 ]] || die "device is not a whole local NVMe namespace: $type/$transport/$rota"
  NVME_MAJMIN=$(lsblk -ndo MAJ:MIN "$NVME_DEVICE" | xargs)
  for critical in / /var/lib/kubelet /var/lib/containerd; do
    [[ -e $critical ]] || die "critical path is absent: $critical"
    disk=$(critical_disk "$critical") || die "cannot resolve critical disk for $critical"
    [[ $disk != "$NVME_DEVICE" ]] || die "$NVME_DEVICE contains $critical"
  done
  descendants=$(lsblk -nrpo NAME "$NVME_DEVICE" | wc -l); [[ $descendants -eq 1 ]] || die 'NVMe has partitions or child devices'
  assert_empty_sysfs "$NVME_DEVICE" holders; assert_empty_sysfs "$NVME_DEVICE" slaves
  run_cmd nvme-mounts findmnt -rn -S "$NVME_DEVICE" -o TARGET,SOURCE,FSTYPE || findmnt_rc=$?
  [[ $findmnt_rc -eq 1 && ! -s $LAST_STDOUT ]] || {
    [[ $findmnt_rc -eq 0 && $(awk 'END {print NR}' "$LAST_STDOUT") -eq 1 && $(awk '{print $1}' "$LAST_STDOUT") == "$NVME_MOUNT" ]] || die 'NVMe has an unexpected mount';
    mounted_source=$(findmnt -M "$NVME_MOUNT" -n -o SOURCE 2>/dev/null || true)
    [[ $(readlink -f "$mounted_source") == "$NVME_DEVICE" ]] || die 'experiment mountpoint has another source'
    [[ -f $MOUNT_OWNER_MARKER && ! -L $MOUNT_OWNER_MARKER && $(<"$MOUNT_OWNER_MARKER") == "$RUN_ID" ]] ||
      die 'owned NVMe mount marker is absent or belongs to another run'
    [[ -f $OWNER_FILE && $(<"$OWNER_FILE") == "$RUN_ID" ]] || die 'mounted NVMe owner does not match RUN_ID'
  }
  ! awk -v dev="$NVME_DEVICE" 'NR > 1 && $1 == dev {found=1} END {exit !found}' /proc/swaps || die 'NVMe is swap'
  find_block_device_fds "$DEVICE_DIR/nvme-process-fds.tsv" "$NVME_MAJMIN"
  [[ ! -s $DEVICE_DIR/nvme-process-fds.tsv ]] || die 'NVMe has unknown raw block users'
  run_cmd nvme-users fuser "$NVME_DEVICE" || users_rc=$?
  users=$(tr -s '[:space:]' ' ' <"$LAST_STDOUT" | xargs)
  [[ -z $users ]] || die "NVMe has unknown users: $users"
  [[ $users_rc -eq 1 || ( $users_rc -eq 0 && $(findmnt -rn -S "$NVME_DEVICE" -o TARGET) == "$NVME_MOUNT" ) ]] ||
    die "unexpected NVMe fuser result: $users_rc"
}

find_block_device_fds() {
  local output=$1 majmin=$2 proc fd hex major minor
  : >"$output"
  for proc in /proc/[0-9]*; do
    [[ -d $proc/fd ]] || continue
    for fd in "$proc"/fd/*; do
      [[ -e $fd ]] || continue
      hex=$(stat -Lc '%t:%T' "$fd" 2>/dev/null || true); [[ $hex == *:* ]] || continue
      major=$((16#${hex%%:*})); minor=$((16#${hex##*:}))
      if [[ $major -eq ${majmin%%:*} && $minor -eq ${majmin##*:} ]]; then
        printf '%s\t%s\t%s\t%s\t%s\n' "${proc##*/}" "${fd##*/}" "$(awk '{print $22}' "$proc/stat" 2>/dev/null)" \
          "$(readlink "$proc/exe" 2>/dev/null) $(tr '\0' ' ' <"$proc/cmdline" 2>/dev/null)" "$(readlink "$fd" 2>/dev/null)" >>"$output"
      fi
    done
  done
}
find_device_fds() { find_block_device_fds "$1" "$LOOP_MAJMIN"; }
metadata_references_device() {
  local needle scan_rc
  for needle in "$LOOP_DEVICE" "$BACKING_FILE" "$LOOP_MAJMIN"; do
    if metadata_references_value "$needle"; then return 0; else scan_rc=$?; fi
    [[ $scan_rc -eq 1 ]] || return 2
  done
  return 1
}
metadata_references_value() {
  local root scan_rc
  for root in /run/vc/sbs /run/kata-containers/sandboxes /run/containerd/io.containerd.runtime.v2.task; do
    if metadata_root_references_value "$root" "$1"; then return 0; else scan_rc=$?; fi
    [[ $scan_rc -eq 1 ]] || return 2
  done
  return 1
}
metadata_root_references_value() {
  local root=$1 needle=$2 match_file=${3:-} list_file error_file mounts mountpoint file scan_rc started remaining
  local -a find_args
  [[ ! -e $root && ! -L $root ]] && return 1
  [[ -d $root && ! -L $root ]] || return 2
  list_file="$COMMAND_DIR/metadata-scan-files.$$"; error_file="$COMMAND_DIR/metadata-scan-errors.$$"
  rm -f -- "$list_file" "$error_file"
  if mounts=$(findmnt -rn -R -e -o TARGET "$root" 2>"$error_file"); then
    :
  else
    scan_rc=$?
    [[ $scan_rc -eq 1 ]] || { rm -f -- "$list_file" "$error_file"; return 2; }
  fi
  find_args=(-P "$root" -xdev)
  while IFS= read -r mountpoint; do
    [[ $mountpoint == "$root/"* ]] || continue
    find_args+=(\( -path "$mountpoint" -prune \) -o)
  done <<<"$mounts"
  find_args+=(\( -type d -name rootfs -prune \) -o \( -type f -print0 \))
  if ! timeout --kill-after=1s 10s find "${find_args[@]}" >"$list_file" 2>"$error_file"; then
    rm -f -- "$list_file" "$error_file"; return 2
  fi
  started=$SECONDS
  while IFS= read -r -d '' file; do
    remaining=$((10 - (SECONDS - started)))
    (( remaining > 0 )) || { rm -f -- "$list_file" "$error_file"; return 2; }
    if [[ $root == /run/containerd/io.containerd.runtime.v2.task && ${file##*/} == config.json ]]; then
      if timeout --kill-after=1s "${remaining}s" jq -e --arg needle "$needle" "$OCI_METADATA_FILTER" "$file" >/dev/null 2>"$error_file"; then
        scan_rc=0
      else
        scan_rc=$?
      fi
    elif timeout --kill-after=1s "${remaining}s" grep -F -l -- "$needle" "$file" >/dev/null 2>"$error_file"; then
      scan_rc=0
    else
      scan_rc=$?
    fi
    if [[ $scan_rc -eq 0 ]]; then
      [[ -z $match_file ]] || printf '%s\n' "$file" >>"$match_file"
      rm -f -- "$list_file" "$error_file"; return 0
    fi
    [[ $scan_rc -eq 1 ]] || { rm -f -- "$list_file" "$error_file"; return 2; }
  done <"$list_file"
  rm -f -- "$list_file" "$error_file"
  return 1
}
direct_metadata_dir() {
  local encoded
  encoded=$(printf '%s' "$WORKSPACE" | base64 -w0 | tr '+/' '-_')
  [[ $encoded =~ ^[A-Za-z0-9_=-]+$ ]] || die 'direct-volume metadata key encoding is invalid'
  printf '%s/%s\n' "$DIRECT_METADATA_ROOT" "$encoded"
}
prove_direct_registration_precondition() {
  local metadata_dir direct_entries direct_owner scan_rc needle
  metadata_dir=$(direct_metadata_dir)
  [[ ! -e $WORKSPACE && ! -L $WORKSPACE ]] || die 'direct registration precondition failed'
  [[ ! -e $metadata_dir && ! -L $metadata_dir ]] || die 'direct registration precondition failed'
  if [[ -e $DIRECT_ROOT || -L $DIRECT_ROOT ]]; then
    [[ -d $DIRECT_ROOT && ! -L $DIRECT_ROOT ]] || die 'direct registration precondition failed'
    if ! direct_owner=$(timeout 10 stat -c %u -- "$DIRECT_ROOT" 2>/dev/null); then
      die 'direct registration precondition failed'
    fi
    [[ $direct_owner =~ ^[0-9]+$ && $direct_owner -eq 0 ]] || die 'direct registration precondition failed'
    if ! direct_entries=$(timeout 10 find "$DIRECT_ROOT" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null); then
      die 'direct registration precondition failed'
    fi
    [[ -z $direct_entries ]] || die 'direct registration precondition failed'
  fi
  if metadata_root_references_value "$DIRECT_METADATA_ROOT" "$LOOP_DEVICE"; then
    die 'direct registration precondition failed'
  else
    scan_rc=$?
  fi
  [[ $scan_rc -eq 1 ]] || die 'direct registration precondition failed'
  for needle in "$BACKING_FILE" "$LOOP_MAJMIN"; do
    if metadata_root_references_value "$DIRECT_METADATA_ROOT" "$needle"; then
      die 'direct registration precondition failed'
    else
      scan_rc=$?
    fi
    [[ $scan_rc -eq 1 ]] || die 'direct registration precondition failed'
  done
  for needle in "$WORKSPACE" "$metadata_dir" "$LOOP_DEVICE" "$BACKING_FILE" "$LOOP_MAJMIN"; do
    if metadata_references_value "$needle"; then
      die 'direct registration precondition failed'
    else
      scan_rc=$?
    fi
    [[ $scan_rc -eq 1 ]] || die 'direct registration precondition failed'
  done
}
direct_registration_absent() {
  local metadata_dir scan_rc needle
  metadata_dir=$(direct_metadata_dir)
  [[ ! -e $metadata_dir && ! -L $metadata_dir ]] || return 1
  [[ ! -e $WORKSPACE && ! -L $WORKSPACE ]] || return 1
  for needle in "$WORKSPACE" "$metadata_dir" "$LOOP_DEVICE" "$BACKING_FILE" "$LOOP_MAJMIN"; do
    if metadata_root_references_value "$DIRECT_METADATA_ROOT" "$needle"; then
      return 1
    else
      scan_rc=$?
    fi
    [[ $scan_rc -eq 1 ]] || return "$scan_rc"
  done
}
remove_direct_registration() {
  local label=${1:-direct-volume-remove}
  [[ ${DIRECT_ADD_BOUNDARY_CROSSED:-0} == 1 ]] || die 'direct-volume removal requires a persisted add ownership boundary'
  run_cmd "$label" "$KATA_RUNTIME" direct-volume remove --volume-path "$WORKSPACE" || return 1
  if [[ -e $WORKSPACE || -L $WORKSPACE ]]; then rmdir "$WORKSPACE" 2>/dev/null || return 1; fi
  if [[ -e $DIRECT_ROOT || -L $DIRECT_ROOT ]]; then rmdir "$DIRECT_ROOT" 2>/dev/null || return 1; fi
  direct_registration_absent || die 'post-removal full metadata scan failed'
}
assert_loop_identity() {
  local backing
  [[ -b ${LOOP_DEVICE:-} ]] || die 'recorded loop device is absent'
  backing=$(losetup -n -O BACK-FILE "$LOOP_DEVICE" | xargs)
  [[ $(readlink -f "$backing") == "$BACKING_FILE" ]] || die 'loop backing identity changed'
  [[ $(blockdev --getsize64 "$LOOP_DEVICE") -eq $BACKING_SIZE_BYTES ]] || die 'loop size changed'
  [[ $(lsblk -ndo MAJ:MIN "$LOOP_DEVICE" | xargs) == "$LOOP_MAJMIN" ]] || die 'loop major/minor changed'
}
detached_now() {
  local fds="$DEVICE_DIR/process-fds.tsv" scan_rc
  assert_loop_identity
  find_device_fds "$fds"; [[ ! -s $fds ]] || return 1
  if metadata_references_device; then return 1; else scan_rc=$?; fi
  [[ $scan_rc -eq 1 ]] || die 'metadata scan failed while proving detach'
  [[ -z $(findmnt -rn -S "$LOOP_DEVICE" -o TARGET) ]] || return 1
  assert_empty_sysfs "$LOOP_DEVICE" holders; assert_empty_sysfs "$LOOP_DEVICE" slaves
  ! awk -v dev="$LOOP_DEVICE" 'NR > 1 && $1 == dev {found=1} END {exit !found}' /proc/swaps
}
wait_detached() {
  local deadline=$((SECONDS + 120))
  until detached_now; do (( SECONDS < deadline )) || die 'detach cannot be proven; preserving all state'; sleep 1; done
  PHASE=detached; save_state
}

resolve_kata_config() {
  local candidate
  KATA_RUNTIME=$(command -v kata-runtime || true); [[ -x $KATA_RUNTIME ]] || die 'kata-runtime is unavailable'
  run_cmd kata-env "$KATA_RUNTIME" env --json || run_cmd kata-env "$KATA_RUNTIME" env
  cp "$LAST_STDOUT" "$DEVICE_DIR/kata-env-before.txt"
  candidate=$(grep -Eo '/[^" ]*configuration[^" ]*\.toml' "$LAST_STDOUT" | head -n1 || true)
  [[ -n $candidate && -f $candidate ]] || die 'cannot resolve effective Kata configuration'
  KATA_CONFIG=$(readlink -f "$candidate")
}
restart_containerd() {
  run_cmd containerd-restart systemctl restart containerd
  run_cmd containerd-active systemctl is-active containerd
  grep -qx active "$LAST_STDOUT" || die 'containerd did not return active'
}
verify_effective_config() {
  local expected=$1
  [[ $(grep -Ec "^[[:space:]]*disable_block_device_use[[:space:]]*=[[:space:]]*$expected" "$KATA_CONFIG") -eq 1 ]] ||
    die "effective Kata configuration does not contain disable_block_device_use=$expected"
  run_cmd "kata-env-config-$expected" "$KATA_RUNTIME" env --json || run_cmd "kata-env-config-$expected" "$KATA_RUNTIME" env
  grep -Fq "$KATA_CONFIG" "$LAST_STDOUT" || die 'kata-runtime does not report the mutated effective configuration'
}
atomic_install() {
  local source=$1 destination=$2 tmp
  tmp="${destination}.kdva-${RUN_ID}-$$"
  install -m "$(stat -c %a "$destination")" -o "$(stat -c %u "$destination")" -g "$(stat -c %g "$destination")" "$source" "$tmp"
  mv -fT "$tmp" "$destination"
}
enable_block_device() {
  local true_count false_count modified
  resolve_kata_config; CONFIG_BACKUP="$DEVICE_DIR/kata-config.original"; cp --preserve=all "$KATA_CONFIG" "$CONFIG_BACKUP"
  CONFIG_HASH_ORIGINAL=$(hash_file "$CONFIG_BACKUP")
  true_count=$(grep -Ec '^[[:space:]]*disable_block_device_use[[:space:]]*=[[:space:]]*true' "$CONFIG_BACKUP" || true)
  false_count=$(grep -Ec '^[[:space:]]*disable_block_device_use[[:space:]]*=[[:space:]]*false' "$CONFIG_BACKUP" || true)
  [[ $((true_count + false_count)) -eq 1 ]] || die 'ambiguous disable_block_device_use setting'
  CONFIG_CHANGED=0; CONFIG_RESTART_REQUIRED=0; CONFIG_HASH_MODIFIED=$CONFIG_HASH_ORIGINAL
  if [[ $true_count -eq 1 ]]; then
    modified="$DEVICE_DIR/kata-config.modified"
    awk '{if ($0 ~ /^[[:space:]]*disable_block_device_use[[:space:]]*=[[:space:]]*true/) sub(/=[[:space:]]*true/, "= false"); print}' "$CONFIG_BACKUP" >"$modified"
    CONFIG_HASH_MODIFIED=$(hash_file "$modified"); CONFIG_CHANGED=1; CONFIG_RESTART_REQUIRED=1; save_state
    [[ $(hash_file "$KATA_CONFIG") == "$CONFIG_HASH_ORIGINAL" ]] || die 'Kata config changed outside this run before mutation'
    atomic_install "$modified" "$KATA_CONFIG"
    [[ $(hash_file "$KATA_CONFIG") == "$CONFIG_HASH_MODIFIED" ]] || die 'Kata config install failed'
    restart_containerd; verify_effective_config false
    CONFIG_RESTART_REQUIRED=0
  else
    verify_effective_config false
  fi
  save_state
}
restore_config() {
  local current
  [[ -n ${CONFIG_HASH_ORIGINAL:-} ]] || return 0
  current=$(hash_file "$KATA_CONFIG")
  if [[ ${CONFIG_CHANGED:-0} == 1 ]]; then
    [[ $current == "$CONFIG_HASH_MODIFIED" || $current == "$CONFIG_HASH_ORIGINAL" ]] || die 'Kata config changed outside this run'
    if [[ $current == "$CONFIG_HASH_MODIFIED" ]]; then
      CONFIG_RESTART_REQUIRED=1; save_state
      atomic_install "$CONFIG_BACKUP" "$KATA_CONFIG"
      [[ $(hash_file "$KATA_CONFIG") == "$CONFIG_HASH_ORIGINAL" ]] || die 'byte-for-byte Kata config restore failed'
      restart_containerd; verify_effective_config true
      CONFIG_RESTART_REQUIRED=0
    else
      CONFIG_RESTART_REQUIRED=1; save_state
      restart_containerd; verify_effective_config true
      CONFIG_RESTART_REQUIRED=0
    fi
    [[ $(hash_file "$KATA_CONFIG") == "$CONFIG_HASH_ORIGINAL" ]] || die 'byte-for-byte Kata config restore failed'
    CONFIG_CHANGED=0; save_state
  else
    [[ $current == "$CONFIG_HASH_ORIGINAL" ]] || die 'Kata config differs from original'
  fi
}

prepare() {
  local allocated existing label scan_rc
  [[ ! -e $DEVICE_DIR ]] || die "device state already exists: $DEVICE_DIR"
  mkdir -p "$DEVICE_DIR" "$COMMAND_DIR"; chmod 0700 "$DEVICE_DIR" "$COMMAND_DIR"
  claim_owner; PHASE=preparing; RUN_MOUNTED_NVME=0; LOOP_DEVICE=; LOOP_MAJMIN=; CONFIG_CHANGED=0; CONFIG_RESTART_REQUIRED=0
  DIRECT_REGISTERED=0; DIRECT_REGISTRATION_PENDING=0; DIRECT_PRECONDITION_PROVEN=0; DIRECT_ADD_BOUNDARY_CROSSED=0; save_state
  validate_nvme
  existing=$(findmnt -M "$NVME_MOUNT" -n -o SOURCE 2>/dev/null || true)
  if [[ -z $existing ]]; then
    [[ ! -e $NVME_MOUNT && ! -L $NVME_MOUNT ]] || die 'owned NVMe mount marker is absent for existing mountpoint'
    if [[ $FORMAT_NVME == yes ]]; then
      label="kdva-${RUN_ID:0:11}"; run_cmd format-nvme mkfs.ext4 -F -L "$label" "$NVME_DEVICE"
      [[ $(blkid -o value -s LABEL "$NVME_DEVICE" 2>/dev/null) == "$label" ]] || die 'NVMe filesystem label differs from the requested label'
    else [[ $(blkid -o value -s TYPE "$NVME_DEVICE" 2>/dev/null) == ext4 ]] || die 'NVMe is not ext4; explicit FORMAT_NVME=yes is required'; fi
    install -d -m 0700 "$NVME_MOUNT"; mount -t ext4 -o nodev,nosuid "$NVME_DEVICE" "$NVME_MOUNT"; RUN_MOUNTED_NVME=1
    printf '%s\n' "$RUN_ID" >"$MOUNT_OWNER_MARKER"; chmod 0600 "$MOUNT_OWNER_MARKER"; save_state
  else
    [[ $(readlink -f "$existing") == "$NVME_DEVICE" ]] || die 'experiment mountpoint has another source'
    [[ -f $MOUNT_OWNER_MARKER && ! -L $MOUNT_OWNER_MARKER && $(<"$MOUNT_OWNER_MARKER") == "$RUN_ID" ]] ||
      die 'owned NVMe mount marker is absent or belongs to another run'
    require_owner
  fi
  install -d -m 0700 "$BACKING_DIR"
  [[ ! -e $BACKING_FILE && ! -L $BACKING_FILE ]] || die 'backing file already exists'
  (set -o noclobber; : >"$BACKING_FILE") 2>/dev/null || die 'backing file already exists'
  fallocate -l "$BACKING_SIZE_BYTES" "$BACKING_FILE"
  [[ $(stat -c %s "$BACKING_FILE") -eq $BACKING_SIZE_BYTES ]] || die 'backing file size is not 4 GiB'
  allocated=$(( $(stat -c %b "$BACKING_FILE") * 512 )); [[ $allocated -ge $BACKING_SIZE_BYTES ]] || die 'backing file is sparse'
  run_cmd attach-loop losetup --find --show --nooverlap "$BACKING_FILE"; LOOP_DEVICE=$(tail -n1 "$LAST_STDOUT" | xargs)
  [[ $LOOP_DEVICE =~ ^/dev/loop[0-9]+$ ]] || die 'unexpected loop device'; LOOP_MAJMIN=$(lsblk -ndo MAJ:MIN "$LOOP_DEVICE" | xargs); save_state
  assert_loop_identity
  find_device_fds "$DEVICE_DIR/preformat-fds.tsv"; [[ ! -s $DEVICE_DIR/preformat-fds.tsv ]] || die 'new loop device already has users'
  if metadata_references_device; then die 'stale Kata/containerd metadata references the new device'; else scan_rc=$?; fi
  [[ $scan_rc -eq 1 ]] || die 'metadata scan failed while checking the new device'
  PHASE=prepared; save_state
  printf 'LOOP_DEVICE=%s\nBACKING_FILE=%s\n' "$LOOP_DEVICE" "$BACKING_FILE"
}

format_device() {
  local mount_dir block_size physical_start mapped
  load_state; require_owner; [[ $PHASE == prepared ]] || die "format requires prepared phase, found $PHASE"; assert_loop_identity; detached_now
  [[ -z $(blkid -o value -s TYPE "$LOOP_DEVICE" 2>/dev/null) ]] || die 'loop already has a filesystem; refusing a second format'
  run_cmd format-loop mkfs.ext4 -F -L "kdva-$DEVICE_ID" "$LOOP_DEVICE"
  FILESYSTEM_UUID=$(blkid -o value -s UUID "$LOOP_DEVICE"); [[ -n $FILESYSTEM_UUID ]] || die 'formatted filesystem UUID is absent'
  mount_dir="$DEVICE_DIR/format-mount"; mkdir "$mount_dir"; mount -t ext4 "$LOOP_DEVICE" "$mount_dir"
  fallocate -l 16384 "$mount_dir/.kata-raw-reserved"
  head -c 4096 /dev/zero | tr '\0' M | dd of="$mount_dir/.kata-raw-reserved" bs=4096 count=1 seek=0 conv=notrunc status=none
  head -c 4096 /dev/zero | tr '\0' N | dd of="$mount_dir/.kata-raw-reserved" bs=4096 count=1 seek=1 conv=notrunc status=none
  head -c 4096 /dev/zero | tr '\0' O | dd of="$mount_dir/.kata-raw-reserved" bs=4096 count=1 seek=2 conv=notrunc status=none
  head -c 4096 /dev/zero | tr '\0' P | dd of="$mount_dir/.kata-raw-reserved" bs=4096 count=1 seek=3 conv=notrunc,fsync status=none
  run_cmd reserved-extents filefrag -b4096 -e "$mount_dir/.kata-raw-reserved"
  mapped=$(awk '
    BEGIN { expected_logical=0; first_physical=-1; expected_physical=-1 }
    /^[[:space:]]*[0-9]+:/ {
      record=$0; normalized=$0
      if (record ~ /(^|[,[:space:]])(unwritten|unknown[^,[:space:]]*|delalloc)([,[:space:]]|$)/) exit 3
      sub(/^[[:space:]]+/, "", normalized); gsub(/\.\./, " ", normalized); gsub(/:/, " ", normalized)
      field_count=split(normalized, fields, /[[:space:]]+/)
      if (field_count < 6) exit 2
      for (i=1; i<=6; i++) if (fields[i] !~ /^[0-9]+$/) exit 2
      logical_start=fields[2]; logical_end=fields[3]; physical_start=fields[4]; physical_end=fields[5]; extent_length=fields[6]
      if (logical_start != expected_logical || logical_end < logical_start || physical_end < physical_start ||
          logical_end - logical_start + 1 != extent_length || physical_end - physical_start + 1 != extent_length) exit 4
      if (first_physical < 0) { first_physical=physical_start; expected_physical=physical_start }
      if (physical_start != expected_physical) exit 5
      expected_logical=logical_end+1; expected_physical=physical_end+1
      count++
    }
    END { if (count < 1 || expected_logical != 4 || expected_physical != first_physical + 4) exit 6; print first_physical }
  ' "$LAST_STDOUT") || { umount "$mount_dir"; die 'cannot map logical blocks 0..3 to four contiguous initialized physical blocks'; }
  [[ $mapped =~ ^[0-9]+$ ]] || { umount "$mount_dir"; die 'reserved extent physical start is invalid'; }
  physical_start=$mapped
  block_size=$(stat -fc %s "$mount_dir"); [[ $block_size -eq 4096 ]] || { umount "$mount_dir"; die 'unexpected ext4 block size'; }
  [[ $physical_start -ge $((16777216 / block_size)) && $physical_start -le $((BACKING_SIZE_BYTES / block_size - 4)) ]] || {
    umount "$mount_dir"; die 'reserved extent physical block range is outside safe backing-file bounds';
  }
  MARKER_OFFSET_ONE=$((physical_start * block_size)); MARKER_OFFSET_TWO=$(((physical_start + 1) * block_size))
  RAW_OFFSET_ONE=$(((physical_start + 2) * block_size)); RAW_OFFSET_TWO=$(((physical_start + 3) * block_size))
  sync; umount "$mount_dir"; rmdir "$mount_dir"
  for offset in "$MARKER_OFFSET_ONE" "$MARKER_OFFSET_TWO" "$RAW_OFFSET_ONE" "$RAW_OFFSET_TWO"; do
    [[ $offset -ge 16777216 && $offset -le $((BACKING_SIZE_BYTES - block_size)) && $((offset % block_size)) -eq 0 ]] ||
      die "reserved marker/raw offset is unsafe: $offset"
  done
  dd if="$LOOP_DEVICE" of="$DEVICE_DIR/raw-original-one.bin" bs=4096 count=1 skip="$((RAW_OFFSET_ONE / 4096))" iflag=direct status=none
  dd if="$LOOP_DEVICE" of="$DEVICE_DIR/raw-original-two.bin" bs=4096 count=1 skip="$((RAW_OFFSET_TWO / 4096))" iflag=direct status=none
  PHASE=formatted; save_state
  printf 'FILESYSTEM_UUID=%s\nMARKER_OFFSET_ONE=%s\nMARKER_OFFSET_TWO=%s\nRAW_OFFSET_ONE=%s\nRAW_OFFSET_TWO=%s\n' \
    "$FILESYSTEM_UUID" "$MARKER_OFFSET_ONE" "$MARKER_OFFSET_TWO" "$RAW_OFFSET_ONE" "$RAW_OFFSET_TWO"
}

verify_device() {
  load_state; require_owner; assert_loop_identity
  [[ $(blkid -o value -s UUID "$LOOP_DEVICE") == "$FILESYSTEM_UUID" ]] || die 'filesystem identity changed'
  [[ $(blkid -o value -s TYPE "$LOOP_DEVICE") == ext4 ]] || die 'filesystem type changed'
  detached_now || die 'device is not safely detached'
  printf 'VERIFIED device=%s majmin=%s filesystem_uuid=%s phase=%s\n' "$LOOP_DEVICE" "$LOOP_MAJMIN" "$FILESYSTEM_UUID" "$PHASE"
}

restore_raw() {
  local verify_one="$DEVICE_DIR/raw-restored-one.bin" verify_two="$DEVICE_DIR/raw-restored-two.bin"
  load_state; require_owner; [[ $PHASE == detached || $PHASE == formatted ]] || die 'raw restore requires detached/formatted phase'; detached_now
  dd if="$DEVICE_DIR/raw-original-one.bin" of="$LOOP_DEVICE" bs=4096 count=1 seek="$((RAW_OFFSET_ONE / 4096))" oflag=direct conv=notrunc,fsync status=none
  dd if="$DEVICE_DIR/raw-original-two.bin" of="$LOOP_DEVICE" bs=4096 count=1 seek="$((RAW_OFFSET_TWO / 4096))" oflag=direct conv=notrunc,fsync status=none
  dd if="$LOOP_DEVICE" of="$verify_one" bs=4096 count=1 skip="$((RAW_OFFSET_ONE / 4096))" iflag=direct status=none
  dd if="$LOOP_DEVICE" of="$verify_two" bs=4096 count=1 skip="$((RAW_OFFSET_TWO / 4096))" iflag=direct status=none
  cmp -s "$DEVICE_DIR/raw-original-one.bin" "$verify_one" && cmp -s "$DEVICE_DIR/raw-original-two.bin" "$verify_two" ||
    die 'raw restore verification failed; preserving device state'
  run_cmd raw-restore-e2fsck e2fsck -fn "$LOOP_DEVICE" || die 'raw restore verification failed: e2fsck reported filesystem damage; preserving device state'
  PHASE=formatted; save_state
}

detect_direct_cli() {
  run_cmd direct-volume-help "$KATA_RUNTIME" direct-volume --help || true
  grep -Eq '(^|[[:space:]])add([[:space:]]|$)' "$LAST_STDOUT" "$LAST_STDERR" || outcome_die 'installed Kata runtime does not advertise direct-volume add'
  run_cmd direct-volume-add-help "$KATA_RUNTIME" direct-volume add --help || true
  grep -q -- '--volume-path' "$LAST_STDOUT" "$LAST_STDERR" && grep -q -- '--mount-info' "$LAST_STDOUT" "$LAST_STDERR" ||
    outcome_die 'installed Kata direct-volume add lacks --volume-path/--mount-info'
  run_cmd direct-volume-remove-help "$KATA_RUNTIME" direct-volume remove --help || true
  grep -q -- '--volume-path' "$LAST_STDOUT" "$LAST_STDERR" || outcome_die 'installed Kata direct-volume remove lacks --volume-path'
}

detect_direct_capabilities() {
  local combined="$DEVICE_DIR/direct-volume-capability-help.txt" status reason
  load_state; require_owner; resolve_kata_config
  : >"$combined"
  run_cmd direct-volume-capability-help "$KATA_RUNTIME" direct-volume --help || true
  cat "$LAST_STDOUT" "$LAST_STDERR" >>"$combined"
  run_cmd direct-volume-capability-add-help "$KATA_RUNTIME" direct-volume add --help || true
  cat "$LAST_STDOUT" "$LAST_STDERR" >>"$combined"
  if grep -Eqi -- '(^|[[:space:]-])(attach|no-mount|without-mount|raw-device|device-path)([[:space:]=]|$)' "$combined"; then
    status=insufficient
    reason=potential-attach-without-mount-interface-requires-reviewed-validation
  elif grep -Eq '(^|[[:space:]])add([[:space:]]|$)' "$combined" &&
       grep -q -- '--volume-path' "$combined" && grep -q -- '--mount-info' "$combined"; then
    status=unsupported
    reason=no-safe-attach-without-mount-interface-advertised-by-runtime
  else
    status=insufficient
    reason=direct-volume-capability-interface-unrecognized
  fi
  printf '%s\t%s\tdirect-volume-capability-help.txt\n' "$status" "$reason" >"$DEVICE_DIR/direct-volume-capability.tsv"
  printf 'PREMOUNT_RAW_STATUS=%s\nPREMOUNT_RAW_REASON=%s\nPREMOUNT_RAW_EVIDENCE=%s\n' "$status" "$reason" "$combined"
  printf '%s\n' '--- direct-volume capability help evidence ---'
  cat "$combined"
}
register_direct() {
  local json actual normalized expected metadata_dir
  load_state; require_owner; [[ $PHASE == formatted || $PHASE == detached ]] || die 'direct registration requires detached formatted device'; detached_now
  [[ ${DIRECT_REGISTERED:-0} == 0 && ${DIRECT_REGISTRATION_PENDING:-0} == 0 &&
     ${DIRECT_PRECONDITION_PROVEN:-0} == 0 && ${DIRECT_ADD_BOUNDARY_CROSSED:-0} == 0 ]] ||
    die 'direct registration state is already active or pending'
  json=$(printf '{"volume-type":"block","device":"%s","fstype":"ext4","metadata":{},"options":["rw"]}' "$LOOP_DEVICE")
  metadata_dir=$(direct_metadata_dir); actual="$metadata_dir/mountInfo.json"
  prove_direct_registration_precondition
  DIRECT_PRECONDITION_PROVEN=1
  PHASE=direct-precondition-proven
  save_state
  enable_block_device; detect_direct_cli
  install -d -m 0700 "$DIRECT_ROOT" "$WORKSPACE"
  DIRECT_REGISTRATION_PENDING=1
  DIRECT_ADD_BOUNDARY_CROSSED=1
  PHASE=direct-registration-pending
  save_state
  if ! run_cmd direct-volume-add "$KATA_RUNTIME" direct-volume add --volume-path "$WORKSPACE" --mount-info "$json"; then
    remove_direct_registration direct-volume-add-rollback ||
      die 'failed direct-volume add left registration state whose exact removal cannot be proven; preserving owned state'
    DIRECT_REGISTRATION_PENDING=0; DIRECT_PRECONDITION_PROVEN=0; DIRECT_ADD_BOUNDARY_CROSSED=0; PHASE=detached; save_state
    outcome_die 'direct-volume CLI/schema is unsupported by this installed Kata version'
  fi
  if [[ -f $actual && ! -L $actual ]] &&
     cp "$actual" "$MOUNT_INFO_COPY" && chmod 0600 "$MOUNT_INFO_COPY" &&
     normalized=$(jq -cS . "$MOUNT_INFO_COPY") && expected=$(printf '%s' "$json" | jq -cS .) &&
     [[ $normalized == "$expected" ]]; then
    :
  else
    remove_direct_registration direct-volume-validation-rollback ||
      die 'direct-volume metadata validation failed and exact removal cannot be proven; preserving owned state'
    DIRECT_REGISTRATION_PENDING=0; DIRECT_PRECONDITION_PROVEN=0; DIRECT_ADD_BOUNDARY_CROSSED=0; PHASE=detached; save_state
    outcome_die 'direct-volume CLI/schema metadata validation is unsupported by this installed Kata version'
  fi
  printf '%s\n' "$actual" >"$DEVICE_DIR/mount-info-path.txt"
  DIRECT_REGISTERED=1; DIRECT_REGISTRATION_PENDING=0; PHASE=direct-registered; save_state
  printf 'WORKSPACE=%s\nMOUNT_INFO=%s\n' "$WORKSPACE" "$actual"
}

unregister_direct() {
  load_state; require_owner
  [[ ${DIRECT_REGISTERED:-0} == 1 || ${DIRECT_REGISTRATION_PENDING:-0} == 1 ]] || return 0
  [[ $PHASE != guest-owned ]] || die 'cannot remove metadata while guest ownership is possible'
  wait_detached
  remove_direct_registration direct-volume-remove ||
    die 'direct-volume exact removal cannot be proven; preserving loop, backing file, configuration, and owner state'
  DIRECT_REGISTERED=0; DIRECT_REGISTRATION_PENDING=0; DIRECT_PRECONDITION_PROVEN=0; DIRECT_ADD_BOUNDARY_CROSSED=0; PHASE=detached; save_state
}

begin_guest() {
  load_state; require_owner; assert_loop_identity; detached_now || die 'device has use signals before guest assignment'
  [[ $PHASE == formatted || $PHASE == direct-registered || $PHASE == detached ]] || die "cannot assign guest in phase $PHASE"
  PHASE=guest-owned; save_state
}
end_guest() { load_state; require_owner; [[ $PHASE == guest-owned ]] || die 'guest-owned phase was not recorded'; wait_detached; }
wait_detached_action() { load_state; require_owner; wait_detached; }

collect_state() {
  local case_dir="$DEVICE_DIR/runtime-$CASE_ID" pids pid start current exe cmdline rows=0 flags access root scan_rc
  load_state; require_owner; assert_loop_identity; mkdir -p "$case_dir"
  run_cmd collect-lsblk lsblk -a -b -O "$LOOP_DEVICE"; cp "$LAST_STDOUT" "$case_dir/lsblk.txt"
  find_device_fds "$case_dir/device-fds.tsv"
  : >"$case_dir/runtime-metadata-index.tsv"
  printf 'sufficient\tmetadata-scan-complete\n' >"$case_dir/runtime-metadata-status.tsv"
  for root in /run/vc/sbs /run/kata-containers/sandboxes /run/containerd/io.containerd.runtime.v2.task; do
    if metadata_root_references_value "$root" "$LOOP_DEVICE" "$case_dir/runtime-metadata-index.tsv"; then
      :
    else
      scan_rc=$?
      if [[ $scan_rc -ne 1 ]]; then
        printf 'insufficient\tmetadata-scan-error\t%s\n' "$root" >>"$case_dir/runtime-metadata-index.tsv"
        printf 'insufficient\tmetadata-scan-error\t%s\n' "$root" >"$case_dir/runtime-metadata-status.tsv"
      fi
    fi
  done
  [[ -s $case_dir/runtime-metadata-index.tsv ]] || printf 'insufficient\tno-readable-runtime-metadata-reference\n' >"$case_dir/runtime-metadata-index.tsv"
  pids=$(awk -F '\t' '$4 ~ /(^|[ \/])cloud-hypervisor([ ]|$)/ {print $1}' "$case_dir/device-fds.tsv" | sort -u)
  if [[ $PHASE == guest-owned ]]; then
    if [[ $(printf '%s\n' "$pids" | awk 'NF {n++} END {print n+0}') -eq 1 ]]; then
      pid=$(printf '%s\n' "$pids" | awk 'NF {print; exit}'); start=$(awk -F '\t' -v p="$pid" '$1==p {print $3; exit}' "$case_dir/device-fds.tsv")
      current=$(awk '{print $22}' "/proc/$pid/stat"); [[ $current == "$start" ]] || die 'Cloud Hypervisor PID identity changed'
      exe=$(readlink "/proc/$pid/exe"); cmdline=$(tr '\0' ' ' <"/proc/$pid/cmdline")
      [[ "$exe $cmdline" =~ (^|[[:space:]/])cloud-hypervisor([[:space:]]|$) ]] || die 'scoped process is not Cloud Hypervisor'
      cp "/proc/$pid/status" "$case_dir/ch-status.txt"; cp "/proc/$pid/cgroup" "$case_dir/ch-cgroup.txt"
      printf 'pid\tstarttime\tfd\toctal_flags\taccess_mode\n' >"$case_dir/ch-fds.tsv"
      while IFS=$'\t' read -r row_pid fd _ _ _; do
        [[ $row_pid == "$pid" ]] || continue
        flags=$(awk '/^flags:/ {print $2}' "/proc/$pid/fdinfo/$fd")
        case $((8#$flags & 3)) in 0) access=O_RDONLY ;; 1) access=O_WRONLY ;; 2) access=O_RDWR ;; *) access=UNKNOWN ;; esac
        printf '%s\t%s\t%s\t%s\t%s\n' "$pid" "$start" "$fd" "$flags" "$access" >>"$case_dir/ch-fds.tsv"; rows=$((rows+1))
      done <"$case_dir/device-fds.tsv"
      [[ $rows -gt 0 ]] || die 'no stable target Cloud Hypervisor FD was captured'
      printf 'sufficient\texactly-one-owned-cloud-hypervisor\n' >"$case_dir/ch-classification.tsv"
    else
      printf 'insufficient\texpected-one-owned-cloud-hypervisor\n' >"$case_dir/ch-classification.tsv"
    fi
  fi
  run_cmd collect-kata-env "$KATA_RUNTIME" env --json || run_cmd collect-kata-env "$KATA_RUNTIME" env
  cp "$LAST_STDOUT" "$case_dir/kata-env.txt"; cp "$KATA_CONFIG" "$case_dir/effective-kata-config.toml"
  sha256sum "$case_dir/effective-kata-config.toml" >"$case_dir/effective-kata-config.sha256"
  run_cmd collect-containerd journalctl --no-pager -u containerd --since '-10 minutes'; cp "$LAST_STDOUT" "$case_dir/containerd.log"
  run_cmd collect-kernel journalctl --no-pager -k --since '-10 minutes'; cp "$LAST_STDOUT" "$case_dir/kernel.log"
  printf 'COLLECTED=%s\n' "$case_dir"
}

verify_fs_marker() {
  local mount_dir="$DEVICE_DIR/verify-marker-$CASE_ID" actual
  load_state; require_owner; [[ $MARKER_NAME =~ ^[A-Za-z0-9_.-]+$ && -n $MARKER_VALUE ]] || die 'marker identity is invalid'
  detached_now || die 'host persistence verification requires detached device'
  mkdir "$mount_dir"; mount -t ext4 -o ro,noload "$LOOP_DEVICE" "$mount_dir"
  if [[ -f $mount_dir/.kdva-$MARKER_NAME ]]; then actual=$(<"$mount_dir/.kdva-$MARKER_NAME"); else actual=; fi
  umount "$mount_dir"; rmdir "$mount_dir"
  [[ $actual == "$MARKER_VALUE" ]] || outcome_die 'filesystem marker is absent or differs after host handoff'
  printf 'HOST_PERSISTENCE_OK marker=%s hash=%s\n' "$MARKER_NAME" "$(printf '%s' "$actual" | sha256sum | awk '{print $1}')"
}

resolve_owned_ch() {
  local output="$DEVICE_DIR/trace-target-fds.tsv" pids pid start exe cmdline
  load_state; require_owner; [[ $PHASE == guest-owned ]] || die 'tracing requires guest-owned phase'
  find_device_fds "$output"
  pids=$(awk -F '\t' '$4 ~ /(^|[ \/])cloud-hypervisor([ ]|$)/ {print $1}' "$output" | sort -u)
  [[ $(printf '%s\n' "$pids" | awk 'NF {n++} END {print n+0}') -eq 1 ]] || outcome_die 'tracing cannot be scoped to exactly one owned Cloud Hypervisor PID'
  pid=$(printf '%s\n' "$pids" | awk 'NF {print; exit}'); start=$(awk -F '\t' -v p="$pid" '$1==p {print $3; exit}' "$output")
  exe=$(readlink "/proc/$pid/exe" 2>/dev/null); cmdline=$(tr '\0' ' ' <"/proc/$pid/cmdline" 2>/dev/null)
  [[ "$exe $cmdline" =~ (^|[[:space:]/])cloud-hypervisor([[:space:]]|$) && $(awk '{print $22}' "/proc/$pid/stat") == "$start" ]] ||
    die 'owned Cloud Hypervisor identity is unstable'
  printf '%s\t%s\n' "$pid" "$start"
}

trace_start() {
  local target pid start tracer tracer_start fd_csv trace_dir="$DEVICE_DIR/traces/$CASE_ID" trace_root instance encoded_dev event
  load_state; require_owner; [[ $PHASE == guest-owned ]] || die 'tracing requires guest-owned phase'
  mkdir -p "$trace_dir"; [[ ! -e $trace_dir/tracer.env ]] || die 'a trace is already registered'
  if ! target=$(resolve_owned_ch); then
    printf 'insufficient\texpected-one-owned-cloud-hypervisor\n' >"$trace_dir/syscall-status.tsv"
    printf 'insufficient\ttrace-not-started-without-owned-cloud-hypervisor\n' >"$trace_dir/block-status.tsv"
    return 2
  fi
  pid=${target%%$'\t'*}; start=${target##*$'\t'}
  fd_csv=$(awk -F '\t' -v p="$pid" '$1 == p {values = values sep $2; sep = ","} END {print values}' "$DEVICE_DIR/trace-target-fds.tsv")
  [[ $fd_csv =~ ^[0-9]+(,[0-9]+)*$ ]] || die 'cannot scope syscall trace to owned Cloud Hypervisor FDs'
  printf '%s\n' "$fd_csv" >"$trace_dir/target-fds.csv"
  if command -v strace >/dev/null && strace --help 2>&1 | grep -F -- 'trace-fds' >/dev/null; then
    setsid timeout 180 strace -ff -ttt -T -yy -s 256 -e trace=io_uring_setup,io_uring_register,io_uring_enter,pread64,pwrite64,read,write,fsync,fdatasync \
      -e "trace-fds=$fd_csv" -p "$pid" >"$trace_dir/syscall.stdout" 2>"$trace_dir/syscall.stderr" &
    tracer=$!; sleep 1
    if kill -0 "$tracer" 2>/dev/null; then
      tracer_start=$(awk '{print $22}' "/proc/$tracer/stat")
      printf 'TARGET_PID=%q\nTARGET_START=%q\nTRACER_PID=%q\nTRACER_START=%q\n' "$pid" "$start" "$tracer" "$tracer_start" >"$trace_dir/tracer.env"
      printf 'available\tpid-and-fd-scoped-syscall-trace\n' >"$trace_dir/syscall-status.tsv"
    else
      wait "$tracer" || true; printf 'insufficient\tstrace-attach-failed\n' >"$trace_dir/syscall-status.tsv"
    fi
  else
    printf 'unsupported\thost-strace-missing-or-lacks-trace-fds\n' >"$trace_dir/syscall-status.tsv"
  fi
  trace_root=/sys/kernel/tracing; [[ -d $trace_root/events/block ]] || trace_root=/sys/kernel/debug/tracing
  instance="$trace_root/instances/kdva-${RUN_ID}-${DEVICE_ID}-${CASE_ID}"
  if [[ -d $trace_root/events/block && -w $trace_root/instances ]]; then
    if mkdir "$instance"; then
      printf '%s\n' "$instance" >"$trace_dir/tracefs-instance"
      encoded_dev=$(( (${LOOP_MAJMIN%%:*} << 20) | ${LOOP_MAJMIN##*:} ))
      for event in block_rq_issue block_rq_complete block_rq_error; do
        if [[ ! -e $instance/events/block/$event/filter ]]; then
          printf 'insufficient\tmissing-%s\n' "$event" >>"$trace_dir/block-status.tsv"; continue
        fi
        if ! printf 'dev == %s\n' "$encoded_dev" >"$instance/events/block/$event/filter" ||
           ! printf 1 >"$instance/events/block/$event/enable"; then
          printf 'insufficient\tcannot-scope-or-enable-%s\n' "$event" >>"$trace_dir/block-status.tsv"
        fi
      done
      printf 1 >"$instance/tracing_on" || printf 'insufficient\tcannot-enable-owned-trace-instance\n' >>"$trace_dir/block-status.tsv"
      [[ -s $trace_dir/block-status.tsv ]] || printf 'available\towned-loop-major-minor-filter\n' >"$trace_dir/block-status.tsv"
    else
      printf 'insufficient\tcannot-create-owned-tracefs-instance\n' >"$trace_dir/block-status.tsv"
    fi
  else
    printf 'insufficient\ttracefs-block-events-unavailable\n' >"$trace_dir/block-status.tsv"
  fi
  printf 'TRACE_READY target_pid=%s target_fds=%s\n' "$pid" "$fd_csv"
}

trace_stop() {
  local trace_dir="$DEVICE_DIR/traces/$CASE_ID" current instance event
  [[ -d $trace_dir ]] || { printf 'TRACE_NOT_ACTIVE\n'; return 0; }
  # Generated by trace_start with numeric shell-escaped values in a root-only directory.
  if [[ -f $trace_dir/tracer.env ]]; then
    source "$trace_dir/tracer.env"
    current=$(awk '{print $22}' "/proc/$TRACER_PID/stat" 2>/dev/null || true)
    [[ -z $current || $current == "$TRACER_START" ]] || die 'tracer PID was reused; refusing to signal it'
    if [[ -n $current ]]; then kill -TERM -- "-$TRACER_PID" 2>/dev/null || true; fi
    timeout 20 bash -c 'while kill -0 "$1" 2>/dev/null; do sleep 1; done' _ "$TRACER_PID" || die 'scoped tracer did not stop'
    if grep -Eq 'io_uring_(setup|register|enter)|pread64|pwrite64|read\(|write\(|fsync|fdatasync' "$trace_dir/syscall.stderr"; then
      printf 'sufficient\tpid-and-fd-scoped-syscall-evidence\n' >"$trace_dir/syscall-status.tsv"
    else
      printf 'insufficient\tno-matching-owned-fd-syscall-evidence\n' >"$trace_dir/syscall-status.tsv"
    fi
    rm -f "$trace_dir/tracer.env"
  fi
  if [[ -f $trace_dir/tracefs-instance ]]; then
    instance=$(<"$trace_dir/tracefs-instance")
    [[ $instance == /sys/kernel/tracing/instances/kdva-* || $instance == /sys/kernel/debug/tracing/instances/kdva-* ]] || die 'unexpected tracefs instance path'
    printf 0 >"$instance/tracing_on" 2>/dev/null || true
    cat "$instance/trace" >"$trace_dir/block.trace" || true
    for event in block_rq_issue block_rq_complete block_rq_error; do printf 0 >"$instance/events/block/$event/enable" 2>/dev/null || true; done
    rmdir "$instance" || die 'owned tracefs instance did not cleanly detach'
    rm -f "$trace_dir/tracefs-instance"
    if [[ -s $trace_dir/block.trace ]] && [[ $(awk -F '\t' '$1 != "available" {bad=1} END {print bad+0}' "$trace_dir/block-status.tsv") -eq 0 ]]; then
      printf 'sufficient\towned-loop-block-events\n' >"$trace_dir/block-status.tsv"
    elif [[ ! -s $trace_dir/block.trace ]]; then
      printf 'insufficient\tno-owned-loop-block-events\n' >"$trace_dir/block-status.tsv"
    fi
  fi
  cat "$trace_dir/syscall-status.tsv" "$trace_dir/block-status.tsv" 2>/dev/null || true
}

cleanup_device() {
  local current
  load_state; require_owner
  [[ $PHASE != guest-owned ]] || wait_detached
  [[ ${DIRECT_REGISTERED:-0} == 0 && ${DIRECT_REGISTRATION_PENDING:-0} == 0 ]] || unregister_direct
  wait_detached; restore_config
  assert_loop_identity
  direct_registration_absent || die 'post-removal full metadata scan failed'
  losetup -d "$LOOP_DEVICE"
  [[ ! -e $BACKING_FILE || $(readlink -f "$BACKING_FILE") == "$BACKING_FILE" ]] || die 'backing path is not canonical'
  rm -f "$BACKING_FILE"; rmdir "$DEVICE_DIR/format-mount" 2>/dev/null || true
  PHASE=cleaned; LOOP_DEVICE=; LOOP_MAJMIN=; save_state
  current=$(find "$RUN_DIR/devices" -mindepth 2 -maxdepth 2 -name state.env -exec grep -L '^PHASE=cleaned$' {} + 2>/dev/null || true)
    if [[ -z $current ]]; then
      if [[ -e $MOUNT_OWNER_MARKER ]] && mountpoint -q "$NVME_MOUNT"; then
      [[ -f $MOUNT_OWNER_MARKER && ! -L $MOUNT_OWNER_MARKER && $(<"$MOUNT_OWNER_MARKER") == "$RUN_ID" ]] ||
        die 'owned NVMe mount marker is absent or belongs to another run during cleanup'
      [[ -z $(losetup --list --noheadings -O BACK-FILE | grep -F "$BACKING_DIR/" || true) ]] || die 'run backing loop remains before NVMe unmount'
      umount "$NVME_MOUNT"; rm -f "$MOUNT_OWNER_MARKER"
    fi
    [[ $(<"$OWNER_FILE") == "$RUN_ID" ]] || die 'owner changed before release'; rm -f "$OWNER_FILE"
  fi
  printf 'CLEANUP_COMPLETE device=%s artifacts=%s\n' "$DEVICE_ID" "$DEVICE_DIR"
}

validate_common
case "$ACTION" in
  prepare) prepare ;;
  enable-config) load_state; require_owner; enable_block_device ;;
  format) format_device ;;
  verify) verify_device ;;
  restore-raw) restore_raw ;;
  register-direct) register_direct ;;
  unregister-direct) unregister_direct ;;
  begin-guest) begin_guest ;;
  end-guest) end_guest ;;
  wait-detached) wait_detached_action ;;
  collect) collect_state ;;
  verify-marker) verify_fs_marker ;;
  trace-start) trace_start ;;
  trace-stop) trace_stop ;;
  direct-capabilities) detect_direct_capabilities ;;
  cleanup) cleanup_device ;;
  *) die "unknown action: $ACTION" ;;
esac
