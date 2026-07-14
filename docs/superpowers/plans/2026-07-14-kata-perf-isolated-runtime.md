# Kata Perf Isolated Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `kata-perf full` compare Kata and default-runtime startup from an empty workload node between runtime jobs.

**Architecture:** Keep the existing two kube-burner create jobs and fixed order, but set job-level `gc: true` on both jobs so kube-burner deletes and waits for each job's generated namespaces before proceeding. Preserve the existing measurement drain settings and update tests and README to encode the isolation contract.

**Tech Stack:** Go, kube-burner YAML workload configuration, Markdown documentation.

## Global Constraints

- The runtime order remains fixed: `startup-smoke` for Kata, then `startup-default-runtime` for default-runtime pods.
- Both runtime jobs must keep `waitWhenFinished: true`, `jobPause: 6m`, and `metricsClosing: afterJob`.
- Isolation means an empty workload node between runtime jobs, not a freshly provisioned node between runtimes.
- The current full-run result must not be treated as a valid isolated runtime comparison until the cluster is reprovisioned and `full` is rerun.

---

## File Structure

- `suites/kata-perf/workload.yml` owns the kube-burner job graph for the suite. Add per-job GC here.
- `internal/examples/examples_test.go` owns suite contract tests. Extend the existing kata-perf render test so future changes cannot remove the isolation barrier.
- `suites/kata-perf/README.md` owns user-facing benchmark semantics and result notes. Update it to describe the empty-node barrier and mark the published result as contaminated pending rerun.
- `docs/superpowers/specs/2026-07-14-kata-perf-isolated-runtime-design.md` records the approved design.

---

### Task 1: Encode Per-Job GC Contract

**Files:**
- Modify: `internal/examples/examples_test.go`
- Modify: `suites/kata-perf/workload.yml`

**Interfaces:**
- Consumes: `run.RenderWorkload(workload map[string]any, mode run.Mode, images map[string]string, prometheusEndpoint string, kubeBurnerReporting bool) (map[string]any, error)`
- Produces: rendered `kata-perf` jobs with `gc: true`, `waitWhenFinished: true`, `jobPause: "6m"`, and `metricsClosing: "afterJob"`

- [ ] **Step 1: Write the failing test assertion**

Edit `internal/examples/examples_test.go` inside `TestKataPerfModesRenderPodLatencyDrainMitigation`. Keep the existing assertions and add the `gc` assertion inside the per-job loop:

```go
if got, want := job["gc"], true; got != want {
	t.Fatalf("kata-perf %s job %v gc = %#v, want %#v", modeName, name, got, want)
}
```

- [ ] **Step 2: Run the focused test to verify it fails**

Run: `go test ./internal/examples -run TestKataPerfModesRenderPodLatencyDrainMitigation -v`

Expected: FAIL with a message like `gc = <nil>, want true`.

- [ ] **Step 3: Add per-job GC to the workload**

Edit `suites/kata-perf/workload.yml` so both create jobs include `gc: true`:

```yaml
  - name: startup-smoke
    jobType: create
    gc: true
    namespace: kata-perf
```

```yaml
  - name: startup-default-runtime
    jobType: create
    gc: true
    namespace: kata-perf
```

- [ ] **Step 4: Run focused tests to verify the contract passes**

Run: `go test ./internal/examples -run TestKataPerfModesRenderPodLatencyDrainMitigation -v`

Expected: PASS.

- [ ] **Step 5: Run the broader kata-perf example tests**

Run: `go test ./internal/examples -run 'TestKataPerf' -v`

Expected: PASS.

---

### Task 2: Update User-Facing Documentation

**Files:**
- Modify: `suites/kata-perf/README.md`

**Interfaces:**
- Consumes: the implemented per-job `gc: true` contract from Task 1.
- Produces: README text that describes the empty-node barrier and disqualifies the current published result as an isolated comparison until rerun.

- [ ] **Step 1: Update the modes/behavior text**

In `suites/kata-perf/README.md`, replace the paragraph after the mode table with text equivalent to:

```markdown
Both modes run the Kata job first and the default-runtime job second. Each job
enables per-job garbage collection, waits for its pods to finish, pauses for 6
minutes so pod latency callbacks can drain, and then deletes its generated
namespaces before the next runtime job starts. Prometheus metrics close before
the drain pause by setting `metricsClosing: afterJob`.
```

- [ ] **Step 2: Add isolation caveat text near the result section**

In the full-run result section, add text equivalent to:

```markdown
This historical result predates the per-job garbage-collection barrier and is
not a valid isolated runtime comparison. The default-runtime job ran after the
Kata pods remained resident on the single workload node. Replace these numbers
after reprovisioning the cluster and rerunning `mode=full` with the isolation
barrier enabled.
```

- [ ] **Step 3: Run documentation-adjacent contract tests**

Run: `go test ./internal/examples -run 'TestKataPerf' -v`

Expected: PASS.

---

### Task 3: Verify and Prepare for E2E Rerun

**Files:**
- No additional source files expected.

**Interfaces:**
- Consumes: Task 1 and Task 2 changes.
- Produces: verified local branch ready for code review and later cluster reprovision/rerun.

- [ ] **Step 1: Run focused package tests**

Run: `go test ./internal/run ./internal/examples ./cmd/perf-runner -v`

Expected: PASS.

- [ ] **Step 2: Run full test suite**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 3: Inspect the rendered workload manually if needed**

Run: `go test ./internal/examples -run TestKataPerfModesRenderPodLatencyDrainMitigation -v`

Expected: PASS and confirms rendered jobs contain `gc: true` through the contract test.

- [ ] **Step 4: Record the required manual E2E validation commands**

After local code review and commit, run these commands from the worktree or merged branch when ready to reprovision:

```bash
TEST_SUITE=kata-perf make destroy
TEST_SUITE=kata-perf make provision
TEST_SUITE=kata-perf TEST_MODE=full make run-suite
```

Expected log evidence: the `startup-smoke` job completes, kube-burner deletes namespaces labeled `kube-burner.io/job=startup-smoke`, and only then starts `startup-default-runtime`.

---

## Self-Review Notes

- Spec coverage: Task 1 implements per-job GC and preserves measurement settings; Task 2 documents isolation and contaminated result status; Task 3 covers automated and manual validation.
- Placeholder scan: no placeholder requirements remain.
- Type consistency: YAML keys and Go map keys match existing rendered workload structure.
