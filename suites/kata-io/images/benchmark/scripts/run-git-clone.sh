#!/usr/bin/env bash
set -euo pipefail

RUN_ID="${RUN_ID:-manual}"
SCENARIO="${SCENARIO:-git-clone}"
SAMPLE_ID="${SAMPLE_ID:-${HOSTNAME:-sample}}"
REPO_URL="${REPO_URL:?REPO_URL is required}"
CLONE_MODE="${CLONE_MODE:-full}"
RUNTIME="${RUNTIME:?RUNTIME is required}"
STORAGE_TYPE="${STORAGE_TYPE:?STORAGE_TYPE is required}"
CONCURRENCY="${CONCURRENCY:?CONCURRENCY is required}"
TIME_BIN="${TIME_BIN:-/usr/bin/time}"
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
cat /proc/self/io > "$OUT_DIR/proc-self-io-before.txt" || true
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-before.txt" || true

set +e
"$TIME_BIN" -v -o "$OUT_DIR/time.txt" \
  git clone "${CLONE_ARGS[@]}" "$REPO_URL" "$TARGET_DIR" \
  > "$OUT_DIR/git-stdout.log" 2> "$OUT_DIR/git-stderr.log"
exit_code="$?"
set -e

end_ns="$(date +%s%N)"
duration_ns="$((end_ns - start_ns))"
duration_seconds="$(awk "BEGIN { print ${duration_ns} / 1000000000 }")"

cat /proc/self/io > "$OUT_DIR/proc-self-io-after.txt" || true
du -sb "$TARGET_DIR" > "$OUT_DIR/repo-size-bytes.txt" 2>/dev/null || printf '0\t%s\n' "$TARGET_DIR" > "$OUT_DIR/repo-size-bytes.txt"
find "$TARGET_DIR" -xdev -type f 2>/dev/null | wc -l > "$OUT_DIR/file-count.txt" || echo 0 > "$OUT_DIR/file-count.txt"
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-after.txt" || true

repo_size_bytes="$(awk '{print $1}' "$OUT_DIR/repo-size-bytes.txt")"
file_count="$(tr -d ' ' < "$OUT_DIR/file-count.txt")"

jq -n \
  --arg runtime "$RUNTIME" \
  --arg storage "$STORAGE_TYPE" \
  --arg profile "$CLONE_MODE" \
  --arg concurrency "$CONCURRENCY" \
  --arg sample "$SAMPLE_ID" \
  --argjson cloneDuration "$duration_seconds" \
  --argjson exitCode "$exit_code" \
  --argjson repositorySize "$repo_size_bytes" \
  --argjson fileCount "$file_count" \
  '{schemaVersion:1,dimensions:{runtime:$runtime,storage:$storage,workload:"git",profile:$profile,concurrency:$concurrency,sample:$sample},metrics:[
    {name:"clone_duration",value:$cloneDuration,unit:"seconds"},
    {name:"exit_code",value:$exitCode,unit:"code"},
    {name:"repository_size",value:$repositorySize,unit:"bytes"},
    {name:"file_count",value:$fileCount,unit:"files"}
  ]}' > "$OUT_DIR/summary.json"

cat "$OUT_DIR/summary.json"
exit "$exit_code"
