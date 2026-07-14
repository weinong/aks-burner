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

## Historical Full Run Result: 2026-07-14T04:00:07Z

This historical `mode=full` result predates the per-job garbage-collection
barrier and is not a valid isolated runtime comparison. The default-runtime job
ran after the Kata pods remained resident on the single workload node. Replace
these numbers after reprovisioning the cluster and rerunning `mode=full` with
the isolation barrier enabled.

Result file:

`results/2026-07-14T04-00-07.742764611Z_kata-perf_full/summary/results.csv`

This `mode=full` run produced 1273 summary rows across the `startup-smoke` and
`startup-default-runtime` jobs:

| Source | Rows |
| --- | ---: |
| `raw/metrics/podCPUUsage.json` | 513 |
| `raw/metrics/podMemoryWorkingSet.json` | 700 |
| `raw/metrics/podLatencyQuantilesMeasurement-startup-smoke.json` | 30 |
| `raw/metrics/podLatencyQuantilesMeasurement-startup-default-runtime.json` | 30 |

The `startup-smoke` job produced 274 rows: 84 CPU, 160 memory, and 30 latency
rows. The `startup-default-runtime` job produced 999 rows: 429 CPU, 540 memory,
and 30 latency rows.

Latency highlights from the CSV:

| Job | Milestone | p50 | p95 | p99 | max |
| --- | --- | ---: | ---: | ---: | ---: |
| `startup-smoke` | `PodScheduled` | 1.467 s | 2.404 s | 2.405 s | 2.406 s |
| `startup-smoke` | `ContainersStarted` | 21.676 s | 27.778 s | 28.058 s | 28.067 s |
| `startup-smoke` | `Ready` | 21.000 s | 27.000 s | 28.000 s | 28.000 s |
| `startup-default-runtime` | `PodScheduled` | 2.399 s | 3.296 s | 3.506 s | 3.714 s |
| `startup-default-runtime` | `ContainersStarted` | 33.112 s | 37.143 s | 37.907 s | 38.139 s |
| `startup-default-runtime` | `Ready` | 33.000 s | 37.000 s | 37.500 s | 38.000 s |

The `kata-perf` workload appeared in `kata-perf-0` and `kata-perf-1` namespaces.
CPU and memory rows include those workload namespaces, and also include
namespaces such as `kube-system`, `gatekeeper-system`, `perf-monitoring`, and
`preload-kube-burner-54555` because metric collection is cluster-wide.

These numbers document one observed run and should not be treated as a stable
performance baseline without repeated runs on comparable cluster capacity.
