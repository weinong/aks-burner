# Kata IO Raw Block Patched Pool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an isolated patched Kata node pool and raw-block Azure Disk fio/Git benchmark path to `kata-io` while preserving the current unpatched filesystem Azure Disk baseline.

**Architecture:** Provision a third Kata-capable user node pool that remains tainted until a setup DaemonSet patches and verifies `/usr/local/bin/containerd-shim-kata-v2`, then removes only its exact readiness taint through least-privilege node RBAC. Every suite run restarts the DaemonSet to revalidate all patched nodes before raw-block jobs can schedule; the main container then sleeps without host access or periodic replacement. New raw-block PVC/job templates map Azure Disk as a block device, format and mount it inside the Kata benchmark pod, run the existing fio/Git scripts, and report block setup duration separately.

**Tech Stack:** Go tests, kube-burner workload YAML, Kubernetes DaemonSet/PVC/Job manifests, AKS node pools, Azure Disk CSI, Kata Pod Sandboxing, Bash benchmark scripts, Bicep.

## Global Constraints

- Implement in an isolated local worktree under `.worktrees/kata-io-raw-block-patched-pool` on branch `feat/kata-io-raw-block-patched-pool`.
- Existing `userpool` stays the unpatched Kata filesystem baseline.
- New `patchpool` uses `count: 4`, `vmSize: Standard_D8s_v5`, `osSKU: AzureLinux`, and `workloadRuntime: KataMshvVmIsolation`.
- New patched pool label is `perf.azure.com/node-role: patchpool`; existing baseline pool label remains `perf.azure.com/node-role: workload`.
- New patched pool taint is `perf.azure.com/kata-shim-patch=pending:NoSchedule`; raw-block jobs do not tolerate it.
- Patch target is exactly `/usr/local/bin/containerd-shim-kata-v2`.
- Patch source is `https://abombo.blob.core.windows.net/public/containerd-shim-kata-v2.bin`.
- Patch source expected size is `66484264` bytes.
- Patch source expected SHA-256 is `d78b0c859c25f795dee201f8ae1b28c987fd6b0537efd0430b5fd6ad47a93ec1`.
- DaemonSet patches once, verifies, then sleeps; it must not restart containerd or systemd services.
- DaemonSet RBAC grants only `get` and `patch` on nodes, and init removes only the exact readiness taint from `spec.nodeName` after binary and metadata verification.
- Benchmark image base is `ubuntu:24.04@sha256:4fbb8e6a8395de5a7550b33509421a2bafbc0aab6c06ba2cef9ebffbc7092d90`; apt packages are not version-pinned without repository snapshots.
- Every fio and Git sample records `fio`, `git`, `mkfs.ext4`, and `mount` versions in `tool-versions.txt`.
- Raw-block storage dimension is `storage-azure-disk-block`; do not reuse `storage-azure-disk` for block-mode results.
- Raw-block runtime dimension is `runtime-kata-patched` to distinguish patched-pool results from unpatched Kata results.
- Raw-block PVCs use `volumeMode: Block`, `managed-csi`, and `ReadWriteOnce`.
- Raw-block benchmark containers use `CAP_SYS_ADMIN` first, not `privileged: true`.
- Formatting and mount time is excluded from existing fio/Git benchmark durations and reported as `block_setup_duration` in seconds.
- Add full Kata-only raw-block matrix to active `fio` and `git` modes: 10 fio cells and 4 Git cells.
- Remove obsolete `suites/kata-io/workload-full.yml`; replace the 84-scenario full-workload contract with active-mode contracts.
- Quota increase is an external prerequisite; remove or update the stale 40-vCPU West US 2 quota guard.
- Do not deploy, provision Azure resources, or run live cluster patching until the user explicitly signals to proceed.

---

## File Structure

- Modify `suites/kata-io/requirements.yml`: add `patchpool` and selector contract.
- Modify `suites/kata-io/suite.yml`: add `kata-shim-patch` setup resource and rollout wait.
- Create `suites/kata-io/setup/patch-kata-shim.yml`: DaemonSet that patches the host Kata shim on `patchpool` only.
- Create `suites/kata-io/templates/work-block-pvc.yml`: raw-block work PVC template.
- Create `suites/kata-io/templates/fio-block-kata-job.yml`: Kata raw-block fio job template.
- Create `suites/kata-io/templates/git-block-kata-job.yml`: Kata raw-block Git job template.
- Create `suites/kata-io/images/benchmark/scripts/run-block-benchmark.sh`: format/mount wrapper for raw-block jobs.
- Modify `suites/kata-io/images/benchmark/scripts/run-fio.sh`: accept optional `BLOCK_SETUP_DURATION_SECONDS` and include `block_setup_duration` metric.
- Modify `suites/kata-io/images/benchmark/scripts/run-git-clone.sh`: accept optional `BLOCK_SETUP_DURATION_SECONDS` and include `block_setup_duration` metric.
- Modify `suites/kata-io/images/benchmark/Dockerfile`: install block setup tools and copy wrapper script.
- Modify `suites/kata-io/templates/preload-pod.yml`: make node selector configurable.
- Modify `suites/kata-io/workload-fio-fast.yml`: preload benchmark image on both pools.
- Modify `suites/kata-io/workload-git-fast.yml`: preload benchmark image on both pools.
- Modify `suites/kata-io/workload-fio.yml`: preload both pools and add 10 raw-block fio cells.
- Modify `suites/kata-io/workload-git.yml`: preload both pools and add 4 raw-block Git cells.
- Delete `suites/kata-io/workload-full.yml`: obsolete unexposed combined matrix.
- Modify `internal/examples/examples_test.go`: update suite contracts and script tests.
- Modify `README.md`: document patched pool, raw-block path, and quota prerequisite.

