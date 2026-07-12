# FIO Inactive Direction And Fail-Fast Design

## Goal

Allow valid read-only and write-only FIO output to produce strict summaries, and make a failed benchmark Job terminate `run-suite` promptly instead of waiting for the global timeout.

## Root Cause

FIO omits `clat_ns.percentile` for an inactive direction. The current parser requires read and write p99 latency unconditionally, so a valid read-only result fails after FIO exits successfully. Kube-burner then waits for Job completion even after Kubernetes marks the Job failed with `BackoffLimitExceeded`.

## FIO Parsing

For every FIO job, require numeric IOPS, bandwidth, runtime, and `total_ios` for both read and write directions.

- When `total_ios > 0`, require numeric p99 completion latency for that direction.
- When `total_ios == 0`, permit the percentile object to be absent and report p99 latency as zero.
- Continue rejecting malformed or incomplete fields for an active direction.
- Preserve diagnostic zero-valued benchmark metrics when FIO itself exits nonzero, while returning the original FIO exit code.

The summary retains a stable metric set for CSV consumers.

## Fail-Fast Behavior

Run kube-burner and a Kubernetes Job watcher under a child execution context. Kube-burner must use `exec.CommandContext`, so canceling that child context terminates a process blocked in `waitWhenFinished`.

The watcher polls Jobs in the artifact namespace and considers only Job names beginning with the rendered run's unique `k8sRunID`. Stale failed Jobs from earlier runs are ignored. A Job is terminally failed only when its `Failed` condition is `True`; temporary pod states do not trigger cancellation.

Lifecycle:

1. Create a child context from the caller's parent context.
2. Start the current-run Job watcher and kube-burner concurrently.
3. When a current-run Job reports `Failed=True`, record an error naming the Job and condition and cancel the child context.
4. Whenever kube-burner exits, successfully or unsuccessfully, cancel the child context and wait for the watcher to exit without leaking goroutines.
5. Copy diagnostic artifacts using the uncanceled parent context.
6. Return the Job failure and skip successful report generation.

On successful kube-burner completion, continue existing wait, copy, and report sequencing after the watcher exits.

## Testing

- A captured read-only FIO shape with no write percentile succeeds and reports write p99 as zero.
- A captured write-only shape with no read percentile succeeds and reports read p99 as zero.
- An active direction missing p99 fails and produces no summary.
- A failed Job causes execution to return before the long timeout and skips reporting.
- A stale failed Job whose name does not match the current `k8sRunID` is ignored.
- Kube-burner child-context cancellation does not cancel diagnostic artifact copying.
- Successful Jobs retain existing execution, wait, copy, and report ordering.
