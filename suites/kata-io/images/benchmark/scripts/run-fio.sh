#!/usr/bin/env bash
set -euo pipefail

RUN_ID="${RUN_ID:-manual}"
SCENARIO="${SCENARIO:-fio}"
SAMPLE_ID="${SAMPLE_ID:-${HOSTNAME:-sample}}"
FIO_PROFILE="${FIO_PROFILE:?FIO_PROFILE is required}"
WORK_DIR="${WORK_DIR:-/work}"
RESULTS_DIR="${RESULTS_DIR:-/results}"
OUT_DIR="${RESULTS_DIR}/${RUN_ID}/${SCENARIO}/${SAMPLE_ID}"
SAMPLE_WORK_DIR="${WORK_DIR}/${RUN_ID}/${SCENARIO}/${SAMPLE_ID}"

mkdir -p "$OUT_DIR" "$SAMPLE_WORK_DIR"

start_ns="$(date +%s%N)"
start_epoch="$(date +%s)"
cat /proc/self/io > "$OUT_DIR/proc-self-io-before.txt" || true
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-before.txt" || true

set +e
/usr/bin/time -v -o "$OUT_DIR/time.txt" \
  fio "$FIO_PROFILE" --directory="$SAMPLE_WORK_DIR" --output-format=json --output="$OUT_DIR/fio.json" \
  > "$OUT_DIR/stdout.log" 2> "$OUT_DIR/stderr.log"
exit_code="$?"
set -e

end_ns="$(date +%s%N)"
end_epoch="$(date +%s)"
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
if [ -s "$OUT_DIR/fio.json" ]; then
  read_iops="$(jq '[.jobs[].read.iops // 0] | add' "$OUT_DIR/fio.json")"
  write_iops="$(jq '[.jobs[].write.iops // 0] | add' "$OUT_DIR/fio.json")"
  read_bw_bytes="$(jq '[.jobs[].read.bw_bytes // 0] | add' "$OUT_DIR/fio.json")"
  write_bw_bytes="$(jq '[.jobs[].write.bw_bytes // 0] | add' "$OUT_DIR/fio.json")"
  read_clat_p99_ns="$(jq '[.jobs[].read.clat_ns.percentile."99.000000" // 0] | max' "$OUT_DIR/fio.json")"
  write_clat_p99_ns="$(jq '[.jobs[].write.clat_ns.percentile."99.000000" // 0] | max' "$OUT_DIR/fio.json")"
  read_runtime_seconds="$(jq '[.jobs[].read.runtime // 0] | max / 1000' "$OUT_DIR/fio.json")"
  write_runtime_seconds="$(jq '[.jobs[].write.runtime // 0] | max / 1000' "$OUT_DIR/fio.json")"
fi
active_runtime_seconds="$(awk "BEGIN { print (${read_runtime_seconds} > ${write_runtime_seconds}) ? ${read_runtime_seconds} : ${write_runtime_seconds} }")"
setup_overhead_seconds="$(awk "BEGIN { value = ${duration_seconds} - ${active_runtime_seconds}; print value > 0 ? value : 0 }")"

cat > "$OUT_DIR/summary.prom" <<EOF
# TYPE fio_total_duration_seconds gauge
fio_total_duration_seconds{run_id="$RUN_ID",scenario="$SCENARIO"} $duration_seconds
# TYPE fio_active_runtime_seconds gauge
fio_active_runtime_seconds{run_id="$RUN_ID",scenario="$SCENARIO"} $active_runtime_seconds
# TYPE fio_read_runtime_seconds gauge
fio_read_runtime_seconds{run_id="$RUN_ID",scenario="$SCENARIO"} $read_runtime_seconds
# TYPE fio_write_runtime_seconds gauge
fio_write_runtime_seconds{run_id="$RUN_ID",scenario="$SCENARIO"} $write_runtime_seconds
# TYPE fio_setup_overhead_seconds gauge
fio_setup_overhead_seconds{run_id="$RUN_ID",scenario="$SCENARIO"} $setup_overhead_seconds
# TYPE fio_exit_code gauge
fio_exit_code{run_id="$RUN_ID",scenario="$SCENARIO"} $exit_code
# TYPE fio_start_time_seconds gauge
fio_start_time_seconds{run_id="$RUN_ID",scenario="$SCENARIO"} $start_epoch
# TYPE fio_end_time_seconds gauge
fio_end_time_seconds{run_id="$RUN_ID",scenario="$SCENARIO"} $end_epoch
# TYPE fio_read_iops gauge
fio_read_iops{run_id="$RUN_ID",scenario="$SCENARIO"} $read_iops
# TYPE fio_write_iops gauge
fio_write_iops{run_id="$RUN_ID",scenario="$SCENARIO"} $write_iops
# TYPE fio_read_bw_bytes_per_second gauge
fio_read_bw_bytes_per_second{run_id="$RUN_ID",scenario="$SCENARIO"} $read_bw_bytes
# TYPE fio_write_bw_bytes_per_second gauge
fio_write_bw_bytes_per_second{run_id="$RUN_ID",scenario="$SCENARIO"} $write_bw_bytes
# TYPE fio_read_clat_p99_nanoseconds gauge
fio_read_clat_p99_nanoseconds{run_id="$RUN_ID",scenario="$SCENARIO"} $read_clat_p99_ns
# TYPE fio_write_clat_p99_nanoseconds gauge
fio_write_clat_p99_nanoseconds{run_id="$RUN_ID",scenario="$SCENARIO"} $write_clat_p99_ns
EOF

cat "$OUT_DIR/summary.prom"
exit "$exit_code"
