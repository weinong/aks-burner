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
`metricsClosing: afterJob`. The installed Prometheus deployment is pinned to
the AKS system node pool so monitoring traffic does not share the Kata workload
node during the startup burst.

## Measurements

- kube-burner `podLatency` quantiles for pod lifecycle milestones such as
  `PodScheduled`, `ContainersStarted`, and `Ready`.
- Prometheus `podCPUUsage`, reported in cores.
- Prometheus `podMemoryWorkingSet`, reported in bytes.

The Prometheus queries in `metrics.yml` aggregate by pod and namespace across
the cluster. CPU and memory rows in the summary therefore include system and
monitoring namespaces as well as `kata-perf-*` workload rows.

## Full Run Result: 2026-07-14T05:59:17Z

This `mode=full` run used a freshly provisioned cluster and the per-job
garbage-collection barrier. The kube-burner log shows `startup-smoke`
namespaces were deleted before `startup-default-runtime` started, and a
post-run check found no benchmark pods or namespaces remaining.

Result file:

`results/2026-07-14T05-59-17.242475499Z_kata-perf_full/summary/results.csv`

This `mode=full` run produced 887 summary rows across the `startup-smoke` and
`startup-default-runtime` jobs:

| Source | Rows |
| --- | ---: |
| `raw/metrics/podCPUUsage.json` | 339 |
| `raw/metrics/podMemoryWorkingSet.json` | 488 |
| `raw/metrics/podLatencyQuantilesMeasurement-startup-smoke.json` | 30 |
| `raw/metrics/podLatencyQuantilesMeasurement-startup-default-runtime.json` | 30 |

The `startup-smoke` job produced 261 rows: 73 CPU, 158 memory, and 30 latency
rows. The `startup-default-runtime` job produced 626 rows: 266 CPU, 330 memory,
and 30 latency rows.

Latency highlights from the CSV:

| Job | Milestone | p50 | p95 | p99 | max |
| --- | --- | ---: | ---: | ---: | ---: |
| `startup-smoke` | `PodScheduled` | 1.058 s | 1.660 s | 1.728 s | 1.729 s |
| `startup-smoke` | `ContainersStarted` | 21.710 s | 27.001 s | 27.339 s | 27.476 s |
| `startup-smoke` | `Ready` | 21.000 s | 26.500 s | 27.000 s | 27.000 s |
| `startup-default-runtime` | `PodScheduled` | 1.138 s | 1.559 s | 1.697 s | 1.726 s |
| `startup-default-runtime` | `ContainersStarted` | 16.887 s | 20.355 s | 20.746 s | 20.811 s |
| `startup-default-runtime` | `Ready` | 16.000 s | 20.000 s | 20.000 s | 20.000 s |

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
