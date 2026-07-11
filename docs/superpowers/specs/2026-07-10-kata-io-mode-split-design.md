# Kata IO Mode Split Design

## Goal

Split the large `kata-io` benchmark surface into practical mode-level entry points so quick end-to-end verification does not require the exhaustive `full` workload.

## Modes

- `fio-fast`: fastest fio end-to-end validation. It covers standard and Kata runtimes on `emptyDir`, uses one short fio profile (`randread-4k`), and runs concurrency 1.
- `git-fast`: fastest Git clone end-to-end validation. It covers standard and Kata runtimes on `emptyDir`, uses blobless clone, and runs concurrency 1.
- `fio`: broader fio benchmark mode. It covers standard and Kata runtimes, `emptyDir`, Azure Disk, Azure Files, all fio profiles, and concurrency 1 and 10.
- `git`: broader Git clone benchmark mode. It covers standard and Kata runtimes, `emptyDir`, Azure Disk, Azure Files, full and blobless clone, and concurrency 1 and 10.
- `smoke` and `full` are removed from the suite's exposed modes; use `fio-fast` or `git-fast` for quick validation.

## Shared Behavior

Every kata-io workload mode must use the same persistent setup and run safety behavior:

- Namespace and results PVC are managed by `suite.setup`, not kube-burner workload jobs.
- Each workload starts with kube-burner delete jobs that clean previous kata-io benchmark Jobs and work PVCs, but never delete the persistent results PVC or the cluster.
- Benchmark Kubernetes Job names include the DNS-safe per-run `k8sRunID` to avoid rerun collisions.
- Kube-burner metrics are written to the run root at `raw/metrics`.
- Artifact copy remains required.

## Verification Expectations

- `fio-fast` and `git-fast` should be suitable for quick end-to-end validation of provisioning, image build, kube-burner rendering/execution, Prometheus collection, and artifact copy.
- `fio` and `git` should be suitable for domain-specific benchmark runs without requiring the entire `full` matrix.
- Tests should validate that each mode selects the expected workload file and that each workload contains only scenarios in its intended domain.

## Non-Goals

- Do not delete the cluster.
- Do not introduce a generic workload matrix engine in this change.
- Do not change benchmark scripts or fio profiles.
