# Optional Kubernetes Context Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add optional `--kube-context` support to `run-suite` so existing contexts can be targeted explicitly while preserving the current credential-refresh workflow by default.

**Architecture:** Introduce a context-only `kubetarget.Target` that constructs complete kubectl commands and kube-burner argument lists. Thread one target through all Kubernetes-facing packages and the `run-suite` orchestration, using an empty target for the existing implicit-current-context path.

**Tech Stack:** Go 1.25, standard library `flag`/`os/exec`, kubectl, kube-burner, GNU Make, YAML metadata.

## Global Constraints

- Add optional `--kube-context`; do not add `--kubeconfig` or `KUBECONFIG_FILE`.
- Explicit-context runs skip `az aks get-credentials`; legacy runs preserve the existing refresh behavior.
- Every Kubernetes subprocess in an explicit run must receive the selected context.
- Explicit runs require a resource group only when the suite declares image builds.
- Legacy runs always require a resource group.
- Record explicit context as `kubeContext`; omit `kubeContext` and preserve current `clusterName` metadata in legacy mode.
- Omit `clusterName` in explicit mode because suite Bicep parameters do not identify an arbitrary existing context.
- Do not change `provision`, `destroy`, Azure ownership, image override, experiment, or latency-reporting behavior.
- Preserve existing benchmark-versus-artifact-copy error precedence.
- Before every task commit, inspect `git diff HEAD`, invoke the `code-reviewer`
  subagent, independently resolve all verified Critical or High findings, and
  rerun that task's verification command.

---

### Task 1: Context-Only Kubernetes Target

**Files:**
- Create: `internal/kubetarget/target.go`
- Create: `internal/kubetarget/target_test.go`

**Interfaces:**
- Produces: `kubetarget.Target{Context string}`.
- Produces: `func (Target) KubectlCommand(args ...string) []string`, returning the executable plus arguments.
- Produces: `func (Target) KubeBurnerArgs(args ...string) []string`, returning arguments only.
- Produces: `func (Target) Output(context.Context, ...string) ([]byte, error)` for kubectl output used by requirement validation.

- [ ] **Step 1: Write command-construction tests**

Create `internal/kubetarget/target_test.go`:

```go
package kubetarget

import (
	"reflect"
	"testing"
)

func TestKubectlCommandAddsExplicitContext(t *testing.T) {
	got := (Target{Context: "preview"}).KubectlCommand("get", "nodes")
	want := []string{"kubectl", "--context", "preview", "get", "nodes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KubectlCommand() = %#v, want %#v", got, want)
	}
}

func TestKubectlCommandPreservesLegacyArguments(t *testing.T) {
	got := (Target{}).KubectlCommand("get", "nodes")
	want := []string{"kubectl", "get", "nodes"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KubectlCommand() = %#v, want %#v", got, want)
	}
}

func TestKubeBurnerArgsAddsExplicitContext(t *testing.T) {
	got := (Target{Context: "preview"}).KubeBurnerArgs("init", "-c", "workload.yml")
	want := []string{"init", "-c", "workload.yml", "--kube-context", "preview"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KubeBurnerArgs() = %#v, want %#v", got, want)
	}
}

func TestKubeBurnerArgsPreservesLegacyArguments(t *testing.T) {
	got := (Target{}).KubeBurnerArgs("init", "-c", "workload.yml")
	want := []string{"init", "-c", "workload.yml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KubeBurnerArgs() = %#v, want %#v", got, want)
	}
}
```

- [ ] **Step 2: Run the target tests and verify they fail**

Run: `go test ./internal/kubetarget -v`

Expected: FAIL because `internal/kubetarget` and `Target` do not exist.

- [ ] **Step 3: Implement the target**

Create `internal/kubetarget/target.go`:

```go
package kubetarget

import (
	"context"
	"os/exec"
)

type Target struct {
	Context string
}

func (t Target) KubectlCommand(args ...string) []string {
	command := []string{"kubectl"}
	if t.Context != "" {
		command = append(command, "--context", t.Context)
	}
	return append(command, args...)
}

func (t Target) KubeBurnerArgs(args ...string) []string {
	result := append([]string(nil), args...)
	if t.Context != "" {
		result = append(result, "--kube-context", t.Context)
	}
	return result
}

func (t Target) Output(ctx context.Context, args ...string) ([]byte, error) {
	command := t.KubectlCommand(args...)
	return exec.CommandContext(ctx, command[0], command[1:]...).Output()
}

```

- [ ] **Step 4: Run tests and commit**

Run: `gofmt -w internal/kubetarget/target.go internal/kubetarget/target_test.go && go test ./internal/kubetarget -v`

Expected: PASS.

```bash
git add internal/kubetarget/target.go internal/kubetarget/target_test.go
git commit -m "feat: add optional kubernetes target"
```

---

### Task 2: Target-Aware Setup And Observability

**Files:**
- Modify: `internal/run/setup.go:3-122`
- Modify: `internal/run/setup_test.go:1-268`
- Modify: `internal/prometheus/prometheus.go:3-89`
- Modify: `internal/prometheus/prometheus_test.go:1-105`
- Modify: `internal/kubestatemetrics/kube_state_metrics.go:3-47`
- Modify: `internal/kubestatemetrics/kube_state_metrics_test.go:1-30`

