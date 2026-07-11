#!/usr/bin/env bash
set -euo pipefail

RUN_ID="${RUN_ID:-manual}"
SCENARIO="${SCENARIO:-fio}"
SAMPLE_ID="${SAMPLE_ID:-${HOSTNAME:-sample}}"
FIO_PROFILE="${FIO_PROFILE:?FIO_PROFILE is required}"
FIO_PROFILE_NAME="${FIO_PROFILE_NAME:?FIO_PROFILE_NAME is required}"
RUNTIME="${RUNTIME:?RUNTIME is required}"
STORAGE_TYPE="${STORAGE_TYPE:?STORAGE_TYPE is required}"
CONCURRENCY="${CONCURRENCY:?CONCURRENCY is required}"
TIME_BIN="${TIME_BIN:-/usr/bin/time}"
WORK_DIR="${WORK_DIR:-/work}"
RESULTS_DIR="${RESULTS_DIR:-/results}"
OUT_DIR="${RESULTS_DIR}/${RUN_ID}/${SCENARIO}/${SAMPLE_ID}"
SAMPLE_WORK_DIR="${WORK_DIR}/${RUN_ID}/${SCENARIO}/${SAMPLE_ID}"

mkdir -p "$OUT_DIR" "$SAMPLE_WORK_DIR"

start_ns="$(date +%s%N)"
cat /proc/self/io > "$OUT_DIR/proc-self-io-before.txt" || true
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-before.txt" || true

set +e
"$TIME_BIN" -v -o "$OUT_DIR/time.txt" \
  fio "$FIO_PROFILE" --directory="$SAMPLE_WORK_DIR" --output-format=json --output="$OUT_DIR/fio.json" \
  > "$OUT_DIR/stdout.log" 2> "$OUT_DIR/stderr.log"
exit_code="$?"
set -e

end_ns="$(date +%s%N)"
duration_ns="$((end_ns - start_ns))"
duration_seconds="$(awk "BEGIN { print ${duration_ns} / 1000000000 }")"

cat /proc/self/io > "$OUT_DIR/proc-self-io-after.txt" || true
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-after.txt" || true

read_iops="0"
write_iops="0"
read_bw_bytes="0"
write_bw_bytes="0"
read_clat_p99_ns="0"
write_clat_p99_ns="0"
read_runtime_seconds="0"
write_runtime_seconds="0"
if [ -s "$OUT_DIR/fio.json" ] && jq -e '.jobs | type == "array" and length > 0' "$OUT_DIR/fio.json" > /dev/null 2>&1; then
  read_iops="$(jq '[.jobs[].read.iops // 0] | add' "$OUT_DIR/fio.json" 2>/dev/null || echo 0)"
  write_iops="$(jq '[.jobs[].write.iops // 0] | add' "$OUT_DIR/fio.json" 2>/dev/null || echo 0)"
  read_bw_bytes="$(jq '[.jobs[].read.bw_bytes // 0] | add' "$OUT_DIR/fio.json" 2>/dev/null || echo 0)"
  write_bw_bytes="$(jq '[.jobs[].write.bw_bytes // 0] | add' "$OUT_DIR/fio.json" 2>/dev/null || echo 0)"
  read_clat_p99_ns="$(jq '[.jobs[].read.clat_ns.percentile."99.000000" // 0] | max' "$OUT_DIR/fio.json" 2>/dev/null || echo 0)"
  write_clat_p99_ns="$(jq '[.jobs[].write.clat_ns.percentile."99.000000" // 0] | max' "$OUT_DIR/fio.json" 2>/dev/null || echo 0)"
  read_runtime_seconds="$(jq '[.jobs[].read.runtime // 0] | max / 1000' "$OUT_DIR/fio.json" 2>/dev/null || echo 0)"
  write_runtime_seconds="$(jq '[.jobs[].write.runtime // 0] | max / 1000' "$OUT_DIR/fio.json" 2>/dev/null || echo 0)"
fi
active_runtime_seconds="$(awk "BEGIN { print (${read_runtime_seconds} > ${write_runtime_seconds}) ? ${read_runtime_seconds} : ${write_runtime_seconds} }")"
setup_overhead_seconds="$(awk "BEGIN { value = ${duration_seconds} - ${active_runtime_seconds}; print (value > 0 ? value : 0) }")"

jq -n \
  --arg runtime "$RUNTIME" \
  --arg storage "$STORAGE_TYPE" \
  --arg profile "$FIO_PROFILE_NAME" \
  --arg concurrency "$CONCURRENCY" \
  --arg sample "$SAMPLE_ID" \
  --argjson totalDuration "$duration_seconds" \
  --argjson activeRuntime "$active_runtime_seconds" \
  --argjson setupOverhead "$setup_overhead_seconds" \
  --argjson exitCode "$exit_code" \
  --argjson readIOPS "$read_iops" \
  --argjson writeIOPS "$write_iops" \
  --argjson readBW "$read_bw_bytes" \
  --argjson writeBW "$write_bw_bytes" \
  --argjson readP99 "$read_clat_p99_ns" \
  --argjson writeP99 "$write_clat_p99_ns" \
  '{schemaVersion:1,dimensions:{runtime:$runtime,storage:$storage,workload:"fio",profile:$profile,concurrency:$concurrency,sample:$sample},metrics:[
    {name:"total_duration",value:$totalDuration,unit:"seconds"},
    {name:"active_runtime",value:$activeRuntime,unit:"seconds"},
    {name:"setup_overhead",value:$setupOverhead,unit:"seconds"},
    {name:"exit_code",value:$exitCode,unit:"code"},
    {name:"read_iops",value:$readIOPS,unit:"operations/second"},
    {name:"write_iops",value:$writeIOPS,unit:"operations/second"},
    {name:"read_bandwidth",value:$readBW,unit:"bytes/second"},
    {name:"write_bandwidth",value:$writeBW,unit:"bytes/second"},
    {name:"read_clat_p99",value:$readP99,unit:"nanoseconds"},
    {name:"write_clat_p99",value:$writeP99,unit:"nanoseconds"}
  ]}' > "$OUT_DIR/summary.json"

cat "$OUT_DIR/summary.json"
exit "$exit_code"
