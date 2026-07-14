# Kata Perf Serialized Latency Design

## Goal

Make `kata-perf` measure startup latency without kubelet sandbox queueing caused by burst creation, and report the sandbox and post-sandbox phases separately.

## Runtime Cadence

Repurpose both existing modes as serialized latency tests:

| Mode | Samples per runtime | Iterations per namespace | QPS | Burst | Drain pause |
| --- | ---: | ---: | ---: | ---: | ---: |
| `smoke` | 5 | 1 | 5 | 5 | 1 minute |
| `full` | 20 | 1 | 5 | 5 | 1 minute |

Both kube-burner jobs set `podWait: true`. Modes set `waitWhenFinished: false`; kube-burner 2.7.3 otherwise bypasses per-iteration `podWait`. One namespace per iteration ensures kube-burner waits for each new pod rather than treating later iterations in the same namespace as already waited. QPS and burst remain modest API safety limits, while readiness waiting provides serialization.

The fixed order remains Kata first and default runtime second. Each job retains `gc: true`, so the first runtime's namespaces are fully deleted before the second starts. Image preload remains enabled. Prometheus metrics close at `afterMeasurements`, keeping the range open through the one-minute drain pause so short smoke jobs include multiple 15-second scrapes without adding sandbox operations.

## Measurements

### Sandbox-Ready Latency

Retain kube-burner `PodReadyToStartContainers` quantiles. This measures pod creation through the Kubernetes condition indicating sandbox and network readiness. It includes scheduler and kubelet queueing before the CRI call and is therefore the end-to-end sandbox-ready measurement.

### CRI RunPodSandbox

Add two Prometheus queries filtered to `perf_azure_com_node_role="workload"` and grouped by `runtime_handler`:

- `runPodSandboxCount`: increase in `kubelet_run_podsandbox_duration_seconds_count`.
- `runPodSandboxMean`: increase in `_sum` divided by increase in `_count`.

The empty default runtime handler is normalized to `default`; Kata remains `kata`. Kube-burner captures raw counters at each job's exact start and end, and reporting derives the count delta and mean. CRI retries and unrelated workload-node pods can still make the operation count exceed the pod sample count.

Do not report histogram p95/p99. This Kubernetes ALPHA histogram uses default buckets whose last finite boundary is 10 seconds, while observed sandbox calls can exceed 10 seconds, making high quantiles `+Inf`.

### Post-Sandbox Container Launch

Extend the kube-burner adapter to read `podLatencyMeasurement` documents and derive, per pod:

`containersStartedLatency - readyToStartContainersLatency`

Group by `jobName` and report p50, p95, p99, max, average, and valid sample count. Latencies use milliseconds; sample count uses samples. Zero sandbox-ready values are excluded because kube-burner's one-second condition precision cannot distinguish a valid subsecond transition from a missing timestamp. Missing container-start values, nonnumeric values, and negative differences fail reporting.

## Documentation And Validation

Update `kata-perf` requirements, tests, and README for latency-only semantics. Update the root reporting description to document this explicit derived aggregation.

Automated tests cover rendered serialization settings, PromQL filters and units, runtime handler normalization, derived quantiles, malformed samples, and preservation of kube-burner lifecycle quantiles.

End-to-end validation runs smoke and full on the existing provisioned cluster, verifies 5 and 20 serialized pods per runtime, records valid derived sample counts, confirms serialized creation in logs, checks CRI handler output, verifies the inter-job GC barrier, and confirms no benchmark namespaces remain. Derived sample counts may be lower because one-second condition precision makes some subsecond sandbox-ready timestamps ambiguous.