**Interfaces:**
- Consumes: `kubetarget.Target.KubectlCommand`.
- Produces: `run.ApplySetup(ctx, target, suiteDir, setup)`.
- Produces target parameters on Prometheus and kube-state-metrics install, rollout, and port-forward functions.

- [ ] **Step 1: Add failing setup propagation tests**

In `internal/run/setup_test.go`, import `internal/kubetarget`, define:

```go
var setupTestTarget = kubetarget.Target{Context: "preview"}
```

Change existing `ApplySetup` tests to call a private injectable helper:

```go
err := applySetup(context.Background(), setupTestTarget, suiteDir, setup, runner)
```

Update expected commands to include the complete command prefix:

```go
want := [][]string{
	{"kubectl", "--context", "preview", "apply", "-f", manifestPath},
	{"kubectl", "--context", "preview", "get", "runtimeclass/custom-kata"},
}
```

Add a legacy assertion using `kubetarget.Target{}` that expects `[]string{"kubectl", "apply", "-f", manifestPath}`.

- [ ] **Step 2: Add failing observability command tests**

Change `PortForwardArgs` and `RolloutStatusArgs` tests to pass `kubetarget.Target{Context: "preview"}` and expect:

```go
[]string{"kubectl", "--context", "preview", "-n", "perf-monitoring", "port-forward", "service/prometheus", "19090:9090"}
[]string{"kubectl", "--context", "preview", "rollout", "status", "deployment/prometheus", "-n", "perf-monitoring", "--timeout=2m"}
[]string{"kubectl", "--context", "preview", "rollout", "status", "deployment/kube-state-metrics", "-n", "perf-monitoring", "--timeout=2m"}
```

Add one empty-target test per package to prove existing command arrays remain unchanged.

Add package-private command runners so install paths are testable without a
real cluster:

```go
func TestInstallTargetsKubectlApply(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "manifest.yml")
	if err := os.WriteFile(manifestPath, []byte("image: {{PROMETHEUS_IMAGE}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var command []string
	var stdin string
	runner := func(_ context.Context, input string, args ...string) error {
		stdin = input
		command = append([]string(nil), args...)
		return nil
	}
	if err := installWithRunner(context.Background(), kubetarget.Target{Context: "preview"}, manifestPath, "prometheus:test", "", runner); err != nil {
		t.Fatal(err)
	}
	want := []string{"kubectl", "--context", "preview", "apply", "-f", "-"}
	if !reflect.DeepEqual(command, want) || !strings.Contains(stdin, "image: prometheus:test") {
		t.Fatalf("command = %#v, stdin = %q", command, stdin)
	}
}
```

Add the equivalent kube-state-metrics test using
`installWithRunner(context.Background(), kubetarget.Target{Context: "preview"}, manifestPath,
"kube-state-metrics:test", runner)` and assert the rendered image and the same
context-aware apply command.

- [ ] **Step 3: Run focused tests and verify they fail**

Run: `go test ./internal/run ./internal/prometheus ./internal/kubestatemetrics -v`

Expected: FAIL because the functions do not accept `kubetarget.Target` and setup runners still receive argument-only commands.

- [ ] **Step 4: Implement target-aware setup**

In `internal/run/setup.go`, add `os/exec` and `internal/kubetarget` imports, then replace the public setup entry point with:

```go
func ApplySetup(ctx context.Context, target kubetarget.Target, suiteDir string, setup suite.Setup) error {
	return applySetup(ctx, target, suiteDir, setup, commandOutput)
}

func applySetup(ctx context.Context, target kubetarget.Target, suiteDir string, setup suite.Setup, runner KubectlRunner) error {
	for _, resource := range setup.Resources {
		manifestPath, err := ResolveSetupPath(suiteDir, resource)
		if err != nil {
			return err
		}
		if _, err := os.Stat(manifestPath); err != nil {
			return fmt.Errorf("setup manifest for %q not found at %s: %w", resource.Name, manifestPath, err)
		}
		resolvedSuiteDir, err := filepath.EvalSymlinks(suiteDir)
		if err != nil {
			return err
		}
		resolvedManifestPath, err := filepath.EvalSymlinks(manifestPath)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(resolvedSuiteDir, resolvedManifestPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("invalid setup path for %q: %q", resource.Name, resource.Path)
		}
		command := target.KubectlCommand("apply", "-f", resolvedManifestPath)
		if _, err := runner(ctx, command...); err != nil {
			return fmt.Errorf("apply setup resource %s: %w", resource.Name, err)
		}
		for _, wait := range resource.Wait {
			args, err := WaitRuleArgs(wait)
			if err != nil {
				return err
			}
			command := target.KubectlCommand(args...)
			if _, err := runner(ctx, command...); err != nil {
				return fmt.Errorf("wait for setup resource %s: %w", resource.Name, err)
			}
		}
	}
	return nil
}

func commandOutput(ctx context.Context, command ...string) ([]byte, error) {
	return exec.CommandContext(ctx, command[0], command[1:]...).Output()
}
```

- [ ] **Step 5: Implement target-aware observability**