---

### Task 1: Prepare Local Worktree

**Files:**
- No source files modified in this task.

**Interfaces:**
- Consumes: current repository at `$REPO_ROOT`.
- Produces: isolated worktree `$REPO_ROOT/.worktrees/kata-io-raw-block-patched-pool` on branch `feat/kata-io-raw-block-patched-pool`.

- [ ] **Step 1: Inspect repository safety state**

Run from `$REPO_ROOT`:

```bash
git status --short --branch
git symbolic-ref refs/remotes/origin/HEAD
git log --oneline -10
git worktree list
```

Expected: default branch is identifiable, no unknown dirty changes would be discarded, and no existing worktree uses the target branch/path.

- [ ] **Step 2: Fetch and create the worktree**

Run:

```bash
git fetch origin
git switch main
git reset --hard origin/main
git worktree add .worktrees/kata-io-raw-block-patched-pool -b feat/kata-io-raw-block-patched-pool main
```

Expected: worktree is created under `.worktrees/kata-io-raw-block-patched-pool`.

- [ ] **Step 3: Verify baseline in the worktree**

Run:

```bash
git status --short --branch
go test ./internal/examples
go test ./internal/infra ./internal/run ./cmd/perf-runner
```

Expected: status is clean and baseline tests pass before implementation.

---

### Task 2: Add Patch Pool And Shim-Patch Setup Contract

**Files:**
- Modify: `internal/examples/examples_test.go`
- Modify: `suites/kata-io/requirements.yml`
- Modify: `suites/kata-io/suite.yml`
- Create: `suites/kata-io/setup/patch-kata-shim.yml`

**Interfaces:**
- Consumes: existing `requirements.Load`, `suite.Load`, and manifest file reads in `internal/examples`.
- Produces: suite requirements with pools `systempool`, `userpool`, and `patchpool`; setup resource `kata-shim-patch`; DaemonSet manifest selected by `perf.azure.com/node-role=patchpool`.

- [ ] **Step 1: Write failing pool/selector tests**

In `internal/examples/examples_test.go`, replace the two-pool assumption in `TestSuiteRequirementsDriveNodePoolsWithoutParameterFiles` with a helper that finds pools by name:

```go
func nodePoolByName(t *testing.T, pools []infra.NodePool, name string) infra.NodePool {
	t.Helper()
	for _, pool := range pools {
		if pool.Name == name {
			return pool
		}
	}
	t.Fatalf("node pool %q not found in %#v", name, pools)
	return infra.NodePool{}
}
```

Add assertions for `kata-io`:

```go
doc, err := requirements.Load(filepath.Join("..", ".."), "kata-io")
if err != nil {
	t.Fatal(err)
}
if len(doc.Requires.Infrastructure.NodePools) != 3 {
	t.Fatalf("kata-io node pools = %#v, want systempool, userpool, patchpool", doc.Requires.Infrastructure.NodePools)
}
user := nodePoolByName(t, doc.Requires.Infrastructure.NodePools, "userpool")
if user.Mode != "User" || user.Count != 4 || user.VMSize != "Standard_D8s_v5" || user.OSSKU != "AzureLinux" || user.WorkloadRuntime != "KataMshvVmIsolation" {
	t.Fatalf("userpool = %#v", user)
}
if user.Labels["perf.azure.com/node-role"] != "workload" {
	t.Fatalf("userpool label = %#v, want workload", user.Labels)
}
patch := nodePoolByName(t, doc.Requires.Infrastructure.NodePools, "patchpool")
if patch.Mode != "User" || patch.Count != 4 || patch.VMSize != "Standard_D8s_v5" || patch.OSSKU != "AzureLinux" || patch.WorkloadRuntime != "KataMshvVmIsolation" {
	t.Fatalf("patchpool = %#v", patch)
}
if patch.Labels["perf.azure.com/node-role"] != "patchpool" {
	t.Fatalf("patchpool label = %#v, want patchpool", patch.Labels)
}
```

Add selector assertions:

```go
selectorsByName := map[string]string{}
for _, selector := range doc.Requires.NodeSelectors {
	selectorsByName[selector.Name] = selector.Pool
}
if selectorsByName["workload"] != "userpool" {
	t.Fatalf("workload selector pool = %q, want userpool", selectorsByName["workload"])
}
if selectorsByName["patched-kata"] != "patchpool" {
	t.Fatalf("patched-kata selector pool = %q, want patchpool", selectorsByName["patched-kata"])
}
```

