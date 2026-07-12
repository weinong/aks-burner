# FIO Inactive Direction And Fail-Fast Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Accept valid inactive FIO directions while terminating failed current-run Kubernetes Jobs promptly and preserving diagnostic artifact copying.

**Architecture:** First make FIO summary extraction conditional on each direction's numeric `total_ios`. Then introduce a context-aware kube-burner executor and a current-run Job failure watcher coordinated under a child context; the caller's parent context remains available for artifact copying after execution failure.

**Tech Stack:** Go 1.25, Bash, jq, Kubernetes Jobs, kubectl JSON, kube-burner 2.7.3.

## Global Constraints

- Inactive FIO directions retain a stable metric set and report p99 latency as zero.
- Active FIO directions require numeric p99 latency and all other summary fields.
- Malformed successful FIO output must fail without creating `summary.json`.
- Nonzero FIO execution preserves a diagnostic summary and original exit code.
- Watch only Jobs whose name starts with the rendered current run's unique `k8sRunID`.
- Fail only on a Job condition with `type=Failed` and `status=True`.
- Kube-burner must run with `exec.CommandContext` under a child context.
- Cancel the child context whenever kube-burner exits or a current-run Job fails; wait for watcher exit.
- Use the uncanceled parent context for diagnostic artifact copying.
- Skip successful reporting after workload or Job failure.

---

### Task 1: Parse Inactive FIO Directions

**Files:**
- Modify: `suites/kata-io/images/benchmark/scripts/run-fio.sh:38-79`
- Modify: `internal/examples/examples_test.go:800-920`

**Interfaces:**
- Consumes: FIO JSON `jobs[].read|write.{iops,bw_bytes,runtime,total_ios,clat_ns.percentile}`.
- Produces: the existing stable version-one `summary.json` metric set.

- [ ] **Step 1: Add failing script cases**

Extend the fake FIO test to emit these successful shapes:

```json
{"jobs":[{"read":{"iops":101.5,"bw_bytes":4096,"runtime":1250,"total_ios":100,"clat_ns":{"percentile":{"99.000000":700}}},"write":{"iops":0,"bw_bytes":0,"runtime":0,"total_ios":0,"clat_ns":{}}}]}
```

and the inverse write-only shape. Assert the inactive direction's p99 metric is `0`. Add an active-read shape with `total_ios: 1` but no read p99; assert nonzero script exit and no `summary.json`.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./internal/examples -run TestKataIOFioSummary -count=1
```

Expected: FAIL because the current parser requires both percentile fields.

- [ ] **Step 3: Implement direction-aware jq extraction**

Define a jq helper:

```jq
def direction($value):
  if (($value.iops | type) != "number") or
     (($value.bw_bytes | type) != "number") or
     (($value.runtime | type) != "number") or
     (($value.total_ios | type) != "number") then
    error("direction summary fields must be numeric")
  elif $value.total_ios < 0 then
    error("total_ios must not be negative")
  elif $value.total_ios > 0 then
    if ($value.clat_ns.percentile."99.000000" | type) != "number" then
      error("active direction p99 completion latency must be numeric")
    else
      [$value.iops, $value.bw_bytes, $value.runtime, $value.clat_ns.percentile."99.000000"]
    end
  else
    [$value.iops, $value.bw_bytes, $value.runtime, 0]
  end;
```

Map each job through `direction(.read)` and `direction(.write)`, then sum IOPS/bandwidth, take maximum runtime/p99, and convert maximum runtime from milliseconds to seconds. Add a multi-job fixture asserting all four aggregation rules and proving zero p99 applies only when `total_ios == 0`.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
go test ./internal/examples -run TestKataIOFioSummary -count=1
bash -n suites/kata-io/images/benchmark/scripts/run-fio.sh
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add suites/kata-io/images/benchmark/scripts/run-fio.sh internal/examples/examples_test.go
git commit -m "fix: handle inactive fio directions"
```

---

### Task 2: Cancel Kube-Burner On Current-Run Job Failure

**Files:**
- Create: `internal/run/job_watcher.go`
- Create: `internal/run/job_watcher_test.go`
- Modify: `internal/run/run.go:186-201`
- Modify: `internal/run/run_test.go:286-394`
- Modify: `cmd/perf-runner/main.go:704-768`
- Modify: `cmd/perf-runner/main_test.go:1880-2040`

**Interfaces:**
- Changes executor type to `type targetKubeBurnerExecutor func(ctx context.Context, workloadPath, logPath string, target kubetarget.Target) error`.
- Produces `run.WatchFailedJobs(ctx context.Context, target kubetarget.Target, namespace, jobNamePrefix string) error`.
- Adds injectable `type targetJobWatcher func(ctx context.Context, target kubetarget.Target, namespace, jobNamePrefix string) error`.
- Consumes the rendered workload's `k8sRunID` through a helper parallel to `artifactSubpathFromRenderedWorkload`.

- [ ] **Step 1: Write failing watcher tests**