Change signatures and command construction in both observability packages:

```go
func PortForwardArgs(target kubetarget.Target, cfg Config) []string {
	return target.KubectlCommand("-n", cfg.Namespace, "port-forward", "service/"+cfg.ServiceName, fmt.Sprintf("%d:%d", cfg.LocalPort, cfg.ServicePort))
}

func RolloutStatusArgs(target kubetarget.Target, cfg Config) []string {
	return target.KubectlCommand("rollout", "status", "deployment/prometheus", "-n", cfg.Namespace, "--timeout=2m")
}

func Install(ctx context.Context, target kubetarget.Target, manifestPath, image string) error {
	return InstallWithScrapeTarget(ctx, target, manifestPath, image, "")
}

func InstallWithScrapeTarget(ctx context.Context, target kubetarget.Target, manifestPath, image, scrapeTarget string) error {
	return installWithRunner(ctx, target, manifestPath, image, scrapeTarget, commandRunner)
}

type Runner func(context.Context, string, ...string) error

func installWithRunner(ctx context.Context, target kubetarget.Target, manifestPath, image, scrapeTarget string, runner Runner) error {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	rendered := RenderManifestWithScrapeTarget(string(manifest), image, scrapeTarget)
	return runner(ctx, rendered, target.KubectlCommand("apply", "-f", "-")...)
}

func WaitRollout(ctx context.Context, target kubetarget.Target, cfg Config) error {
	args := RolloutStatusArgs(target, cfg)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func commandRunner(ctx context.Context, stdin string, command ...string) error {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func PortForward(ctx context.Context, target kubetarget.Target, cfg Config) (*exec.Cmd, string, error) {
	args := PortForwardArgs(target, cfg)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	return cmd, EndpointURL(cfg), nil
}
```

In `internal/kubestatemetrics/kube_state_metrics.go`, use these exact bodies:

```go
func RolloutStatusArgs(target kubetarget.Target, cfg Config) []string {
	return target.KubectlCommand("rollout", "status", "deployment/kube-state-metrics", "-n", cfg.Namespace, "--timeout=2m")
}

func Install(ctx context.Context, target kubetarget.Target, manifestPath, image string) error {
	return installWithRunner(ctx, target, manifestPath, image, commandRunner)
}

type Runner func(context.Context, string, ...string) error

func installWithRunner(ctx context.Context, target kubetarget.Target, manifestPath, image string, runner Runner) error {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	return runner(ctx, RenderManifest(string(manifest), image), target.KubectlCommand("apply", "-f", "-")...)
}

func WaitRollout(ctx context.Context, target kubetarget.Target, cfg Config) error {
	args := RolloutStatusArgs(target, cfg)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func commandRunner(ctx context.Context, stdin string, command ...string) error {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

- [ ] **Step 6: Update production callers with the legacy empty target**

In `cmd/perf-runner/main.go`, import `internal/kubetarget`, create
`target := kubetarget.Target{}` near the beginning of `runSuite`, and pass it to
`ApplySetup`, Prometheus, and kube-state-metrics calls. This is a compile-only
bridge; Task 5 populates the target from the CLI flag.

- [ ] **Step 7: Run tests and commit**

Run: `gofmt -w internal/run/setup.go internal/run/setup_test.go internal/prometheus/prometheus.go internal/prometheus/prometheus_test.go internal/kubestatemetrics/kube_state_metrics.go internal/kubestatemetrics/kube_state_metrics_test.go cmd/perf-runner/main.go && go test ./internal/run ./internal/prometheus ./internal/kubestatemetrics -v && go test ./...`

Expected: PASS.

```bash
git add internal/run/setup.go internal/run/setup_test.go internal/prometheus internal/kubestatemetrics cmd/perf-runner/main.go
git commit -m "feat: target setup and observability commands"
```

---

### Task 3: Target-Aware Artifact Copy And Cleanup

**Files:**
- Modify: `internal/artifacts/artifacts.go:22-146`
- Modify: `internal/artifacts/artifacts_test.go:1-104`

**Interfaces:**
- Consumes: `kubetarget.Target.KubectlCommand`.
- Produces: `artifacts.Copy(ctx, target, cfg, destination)`.
- Produces: `artifacts.CopySubpath(ctx, target, cfg, destination, subpath)`.
- Preserves: `CopyWithRunner` and `CopySubpathWithRunner` as argument-only test helpers.

- [ ] **Step 1: Add a failing full-lifecycle context test**

In `internal/artifacts/artifacts_test.go`, add:

```go
func TestCopyWithTargetRunnerTargetsApplyWaitCopyAndDelete(t *testing.T) {
	cfg := Config{Enabled: true, Namespace: "kata-io", PVCName: "results", MountPath: "/results", CopyImage: "busybox:test"}
	var calls [][]string
	runner := func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		if args[len(args)-1] == "--timeout=2m" {
			return errors.New("wait failed")
		}
		return nil
	}

	err := copyWithTargetRunnerAndPodName(context.Background(), kubetarget.Target{Context: "preview"}, cfg, t.TempDir(), runner, "copy-pod")
	if err == nil || !strings.Contains(err.Error(), "wait failed") {
		t.Fatalf("copy error = %v, want wait failure", err)
	}
	want := [][]string{
		{"kubectl", "--context", "preview", "apply", "-f", "-"},
		{"kubectl", "--context", "preview", "wait", "--for=condition=Ready", "pod/copy-pod", "-n", "kata-io", "--timeout=2m"},
		{"kubectl", "--context", "preview", "delete", "pod", "copy-pod", "-n", "kata-io", "--ignore-not-found=true"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands = %#v, want %#v", calls, want)
	}
}
```

Add this successful variant and imports for `errors`, `reflect`, and `internal/kubetarget`:

```go
func TestCopyWithTargetRunnerTargetsSuccessfulLifecycle(t *testing.T) {
	cfg := Config{Enabled: true, Namespace: "kata-io", PVCName: "results", MountPath: "/results", CopyImage: "busybox:test"}
	var calls [][]string
	runner := func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	if err := copyWithTargetRunnerAndPodName(context.Background(), kubetarget.Target{Context: "preview"}, cfg, t.TempDir(), runner, "copy-pod"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %#v, want apply, wait, copy, delete", calls)
	}
	wantPrefix := []string{"kubectl", "--context", "preview"}
	for _, call := range calls {
		if len(call) < len(wantPrefix) || !reflect.DeepEqual(call[:len(wantPrefix)], wantPrefix) {
			t.Fatalf("command = %#v, want prefix %#v", call, wantPrefix)
		}
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run: `go test ./internal/artifacts -v`

Expected: FAIL because `copyWithTargetRunnerAndPodName` and target-aware public functions do not exist.

- [ ] **Step 3: Add target wrappers without changing core copy behavior**

In `internal/artifacts/artifacts.go`, change public production functions and add an adapter:

```go
func Copy(ctx context.Context, target kubetarget.Target, cfg Config, destination string) error {
	return copyWithTargetRunnerAndPodName(ctx, target, cfg, destination, kubectlRunner, uniqueCopyPodName())
}

func CopySubpath(ctx context.Context, target kubetarget.Target, cfg Config, destination, subpath string) error {
	if err := ValidateSubpath(subpath); err != nil {
		return err
	}
	return copySubpathWithTargetRunnerAndPodName(ctx, target, cfg, destination, subpath, kubectlRunner, uniqueCopyPodName())
}

func copyWithTargetRunnerAndPodName(ctx context.Context, target kubetarget.Target, cfg Config, destination string, runner Runner, podName string) error {
	return copySubpathWithTargetRunnerAndPodName(ctx, target, cfg, destination, "", runner, podName)
}

func copySubpathWithTargetRunnerAndPodName(ctx context.Context, target kubetarget.Target, cfg Config, destination, subpath string, runner Runner, podName string) error {
	targetRunner := func(ctx context.Context, stdin string, args ...string) error {
		return runner(ctx, stdin, target.KubectlCommand(args...)...)
	}
	return copySubpathWithRunnerAndPodName(ctx, cfg, destination, subpath, targetRunner, podName)
}
```

Change `kubectlRunner` to execute a complete command:

```go
cmd := exec.CommandContext(ctx, args[0], args[1:]...)
```

Do not change `cleanupCopyPod`; its target is preserved by the closure captured in `targetRunner`, including the `context.Background()` cleanup path.

- [ ] **Step 4: Update production artifact callers with the legacy empty target**

In `cmd/perf-runner/main.go`, adapt the existing `copyArtifacts` helper to call
`artifacts.Copy(ctx, kubetarget.Target{}, ...)` and
`artifacts.CopySubpath(ctx, kubetarget.Target{}, ...)`. Do not yet add the CLI
flag; Task 5 replaces this compile bridge with the actual run target.

- [ ] **Step 5: Run tests and commit**

Run: `gofmt -w internal/artifacts/artifacts.go internal/artifacts/artifacts_test.go cmd/perf-runner/main.go && go test ./internal/artifacts -v && go test ./...`

Expected: PASS, including existing subpath and error-precedence tests.

```bash
git add internal/artifacts/artifacts.go internal/artifacts/artifacts_test.go cmd/perf-runner/main.go
git commit -m "feat: target artifact copy commands"
```

---

### Task 4: Target-Aware Kube-Burner And Metadata

**Files:**
- Modify: `internal/run/run.go:15-69,180-190`
- Modify: `internal/run/run_test.go:284-388`

**Interfaces:**
- Consumes: `kubetarget.Target.KubeBurnerArgs`.
- Produces: `run.ExecuteKubeBurner(workloadPath, logPath string, target kubetarget.Target) error`.
- Produces: `Metadata.KubeContext string` with `yaml:"kubeContext,omitempty"`.
- Changes: `Metadata.ClusterName` to `yaml:"clusterName,omitempty"`.

- [ ] **Step 1: Add failing kube-burner target tests**

Update the existing fake-binary tests to call:

```go
ExecuteKubeBurner(workloadPath, logPath, kubetarget.Target{Context: "preview"})
```

Assert output contains:

```text
repo-local init -c workload.yml --kube-context preview
```

Add a legacy call with `kubetarget.Target{}` and retain the current expected output without a context flag.

- [ ] **Step 2: Add failing metadata serialization tests**

Add to `internal/run/run_test.go`:

```go
func TestWriteMetadataRecordsExplicitContextAndOmitsClusterName(t *testing.T) {
	runDir := t.TempDir()
	err := WriteMetadata(runDir, Metadata{Suite: "kata-perf", Mode: "smoke", KubeContext: "preview"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "metadata", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "kubeContext: preview") || strings.Contains(text, "clusterName:") {
		t.Fatalf("metadata = %s", text)
	}
}

func TestWriteMetadataPreservesLegacyClusterName(t *testing.T) {
	runDir := t.TempDir()
	err := WriteMetadata(runDir, Metadata{Suite: "kata-perf", Mode: "smoke", ClusterName: "akskataperf"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "metadata", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "clusterName: akskataperf") || strings.Contains(text, "kubeContext:") {
		t.Fatalf("metadata = %s", text)
	}
}
```

- [ ] **Step 3: Run focused tests and verify they fail**

Run: `go test ./internal/run -run 'TestExecuteKubeBurner|TestWriteMetadata' -v`

Expected: FAIL due to the old function signature and missing metadata field/tags.

- [ ] **Step 4: Implement kube-burner and metadata changes**

Import `internal/kubetarget`, update metadata:

```go
type Metadata struct {
	Suite         string            `yaml:"suite"`
	Mode          string            `yaml:"mode"`
	Timestamp     string            `yaml:"timestamp"`
	ResourceGroup string            `yaml:"resourceGroup"`
	ClusterName   string            `yaml:"clusterName,omitempty"`
	KubeContext   string            `yaml:"kubeContext,omitempty"`
	Images        map[string]string `yaml:"images"`
	BuiltImages   []acr.BuiltImage  `yaml:"builtImages,omitempty"`
	Setup         suite.Setup       `yaml:"setup,omitempty"`
}
```

Update execution:

```go
func ExecuteKubeBurner(workloadPath, logPath string, target kubetarget.Target) error {
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	args := target.KubeBurnerArgs("init", "-c", filepath.Base(workloadPath))
	cmd := exec.Command(kubeBurnerExecutable(workloadPath), args...)
	cmd.Dir = filepath.Dir(workloadPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
}
```

- [ ] **Step 5: Update the production caller with the legacy empty target**

In `cmd/perf-runner/main.go`, adapt the kube-burner call to
`runpkg.ExecuteKubeBurner(workloadPath, logPath, kubetarget.Target{})`. Task 5
replaces this compile bridge with the actual run target.

- [ ] **Step 6: Run tests and commit**

Run: `gofmt -w internal/run/run.go internal/run/run_test.go cmd/perf-runner/main.go && go test ./internal/run -v && go test ./...`

Expected: PASS.

```bash
git add internal/run/run.go internal/run/run_test.go cmd/perf-runner/main.go
git commit -m "feat: target kube-burner runs"
```

---

### Task 5: Run-Suite CLI And End-To-End Propagation

**Files:**
- Modify: `cmd/perf-runner/main.go:394-654`
- Modify: `cmd/perf-runner/main_test.go:39-126,458-600`

**Interfaces:**
- Consumes all target-aware package APIs from Tasks 1-4.
- Produces optional `run-suite --kube-context CONTEXT`.
- Produces `validateRunSuiteTarget(kubeContext, resourceGroup string, builds []acr.ImageBuild) error`.
- Produces target-aware benchmark/artifact orchestration while preserving existing error precedence.

- [ ] **Step 1: Add failing resource-group validation tests**

Add to `cmd/perf-runner/main_test.go`:

```go
func TestValidateRunSuiteTargetResourceGroupRules(t *testing.T) {
	builds := []acr.ImageBuild{{Key: "benchmark"}}
	for _, tc := range []struct {
		name, context, resourceGroup string
		builds                       []acr.ImageBuild
		wantErr                      bool
	}{
		{name: "legacy requires group", wantErr: true},
		{name: "legacy with group", resourceGroup: "rg-test"},
		{name: "explicit no builds", context: "preview"},
		{name: "explicit builds require group", context: "preview", builds: builds, wantErr: true},
		{name: "explicit builds with group", context: "preview", resourceGroup: "rg-build", builds: builds},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRunSuiteTarget(tc.context, tc.resourceGroup, tc.builds)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2: Add failing orchestration tests for explicit no-build mode**

Add this helper in `cmd/perf-runner/main_test.go` to create the minimum valid suite. Its Bicep paths are deliberately valid schema strings that do not exist on disk:

```go
func writeNoBuildContextSuite(t *testing.T, root string) {
	t.Helper()
	suiteDir := filepath.Join(root, "suites", "existing")
	if err := os.MkdirAll(filepath.Join(suiteDir, "vars"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(suiteDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"suite.yml": "name: existing\ndescription: Existing cluster suite\ntests:\n  - startup\n",
		"requirements.yml": `suite: existing
requires:
  infrastructure:
    provider: aks
    bicep:
      template: ../outside/main.bicep
      parameters: ../outside/params.bicepparam
  kubernetes:
    minVersion: "9.99"
  nodeSelectors: []
  observability:
    prometheus:
      required: false
      install: false
      namespace: perf-monitoring
      imageKey: prometheus
      serviceName: prometheus
      servicePort: 9090
      localPort: 9090
      requiredMetrics: []
`,
		"workload.yml": "jobs: []\n",
		"metrics.yml": "[]\n",
		filepath.Join("vars", "smoke.yml"): `iterations: 1
iterationsPerNamespace: 1
qps: 1
burst: 1
cleanup: true
waitWhenFinished: true
preLoadImages: false
templateVars: {}
imageVars: {}
`,
	}
	for name, content := range files {
		path := filepath.Join(suiteDir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "images.yml"), []byte("pause: pause:test\nprometheus: prometheus:test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeRecordingCommand(t *testing.T, dir, name, marker, stdout string) {
	t.Helper()
	content := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(marker) + "\n" +
		"printf '%s' " + strconv.Quote(stdout) + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
```

For explicit mode, use the out-of-repository Bicep paths above. Make fake
kubectl return a valid server version and fake kube-burner exit successfully.
Call:

```go
err := run([]string{"run-suite", "--suite", "existing", "--mode", "smoke", "--kube-context", "preview"})
```

Assert:

```go
if err != nil {
	t.Fatalf("run-suite error = %v", err)
}
if data, _ := os.ReadFile(azMarker); len(data) != 0 {
	t.Fatalf("explicit run invoked az: %s", data)
}
assertFileContains(t, kubectlMarker, "--context preview version -o json")
runMetadata := singleRunMetadataPath(t, filepath.Join(root, "results"))
data, err := os.ReadFile(runMetadata)
if err != nil {
	t.Fatal(err)
}
if !strings.Contains(string(data), "kubeContext: preview") || strings.Contains(string(data), "clusterName:") {
	t.Fatalf("metadata = %s", data)
}
```

Construct the test with:

```go
root := testRepoRoot(t)
writeNoBuildContextSuite(t, root)
binDir := t.TempDir()
azMarker := filepath.Join(t.TempDir(), "az.log")
kubectlMarker := filepath.Join(t.TempDir(), "kubectl.log")
writeRecordingCommand(t, binDir, "az", azMarker, "")
writeRecordingCommand(t, binDir, "kubectl", kubectlMarker, `{"serverVersion":{"gitVersion":"v9.99.0"}}`)
writeRecordingCommand(t, binDir, "kube-burner", filepath.Join(t.TempDir(), "kube-burner.log"), "")
t.Setenv("PATH", binDir)
withWorkingDir(t, root)
```

Implement `singleRunMetadataPath` by reading the single child of `results/`
and returning `<child>/metadata/run.yml`. The out-of-repository paths guarantee
the test fails if explicit no-build orchestration calls either path resolver.

- [ ] **Step 3: Add failing legacy credential and invalid-combination tests**

Create a valid Bicep template and parameter file for a legacy suite, run with `--resource-group rg-test`, and assert `azMarker` contains `aks get-credentials --resource-group rg-test --name aksexisting --overwrite-existing`. Assert the kubectl marker contains `version -o json` but not `--context`.

For invalid combinations, call `validateRunSuiteTarget` directly and also call `run-suite` for an explicit suite with one image build but no resource group. Use marker files for `az`, `kubectl`, and `kube-burner`; assert all remain empty and `results/` does not exist.

Add a direct requirement-runner unit test that uses both a minimum version and
a required node selector. The recording runner must receive these argument
lists, proving the same target-aware runner is used for both checks:

```go
want := [][]string{
	{"version", "-o", "json"},
	{"get", "nodes", "-l", "kubernetes.azure.com/os-sku=AzureLinux", "-o", "name"},
}
```

At the CLI integration level, assert the fake kubectl marker contains
`--context preview get nodes -l kubernetes.azure.com/os-sku=AzureLinux -o name`.

Add an explicit-context suite with one image build and `--resource-group
rg-build`. Use fake `az` and `kubectl` commands plus a suite-local fake build
context. Assert the Azure marker contains the deployment-output/ACR build
operations needed by the existing image path but does not contain `aks
get-credentials`; assert every Kubernetes marker line contains `--context
preview`.

- [ ] **Step 4: Add failing target-aware artifact orchestration tests**

Introduce target callback types in tests and update existing orchestration assertions:

```go
execute := func(_ string, _ string, target kubetarget.Target) error {
	if target.Context != "preview" {
		t.Fatalf("executor target = %#v", target)
	}
	return errors.New("kube-burner failed")
}
copyArtifacts := func(_ context.Context, target kubetarget.Target, _ artifacts.Config, _, _ string) error {
	if target.Context != "preview" {
		t.Fatalf("copy target = %#v", target)
	}
	return errors.New("artifact copy failed")
}
```

Call the target-aware orchestration with `kubetarget.Target{Context: "preview"}` and retain the assertion that the returned error wraps the kube-burner error and reports the copy failure.

- [ ] **Step 5: Run CLI tests and verify they fail**

Run: `go test ./cmd/perf-runner -run 'TestValidateRunSuiteTarget|TestRunSuite.*Context|TestExecuteRunAndCopyArtifacts' -v`

Expected: FAIL because the flag, validation helper, target callbacks, and orchestration do not exist.

- [ ] **Step 6: Implement CLI validation before side effects**

Parse the new flag without requiring the resource group in the initial usage check:

```go
kubeContext := fs.String("kube-context", "", "kube context")
if *suiteName == "" {
	return fmt.Errorf("usage: perf-runner run-suite --suite SUITE --mode MODE [--resource-group RG] [--kube-context CONTEXT]")
}
target := kubetarget.Target{Context: *kubeContext}
```

After requirements are loaded, validate before Bicep resolution, external commands, or run-directory creation:

```go
func validateRunSuiteTarget(kubeContext, resourceGroup string, builds []acr.ImageBuild) error {
	if resourceGroup == "" && (kubeContext == "" || len(builds) > 0) {
		if kubeContext == "" {
			return fmt.Errorf("resource-group is required when kube-context is omitted")
		}
		return fmt.Errorf("resource-group is required when suite image builds are configured")
	}
	return nil
}
```

- [ ] **Step 7: Split legacy cluster lookup from explicit target selection**

Use this control flow after validation:

```go
parametersPath := ""
clusterName := ""
if *kubeContext == "" || len(req.Requires.Images.Builds) > 0 {
	parametersPath, err = resolveSuitePath(root, *suiteName, req.Requires.Infrastructure.Bicep.Parameters)
	if err != nil {
		return err
	}
	if _, err := resolveRepoPath(root, req.Requires.Infrastructure.Bicep.Template); err != nil {
		return err
	}
}
if *kubeContext == "" {
	clusterName, err = readBicepParamString(parametersPath, "clusterName")
	if err != nil {
		return err
	}
}

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
if *kubeContext == "" {
	if err := infra.GetCredentials(ctx, *resourceGroup, clusterName); err != nil {
		return err
	}
}
if err := runpkg.ValidateRequirements(ctx, runpkg.Requirements{
	Kubernetes: req.Requires.Kubernetes,
	NodeSelectors: req.Requires.NodeSelectors,
}, target.Output); err != nil {
	return err
}
```

This makes explicit no-build runs independent of Bicep files and all explicit runs independent of credential refresh.

- [ ] **Step 8: Pass the target through all package calls and metadata**

Update setup and observability calls to include `target`. Write metadata with:

```go
runpkg.Metadata{
	Suite: *suiteName, Mode: *modeName, Timestamp: runTimestamp.Format(time.RFC3339),
	ResourceGroup: *resourceGroup, ClusterName: clusterName, KubeContext: *kubeContext,
	Images: images, BuiltImages: builtImages, Setup: suiteCfg.Setup,
}
```

Replace argument-only artifact helpers with target-aware signatures:

```go
type targetKubeBurnerExecutor func(string, string, kubetarget.Target) error
type targetArtifactJobWaiter func(context.Context, kubetarget.Target, artifacts.Config) error
type targetArtifactCopier func(context.Context, kubetarget.Target, artifacts.Config, string, string) error
```

Implement the target-aware production helpers:

```go
func waitArtifactJobsComplete(ctx context.Context, target kubetarget.Target, cfg artifacts.Config) error {
	if !cfg.Enabled || cfg.Namespace == "" {
		return nil
	}
	args := target.KubectlCommand("wait", "--for=condition=complete", "job", "--all", "-n", cfg.Namespace, "--timeout=15m")
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyArtifacts(ctx context.Context, target kubetarget.Target, cfg artifacts.Config, destination, subpath string) error {
	if subpath == "" {
		return artifacts.Copy(ctx, target, cfg, destination)
	}
	return artifacts.CopySubpath(ctx, target, cfg, destination, subpath)
}
```

Add a direct `waitArtifactJobsComplete` test using a fake kubectl executable in
`PATH`. Call it with `kubetarget.Target{Context: "preview"}` and assert the
recorded command is:

```text
--context preview wait --for=condition=complete job --all -n kata-io --timeout=15m
```

Pass `target` to execution, job wait, and copy callbacks while retaining the existing image resolution, execution, wait, copy, and error-precedence order.

- [ ] **Step 9: Run CLI tests and the full suite, then commit**

Run: `gofmt -w cmd/perf-runner/main.go cmd/perf-runner/main_test.go && go test ./cmd/perf-runner -v && go test ./...`

Expected: PASS.

```bash
git add cmd/perf-runner/main.go cmd/perf-runner/main_test.go
git commit -m "feat: run suites against optional context"
```

---

### Task 6: Makefile, User Documentation, And Verification

**Files:**
- Modify: `Makefile:1-64`
- Modify: `README.md:5-57`
- Modify: `cmd/perf-runner/main_test.go`

**Interfaces:**
- Consumes: optional `run-suite --kube-context` from Task 5.
- Produces: optional `KUBE_CONTEXT` Make variable with run-suite-only resource-group suppression.

- [ ] **Step 1: Add failing Make dry-run tests**

Add the following `make -n` assertions to `cmd/perf-runner/main_test.go`:

```go
func TestMakeRunSuiteExplicitContextOmitsDefaultResourceGroup(t *testing.T) {
	output := makeDryRun(t, "run-suite", "TEST_SUITE=kata-perf", "KUBE_CONTEXT=preview")
	if !strings.Contains(output, `--kube-context "preview"`) {
		t.Fatalf("make output missing context: %s", output)
	}
	if strings.Contains(output, "rg-aks-burner-kata-perf") {
		t.Fatalf("make output forwarded default resource group: %s", output)
	}
}

func TestMakeRunSuiteExplicitContextForwardsSuppliedResourceGroup(t *testing.T) {
	output := makeDryRun(t, "run-suite", "TEST_SUITE=kata-io", "KUBE_CONTEXT=preview", "RESOURCE_GROUP=rg-build")
	if !strings.Contains(output, `--resource-group "rg-build"`) {
		t.Fatalf("make output missing supplied resource group: %s", output)
	}
}

func TestMakeLegacyAndLifecycleResourceGroupDefaultsRemain(t *testing.T) {
	for _, target := range []string{"run-suite", "provision", "destroy"} {
		args := []string{target, "TEST_SUITE=kata-perf"}
		if target != "run-suite" {
			args = append(args, "KUBE_CONTEXT=preview")
		}
		output := makeDryRun(t, args...)
		if !strings.Contains(output, "rg-aks-burner-kata-perf") {
			t.Fatalf("%s lost default resource group: %s", target, output)
		}
	}
}
```

Implement this helper and add `os/exec` to the imports:

```go
func makeDryRun(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("make", append([]string{"-n"}, args...)...)
	cmd.Dir = testSourceRoot
	cmd.Env = filteredEnv(os.Environ(), "KUBE_CONTEXT", "RESOURCE_GROUP")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func filteredEnv(env []string, names ...string) []string {
	blocked := map[string]bool{}
	for _, name := range names {
		blocked[name] = true
	}
	result := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if !blocked[name] {
			result = append(result, entry)
		}
	}
	return result
}
```

Add an environment-origin variant rather than passing assignments on the make
command line:

```go
func TestMakeRunSuiteForwardsEnvironmentResourceGroup(t *testing.T) {
	cmd := exec.Command("make", "-n", "run-suite", "TEST_SUITE=kata-io")
	cmd.Dir = testSourceRoot
	cmd.Env = append(filteredEnv(os.Environ(), "KUBE_CONTEXT", "RESOURCE_GROUP"), "KUBE_CONTEXT=preview", "RESOURCE_GROUP=rg-environment")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n run-suite: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), `--resource-group "rg-environment"`) {
		t.Fatalf("make output missing environment resource group: %s", output)
	}
}
```

- [ ] **Step 2: Run Make tests and verify they fail**

Run: `go test ./cmd/perf-runner -run 'TestMake' -v`

Expected: FAIL because the Makefile does not forward context and still forwards the default resource group.

- [ ] **Step 3: Add run-suite-only Make argument derivation**

At the top of `Makefile`, add:

```make
KUBE_CONTEXT ?=
RUN_SUITE_RESOURCE_GROUP = $(if $(and $(strip $(KUBE_CONTEXT)),$(filter file,$(origin RESOURCE_GROUP))),,$(RESOURCE_GROUP))
RUN_SUITE_CONTEXT_ARGS = $(if $(strip $(KUBE_CONTEXT)),--kube-context "$(KUBE_CONTEXT)")
RUN_SUITE_RESOURCE_GROUP_ARGS = $(if $(strip $(RUN_SUITE_RESOURCE_GROUP)),--resource-group "$(RUN_SUITE_RESOURCE_GROUP)")
```

Change only `run-suite`:

```make
	go run ./cmd/perf-runner run-suite --suite "$(TEST_SUITE)" --mode "$(TEST_MODE)" $(RUN_SUITE_RESOURCE_GROUP_ARGS) $(RUN_SUITE_CONTEXT_ARGS)
```

Do not change `provision` or `destroy`. `$(origin RESOURCE_GROUP)` equals `file` only for the Makefile's `?=` default; command-line and environment values remain caller-supplied and are forwarded.

- [ ] **Step 4: Document both invocation modes**

In `README.md`, add an existing-context example:

```bash
TEST_SUITE=kata-perf TEST_MODE=smoke KUBE_CONTEXT=<existing-context> make run-suite
```

Document that explicit context skips `az aks get-credentials`, targets every kubectl and kube-burner operation, and may omit `RESOURCE_GROUP` only for suites without image builds. State that suites with image builds still require a resource group for ACR/deployment access and that no separate kubeconfig option is supported.

- [ ] **Step 5: Run focused and full verification**

Run:

```bash
gofmt -w cmd/perf-runner/main_test.go
go test ./cmd/perf-runner -run 'TestMake' -v
go test ./...
go vet ./...
go build ./cmd/perf-runner
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 6: Review the complete implementation diff**

Run:

```bash
git status --short
git diff HEAD
git log --oneline -10
```

Invoke the `code-reviewer` subagent with the complete diff. Independently verify and resolve every Critical or High finding, then rerun the verification commands from Step 5.

- [ ] **Step 7: Commit integration and documentation**

```bash
git add Makefile README.md cmd/perf-runner/main_test.go
git commit -m "docs: describe explicit kube contexts"
```

Run `git status --short --branch`; expected output shows `feature/optional-kube-context` with a clean worktree.