- [ ] **Step 2: Write failing DaemonSet manifest test**

Add a test in `internal/examples/examples_test.go`:

```go
func TestKataIOShimPatchDaemonSetContract(t *testing.T) {
	root := filepath.Join("..", "..")
	suiteData, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "suite.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name: kata-shim-patch",
		"path: setup/patch-kata-shim.yml",
		"resource: daemonset/kata-shim-patch",
		"namespace: kube-system",
		"timeout: 15m",
	} {
		if !strings.Contains(string(suiteData), want) {
			t.Fatalf("suite.yml missing %q", want)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "setup", "patch-kata-shim.yml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(data)
	for _, want := range []string{
		"kind: DaemonSet",
		"name: kata-shim-patch",
		"namespace: kube-system",
		"perf.azure.com/node-role: patchpool",
		"https://abombo.blob.core.windows.net/public/containerd-shim-kata-v2.bin",
		"d78b0c859c25f795dee201f8ae1b28c987fd6b0537efd0430b5fd6ad47a93ec1",
		"66484264",
		"/host-bin",
		"containerd-shim-kata-v2",
		"sleep infinity",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("patch DaemonSet missing %q", want)
		}
	}
	for _, forbidden := range []string{"systemctl restart", "service containerd restart", "pkill containerd", "containerd-shim-v2"} {
		if strings.Contains(manifest, forbidden) {
			t.Fatalf("patch DaemonSet contains forbidden restart or wrong target %q", forbidden)
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./internal/examples -run 'TestSuiteRequirementsDriveNodePoolsWithoutParameterFiles|TestKataIOShimPatchDaemonSetContract'
```

Expected: FAIL because `patchpool` and `patch-kata-shim.yml` do not exist yet.

- [ ] **Step 4: Add `patchpool` and selector**

Modify `suites/kata-io/requirements.yml` so `nodePools` includes:

```yaml
      - name: patchpool
        mode: User
        count: 4
        vmSize: Standard_D8s_v5
        osType: Linux
        osSKU: AzureLinux
        workloadRuntime: KataMshvVmIsolation
        labels:
          perf.azure.com/node-role: patchpool
        taints:
          - perf.azure.com/kata-shim-patch=pending:NoSchedule
```

Modify `nodeSelectors` so it includes:

```yaml
    - name: patched-kata
      pool: patchpool
      required: true
      minNodes: 4
      labels:
        perf.azure.com/node-role: patchpool
```

- [ ] **Step 5: Add setup resource and DaemonSet**

Modify `suites/kata-io/suite.yml` setup resources so the patch resource appears after namespace creation and before the results PVC:

```yaml
    - name: kata-shim-patch
      path: setup/patch-kata-shim.yml
      restart:
        resource: daemonset/kata-shim-patch
        namespace: kube-system
      wait:
        - kind: rollout
          resource: daemonset/kata-shim-patch
          namespace: kube-system
          timeout: 15m
```

Create `suites/kata-io/setup/patch-kata-shim.yml` as a multi-document manifest with:

- A digest-pinned patch init container that exclusively mounts host `/usr/local/bin` and a 600-second projected service-account token.
- `automountServiceAccountToken: false`, so the long-lived unprivileged sleeper receives neither node-patch credentials nor host mounts.
- Exact source size/SHA verification, numeric UID/GID/mode capture, `chown` before `chmod`, atomic replacement, and post-install metadata/hash verification.
- A `perf.azure.com/kata-shim-patch=pending:NoSchedule` toleration and exact JSON Patch removal of only that taint from `spec.nodeName` after successful verification.
- A ServiceAccount plus node-only `get`/`patch` ClusterRole and binding.
- No containerd restart and no repeated binary replacement; suite setup forces a fresh DaemonSet rollout so the init verification runs before each benchmark invocation.

- [ ] **Step 6: Remove stale quota guard**

Modify `TestKataIOInfraDefaultsFitWestUS2Quota` in `internal/examples/examples_test.go` to stop enforcing `observedRemainingQuota = 40`. Replace it with a contract that calculates and logs the requested DSv5-family vCPU count for documentation, or delete the test if no stable local quota value exists.

- [ ] **Step 7: Run focused tests**

Run:

```bash
go test ./internal/examples -run 'TestSuiteRequirementsDriveNodePoolsWithoutParameterFiles|TestKataIOShimPatchDaemonSetContract|TestKataIOInfraDefaults'
kubectl apply --dry-run=client --validate=false -f suites/kata-io/setup/patch-kata-shim.yml
```

Expected: tests pass and the manifest parses client-side.

- [ ] **Step 8: Review checkpoint**

Run:

```bash
git diff HEAD -- suites/kata-io/requirements.yml suites/kata-io/suite.yml suites/kata-io/setup/patch-kata-shim.yml internal/examples/examples_test.go
```

