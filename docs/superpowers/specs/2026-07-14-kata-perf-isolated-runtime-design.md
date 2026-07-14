# Kata Perf Isolated Runtime Design

## Context

The current `kata-perf` full-run result compares the Kata startup job with the default-runtime startup job in one kube-burner run. The raw metrics show the default-runtime job started after the Kata job left 90 Kata pods resident on the single workload node. Memory was already near node capacity, and the default-runtime latency distribution became dominated by pod sandbox readiness delays rather than normal OCI container startup.

The goal is to make `kata-perf full` an isolated runtime comparison: each runtime should create its pods after the workload node is empty of previous benchmark pods.

## Selected Approach

Use kube-burner per-job garbage collection for both `kata-perf` create jobs.

Each create job will set `gc: true`. kube-burner 2.7.3 runs job-level garbage collection after the job finishes, after `jobPause`, and after pod-latency measurements stop. Measurement indexing may continue asynchronously while garbage collection runs. Kube-burner's default namespace cleanup waits until namespaces with the job label are deleted before the next job proceeds.

The suite will keep the fixed job order:

1. `startup-smoke` for Kata pods.
2. `startup-default-runtime` for default-runtime pods.

The suite will keep the existing latency drain behavior:

- `waitWhenFinished: true`
- `jobPause: 6m`
- `metricsClosing: afterJob`

## Alternatives Considered

### Explicit Delete Job

An explicit `jobType: delete` barrier could delete pods or namespaces between runtimes. This makes the barrier visible in YAML, but it adds an extra kube-burner job to the reporting flow and duplicates kube-burner's existing job garbage-collection path.

### Separate Kube-Burner Invocations

Running Kata and default runtime as separate kube-burner executions would isolate reporting state, but it complicates `perf-runner` orchestration and still would not reset node caches or runtime/CNI state.

### Separate Workload Pools

Dedicated Kata and OCI pools would avoid overlap, but it introduces pool-to-pool variance and no longer measures equivalent placement on the same configured workload node shape.

## Validation

Automated validation will assert that rendered `kata-perf` smoke and full modes:

- Produce the two runtime create jobs.
- Set per-job `gc: true` on both jobs.
- Preserve `waitWhenFinished: true`.
- Preserve `jobPause: 6m`.
- Preserve `metricsClosing: afterJob`.

End-to-end validation will reprovision the `kata-perf` cluster, run `full`, and confirm from logs that the Kata job namespace garbage collection completes before the default-runtime job starts. The README's contaminated result will be replaced only after this isolated rerun succeeds.

## Documentation Updates

The suite README will describe the isolation contract as an empty workload node between runtime jobs. It will also clarify that this does not mean a freshly provisioned node between runtimes; runtime/CNI/node cache effects can still exist because the comparison intentionally uses one fixed job order on the same provisioned cluster.

The existing full-run result should be treated as invalid for runtime comparison and replaced after the isolated rerun.
