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

Both modes run both jobs, enable cleanup, wait for the jobs to finish, preload
images, and pause each job for 6 minutes before stopping kube-burner
measurements so pod latency callbacks can drain. Prometheus metrics close
before this drain pause by setting `metricsClosing: afterJob`.

## Measurements

- kube-burner `podLatency` quantiles for pod lifecycle milestones such as
  `PodScheduled`, `ContainersStarted`, and `Ready`.
- Prometheus `podCPUUsage`, reported in cores.
- Prometheus `podMemoryWorkingSet`, reported in bytes.

The Prometheus queries in `metrics.yml` aggregate by pod and namespace across
the cluster. CPU and memory rows in the summary therefore include system and
monitoring namespaces as well as `kata-perf-*` workload rows.

## Full Run Result: 2026-07-14T02:44:56Z

Result file:

`results/2026-07-14T02-44-56.875253731Z_kata-perf_full/summary/results.csv`

This `mode=full` run produced 975 summary rows across the `startup-smoke` and
`startup-default-runtime` jobs:

| Source | Rows |
| --- | ---: |
| `raw/metrics/podCPUUsage.json` | 295 |
| `raw/metrics/podMemoryWorkingSet.json` | 620 |
| `raw/metrics/podLatencyQuantilesMeasurement-startup-smoke.json` | 30 |
| `raw/metrics/podLatencyQuantilesMeasurement-startup-default-runtime.json` | 30 |

The `startup-smoke` job produced 435 rows: 123 CPU, 282 memory, and 30 latency
rows. The `startup-default-runtime` job produced 540 rows: 172 CPU, 338 memory,
and 30 latency rows.

Latency highlights from the CSV:

| Job | Milestone | p50 | p95 | p99 | max |
| --- | --- | ---: | ---: | ---: | ---: |
| `startup-smoke` | `PodScheduled` | 2.046 s | 2.729 s | 2.934 s | 2.978 s |
| `startup-smoke` | `ContainersStarted` | 27.040 s | 33.568 s | 33.770 s | 33.809 s |
| `startup-smoke` | `Ready` | 26.500 s | 33.000 s | 33.000 s | 33.000 s |
| `startup-default-runtime` | `PodScheduled` | 963 ms | 976 ms | 976 ms | 976 ms |
| `startup-default-runtime` | `ContainersStarted` | 2.808 s | 3.708 s | 3.708 s | 3.809 s |
| `startup-default-runtime` | `Ready` | 3.000 s | 3.000 s | 3.000 s | 3.000 s |

The `kata-perf` workload appeared in `kata-perf-0` and `kata-perf-1` namespaces.
CPU and memory rows include those workload namespaces, and also include
namespaces such as `kube-system`, `gatekeeper-system`, `perf-monitoring`, and
`preload-kube-burner-98343` because metric collection is cluster-wide.

These numbers document one observed run and should not be treated as a stable
performance baseline without repeated runs on comparable cluster capacity.