Use an injected kubectl-output function in `job_watcher.go` to test JSON containing:

```json
{"items":[
  {"metadata":{"name":"old-run-job"},"status":{"conditions":[{"type":"Failed","status":"True","reason":"BackoffLimitExceeded"}]}},
  {"metadata":{"name":"current-run-job"},"status":{"conditions":[{"type":"Failed","status":"True","reason":"BackoffLimitExceeded","message":"Job has reached the specified backoff limit"}]}}
]}
```

Assert only `current-run-` triggers an error, and that temporary `Failed=False` is ignored. Use a short injected poll interval. Inject a transient kubectl/JSON error followed by a healthy response and assert the watcher retries rather than canceling the benchmark.

- [ ] **Step 2: Write failing lifecycle tests**

Add tests where:

- A fake context-aware executor blocks until its child context is canceled.
- An injected watcher returns a current-run failure; assert executor cancellation, Job-naming error, and report not called. The copier must assert `ctx.Err() == nil` and read a marker inherited from the parent context.
- The executor returns an unrelated error; assert watcher child context exits and artifact copy still runs.
- Successful execution stops the watcher and preserves execute/wait/copy/report order.
- Job failure plus copy failure preserves the Job error and appends copy diagnostics.
- Simultaneous-ready watcher and executor results retain source-aware precedence.

- [ ] **Step 3: Verify RED**

Run:

```bash
go test ./internal/run ./cmd/perf-runner -run 'WatchFailedJobs|FailFast|ExecuteRunCopyAndReport' -count=1
```

Expected: FAIL because the watcher and context-aware executor do not exist.

- [ ] **Step 4: Make kube-burner context-aware**

Change `ExecuteKubeBurner` to accept `context.Context` and use:

```go
cmd := exec.CommandContext(ctx, KubeBurnerExecutable(root), args...)
```

Update all callers and fake executors.

- [ ] **Step 5: Implement the scoped watcher**

Poll:

```text
kubectl [--context ...] get jobs -n <namespace> -o json
```

Parse only names with `strings.HasPrefix(name, jobNamePrefix)`. Represent a confirmed terminal Job condition with an exported typed `run.JobFailedError`. Return:

```text
benchmark job <name> failed: <reason>: <message>
```

only for `Failed=True`. Transient kubectl execution and JSON decoding errors are retained as the latest poll error and retried on the next interval; they do not cancel kube-burner. On context cancellation return `ctx.Err()`; the coordinator owns cancellation-cause interpretation. If the parent context reaches its deadline, wrap the latest poll error for diagnostics.

- [ ] **Step 6: Coordinate child lifecycle**

Add a separate `jobNamePrefix` parameter to `executeRunCopyAndReport`; production extracts it from rendered `k8sRunID`. Test extraction, a missing prefix, and that artifact `runID` is never substituted.

In `executeRunCopyAndReport`, for enabled artifacts and non-empty current-run prefix:

1. Create `executionCtx, cancel := context.WithCancelCause(ctx)`.
2. Start the injected watcher and kube-burner in separate goroutines, each writing once to its own buffered result channel.
3. `select` the first result. Only a typed `JobFailedError` from the watcher cancels execution as a workload failure. Watcher infrastructure errors are retried internally. Job failure first preserves that exact error and suppresses the resulting process-kill error. Executor first preserves its success/error, cancels the watcher, and suppresses expected watcher cancellation.
4. Drain both buffered channels before proceeding. If both were ready simultaneously, prefer a real Job failure over watcher cancellation; otherwise preserve the independent executor error.
5. Run artifact copy with parent `ctx`.
6. Preserve existing workload/wait/copy/report precedence.

- [ ] **Step 7: Verify GREEN and full suite**

Run:

```bash
go test ./internal/run ./cmd/perf-runner -run 'WatchFailedJobs|FailFast|ExecuteRunCopyAndReport' -count=1
go test -race ./internal/run ./cmd/perf-runner
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/run/job_watcher.go internal/run/job_watcher_test.go internal/run/run.go internal/run/run_test.go cmd/perf-runner/main.go cmd/perf-runner/main_test.go
git commit -m "fix: fail fast on benchmark job errors"
```

---

### Task 3: Review, Merge, And Resume Live Validation

**Files:**
- Verify all Task 1-2 files.

- [ ] **Step 1: Run full verification**

```bash
gofmt -w internal/run/*.go cmd/perf-runner/*.go internal/examples/*.go
```

- [ ] **Step 2: Request full diff review**

Review from the feature branch merge base through HEAD. Fix verified Critical/High findings and rerun Step 1.

- [ ] **Step 3: Merge locally and verify main**

Fast-forward local `main`, rerun `go test ./...`, and remove the owned worktree/feature branch.

- [ ] **Step 4: Resume approved live sequence**

Rerun Kata IO provision → `fio-fast` → verify → destroy. Only after successful deletion, run Kata Perf provision → `smoke` → verify → destroy. Preserve results and confirm no derived resource groups remain.