Dispatch a code-reviewer subagent on this diff and independently verify any Critical or High findings.

---

### Task 3: Add Raw-Block Benchmark Scripts And Templates

**Files:**
- Modify: `internal/examples/examples_test.go`
- Create: `suites/kata-io/templates/work-block-pvc.yml`
- Create: `suites/kata-io/templates/fio-block-kata-job.yml`
- Create: `suites/kata-io/templates/git-block-kata-job.yml`
- Create: `suites/kata-io/images/benchmark/scripts/run-block-benchmark.sh`
- Modify: `suites/kata-io/images/benchmark/scripts/run-fio.sh`
- Modify: `suites/kata-io/images/benchmark/scripts/run-git-clone.sh`
- Modify: `suites/kata-io/images/benchmark/Dockerfile`

**Interfaces:**
- Consumes: existing summary format from `run-fio.sh` and `run-git-clone.sh`.
- Produces: `run-block-benchmark.sh` wrapper and summary metric `block_setup_duration` for all standard summaries.

- [ ] **Step 1: Write failing raw-block template contract test**

Add a test in `internal/examples/examples_test.go`:

```go
func TestKataIORawBlockTemplates(t *testing.T) {
	root := filepath.Join("..", "..")
	pvc, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "templates", "work-block-pvc.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"volumeMode: Block", "storageClassName: {{.workStorageClass}}", "ReadWriteOnce", "pvc-role: work", "storage-type: {{.storageType}}"} {
		if !strings.Contains(string(pvc), want) {
			t.Fatalf("work-block-pvc.yml missing %q", want)
		}
	}

	for _, file := range []string{"fio-block-kata-job.yml", "git-block-kata-job.yml"} {
		data, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "templates", file))
		if err != nil {
			t.Fatal(err)
		}
		manifest := string(data)
		for _, want := range []string{
			"runtimeClassName: {{.kataRuntimeClassName}}",
			"perf.azure.com/node-role: patchpool",
			"volumeDevices:",
			"devicePath: /dev/work-block",
			"claimName: {{.jobName}}-work-{{.Iteration}}",
			"SYS_ADMIN",
			"run-block-benchmark.sh",
			"storageType",
			"kata-io-results",
		} {
			if !strings.Contains(manifest, want) {
				t.Fatalf("%s missing %q", file, want)
			}
		}
		if strings.Contains(manifest, "mountPath: /work") {
			t.Fatalf("%s must not mount the raw-block work volume as /work", file)
		}
	}
}
```

- [ ] **Step 2: Write failing script summary tests**

Update existing `assertKataIOSummary` metric maps in `TestKataIOFioSummary` and `TestKataIOGitSummary` so they require `block_setup_duration: seconds` and expect value `0` when `BLOCK_SETUP_DURATION_SECONDS` is unset.

Add a wrapper test:

```go
func TestKataIOBlockBenchmarkWrapper(t *testing.T) {
	requireKataIOScriptTools(t)
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "mkfs.ext4"), `#!/usr/bin/env bash
set -euo pipefail
printf 'mkfs %s\n' "$*" >> "${FAKE_BLOCK_LOG:?}"
`)
	writeExecutable(t, filepath.Join(binDir, "mount"), `#!/usr/bin/env bash
set -euo pipefail
printf 'mount %s\n' "$*" >> "${FAKE_BLOCK_LOG:?}"
mkdir -p "${@: -1}"
`)
	writeExecutable(t, filepath.Join(binDir, "umount"), `#!/usr/bin/env bash
set -euo pipefail
printf 'umount %s\n' "$*" >> "${FAKE_BLOCK_LOG:?}"
`)
	writeExecutable(t, filepath.Join(binDir, "benchmark"), `#!/usr/bin/env bash
set -euo pipefail
test -d "${WORK_DIR:?}"
test "${BLOCK_SETUP_DURATION_SECONDS:-}" != ""
printf 'benchmark setup=%s\n' "$BLOCK_SETUP_DURATION_SECONDS" >> "${FAKE_BLOCK_LOG:?}"
exit "${FAKE_BENCHMARK_EXIT:?}"
`)
	logPath := filepath.Join(tempDir, "block.log")
	cmd := exec.Command("bash", filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark", "scripts", "run-block-benchmark.sh"), "benchmark")
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_BLOCK_DEVICE="+filepath.Join(tempDir, "device"),
		"FAKE_BLOCK_LOG="+logPath,
		"FAKE_BENCHMARK_EXIT=17",
		"WORK_DIR="+filepath.Join(tempDir, "work"),
	)
	assertCommandExitCode(t, cmd, 17)
	assertFileContains(t, logPath, "mkfs -F")
	assertFileContains(t, logPath, "mount")
	assertFileContains(t, logPath, "benchmark setup=")
	assertFileContains(t, logPath, "umount")
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./internal/examples -run 'TestKataIO(RawBlockTemplates|FioSummary|GitSummary|BlockBenchmarkWrapper|BenchmarkImageFilesExist)'
```

