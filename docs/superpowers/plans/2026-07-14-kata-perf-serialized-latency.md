# Kata Perf Serialized Latency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serialize kata-perf samples and report sandbox-ready, CRI sandbox, and post-sandbox container-launch latency separately.

**Architecture:** Keep serialization in the suite workload and mode files. Add workload-node-scoped Prometheus sandbox count/mean queries, and narrowly extend the kube-burner reporting adapter to aggregate post-sandbox launch values from raw pod-latency documents.

**Tech Stack:** Go 1.25, kube-burner 2.7.3, Kubernetes 1.36, PromQL, YAML.

## Global Constraints

- Keep modes named `smoke` and `full`.
- Smoke uses 5 samples per runtime; full uses 20.
- Both modes use `podWait: true`, `waitWhenFinished: false`, `iterationsPerNamespace: 1`, `qps: 5`, `burst: 5`, and `jobPause: 1m`.
- Preserve Kata-first ordering, image preload, `metricsClosing: afterMeasurements`, and per-job `gc: true`.
- Do not add histogram quantiles for `kubelet_run_podsandbox_duration_seconds_bucket`.
- Do not use aks-tsg.

---

### Task 1: Serialize Kata Perf Modes

**Files:**
- Modify: `internal/examples/examples_test.go`
- Modify: `suites/kata-perf/workload.yml`
- Modify: `suites/kata-perf/vars/smoke.yml`
- Modify: `suites/kata-perf/vars/full.yml`

**Interfaces:**
- Consumes: existing `run.RenderWorkload` default injection.
- Produces: two runtime jobs serialized by per-iteration readiness.

- [ ] Add failing contract assertions for sample counts and exact scheduling fields.
- [ ] Run `go test ./internal/examples -run 'TestKataPerf' -count=1` and confirm failure.
- [ ] Set `podWait: true` on both jobs and update mode values to the approved table.
- [ ] Rerun the focused tests and confirm success.

### Task 2: Add CRI Sandbox Count And Mean

**Files:**
- Modify: `internal/examples/examples_test.go`
- Modify: `suites/kata-perf/metrics.yml`
- Modify: `suites/kata-perf/requirements.yml`
- Modify: `internal/reporting/kube_burner_test.go`
- Modify: `internal/reporting/kube_burner.go`

**Interfaces:**
- Produces: `runPodSandboxCount` in `count` and `runPodSandboxMean` in `seconds`, dimensioned by normalized `label.runtime_handler`.

- [ ] Add failing tests requiring workload-node filtering, count/mean queries, units, required source metrics, and empty-handler normalization.
- [ ] Run focused example/reporting tests and confirm failure.
- [ ] Replace CPU/memory queries and units with sandbox count/mean; normalize empty `runtime_handler` to `default` in `appendPrometheusRow`.
- [ ] Rerun focused tests and confirm success.

### Task 3: Derive Post-Sandbox Launch Summaries

**Files:**
- Modify: `internal/reporting/kube_burner.go`
- Modify: `internal/reporting/kube_burner_test.go`
- Rename/modify: `internal/reporting/testdata/kube-burner-2.7.3/ignored-podLatencyMeasurement.json` to `podLatencyMeasurement.json`

**Interfaces:**
- Consumes raw `podLatencyMeasurement` fields `jobName`, `containersStartedLatency`, and `readyToStartContainersLatency`.
- Produces six `post_sandbox_container_launch_latency_*` rows per job: p50, p95, p99, max, avg, and sample count.

- [ ] Add failing tests with multiple jobs and known latency differences, plus malformed and negative cases.
- [ ] Run `go test ./internal/reporting -run 'TestReadKubeBurnerMetrics' -count=1` and confirm failure.
- [ ] Implement grouping with kube-burner's pinned `montanaflynn/stats` percentile convention, average, max, and sample count.
- [ ] Rerun reporting tests and confirm success.

### Task 4: Document And Verify

**Files:**
- Modify: `suites/kata-perf/README.md`
- Modify: `README.md`

- [ ] Rewrite mode and measurement documentation for serialized latency semantics.
- [ ] Mark the prior burst result obsolete until a serialized full rerun replaces it.
- [ ] Run `go test ./...` and `go build ./cmd/perf-runner`.
- [ ] Run smoke and full against the existing cluster; verify sample counts, handler labels, sequential log timing, GC barrier, and cleanup.
- [ ] Request final code review, fix verified findings, commit, merge locally to `main`, rerun `go test ./...`, and remove only this workflow's worktree and branch.

## Self-Review

- The plan covers every approved design requirement.
- No generic mode schema or new mode is introduced.
- CRI histogram quantiles are intentionally excluded.
- Reporting aggregation is scoped to the pinned kube-burner document shape.
