# Kata Performance Suite

`kata-perf` compares serialized Kata and default-runtime pod startup latency on
AKS. It creates pause pods through kube-burner and separates end-to-end sandbox
readiness, the CRI `RunPodSandbox` call, and post-sandbox container launch.

## What It Tests

- Runs Kata first with `runtimeClassName: kata-vm-isolation`, then the default
  runtime without a runtime class.
- Creates one pod and waits for it to become ready before creating the next.
- Uses `mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2`, preloaded before the
  jobs start.
- Schedules workload pods only on Azure Linux nodes labeled
  `perf.azure.com/node-role=workload`.
- Deletes all namespaces from the first runtime before the second starts.
- Pins installed Prometheus to the AKS system node pool.

## Modes

| Mode | Samples per runtime | Iterations per namespace | QPS | Burst | Drain pause |
| --- | ---: | ---: | ---: | ---: | ---: |
| `smoke` | 5 | 1 | 5 | 5 | 1 minute |
| `full` | 20 | 1 | 5 | 5 | 1 minute |

Both jobs use `podWait: true` and `waitWhenFinished: false`. In kube-burner
2.7.3 this combination waits after every iteration. One namespace per
iteration prevents a previously waited namespace from allowing later pods to
queue. QPS and burst limit Kubernetes API traffic; they do not provide the
serialization.

Prometheus closes each job's metric window after the one-minute event-drain
pause. This guarantees multiple 15-second Prometheus scrapes even when five
serialized pods start quickly. No additional sandbox operations occur during
the pause. Per-job garbage collection then waits for namespace deletion before
the next runtime starts.

## Measurements

### Sandbox Ready

kube-burner `PodReadyToStartContainers` quantiles report elapsed time from pod
creation until Kubernetes says the sandbox and networking are ready. This
includes scheduling and kubelet queueing before the CRI call.

### CRI Sandbox Call

Prometheus reports `runPodSandboxCount` and `runPodSandboxMean` from
`kubelet_run_podsandbox_duration_seconds_count` and `_sum`, filtered to the
workload node and grouped by `runtime_handler`. The empty default handler is
shown as `default`; Kata is shown as `kata`.

The suite captures raw counters at each job's exact start and end; reporting
derives the count delta and mean from those values. CRI retries or unrelated
workload-node pods can still add operations, so handler labels and counts remain
visible rather than being collapsed into one runtime value.
Histogram p95/p99 are intentionally omitted: this ALPHA metric's final finite
default bucket is 10 seconds, so slower calls make high quantiles infinite.

### Post-Sandbox Container Launch

The reporter derives per-pod launch latency as:

`containersStartedLatency - readyToStartContainersLatency`

It reports p50, p95, p99, max, average, and valid sample count for each runtime
job. Kube-burner condition timestamps have one-second precision, so a valid
subsecond sandbox-ready transition can appear as zero and is excluded as
ambiguous. The sample-count row makes this visible. Missing container-start
events, malformed fields, and negative differences fail reporting.

## Serialized Full Result: 2026-07-14T18:58:30Z

Result file:

`results/2026-07-14T18-58-30.413356933Z_kata-perf_full/summary/results.csv`

The run created 20 pods serially for each runtime. The kube-burner log shows a
wait for every iteration and deletion of all 20 Kata namespaces before the
default-runtime job started. No benchmark namespaces remained afterward.

| Runtime | Sandbox-ready p50 | Sandbox-ready p95 | Sandbox-ready p99 | CRI sandbox count | CRI sandbox mean | Post-sandbox p50 | Post-sandbox p95 | Valid launch samples |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Kata | 1.000 s | 2.000 s | 2.000 s | 20 | 1.111 s | 0.856 s | 1.195 s | 20/20 |
| Default | 1.000 s | 1.000 s | 1.000 s | 20 | 0.391 s | 0.561 s | 0.818 s | 15/20 |

These full-run launch percentiles were produced before the adapter was aligned
to kube-burner's pinned percentile algorithm. The count/mean and lifecycle
values remain valid; replace the launch percentile values after the next
serialized full run.

One run is not a stable baseline. Repeat runs on comparable cluster capacity
before drawing performance conclusions.