Expected: FAIL because templates, wrapper, Dockerfile copy, and new summary metric do not exist yet.

- [ ] **Step 4: Add raw-block PVC template**

Create `suites/kata-io/templates/work-block-pvc.yml`:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{.jobName}}-work-{{.Iteration}}
  namespace: {{.namespace}}
  labels:
    app: kata-io
    benchmark: io
    pvc-role: work
    storage-type: {{.storageType}}
spec:
  volumeMode: Block
  accessModes:
    - ReadWriteOnce
  storageClassName: {{.workStorageClass}}
  resources:
    requests:
      storage: {{.workVolumeSize}}
```

- [ ] **Step 5: Add raw-block fio template**

Create `suites/kata-io/templates/fio-block-kata-job.yml` based on `fio-pvc-kata-job.yml`, with these required differences:

```yaml
spec:
  template:
    spec:
      runtimeClassName: {{.kataRuntimeClassName}}
      nodeSelector:
        perf.azure.com/node-role: patchpool
      containers:
        - name: fio
          image: {{.benchmarkImage}}
          command: [/usr/local/bin/run-block-benchmark.sh]
          args: [/usr/local/bin/run-fio.sh]
          securityContext:
            capabilities:
              add:
                - SYS_ADMIN
          env:
            - name: BLOCK_DEVICE
              value: /dev/work-block
            - name: WORK_DIR
              value: /work
            - name: RESULTS_DIR
              value: /results
          volumeDevices:
            - name: work
              devicePath: /dev/work-block
          volumeMounts:
            - name: results
              mountPath: /results
      volumes:
        - name: work
          persistentVolumeClaim:
            claimName: {{.jobName}}-work-{{.Iteration}}
        - name: results
          persistentVolumeClaim:
            claimName: kata-io-results
```

Keep all existing fio labels, annotations, resources, and environment variables from `fio-pvc-kata-job.yml`.

- [ ] **Step 6: Add raw-block Git template**

Create `suites/kata-io/templates/git-block-kata-job.yml` based on `git-pvc-kata-job.yml`, with the same block-specific differences as Step 5, but command args:

```yaml
command: [/usr/local/bin/run-block-benchmark.sh]
args: [/usr/local/bin/run-git-clone.sh]
```

Keep all existing Git labels, annotations, resources, and environment variables from `git-pvc-kata-job.yml`.

- [ ] **Step 7: Add block wrapper script**

Create `suites/kata-io/images/benchmark/scripts/run-block-benchmark.sh`:

The wrapper must:

- Validate the raw block device, create the work directory, and honor pending signals between setup phases.
- Run `mkfs.ext4 -F -E lazy_itable_init=0,lazy_journal_init=0`, mount the device, mark it mounted before honoring a signal delivered during `mount`, and `sync` before ending setup timing.
- Launch the benchmark in an isolated `setsid` process group so INT/TERM reaches the script, `/usr/bin/time`, fio/Git, and descendants.
- Preserve signal > benchmark > cleanup exit precedence.
- Escalate TERM/INT to process-group KILL after `SIGNAL_GRACE_SECONDS`, then perform a bounded `POST_KILL_GRACE_SECONDS` drain that ignores zombie processes.
- Refuse to unmount while live benchmark processes remain; otherwise unmount exactly once on success, failure, or signal.
- Export `BLOCK_SETUP_DURATION_SECONDS` only after formatting, mounting, and flushing complete.

- [ ] **Step 8: Add `block_setup_duration` to fio summary**

In `run-fio.sh`, define:

```bash
block_setup_duration="${BLOCK_SETUP_DURATION_SECONDS:-0}"
```

Pass it into `jq`:

```bash
  --argjson blockSetupDuration "$block_setup_duration" \
```

Add metric object after `setup_overhead`:

```jq
    {name:"block_setup_duration",value:$blockSetupDuration,unit:"seconds"},
```

- [ ] **Step 9: Add `block_setup_duration` to Git summary**

In `run-git-clone.sh`, define:

```bash
block_setup_duration="${BLOCK_SETUP_DURATION_SECONDS:-0}"
```

Pass it into `jq`:

```bash
  --argjson blockSetupDuration "$block_setup_duration" \
```

Add metric object before `clone_duration`:

```jq
    {name:"block_setup_duration",value:$blockSetupDuration,unit:"seconds"},
```

- [ ] **Step 10: Update Dockerfile**

Modify `suites/kata-io/images/benchmark/Dockerfile` package install list to include:

```dockerfile
       e2fsprogs \
       mount \
