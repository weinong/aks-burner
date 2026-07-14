#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 1 ]; then
  echo "usage: run-block-benchmark.sh <benchmark-command> [args...]" >&2
  exit 2
fi

BLOCK_DEVICE="${BLOCK_DEVICE:-${FAKE_BLOCK_DEVICE:-/dev/work-block}}"
WORK_DIR="${WORK_DIR:-/work}"
mounted=false
cleanup_rc=0
cleanup_blocked=false
child_pid=""
session_pid_file=""
signal_rc=0
signal_name=""
force_kill_pid=""
SIGNAL_GRACE_SECONDS="${SIGNAL_GRACE_SECONDS:-5}"
POST_KILL_GRACE_SECONDS="${POST_KILL_GRACE_SECONDS:-1}"

cleanup() {
  if [ "$mounted" != true ]; then
    return
  fi
  if [ "$cleanup_blocked" = true ]; then
    cleanup_rc=1
    printf 'refusing to unmount %s while benchmark processes are still running\n' "$WORK_DIR" >&2
    return
  fi
  mounted=false
  if umount "$WORK_DIR"; then
    return
  else
    cleanup_rc="$?"
    printf 'failed to unmount %s (exit code %s)\n' "$WORK_DIR" "$cleanup_rc" >&2
  fi
}

on_exit() {
  local rc="$?"
  trap - EXIT INT TERM
  if [ -n "$session_pid_file" ]; then
    rm -f "$session_pid_file"
  fi
  if [ -n "$force_kill_pid" ]; then
    kill "$force_kill_pid" 2>/dev/null || true
  fi
  cleanup
  if [ "$rc" -ne 0 ]; then
    exit "$rc"
  fi
  exit "$cleanup_rc"
}

trap on_exit EXIT

forward_signal() {
  local name="$1"
  local rc="$2"
  if [ "$signal_rc" -eq 0 ]; then
    signal_name="$name"
    signal_rc="$rc"
  fi
  if [ -n "$child_pid" ] && kill -0 -- "-$child_pid" 2>/dev/null; then
    kill "-$name" -- "-$child_pid" 2>/dev/null || true
    if [ -z "$force_kill_pid" ]; then
      (
        sleep "$SIGNAL_GRACE_SECONDS"
        if kill -0 -- "-$child_pid" 2>/dev/null; then
          kill -KILL -- "-$child_pid" 2>/dev/null || true
        fi
      ) &
      force_kill_pid="$!"
    fi
  fi
}

process_group_has_live_members() {
  local stat rest state pgrp
  for stat in /proc/[0-9]*/stat; do
    [ -r "$stat" ] || continue
    IFS= read -r rest < "$stat" 2>/dev/null || continue
    rest="${rest##*) }"
    set -- $rest
    state="${1:-}"
    pgrp="${3:-}"
    if [ "$pgrp" = "$child_pid" ] && [ "$state" != "Z" ]; then
      return 0
    fi
  done
  return 1
}

exit_if_signaled() {
  if [ "$signal_rc" -ne 0 ]; then
    exit "$signal_rc"
  fi
}

trap 'forward_signal INT 130' INT
trap 'forward_signal TERM 143' TERM

if [ -z "${FAKE_BLOCK_DEVICE:-}" ] && [ ! -b "$BLOCK_DEVICE" ]; then
  printf '%s is not a block device\n' "$BLOCK_DEVICE" >&2
  exit 1
fi
exit_if_signaled

setup_start_ns="$(date +%s%N)"
if mkdir -p "$WORK_DIR"; then
  :
else
  rc="$?"
  exit_if_signaled
  printf 'failed to create work directory %s (exit code %s)\n' "$WORK_DIR" "$rc" >&2
  exit "$rc"
fi
exit_if_signaled
if mkfs.ext4 -F -E lazy_itable_init=0,lazy_journal_init=0 "$BLOCK_DEVICE"; then
  :
else
  rc="$?"
  exit_if_signaled
  printf 'failed to format block device %s (exit code %s)\n' "$BLOCK_DEVICE" "$rc" >&2
  exit "$rc"
fi
exit_if_signaled
if mount "$BLOCK_DEVICE" "$WORK_DIR"; then
  :
else
  rc="$?"
  printf 'failed to mount block device %s at %s (exit code %s)\n' "$BLOCK_DEVICE" "$WORK_DIR" "$rc" >&2
  exit "$rc"
fi
mounted=true
sync "$WORK_DIR"
setup_end_ns="$(date +%s%N)"
export BLOCK_SETUP_DURATION_SECONDS="$(awk "BEGIN { print (${setup_end_ns} - ${setup_start_ns}) / 1000000000 }")"
exit_if_signaled

# The waiting setsid supervisor preserves benchmark status while its child owns
# an isolated session/process group. Job control keeps SIGINT enabled at spawn.
session_pid_file="$(mktemp)"
set -m
setsid --fork --wait bash -c '
  pid_file="$1"
  shift
  printf "%s\n" "$$" > "$pid_file"
  exec "$@"
' bash "$session_pid_file" "$@" &
wait_pid="$!"
set +m

while [ ! -s "$session_pid_file" ] && kill -0 "$wait_pid" 2>/dev/null; do
  sleep 0.01
done
if [ -s "$session_pid_file" ]; then
  child_pid="$(cat "$session_pid_file")"
fi
if [ -n "$signal_name" ]; then
  forward_signal "$signal_name" "$signal_rc"
fi

benchmark_rc=0
while true; do
  set +e
  wait "$wait_pid"
  wait_rc="$?"
  set -e
  if ! kill -0 "$wait_pid" 2>/dev/null; then
    benchmark_rc="$wait_rc"
    break
  fi
done

# The leader can exit while descendants still ignore TERM. Let the scheduled
# escalation issue process-group KILL before beginning the bounded drain.
if [ -n "$force_kill_pid" ]; then
  if [ -n "$child_pid" ] && process_group_has_live_members; then
    wait "$force_kill_pid" 2>/dev/null || true
  else
    kill "$force_kill_pid" 2>/dev/null || true
    wait "$force_kill_pid" 2>/dev/null || true
  fi
  force_kill_pid=""
fi

# Ignore zombies and bound the post-KILL drain so a broken process tree cannot
# block PID 1 indefinitely.
post_kill_deadline="$(awk "BEGIN { printf \"%.0f\", ($(date +%s%N) + (${POST_KILL_GRACE_SECONDS} * 1000000000)) }")"
while [ -n "$child_pid" ] && process_group_has_live_members; do
  if [ "$(date +%s%N)" -ge "$post_kill_deadline" ]; then
    printf 'timed out waiting for benchmark process group %s to exit\n' "$child_pid" >&2
    cleanup_blocked=true
    break
  fi
  sleep 0.01
done
child_pid=""
rm -f "$session_pid_file"
session_pid_file=""

cleanup
trap - EXIT INT TERM
if [ "$signal_rc" -ne 0 ]; then
  exit "$signal_rc"
fi
if [ "$benchmark_rc" -ne 0 ]; then
  exit "$benchmark_rc"
fi
exit "$cleanup_rc"
