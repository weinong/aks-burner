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
if [ "$exit_code" -eq 0 ]; then
  if ! parsed_metrics="$(jq -er '
    def numeric: type == "number";
    if (.jobs | type) != "array" or (.jobs | length) == 0 then
      error("jobs must be a non-empty array")
    elif all(.jobs[];
      (.read.iops | numeric) and
      (.write.iops | numeric) and
      (.read.bw_bytes | numeric) and
      (.write.bw_bytes | numeric) and
      (.read.clat_ns.percentile."99.000000" | numeric) and
      (.write.clat_ns.percentile."99.000000" | numeric) and
      (.read.runtime | numeric) and
      (.write.runtime | numeric)
    ) | not then
      error("summary fields must all be numeric")
    else
      [
        ([.jobs[].read.iops] | add),
        ([.jobs[].write.iops] | add),
        ([.jobs[].read.bw_bytes] | add),
        ([.jobs[].write.bw_bytes] | add),
        ([.jobs[].read.clat_ns.percentile."99.000000"] | max),
        ([.jobs[].write.clat_ns.percentile."99.000000"] | max),
        ([.jobs[].read.runtime] | max / 1000),
        ([.jobs[].write.runtime] | max / 1000)
      ] | @tsv
    end
  ' "$OUT_DIR/fio.json")"; then
    printf 'failed to parse required numeric FIO summary fields from %s\n' "$OUT_DIR/fio.json" >&2
    exit 1
  fi
  IFS=$'\t' read -r read_iops write_iops read_bw_bytes write_bw_bytes read_clat_p99_ns write_clat_p99_ns read_runtime_seconds write_runtime_seconds <<< "$parsed_metrics"
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