```

Add copy and chmod lines:

```dockerfile
COPY scripts/run-block-benchmark.sh /usr/local/bin/run-block-benchmark.sh
RUN chmod +x /usr/local/bin/override /usr/local/bin/run-fio.sh /usr/local/bin/run-git-clone.sh /usr/local/bin/run-block-benchmark.sh
```

- [ ] **Step 11: Run focused tests and syntax checks**

Run:

```bash
bash -n suites/kata-io/images/benchmark/scripts/run-fio.sh
bash -n suites/kata-io/images/benchmark/scripts/run-git-clone.sh
bash -n suites/kata-io/images/benchmark/scripts/run-block-benchmark.sh
go test ./internal/examples -run 'TestKataIO(RawBlockTemplates|FioSummary|GitSummary|BlockBenchmarkWrapper|BenchmarkImageFilesExist)'
```

Expected: all pass.

- [ ] **Step 12: Verify image tools if Docker is available**

Run:

```bash
docker build -t aks-burner-kata-io-benchmark:test suites/kata-io/images/benchmark
docker run --rm aks-burner-kata-io-benchmark:test bash -lc 'command -v mkfs.ext4 && command -v mount && command -v fio && command -v git && command -v jq'
```

Expected: all tools are found. If Docker is unavailable, record that this verification was skipped and rely on `go test` plus ACR build during live run.

- [ ] **Step 13: Review checkpoint**

Run:

```bash
git diff HEAD -- suites/kata-io/templates suites/kata-io/images/benchmark internal/examples/examples_test.go
```

Dispatch code-reviewer and independently verify all Critical/High findings.

---

### Task 4: Extend Active Workload Matrices And Remove Obsolete Full Workload

**Files:**
- Modify: `internal/examples/examples_test.go`
- Modify: `suites/kata-io/templates/preload-pod.yml`
- Modify: `suites/kata-io/workload-fio-fast.yml`
- Modify: `suites/kata-io/workload-git-fast.yml`
- Modify: `suites/kata-io/workload-fio.yml`
- Modify: `suites/kata-io/workload-git.yml`
- Delete: `suites/kata-io/workload-full.yml`

**Interfaces:**
- Consumes: templates from Task 3 and pool selectors from Task 2.
- Produces: active mode matrix contracts for fio and Git; raw-block scenarios scheduled only to `patchpool`.

- [ ] **Step 1: Write failing active-mode matrix tests**

Replace `TestKataIOFullWorkloadCoversRequiredScenarios` with two tests:

```go
func TestKataIOFioWorkloadCoversActiveScenarios(t *testing.T) {
	assertKataIOActiveWorkloadScenarios(t, "workload-fio.yml", 70, map[string]int{
		"storage-emptydir": 20,
		"storage-azure-disk": 20,
		"storage-azure-files": 20,
		"storage-azure-disk-block": 10,
	})
}

func TestKataIOGitWorkloadCoversActiveScenarios(t *testing.T) {
	assertKataIOActiveWorkloadScenarios(t, "workload-git.yml", 28, map[string]int{
		"storage-emptydir": 8,
		"storage-azure-disk": 8,
		"storage-azure-files": 8,
		"storage-azure-disk-block": 4,
	})
}
```

Implement helper logic that parses workload jobs, finds each object with `inputVars.scenario`, counts unique scenarios, and asserts:

```go
if storage == "storage-azure-disk-block" {
	if runtime != "runtime-kata-patched" {
		t.Fatalf("block scenario %s runtime = %q, want runtime-kata-patched", scenario, runtime)
	}
	if mainObjectTemplate != "templates/fio-block-kata-job.yml" && mainObjectTemplate != "templates/git-block-kata-job.yml" {
		t.Fatalf("block scenario %s template = %q", scenario, mainObjectTemplate)
	}
	if workPVCInputVars == nil || workPVCObjectTemplate != "templates/work-block-pvc.yml" {
		t.Fatalf("block scenario %s missing work-block-pvc.yml", scenario)
	}
}
```

Add obsolete-file assertion:

```go
func TestKataIOObsoleteFullWorkloadRemoved(t *testing.T) {
	path := filepath.Join("..", "..", "suites", "kata-io", "workload-full.yml")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("workload-full.yml should be removed, stat err = %v", err)
	}
}
```

- [ ] **Step 2: Write failing preload contract test**

Add or update preload tests so all four active workloads contain preload objects for both pools:

```go
func TestKataIOWorkloadsPreloadBenchmarkImageOnBothPools(t *testing.T) {
	for _, workloadFile := range []string{"workload-fio-fast.yml", "workload-git-fast.yml", "workload-fio.yml", "workload-git.yml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", workloadFile))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, want := range []string{"nodeRole: workload", "nodeRole: patchpool"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing preload %q", workloadFile, want)
			}
		}
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./internal/examples -run 'TestKataIO(FioWorkloadCoversActiveScenarios|GitWorkloadCoversActiveScenarios|ObsoleteFullWorkloadRemoved|WorkloadsPreloadBenchmarkImageOnBothPools)'
```

Expected: FAIL because active workloads lack block scenarios, preload is single-pool, and `workload-full.yml` still exists.

- [ ] **Step 4: Make preload node selector configurable**

Modify `suites/kata-io/templates/preload-pod.yml`:

```yaml
      nodeSelector:
        perf.azure.com/node-role: {{.nodeRole}}
