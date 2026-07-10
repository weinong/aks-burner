#!/usr/bin/env bash
set -euo pipefail

RUN_ID="${RUN_ID:-manual}"
SCENARIO="${SCENARIO:-git-clone}"
SAMPLE_ID="${SAMPLE_ID:-${HOSTNAME:-sample}}"
REPO_URL="${REPO_URL:?REPO_URL is required}"
CLONE_MODE="${CLONE_MODE:-full}"
WORK_DIR="${WORK_DIR:-/work}"
RESULTS_DIR="${RESULTS_DIR:-/results}"
OUT_DIR="${RESULTS_DIR}/${RUN_ID}/${SCENARIO}/${SAMPLE_ID}"
SAMPLE_WORK_DIR="${WORK_DIR}/${RUN_ID}/${SCENARIO}/${SAMPLE_ID}"
TARGET_DIR="${TARGET_DIR:-${SAMPLE_WORK_DIR}/repo}"

mkdir -p "$OUT_DIR" "$SAMPLE_WORK_DIR"
rm -rf "$TARGET_DIR"

export GIT_TERMINAL_PROMPT=0
export GIT_TRACE2_EVENT="$OUT_DIR/git-trace2-event.json"
export GIT_TRACE2_PERF="$OUT_DIR/git-trace2-perf.log"

case "$CLONE_MODE" in
  full)
    CLONE_ARGS=()
    ;;
  blobless)
    CLONE_ARGS=(--filter=blob:none)
    ;;
  *)
    echo "unknown CLONE_MODE=$CLONE_MODE" >&2
    exit 2
    ;;
esac

start_ns="$(date +%s%N)"
start_epoch="$(date +%s)"
cat /proc/self/io > "$OUT_DIR/proc-self-io-before.txt" || true
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-before.txt" || true

set +e
/usr/bin/time -v -o "$OUT_DIR/time.txt" \
  git clone "${CLONE_ARGS[@]}" "$REPO_URL" "$TARGET_DIR" \
  > "$OUT_DIR/git-stdout.log" 2> "$OUT_DIR/git-stderr.log"
exit_code="$?"
set -e

end_ns="$(date +%s%N)"
end_epoch="$(date +%s)"
duration_ns="$((end_ns - start_ns))"
duration_seconds="$(awk "BEGIN { print ${duration_ns} / 1000000000 }")"

cat /proc/self/io > "$OUT_DIR/proc-self-io-after.txt" || true
du -sb "$TARGET_DIR" > "$OUT_DIR/repo-size-bytes.txt" 2>/dev/null || printf '0\t%s\n' "$TARGET_DIR" > "$OUT_DIR/repo-size-bytes.txt"
find "$TARGET_DIR" -xdev -type f 2>/dev/null | wc -l > "$OUT_DIR/file-count.txt" || echo 0 > "$OUT_DIR/file-count.txt"
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-after.txt" || true

repo_size_bytes="$(awk '{print $1}' "$OUT_DIR/repo-size-bytes.txt")"
file_count="$(tr -d ' ' < "$OUT_DIR/file-count.txt")"

cat > "$OUT_DIR/summary.prom" <<EOF
# TYPE git_clone_duration_seconds gauge
git_clone_duration_seconds{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $duration_seconds
# TYPE git_clone_exit_code gauge
git_clone_exit_code{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $exit_code
# TYPE git_clone_start_time_seconds gauge
git_clone_start_time_seconds{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $start_epoch
# TYPE git_clone_end_time_seconds gauge
git_clone_end_time_seconds{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $end_epoch
# TYPE git_clone_repo_size_bytes gauge
git_clone_repo_size_bytes{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $repo_size_bytes
# TYPE git_clone_file_count gauge
git_clone_file_count{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $file_count
EOF

cat "$OUT_DIR/summary.prom"
exit "$exit_code"
