# Kata IO Output Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `kata-io` run output meaningful by placing kube-burner metric JSON in the run root and removing known-noisy setup/podLatency paths from the suite workload.

**Architecture:** Keep the existing perf-runner lifecycle and existing suite setup mechanism. Fix the kube-burner local metrics directory relative to the rendered config working directory, move persistent kata-io namespace/results PVC setup into suite setup resources, and keep benchmark workloads focused on benchmark jobs only.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3`, kube-burner, Kubernetes YAML, AKS, Prometheus.

## Global Constraints

- Do not delete the provisioned AKS cluster.
- Work only inside `.worktrees/fix-kata-io-output`.
- Use failing tests before production changes.
- Keep changes minimal; do not redesign the full kata-io matrix.
- Use existing `suite.setup` support rather than adding a new setup mechanism.

---

### Task 1: Put kube-burner metrics under the run root

**Files:**
- Modify: `internal/run/run_test.go`
- Modify: `internal/run/run.go`

**Interfaces:**
- Consumes: `RenderWorkload(workload map[string]any, mode Mode, images map[string]string, prometheusEndpoint string) (map[string]any, error)`.
- Produces: rendered `metricsEndpoints[0].indexer.metricsDirectory == "../raw/metrics"`.

- [ ] **Step 1: Write failing test**

Add a test that renders a Prometheus-enabled workload and asserts the local indexer writes to `../raw/metrics`.

- [ ] **Step 2: Verify red**

Run: `go test ./internal/run -run TestRenderWorkloadWritesPrometheusMetricsToRunRoot`

Expected: FAIL because current output is `raw/metrics`.

- [ ] **Step 3: Implement minimal fix**

Change `metricsDirectory` in `internal/run/run.go` to `../raw/metrics` because kube-burner runs with `cmd.Dir` set to `results/<run>/rendered`.

- [ ] **Step 4: Verify green**

Run: `go test ./internal/run -run TestRenderWorkloadWritesPrometheusMetricsToRunRoot`

Expected: PASS.

---

### Task 2: Move persistent kata-io setup out of kube-burner jobs

**Files:**
- Modify: `internal/examples/examples_test.go`
- Modify: `suites/kata-io/suite.yml`
- Create: `suites/kata-io/setup/namespace.yml`
- Create: `suites/kata-io/setup/results-pvc.yml`
- Modify: `suites/kata-io/workload-smoke.yml`
- Modify: `suites/kata-io/workload-full.yml`

**Interfaces:**
- Consumes: existing `suite.setup.resources` schema and `runpkg.ApplySetup`.
- Produces: idempotent setup resources applied before kube-burner and no namespace/results PVC setup jobs in kata-io workloads.

- [ ] **Step 1: Write failing contract tests**

Add examples tests that require kata-io suite setup resources and reject setup job names from smoke/full workloads.

- [ ] **Step 2: Verify red**

Run: `go test ./internal/examples -run 'TestKataIOUsesSuiteSetupForPersistentResources|TestKataIOWorkloadsExcludePersistentSetupJobs'`

Expected: FAIL because setup resources are absent and workloads still contain setup jobs.

- [ ] **Step 3: Add static setup resources**

Add `setup/namespace.yml` and `setup/results-pvc.yml`, then reference them from `suite.yml`.

- [ ] **Step 4: Remove setup jobs from workloads**

Remove `kio-namespace`, `kio-results-pvc`, and `kata-io-results-pvc` jobs from smoke/full workload YAML.

- [ ] **Step 5: Verify green**

Run: `go test ./internal/examples -run 'TestKataIOUsesSuiteSetupForPersistentResources|TestKataIOWorkloadsExcludePersistentSetupJobs'`

Expected: PASS.

---

### Task 3: Remove broken kata-io podLatency measurement and tighten metrics

**Files:**
- Modify: `internal/examples/examples_test.go`
- Modify: `suites/kata-io/workload-smoke.yml`
- Modify: `suites/kata-io/workload-full.yml`
- Modify: `suites/kata-io/metrics.yml`

**Interfaces:**
- Consumes: kata-io workloads and metric profile.
- Produces: no global `podLatency` measurement in kata-io workloads, CPU/memory queries scoped to `namespace="kata-io"`.

- [ ] **Step 1: Write failing contract tests**

Add examples tests that require no `podLatency` in kata-io workloads and namespace filtering in CPU/memory queries.

- [ ] **Step 2: Verify red**

Run: `go test ./internal/examples -run 'TestKataIOWorkloadsDoNotUseBrokenPodLatency|TestKataIOMetricsScopeCPUAndMemoryToNamespace'`

Expected: FAIL because podLatency is present and CPU/memory queries are broad.

- [ ] **Step 3: Implement minimal suite changes**

Remove `global.measurements` from kata-io workloads and add `namespace="kata-io"` selectors to CPU/memory Prometheus queries.

- [ ] **Step 4: Verify green**

Run: `go test ./internal/examples -run 'TestKataIOWorkloadsDoNotUseBrokenPodLatency|TestKataIOMetricsScopeCPUAndMemoryToNamespace'`

Expected: PASS.

---

### Task 4: Full verification and review

**Files:**
- Verify all touched files.

**Interfaces:**
- Consumes: completed Tasks 1-3.
- Produces: reviewed, verified branch ready for user decision.

- [ ] **Step 1: Run targeted tests**

Run: `go test ./internal/run ./internal/examples ./internal/suite ./cmd/perf-runner`

Expected: PASS.

- [ ] **Step 2: Run full local tests**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 3: Request code review**

Run code-reviewer over `git diff HEAD` and independently verify Critical/High findings.

- [ ] **Step 4: Report state**

Summarize the changes, tests, known cluster state, and any remaining manual smoke validation needed.
