# Kata Performance Suite

`kata-perf` measures Kata pod startup behavior on AKS by creating short-lived
pause pods through kube-burner and collecting pod latency plus Prometheus pod
resource metrics.

## What It Tests

- Runs the `startup-smoke` kube-burner create job from `workload.yml`.
- Creates one pod per iteration with `runtimeClassName: kata-vm-isolation`.
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

Both modes enable cleanup, wait for the job to finish, and preload images.

## Measurements

- kube-burner `podLatency` quantiles for pod lifecycle milestones such as
  `PodScheduled`, `ContainersStarted`, and `Ready`.
- Prometheus `podCPUUsage`, reported in cores.
- Prometheus `podMemoryWorkingSet`, reported in bytes.

The Prometheus queries in `metrics.yml` aggregate by pod and namespace across
the cluster. CPU and memory rows in the summary therefore include system and
monitoring namespaces; this run captured `kata-perf-*` rows for memory, but not
for CPU.

## Full Run Result: 2026-07-14T01:39:33Z

Result file committed with this README:

`results/2026-07-14T01-39-33.336647736Z_kata-perf_full/summary/results.csv`

This run used `mode=full`. It produced 344 summary rows for the single
`startup-smoke` job:

| Source | Rows |
| --- | ---: |
| `raw/metrics/podCPUUsage.json` | 60 |
| `raw/metrics/podMemoryWorkingSet.json` | 254 |
| `raw/metrics/podLatencyQuantilesMeasurement-startup-smoke.json` | 30 |

Latency highlights from the CSV:

| Milestone | p50 | p95 | p99 | max |
| --- | ---: | ---: | ---: | ---: |
| `PodScheduled` | 984 ms | 1260 ms | 1262 ms | 1263 ms |
| `ContainersStarted` | 13.903 s | 24.614 s | 25.042 s | 25.192 s |
| `Ready` | 13.000 s | 24.000 s | 24.500 s | 25.000 s |

The `kata-perf` workload appeared in `kata-perf-0` and `kata-perf-1` namespaces.
Memory rows include those workload namespaces. CPU and memory rows also include
namespaces such as `kube-system`, `gatekeeper-system`, `perf-monitoring`, and
the kube-burner preload namespace because metric collection is cluster-wide.

These numbers document one observed run and should not be treated as a stable
performance baseline without repeated runs on comparable cluster capacity.