```

Update each `kio-preload-images` job in active workloads to include two objects:

```yaml
  objects:
  - objectTemplate: templates/preload-pod.yml
    replicas: 1
    inputVars:
      jobName: '{{.k8sRunID}}-preload-workload'
      nodeRole: workload
  - objectTemplate: templates/preload-pod.yml
    replicas: 1
    inputVars:
      jobName: '{{.k8sRunID}}-preload-patchpool'
      nodeRole: patchpool
```

- [ ] **Step 5: Add 10 fio raw-block cells**

Append to `suites/kata-io/workload-fio.yml` one job for each profile/concurrency pair:

```yaml
- name: kio-block-1
  jobType: create
  namespace: kata-io
  namespacedIterations: false
  jobIterations: 1
  qps: 1
  burst: 1
  cleanup: true
  waitWhenFinished: true
  preLoadImages: false
  objects:
  - objectTemplate: templates/work-block-pvc.yml
    replicas: 1
    inputVars:
      jobName: '{{.k8sRunID}}-kio-block-1'
      storageType: storage-azure-disk-block
      workStorageClass: managed-csi
  - objectTemplate: templates/fio-block-kata-job.yml
    replicas: 1
    inputVars:
      jobName: '{{.k8sRunID}}-kio-block-1'
      scenario: runtime-kata-patched-storage-azure-disk-block-fio-randread-4k-concurrency-1
      runtime: runtime-kata-patched
      storageType: storage-azure-disk-block
      workloadType: fio
      fioProfile: /profiles/randread-4k.fio
      fioProfileName: randread-4k
      concurrency: '1'
```

Repeat the structure for:

```text
randwrite-4k concurrency 1
seqread concurrency 1
seqwrite concurrency 1
fsync-heavy concurrency 1
randread-4k concurrency 10 with jobIterations/qps/burst 10
randwrite-4k concurrency 10 with jobIterations/qps/burst 10
seqread concurrency 10 with jobIterations/qps/burst 10
seqwrite concurrency 10 with jobIterations/qps/burst 10
fsync-heavy concurrency 10 with jobIterations/qps/burst 10
```

Use unique job names and `jobName` suffixes for every cell.

- [ ] **Step 6: Add 4 Git raw-block cells**

Append to `suites/kata-io/workload-git.yml` one job for each clone-mode/concurrency pair:

```yaml
- name: kio-block-git-1
  jobType: create
  namespace: kata-io
  namespacedIterations: false
  jobIterations: 1
  qps: 1
  burst: 1
  cleanup: true
  waitWhenFinished: true
  preLoadImages: false
  objects:
  - objectTemplate: templates/work-block-pvc.yml
    replicas: 1
    inputVars:
      jobName: '{{.k8sRunID}}-kio-block-git-1'
      storageType: storage-azure-disk-block
      workStorageClass: managed-csi
  - objectTemplate: templates/git-block-kata-job.yml
    replicas: 1
    inputVars:
      jobName: '{{.k8sRunID}}-kio-block-git-1'
      scenario: runtime-kata-patched-storage-azure-disk-block-git-full-concurrency-1
      runtime: runtime-kata-patched
      storageType: storage-azure-disk-block
      workloadType: git
      cloneMode: full
      concurrency: '1'
```

Repeat for:

```text
blobless concurrency 1
full concurrency 10 with jobIterations/qps/burst 10
blobless concurrency 10 with jobIterations/qps/burst 10
```

- [ ] **Step 7: Delete obsolete full workload**

Run:

```bash
rm suites/kata-io/workload-full.yml
```

- [ ] **Step 8: Run focused workload tests**

Run:

```bash
go test ./internal/examples -run 'TestKataIO(FioWorkloadCoversActiveScenarios|GitWorkloadCoversActiveScenarios|ObsoleteFullWorkloadRemoved|WorkloadsPreloadBenchmarkImageOnBothPools|WorkloadsCleanPreviousPodsAndWorkPVCs|ModesUsePerRunIDPlaceholder)'
```

Expected: all pass.

- [ ] **Step 9: Review checkpoint**

Run:

```bash
git diff HEAD -- suites/kata-io/workload-fio.yml suites/kata-io/workload-git.yml suites/kata-io/workload-fio-fast.yml suites/kata-io/workload-git-fast.yml suites/kata-io/templates/preload-pod.yml internal/examples/examples_test.go
```

Dispatch code-reviewer and independently verify all Critical/High findings.

---

### Task 5: Documentation And Repository-Wide Verification

**Files:**
- Modify: `README.md`
- Optionally modify: `suites/kata-io/findings.md`

**Interfaces:**
- Consumes: final suite behavior from Tasks 2-4.
- Produces: user-facing docs and verified complete branch.

- [ ] **Step 1: Write documentation update**

Modify `README.md` near the `kata-io` description to state:

```markdown
`kata-io` now provisions two Kata workload pools: an unpatched baseline pool for existing filesystem-backed storage scenarios and a patched pool for raw-block Azure Disk scenarios. The patched pool runs a setup DaemonSet that replaces `/usr/local/bin/containerd-shim-kata-v2` with the verified experimental binary before benchmark jobs start. Raw-block scenarios are reported as `storage-azure-disk-block` and `runtime-kata-patched`.

