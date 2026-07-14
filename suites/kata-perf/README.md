# Kata Performance Suite

`kata-perf` measures Kata and default-runtime pod startup behavior on AKS by
creating short-lived pause pods through kube-burner and collecting pod latency
plus Prometheus pod resource metrics.

## What It Tests

- Runs the `startup-smoke` and `startup-default-runtime` kube-burner create
  jobs from `workload.yml`.
- Creates one Kata pod per iteration with `runtimeClassName: kata-vm-isolation`
  and one default-runtime pod per iteration without a runtime class.
- Uses the `pause` image from `config/images.yml`:
  `mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2`.
- Schedules the pods only onto Azure Linux workload nodes matching:
  `perf.azure.com/node-role=workload` and
  `kubernetes.azure.com/os-sku=AzureLinux`.
- Requires an AKS cluster with Kubernetes `>= 1.36`, a Ubuntu OCI system pool,
  and an AzureLinux user pool configured for `KataMshvVmIsolation`.

## Modes

| Mode | Iterations | Iterations per namespace | QPS | Burst |
| --- | ---: | ---: | ---: | ---: |
| `smoke` | 20 | 20 | 20 | 20 |
| `full` | 90 | 50 | 50 | 50 |

Both modes run the Kata job first and the default-runtime job second. Image
preloading is enabled for both jobs. Each job enables per-job garbage
collection, waits for its pods to finish, pauses for 6 minutes so pod latency
callbacks can drain, and then deletes its generated namespaces before the next
runtime job starts. Prometheus metrics close before the drain pause by setting
`metricsClosing: afterJob`.

## Measurements

- kube-burner `podLatency` quantiles for pod lifecycle milestones such as
  `PodScheduled`, `ContainersStarted`, and `Ready`.
- Prometheus `podCPUUsage`, reported in cores.
- Prometheus `podMemoryWorkingSet`, reported in bytes.

The Prometheus queries in `metrics.yml` aggregate by pod and namespace across
the cluster. CPU and memory rows in the summary therefore include system and
monitoring namespaces as well as `kata-perf-*` workload rows.

## Full Run Result: 2026-07-14T05:27:29Z

This `mode=full` run used a freshly provisioned cluster and the per-job
garbage-collection barrier. The kube-burner log shows `startup-smoke`
namespaces were deleted before `startup-default-runtime` started, and a
post-run check found no benchmark pods or namespaces remaining.

Result file:

`results/2026-07-14T05-27-29.296529536Z_kata-perf_full/summary/results.csv`

This `mode=full` run produced 1029 summary rows across the `startup-smoke` and
`startup-default-runtime` jobs:

| Source | Rows |
| --- | ---: |
| `raw/metrics/podCPUUsage.json` | 372 |
| `raw/metrics/podMemoryWorkingSet.json` | 597 |
| `raw/metrics/podLatencyQuantilesMeasurement-startup-smoke.json` | 30 |
| `raw/metrics/podLatencyQuantilesMeasurement-startup-default-runtime.json` | 30 |

The `startup-smoke` job produced 415 rows: 118 CPU, 267 memory, and 30 latency
rows. The `startup-default-runtime` job produced 614 rows: 254 CPU, 330 memory,
and 30 latency rows.

Latency highlights from the CSV:

| Job | Milestone | p50 | p95 | p99 | max |
| --- | --- | ---: | ---: | ---: | ---: |
| `startup-smoke` | `PodScheduled` | 0.757 s | 1.320 s | 1.467 s | 1.468 s |
| `startup-smoke` | `ContainersStarted` | 25.626 s | 30.919 s | 31.217 s | 31.225 s |
| `startup-smoke` | `Ready` | 25.000 s | 30.000 s | 31.000 s | 31.000 s |
| `startup-default-runtime` | `PodScheduled` | 0.834 s | 1.448 s | 1.594 s | 1.623 s |
| `startup-default-runtime` | `ContainersStarted` | 13.224 s | 17.336 s | 17.832 s | 18.146 s |
| `startup-default-runtime` | `Ready` | 13.000 s | 17.000 s | 17.500 s | 18.000 s |

The `kata-perf` workload appeared in `kata-perf-0` and `kata-perf-1` namespaces.
CPU and memory rows include those workload namespaces, and also include
namespaces such as `kube-system`, `gatekeeper-system`, `perf-monitoring`, and
`preload-kube-burner-54555` because metric collection is cluster-wide.
Prometheus CPU and memory rows can still include recently deleted workload pods
from the prior runtime because the metrics are collected after the run and use
range queries. Use kube-burner `podLatency` rows for the isolated startup
comparison.

These numbers document one observed run and should not be treated as a stable
performance baseline without repeated runs on comparable cluster capacity.