The default `kata-io` infrastructure uses one `Standard_D4s_v5` system node, four `Standard_D8s_v5` baseline Kata nodes, and four `Standard_D8s_v5` patched Kata nodes. Ensure the target region has enough DSv5-family quota before provisioning.
```

- [ ] **Step 2: Run all focused tests**

Run:

```bash
go test ./internal/examples
go test ./internal/infra ./internal/run ./cmd/perf-runner
```

Expected: all pass.

- [ ] **Step 3: Run full repository tests and build**

Run:

```bash
go test ./...
go build ./...
```

Expected: all pass.

- [ ] **Step 4: Validate Bicep template**

Run:

```bash
az bicep build --file infra/aks/main.bicep --stdout >/dev/null
```

Expected: command exits 0.

- [ ] **Step 5: Validate changed YAML client-side**

Run:

```bash
kubectl apply --dry-run=client --validate=false -f suites/kata-io/setup/patch-kata-shim.yml
kubectl apply --dry-run=client --validate=false -f suites/kata-io/setup/namespace.yml
kubectl apply --dry-run=client --validate=false -f suites/kata-io/setup/results-pvc.yml
```

Expected: all manifests parse client-side.

- [ ] **Step 6: Inspect final diff**

Run:

```bash
git status --short
git diff HEAD
git log --oneline -10
```

Expected: only intended files are changed.

- [ ] **Step 7: Final code review before commit**

Dispatch `code-reviewer` with `git diff HEAD`. Independently verify every Critical and High finding before applying or dismissing it.

- [ ] **Step 8: Commit implementation**

Run:

```bash
git add README.md docs/superpowers/plans/2026-07-14-kata-io-raw-block-patched-pool.md internal schemas suites
git diff --cached
git commit -m "feat: add kata raw block benchmark path"
```

Expected: one implementation commit on `feat/kata-io-raw-block-patched-pool`.

---

### Task 6: Merge Back To Local Main After User Approval

**Files:**
- No new source files modified beyond merge result.

**Interfaces:**
- Consumes: reviewed and verified feature branch commit.
- Produces: local `main` containing the feature branch merge.

- [ ] **Step 1: Invoke finishing workflow**

Use `finishing-a-development-branch` before merging or cleanup.

- [ ] **Step 2: Merge**

Run from `$REPO_ROOT`:

```bash
git status --short --branch
git switch main
git fetch origin
git status --short --branch
git merge --no-ff feat/kata-io-raw-block-patched-pool
```

Expected: merge succeeds without discarding unrelated user changes.

- [ ] **Step 3: Verify merged main**

Run:

```bash
go test ./...
go build ./...
az bicep build --file infra/aks/main.bicep --stdout >/dev/null
git status --short --branch
```

Expected: all verification passes and local `main` is clean.

- [ ] **Step 4: Stop before live cluster actions**

Do not run `make provision`, `make run-suite`, or any command that patches nodes until the user explicitly requests live validation.

---

## Live Cluster Validation Plan

Run only after explicit user approval and after DSv5-family quota is sufficient.

- [ ] **Step 1: Provision updated cluster**

```bash
TEST_SUITE=kata-io make provision
```

- [ ] **Step 2: Confirm pools and patch rollout**

```bash
kubectl get nodes -L perf.azure.com/node-role,kubernetes.azure.com/agentpool,kubernetes.azure.com/kata-vm-isolation
kubectl -n kube-system rollout status daemonset/kata-shim-patch --timeout=15m
kubectl -n kube-system get daemonset kata-shim-patch -o wide
```

- [ ] **Step 3: Run full fio and Git modes**

```bash
TEST_SUITE=kata-io TEST_MODE=fio make run-suite
TEST_SUITE=kata-io TEST_MODE=git make run-suite
```

- [ ] **Step 4: Inspect results**

```bash
rg 'azure-disk-block|block_setup_duration|runtime-kata-patched' results/*_kata-io_fio/summary/results.csv results/*_kata-io_git/summary/results.csv
```

Expected: raw-block results are present, filesystem baseline results remain present, and `block_setup_duration` appears only as a separate metric.

---

## Self-Review

- Spec coverage: the plan covers the dedicated patch pool, one-time DaemonSet, raw-block PVCs, in-pod formatting/mounting, full active matrix expansion, obsolete full-workload removal, quota assumption update, local worktree delivery, tests, review, commit, merge, and live validation gate.
- Placeholder scan: no deferred or incomplete work markers remain.
- Type/name consistency: pool names are `userpool` and `patchpool`; selectors are `workload` and `patched-kata`; storage/runtime dimensions are `storage-azure-disk-block` and `runtime-kata-patched`; wrapper metric is `block_setup_duration`; shim target is `containerd-shim-kata-v2`.
