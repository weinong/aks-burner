# Kata I/O Suite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an end-to-end `kata-io` exploratory benchmark suite for AKS Kata Pod Sandboxing I/O and Git clone performance.

**Architecture:** Keep the existing `perf-runner` lifecycle and add a new `suites/kata-io` suite with explicit kube-burner jobs, a benchmark image, storage/result PVC templates, and fio profiles. Extend the runner narrowly for mode-selectable workload files, kube-state-metrics installation, and results PVC artifact copy. Extend AKS Bicep parameters so this suite provisions a Kata-capable workload node pool without changing the public lifecycle commands.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3`, existing JSON schema validation, Bicep, Kubernetes manifests, kube-burner, Bash, fio, Git Trace2, Azure AKS, ACR, Prometheus, kube-state-metrics `mcr.microsoft.com/oss/v2/kubernetes/kube-state-metrics:v2.19.0`.

## Global Constraints

- New suite name is `kata-io`.
- Existing `kata-perf` suite must remain unchanged in behavior.
- Default Git repo is `https://github.com/kubernetes/kubernetes`.
- Default Kata RuntimeClass is `kata-vm-isolation`.
- AKS workload runtime value is `KataVmIsolation` for the `kata-io` workload pool.
- kube-state-metrics image is `mcr.microsoft.com/oss/v2/kubernetes/kube-state-metrics:v2.19.0`.
- Artifact copy image is `mcr.microsoft.com/oss/busybox/busybox:1.36.1` because `kubectl cp` requires `tar` in the container.
- Results artifacts are required and copied to `results/<timestamp>_kata-io_<mode>/artifacts/`.
- v1 does not add a general matrix engine, Pushgateway, in-cluster Git mirror, Grafana dashboards, Azure Monitor backend storage metrics, custom RuntimeClass creation, shallow clone, or sparse checkout.
- End-to-end smoke validation is required before implementation is considered complete.
- Do not run `git commit` unless the user explicitly authorizes committing during execution; listed commit messages are for an approved commit workflow.

---

## File Structure

- `internal/run/run.go`: add optional `workloadFile` to modes and preserve explicit kube-burner job scheduling fields when rendering.
- `schemas/mode.schema.json`: allow optional `workloadFile`.
- `cmd/perf-runner/main.go`: load the selected workload file, parse kube-state-metrics and artifact requirements, install kube-state-metrics, and copy artifacts after kube-burner completes.
- `schemas/requirements.schema.json`: add optional `requires.observability.kubeStateMetrics` and `requires.artifacts` objects.
- `config/images.yml`: add the pinned `kube-state-metrics` image key.
- `internal/kubestatemetrics/kube_state_metrics.go`: render, install, and wait for kube-state-metrics.
- `internal/kubestatemetrics/kube_state_metrics_test.go`: unit tests for manifest rendering and rollout command args.
- `observability/kube-state-metrics/kube-state-metrics.yaml`: Kubernetes manifest for kube-state-metrics in `perf-monitoring`.
- `observability/prometheus/prometheus.yaml`: add scrape config for kube-state-metrics.
- `internal/artifacts/artifacts.go`: copy a results PVC to a local run directory using a temporary copy pod.
- `internal/artifacts/artifacts_test.go`: unit tests for copy pod manifest and kubectl command sequencing.
- `infra/aks/main.bicep`: add user node pool OS SKU and workload runtime parameters.
- `suites/kata-io/**`: add the new suite, benchmark image, scripts, fio profiles, templates, modes, metrics, and infra params.
- `internal/examples/examples_test.go`: validate `kata-io` contracts and core file contents.
- `README.md`: add the `kata-io` lifecycle and required end-to-end validation note.

---

### Task 1: Runner Workload Selection And Explicit Job Settings

**Files:**
- Modify: `internal/run/run.go`
- Modify: `internal/run/run_test.go`
- Modify: `schemas/mode.schema.json`
- Modify: `cmd/perf-runner/main.go`
- Modify: `cmd/perf-runner/main_test.go`

**Interfaces:**
- Consumes: existing `run.Mode`, `run.RenderWorkload(workload map[string]any, mode Mode, images map[string]string, prometheusEndpoint string) (map[string]any, error)`.
- Produces: `Mode.WorkloadFile string`, `Mode.SelectedWorkloadFile() string`, and render behavior that only fills scheduling defaults when a job omits those fields.

- [ ] **Step 1: Add failing mode workload-file tests**

Add these tests to `internal/run/run_test.go`:

```go
func TestModeSelectedWorkloadFileDefaultsToWorkloadYAML(t *testing.T) {
	mode := Mode{}
	if got := mode.SelectedWorkloadFile(); got != "workload.yml" {
		t.Fatalf("SelectedWorkloadFile() = %q, want workload.yml", got)
	}
}

func TestModeSelectedWorkloadFileUsesConfiguredFile(t *testing.T) {
	mode := Mode{WorkloadFile: "workload-smoke.yml"}
	if got := mode.SelectedWorkloadFile(); got != "workload-smoke.yml" {
		t.Fatalf("SelectedWorkloadFile() = %q, want workload-smoke.yml", got)
	}
}
```

- [ ] **Step 2: Add failing render preservation test**

Add this test to `internal/run/run_test.go`:

```go
func TestRenderWorkloadPreservesExplicitJobScheduling(t *testing.T) {
	workload := map[string]any{
		"jobs": []any{
			map[string]any{
				"name":                   "explicit-concurrency",
				"jobIterations":          10,
				"iterationsPerNamespace": 10,
				"qps":                    10,
				"burst":                  10,
				"cleanup":                false,
				"waitWhenFinished":       true,
				"preLoadImages":          false,
				"objects": []any{
					map[string]any{"objectTemplate": "templates/job.yml", "replicas": 1, "inputVars": map[string]any{}},
				},
			},
		},
	}
	mode := Mode{Iterations: 1, IterationsPerNamespace: 1, QPS: 1, Burst: 1, Cleanup: true, WaitWhenFinished: true, PreLoadImages: true}
	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	job := rendered["jobs"].([]any)[0].(map[string]any)
	checks := map[string]any{
		"jobIterations":          10,
		"iterationsPerNamespace": 10,
		"qps":                    10,
		"burst":                  10,
		"cleanup":                false,
		"waitWhenFinished":       true,
		"preLoadImages":          false,
	}
	for key, want := range checks {
		if got := job[key]; got != want {
			t.Fatalf("job[%s] = %#v, want %#v", key, got, want)
		}
	}
}
```

- [ ] **Step 3: Run focused tests to verify failure**

Run: `go test ./internal/run -run 'TestModeSelectedWorkloadFile|TestRenderWorkloadPreservesExplicitJobScheduling'`

Expected: FAIL because `Mode.WorkloadFile` and `SelectedWorkloadFile` do not exist and explicit scheduling is overwritten.

- [ ] **Step 4: Implement `Mode.WorkloadFile` and scheduling preservation**

Update `internal/run/run.go`:

```go
type Mode struct {
	Iterations             int               `yaml:"iterations"`
	IterationsPerNamespace int               `yaml:"iterationsPerNamespace"`
	QPS                    int               `yaml:"qps"`
	Burst                  int               `yaml:"burst"`
	Cleanup                bool              `yaml:"cleanup"`
	WaitWhenFinished       bool              `yaml:"waitWhenFinished"`
	PreLoadImages          bool              `yaml:"preLoadImages"`
	WorkloadFile           string            `yaml:"workloadFile,omitempty"`
	TemplateVars           map[string]any    `yaml:"templateVars"`
	ImageVars              map[string]string `yaml:"imageVars"`
}

func (m Mode) SelectedWorkloadFile() string {
	if m.WorkloadFile == "" {
		return "workload.yml"
	}
	return m.WorkloadFile
}
```

Replace the unconditional job scheduling assignments inside `RenderWorkload` with default-preserving assignments:

```go
setDefault(job, "jobIterations", mode.Iterations)
setDefault(job, "iterationsPerNamespace", mode.IterationsPerNamespace)
setDefault(job, "qps", mode.QPS)
setDefault(job, "burst", mode.Burst)
setDefault(job, "cleanup", mode.Cleanup)
setDefault(job, "waitWhenFinished", mode.WaitWhenFinished)
setDefault(job, "preLoadImages", mode.PreLoadImages)
```

Add this helper near `ensureMap`:

```go
func setDefault(parent map[string]any, key string, value any) {
	if _, exists := parent[key]; exists {
		return
	}
	parent[key] = value
}
```

- [ ] **Step 5: Allow `workloadFile` in mode schema**

Update `schemas/mode.schema.json` by adding this property under `properties`:

```json
"workloadFile": { "type": "string", "minLength": 1 }
```

Keep the existing `required` array unchanged so old mode files remain valid.

- [ ] **Step 6: Load the selected workload file in runner**

In `cmd/perf-runner/main.go`, move mode loading before workload loading and replace the hard-coded workload path with `mode.SelectedWorkloadFile()`:

```go
var mode runpkg.Mode
modePath, err := resolveSuitePath(root, *suiteName, filepath.Join("vars", *modeName+".yml"))
if err != nil {
	return err
}
if err := config.ValidateYAML(filepath.Join(root, "schemas", "mode.schema.json"), modePath); err != nil {
	return err
}
if err := config.LoadYAML(modePath, &mode); err != nil {
	return err
}

var workload map[string]any
workloadFile, err := resolveSuitePath(root, *suiteName, mode.SelectedWorkloadFile())
if err != nil {
	return err
}
if err := config.LoadYAML(workloadFile, &workload); err != nil {
	return err
}
```

Remove the earlier hard-coded `workload.yml` load block.

- [ ] **Step 7: Add runner path test**

Add this focused test to `cmd/perf-runner/main_test.go`:

```go
func TestModeWorkloadFileResolvesInsideSuite(t *testing.T) {
	root := t.TempDir()
	got, err := resolveSuitePath(root, "kata-io", "workload-smoke.yml")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "suites", "kata-io", "workload-smoke.yml")
	if got != want {
		t.Fatalf("resolveSuitePath() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 8: Run tests**

Run: `go test ./internal/run ./cmd/perf-runner`

Expected: PASS.

- [ ] **Step 9: Approved commit command**

If commits are authorized, run:

```bash
git add internal/run/run.go internal/run/run_test.go schemas/mode.schema.json cmd/perf-runner/main.go cmd/perf-runner/main_test.go
git commit -m "feat: support suite-selected workload files"
```

---

### Task 2: Requirements Schema For kube-state-metrics And Artifacts

**Files:**
- Modify: `schemas/requirements.schema.json`
- Modify: `config/images.yml`
- Modify: `cmd/perf-runner/main.go`
- Modify: `cmd/perf-runner/main_test.go`

**Interfaces:**
- Consumes: existing `prometheus.Config` and suite requirements parsing in `runSuite`.
- Produces: `observabilityConfig` parsing with `KubeStateMetrics kubestatemetrics.Config` and `artifacts.Config` fields, plus static image key `kube-state-metrics`.

- [ ] **Step 1: Add failing requirements schema test for new fields**

Add this test to `cmd/perf-runner/main_test.go`:

```go
func TestRequirementsSchemaAcceptsKubeStateMetricsAndArtifacts(t *testing.T) {
	root := testRepoRoot(t)
	path := filepath.Join(root, "requirements.yml")
	data := []byte(`suite: kata-io
requires:
  infrastructure:
    provider: aks
    bicep:
      template: infra/aks/main.bicep
      parameters: suites/kata-io/infra.bicepparam
  kubernetes:
    minVersion: "1.36"
  observability:
    prometheus:
      required: true
      install: true
      namespace: perf-monitoring
      imageKey: prometheus
      serviceName: prometheus
      servicePort: 9090
      localPort: 9090
      requiredMetrics:
        - container_cpu_usage_seconds_total
    kubeStateMetrics:
      required: true
      install: true
      namespace: perf-monitoring
      imageKey: kube-state-metrics
      serviceName: kube-state-metrics
      servicePort: 8080
  artifacts:
    enabled: true
    namespace: kata-io
    pvcName: kata-io-results
    mountPath: /results
    copyImage: artifact-copy
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), path); err != nil {
		t.Fatalf("requirements schema rejected kube-state-metrics/artifacts: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./cmd/perf-runner -run TestRequirementsSchemaAcceptsKubeStateMetricsAndArtifacts`

Expected: FAIL because the schema rejects `kubeStateMetrics` and `artifacts`.

- [ ] **Step 3: Extend requirements schema**

In `schemas/requirements.schema.json`, add `"artifacts"` under `requires.properties`:

```json
"artifacts": {
  "type": "object",
  "additionalProperties": false,
  "required": ["enabled", "namespace", "pvcName", "mountPath", "copyImage"],
  "properties": {
    "enabled": { "type": "boolean" },
    "namespace": { "type": "string", "minLength": 1 },
    "pvcName": { "type": "string", "minLength": 1 },
    "mountPath": { "type": "string", "minLength": 1 },
    "copyImage": { "type": "string", "minLength": 1 }
  }
}
```

Under `requires.properties.observability.properties`, add `kubeStateMetrics`:

```json
"kubeStateMetrics": {
  "type": "object",
  "additionalProperties": false,
  "required": ["required", "install", "namespace", "imageKey", "serviceName", "servicePort"],
  "properties": {
    "required": { "type": "boolean" },
    "install": { "type": "boolean" },
    "namespace": { "type": "string", "minLength": 1 },
    "imageKey": { "type": "string", "minLength": 1 },
    "serviceName": { "type": "string", "minLength": 1 },
    "servicePort": { "type": "integer", "minimum": 1, "maximum": 65535 },
    "requiredMetrics": { "type": "array", "items": { "type": "string", "minLength": 1 } }
  }
}
```

Keep only `prometheus` in the `observability.required` array so existing suites remain valid.

- [ ] **Step 4: Add static image key**

Update `config/images.yml`:

```yaml
images:
  pause: mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2
  prometheus: mcr.microsoft.com/oss/v2/prometheus/prometheus:v3.11.3
  kube-state-metrics: mcr.microsoft.com/oss/v2/kubernetes/kube-state-metrics:v2.19.0
  artifact-copy: mcr.microsoft.com/oss/busybox/busybox:1.36.1
```

- [ ] **Step 5: Add temporary parsing structs in runner**

In `cmd/perf-runner/main.go`, extend the anonymous requirements struct inside `runSuite`:

```go
Artifacts artifacts.Config `yaml:"artifacts"`
```

Inside `Observability`, add:

```go
KubeStateMetrics kubestatemetrics.Config `yaml:"kubeStateMetrics"`
```

Add imports for the packages that later tasks create:

```go
"github.com/Azure/aks-burner/internal/artifacts"
"github.com/Azure/aks-burner/internal/kubestatemetrics"
```

This step will not compile until Tasks 3 and 4 create the packages; keep the code change grouped with those tasks if executing inline.

- [ ] **Step 6: Run schema test**

Run: `go test ./cmd/perf-runner -run TestRequirementsSchemaAcceptsKubeStateMetricsAndArtifacts`

Expected after Tasks 3 and 4 compile packages: PASS.

- [ ] **Step 7: Approved commit command**

If commits are authorized, run after Tasks 3 and 4 are complete:

```bash
git add schemas/requirements.schema.json config/images.yml cmd/perf-runner/main.go cmd/perf-runner/main_test.go
git commit -m "feat: model kata io observability requirements"
```

---

### Task 3: kube-state-metrics Installation And Prometheus Scrape

**Files:**
- Create: `internal/kubestatemetrics/kube_state_metrics.go`
- Create: `internal/kubestatemetrics/kube_state_metrics_test.go`
- Create: `observability/kube-state-metrics/kube-state-metrics.yaml`
- Modify: `observability/prometheus/prometheus.yaml`
- Modify: `cmd/perf-runner/main.go`

**Interfaces:**
- Consumes: static image key `kube-state-metrics` from `config/images.yml`.
- Produces: `kubestatemetrics.Config`, `kubestatemetrics.Install`, `kubestatemetrics.WaitRollout`, and Prometheus scrape target `kube-state-metrics.perf-monitoring.svc:8080`.

- [ ] **Step 1: Write failing kube-state-metrics unit tests**

Create `internal/kubestatemetrics/kube_state_metrics_test.go`:

```go
package kubestatemetrics

import "testing"

func TestRenderManifestReplacesImage(t *testing.T) {
	manifest := "image: {{KUBE_STATE_METRICS_IMAGE}}\n"
	rendered := RenderManifest(manifest, "mcr.microsoft.com/oss/v2/kubernetes/kube-state-metrics:v2.19.0")
	want := "image: mcr.microsoft.com/oss/v2/kubernetes/kube-state-metrics:v2.19.0\n"
	if rendered != want {
		t.Fatalf("RenderManifest() = %q, want %q", rendered, want)
	}
}

func TestRolloutStatusArgs(t *testing.T) {
	args := RolloutStatusArgs(Config{Namespace: "perf-monitoring"})
	want := []string{"kubectl", "rollout", "status", "deployment/kube-state-metrics", "-n", "perf-monitoring", "--timeout=2m"}
	if len(args) != len(want) {
		t.Fatalf("args length = %d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/kubestatemetrics`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement kube-state-metrics package**

Create `internal/kubestatemetrics/kube_state_metrics.go`:

```go
package kubestatemetrics

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

type Config struct {
	Required    bool     `yaml:"required"`
	Install     bool     `yaml:"install"`
	Namespace   string   `yaml:"namespace"`
	ImageKey    string   `yaml:"imageKey"`
	ServiceName string   `yaml:"serviceName"`
	ServicePort int      `yaml:"servicePort"`
	Metrics     []string `yaml:"requiredMetrics"`
}

func RenderManifest(manifest string, image string) string {
	return strings.ReplaceAll(manifest, "{{KUBE_STATE_METRICS_IMAGE}}", image)
}

func RolloutStatusArgs(cfg Config) []string {
	return []string{"kubectl", "rollout", "status", "deployment/kube-state-metrics", "-n", cfg.Namespace, "--timeout=2m"}
}

func Install(ctx context.Context, manifestPath string, image string) error {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	rendered := RenderManifest(string(manifest), image)
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(rendered)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func WaitRollout(ctx context.Context, cfg Config) error {
	args := RolloutStatusArgs(cfg)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

- [ ] **Step 4: Add kube-state-metrics manifest**

Create `observability/kube-state-metrics/kube-state-metrics.yaml` with namespace, RBAC, deployment, and service:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: perf-monitoring
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kube-state-metrics
  namespace: perf-monitoring
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: perf-kube-state-metrics
rules:
  - apiGroups: [""]
    resources: [configmaps, nodes, pods, services, resourcequotas, replicationcontrollers, limitranges, persistentvolumeclaims, persistentvolumes, namespaces, endpoints]
    verbs: [list, watch]
  - apiGroups: [apps]
    resources: [statefulsets, daemonsets, deployments, replicasets]
    verbs: [list, watch]
  - apiGroups: [batch]
    resources: [cronjobs, jobs]
    verbs: [list, watch]
  - apiGroups: [autoscaling]
    resources: [horizontalpodautoscalers]
    verbs: [list, watch]
  - apiGroups: [node.k8s.io]
    resources: [runtimeclasses]
    verbs: [list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: perf-kube-state-metrics
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: perf-kube-state-metrics
subjects:
  - kind: ServiceAccount
    name: kube-state-metrics
    namespace: perf-monitoring
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kube-state-metrics
  namespace: perf-monitoring
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kube-state-metrics
  template:
    metadata:
      labels:
        app: kube-state-metrics
    spec:
      serviceAccountName: kube-state-metrics
      containers:
        - name: kube-state-metrics
          image: {{KUBE_STATE_METRICS_IMAGE}}
          args:
            - --port=8080
            - --telemetry-port=8081
          ports:
            - name: http-metrics
              containerPort: 8080
            - name: telemetry
              containerPort: 8081
---
apiVersion: v1
kind: Service
metadata:
  name: kube-state-metrics
  namespace: perf-monitoring
spec:
  selector:
    app: kube-state-metrics
  ports:
    - name: http-metrics
      port: 8080
      targetPort: 8080
```

- [ ] **Step 5: Add Prometheus scrape config**

In `observability/prometheus/prometheus.yaml`, add this scrape config under `scrape_configs`:

```yaml
      - job_name: kube-state-metrics
        static_configs:
          - targets:
              - kube-state-metrics.perf-monitoring.svc:8080
```

- [ ] **Step 6: Install kube-state-metrics in runner**

In `cmd/perf-runner/main.go`, after `images := mergeImages(...)` and before Prometheus install, add:

```go
if req.Requires.Observability.KubeStateMetrics.Required && req.Requires.Observability.KubeStateMetrics.Install {
	kubeStateMetricsImage, err := config.ResolveImage(images, req.Requires.Observability.KubeStateMetrics.ImageKey)
	if err != nil {
		return err
	}
	if err := kubestatemetrics.Install(ctx, filepath.Join(root, "observability", "kube-state-metrics", "kube-state-metrics.yaml"), kubeStateMetricsImage); err != nil {
		return err
	}
	if err := kubestatemetrics.WaitRollout(ctx, req.Requires.Observability.KubeStateMetrics); err != nil {
		return err
	}
}
```

- [ ] **Step 7: Run tests**

Run: `go test ./internal/kubestatemetrics ./cmd/perf-runner`

Expected: PASS after Task 2 parsing imports are in place.

- [ ] **Step 8: Approved commit command**

If commits are authorized, run:

```bash
git add internal/kubestatemetrics observability/kube-state-metrics/kube-state-metrics.yaml observability/prometheus/prometheus.yaml cmd/perf-runner/main.go
git commit -m "feat: install kube-state-metrics for benchmark suites"
```

---

### Task 4: Results PVC Artifact Copy

**Files:**
- Create: `internal/artifacts/artifacts.go`
- Create: `internal/artifacts/artifacts_test.go`
- Modify: `cmd/perf-runner/main.go`

**Interfaces:**
- Consumes: `runDir` from `runpkg.CreateRunDir` and `requires.artifacts` config.
- Produces: `artifacts.Config`, `artifacts.Copy(ctx context.Context, cfg Config, destination string) error`, and local artifacts under `filepath.Join(runDir, "artifacts")`.

- [ ] **Step 1: Write failing artifact package tests**

Create `internal/artifacts/artifacts_test.go`:

```go
package artifacts

import (
	"context"
	"strings"
	"testing"
)

func TestCopyPodManifestMountsConfiguredPVC(t *testing.T) {
	cfg := Config{Namespace: "kata-io", PVCName: "kata-io-results", MountPath: "/results", CopyImage: "mcr.microsoft.com/oss/busybox/busybox:1.36.1"}
	manifest := CopyPodManifest(cfg, "kata-io-artifact-copy")
	for _, want := range []string{
		"name: kata-io-artifact-copy",
		"namespace: kata-io",
		"image: mcr.microsoft.com/oss/busybox/busybox:1.36.1",
		"command: [/bin/sh, -c, sleep 3600]",
		"claimName: kata-io-results",
		"mountPath: /results",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("manifest missing %q:\n%s", want, manifest)
		}
	}
}

func TestCopyWithRunnerRunsApplyWaitCopyDelete(t *testing.T) {
	cfg := Config{Enabled: true, Namespace: "kata-io", PVCName: "kata-io-results", MountPath: "/results", CopyImage: "mcr.microsoft.com/oss/busybox/busybox:1.36.1"}
	calls := []string{}
	runner := func(ctx context.Context, stdin string, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	if err := CopyWithRunner(context.Background(), cfg, "/tmp/out", runner); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"apply -f -",
		"wait --for=condition=Ready pod/kata-io-artifact-copy -n kata-io --timeout=2m",
		"cp kata-io/kata-io-artifact-copy:/results/. /tmp/out",
		"delete pod kata-io-artifact-copy -n kata-io --ignore-not-found=true",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls[%d] = %q, want %q", i, calls[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/artifacts`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement artifact package**

Create `internal/artifacts/artifacts.go`:

```go
package artifacts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type Config struct {
	Enabled   bool   `yaml:"enabled"`
	Namespace string `yaml:"namespace"`
	PVCName   string `yaml:"pvcName"`
	MountPath string `yaml:"mountPath"`
	CopyImage string `yaml:"copyImage"`
}

type Runner func(ctx context.Context, stdin string, args ...string) error

func Copy(ctx context.Context, cfg Config, destination string) error {
	return CopyWithRunner(ctx, cfg, destination, kubectlRunner)
}

func CopyWithRunner(ctx context.Context, cfg Config, destination string, runner Runner) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Namespace == "" || cfg.PVCName == "" || cfg.MountPath == "" || cfg.CopyImage == "" {
		return fmt.Errorf("artifact namespace, pvcName, mountPath, and copyImage are required when artifacts are enabled")
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	podName := "kata-io-artifact-copy"
	manifest := CopyPodManifest(cfg, podName)
	if err := runner(ctx, manifest, "apply", "-f", "-"); err != nil {
		return err
	}
	defer func() { _ = runner(context.Background(), "", "delete", "pod", podName, "-n", cfg.Namespace, "--ignore-not-found=true") }()
	if err := runner(ctx, "", "wait", "--for=condition=Ready", "pod/"+podName, "-n", cfg.Namespace, "--timeout=2m"); err != nil {
		return err
	}
	return runner(ctx, "", "cp", cfg.Namespace+"/"+podName+":"+cfg.MountPath+"/.", filepath.Clean(destination))
}

func CopyPodManifest(cfg Config, podName string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: kata-io-artifact-copy
spec:
  restartPolicy: Never
  containers:
    - name: copy
      image: %s
      command: [/bin/sh, -c, sleep 3600]
      volumeMounts:
        - name: results
          mountPath: %s
  volumes:
    - name: results
      persistentVolumeClaim:
        claimName: %s
`, podName, cfg.Namespace, cfg.CopyImage, cfg.MountPath, cfg.PVCName)
}

func kubectlRunner(ctx context.Context, stdin string, args ...string) error {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	if stdin != "" {
		pipe, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		go func() {
			_, _ = pipe.Write([]byte(stdin))
			_ = pipe.Close()
		}()
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

- [ ] **Step 4: Wire artifact copy into runner**

In `cmd/perf-runner/main.go`, replace the direct kube-burner return:

```go
executeErr := runpkg.ExecuteKubeBurner(workloadPath, filepath.Join(runDir, "logs", "kube-burner.log"))
if req.Requires.Artifacts.Enabled {
	copyImage, err := config.ResolveImage(images, req.Requires.Artifacts.CopyImage)
	if err != nil {
		return err
	}
	req.Requires.Artifacts.CopyImage = copyImage
}
artifactErr := artifacts.Copy(ctx, req.Requires.Artifacts, filepath.Join(runDir, "artifacts"))
if executeErr != nil {
	if artifactErr != nil {
		return fmt.Errorf("kube-burner failed: %w; artifact copy also failed: %v", executeErr, artifactErr)
	}
	return executeErr
}
return artifactErr
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/artifacts ./cmd/perf-runner`

Expected: PASS after Task 2 parsing imports are in place.

- [ ] **Step 6: Approved commit command**

If commits are authorized, run:

```bash
git add internal/artifacts cmd/perf-runner/main.go
git commit -m "feat: copy benchmark artifacts from results pvc"
```

---

### Task 5: AKS Infrastructure Parameters For Kata Workload Runtime

**Files:**
- Modify: `infra/aks/main.bicep`
- Modify: `suites/kata-perf/infra.bicepparam` only if schema/parameter validation requires new explicit defaults; otherwise leave unchanged.
- Modify: `cmd/perf-runner/main.go`
- Modify: `cmd/perf-runner/main_test.go`

**Interfaces:**
- Consumes: existing suite `.bicepparam` files.
- Produces: Bicep parameters `userNodeOsSKU` and `userNodeWorkloadRuntime`, with `kata-io` setting `AzureLinux` and `KataVmIsolation`.

- [ ] **Step 1: Write failing Bicep content test**

Add this test to `cmd/perf-runner/main_test.go`:

```go
func TestInfraBicepSupportsKataWorkloadRuntimeParameters(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testSourceRoot, "infra", "aks", "main.bicep"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"param userNodeOsSKU string = 'Ubuntu'",
		"param userNodeWorkloadRuntime string = 'OCIContainer'",
		"osSKU: userNodeOsSKU",
		"workloadRuntime: userNodeWorkloadRuntime",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("main.bicep missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./cmd/perf-runner -run TestInfraBicepSupportsKataWorkloadRuntimeParameters`

Expected: FAIL because the Bicep parameters do not exist.

- [ ] **Step 3: Update AKS Bicep**

In `infra/aks/main.bicep`, add parameters near the existing user node parameters:

```bicep
param userNodeOsSKU string = 'Ubuntu'
param userNodeWorkloadRuntime string = 'OCIContainer'
```

In the user pool `agentPoolProfiles` object, add:

```bicep
osSKU: userNodeOsSKU
workloadRuntime: userNodeWorkloadRuntime
```

- [ ] **Step 4: Update generated suite defaults**

Update `infraBicepParam` in `cmd/perf-runner/main.go` so generated suites include explicit defaults:

```go
return fmt.Sprintf("using '../../infra/aks/main.bicep'\n\nparam clusterName = '%s'\nparam kubernetesVersion = '%s'\nparam userNodeCount = %d\nparam userNodeVmSize = '%s'\nparam userNodeOsSKU = 'Ubuntu'\nparam userNodeWorkloadRuntime = 'OCIContainer'\nparam userNodeLabels = {\n  'perf.azure.com/node-role': 'workload'\n}\n", opts.ClusterName, opts.KubernetesVersion, opts.NodeCount, opts.NodeVMSize)
```

Existing tests that look for `param userNodeCount = 1` should continue to pass.

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/perf-runner`

Expected: PASS.

- [ ] **Step 6: Approved commit command**

If commits are authorized, run:

```bash
git add infra/aks/main.bicep cmd/perf-runner/main.go cmd/perf-runner/main_test.go
git commit -m "feat: parameterize aks workload runtime"
```

---

### Task 6: Benchmark Image, fio Profiles, And Workload Scripts

**Files:**
- Create: `suites/kata-io/images/benchmark/Dockerfile`
- Create: `suites/kata-io/images/benchmark/scripts/run-fio.sh`
- Create: `suites/kata-io/images/benchmark/scripts/run-git-clone.sh`
- Create: `suites/kata-io/images/benchmark/fio-profiles/randread-4k.fio`
- Create: `suites/kata-io/images/benchmark/fio-profiles/randwrite-4k.fio`
- Create: `suites/kata-io/images/benchmark/fio-profiles/seqread.fio`
- Create: `suites/kata-io/images/benchmark/fio-profiles/seqwrite.fio`
- Create: `suites/kata-io/images/benchmark/fio-profiles/fsync-heavy.fio`
- Modify: `internal/examples/examples_test.go`

**Interfaces:**
- Consumes: env vars from Kubernetes job templates.
- Produces: benchmark image command scripts and raw artifact files under `$RESULTS_DIR/$RUN_ID/$SCENARIO/$SAMPLE_ID`.

- [ ] **Step 1: Add failing file-existence/content test**

Add this test to `internal/examples/examples_test.go`:

```go
func TestKataIOBenchmarkImageFilesExist(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{
		"suites/kata-io/images/benchmark/Dockerfile",
		"suites/kata-io/images/benchmark/scripts/run-fio.sh",
		"suites/kata-io/images/benchmark/scripts/run-git-clone.sh",
		"suites/kata-io/images/benchmark/fio-profiles/randread-4k.fio",
		"suites/kata-io/images/benchmark/fio-profiles/randwrite-4k.fio",
		"suites/kata-io/images/benchmark/fio-profiles/seqread.fio",
		"suites/kata-io/images/benchmark/fio-profiles/seqwrite.fio",
		"suites/kata-io/images/benchmark/fio-profiles/fsync-heavy.fio",
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("%s missing: %v", file, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s is empty", file)
		}
	}
}
```

- [ ] **Step 2: Run test to verify failure**

Run: `go test ./internal/examples -run TestKataIOBenchmarkImageFilesExist`

Expected: FAIL because the files do not exist.

- [ ] **Step 3: Create benchmark Dockerfile**

Create `suites/kata-io/images/benchmark/Dockerfile`:

```dockerfile
FROM ubuntu:24.04

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       bash \
       ca-certificates \
       coreutils \
       curl \
       fio \
       git \
       jq \
       time \
    && rm -rf /var/lib/apt/lists/*

COPY scripts/run-fio.sh /usr/local/bin/run-fio.sh
COPY scripts/run-git-clone.sh /usr/local/bin/run-git-clone.sh
COPY fio-profiles /profiles

RUN chmod +x /usr/local/bin/run-fio.sh /usr/local/bin/run-git-clone.sh
```

- [ ] **Step 4: Create `run-fio.sh`**

Create `suites/kata-io/images/benchmark/scripts/run-fio.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

RUN_ID="${RUN_ID:-manual}"
SCENARIO="${SCENARIO:-fio}"
SAMPLE_ID="${SAMPLE_ID:-${HOSTNAME:-sample}}"
FIO_PROFILE="${FIO_PROFILE:?FIO_PROFILE is required}"
WORK_DIR="${WORK_DIR:-/work}"
RESULTS_DIR="${RESULTS_DIR:-/results}"
OUT_DIR="${RESULTS_DIR}/${RUN_ID}/${SCENARIO}/${SAMPLE_ID}"
SAMPLE_WORK_DIR="${WORK_DIR}/${RUN_ID}/${SCENARIO}/${SAMPLE_ID}"

mkdir -p "$OUT_DIR" "$SAMPLE_WORK_DIR"

start_ns="$(date +%s%N)"
start_epoch="$(date +%s)"
cat /proc/self/io > "$OUT_DIR/proc-self-io-before.txt" || true
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-before.txt" || true

set +e
/usr/bin/time -v -o "$OUT_DIR/time.txt" \
  fio "$FIO_PROFILE" --directory="$SAMPLE_WORK_DIR" --output-format=json --output="$OUT_DIR/fio.json" \
  > "$OUT_DIR/stdout.log" 2> "$OUT_DIR/stderr.log"
exit_code="$?"
set -e

end_ns="$(date +%s%N)"
end_epoch="$(date +%s)"
duration_ns="$((end_ns - start_ns))"
duration_seconds="$(awk "BEGIN { print ${duration_ns} / 1000000000 }")"

cat /proc/self/io > "$OUT_DIR/proc-self-io-after.txt" || true
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-after.txt" || true

read_iops="0"
write_iops="0"
read_bw_bytes="0"
write_bw_bytes="0"
read_clat_p99_ns="0"
write_clat_p99_ns="0"
if [ -s "$OUT_DIR/fio.json" ]; then
  read_iops="$(jq '[.jobs[].read.iops // 0] | add' "$OUT_DIR/fio.json")"
  write_iops="$(jq '[.jobs[].write.iops // 0] | add' "$OUT_DIR/fio.json")"
  read_bw_bytes="$(jq '[.jobs[].read.bw_bytes // 0] | add' "$OUT_DIR/fio.json")"
  write_bw_bytes="$(jq '[.jobs[].write.bw_bytes // 0] | add' "$OUT_DIR/fio.json")"
  read_clat_p99_ns="$(jq '[.jobs[].read.clat_ns.percentile."99.000000" // 0] | max' "$OUT_DIR/fio.json")"
  write_clat_p99_ns="$(jq '[.jobs[].write.clat_ns.percentile."99.000000" // 0] | max' "$OUT_DIR/fio.json")"
fi

cat > "$OUT_DIR/summary.prom" <<EOF
# TYPE fio_duration_seconds gauge
fio_duration_seconds{run_id="$RUN_ID",scenario="$SCENARIO"} $duration_seconds
# TYPE fio_exit_code gauge
fio_exit_code{run_id="$RUN_ID",scenario="$SCENARIO"} $exit_code
# TYPE fio_start_time_seconds gauge
fio_start_time_seconds{run_id="$RUN_ID",scenario="$SCENARIO"} $start_epoch
# TYPE fio_end_time_seconds gauge
fio_end_time_seconds{run_id="$RUN_ID",scenario="$SCENARIO"} $end_epoch
# TYPE fio_read_iops gauge
fio_read_iops{run_id="$RUN_ID",scenario="$SCENARIO"} $read_iops
# TYPE fio_write_iops gauge
fio_write_iops{run_id="$RUN_ID",scenario="$SCENARIO"} $write_iops
# TYPE fio_read_bw_bytes_per_second gauge
fio_read_bw_bytes_per_second{run_id="$RUN_ID",scenario="$SCENARIO"} $read_bw_bytes
# TYPE fio_write_bw_bytes_per_second gauge
fio_write_bw_bytes_per_second{run_id="$RUN_ID",scenario="$SCENARIO"} $write_bw_bytes
# TYPE fio_read_clat_p99_nanoseconds gauge
fio_read_clat_p99_nanoseconds{run_id="$RUN_ID",scenario="$SCENARIO"} $read_clat_p99_ns
# TYPE fio_write_clat_p99_nanoseconds gauge
fio_write_clat_p99_nanoseconds{run_id="$RUN_ID",scenario="$SCENARIO"} $write_clat_p99_ns
EOF

cat "$OUT_DIR/summary.prom"
exit "$exit_code"
```

- [ ] **Step 5: Create `run-git-clone.sh`**

Create `suites/kata-io/images/benchmark/scripts/run-git-clone.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

RUN_ID="${RUN_ID:-manual}"
SCENARIO="${SCENARIO:-git-clone}"
SAMPLE_ID="${SAMPLE_ID:-${HOSTNAME:-sample}}"
REPO_URL="${REPO_URL:?REPO_URL is required}"
CLONE_MODE="${CLONE_MODE:-full}"
WORK_DIR="${WORK_DIR:-/work}"
RESULTS_DIR="${RESULTS_DIR:-/results}"
OUT_DIR="${RESULTS_DIR}/${RUN_ID}/${SCENARIO}/${SAMPLE_ID}"
SAMPLE_WORK_DIR="${WORK_DIR}/${RUN_ID}/${SCENARIO}/${SAMPLE_ID}"
TARGET_DIR="${TARGET_DIR:-${SAMPLE_WORK_DIR}/repo}"

mkdir -p "$OUT_DIR" "$SAMPLE_WORK_DIR"
rm -rf "$TARGET_DIR"

export GIT_TERMINAL_PROMPT=0
export GIT_TRACE2_EVENT="$OUT_DIR/git-trace2-event.json"
export GIT_TRACE2_PERF="$OUT_DIR/git-trace2-perf.log"

case "$CLONE_MODE" in
  full)
    CLONE_ARGS=()
    ;;
  blobless)
    CLONE_ARGS=(--filter=blob:none)
    ;;
  *)
    echo "unknown CLONE_MODE=$CLONE_MODE" >&2
    exit 2
    ;;
esac

start_ns="$(date +%s%N)"
start_epoch="$(date +%s)"
cat /proc/self/io > "$OUT_DIR/proc-self-io-before.txt" || true
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-before.txt" || true

set +e
/usr/bin/time -v -o "$OUT_DIR/time.txt" \
  git clone "${CLONE_ARGS[@]}" "$REPO_URL" "$TARGET_DIR" \
  > "$OUT_DIR/git-stdout.log" 2> "$OUT_DIR/git-stderr.log"
exit_code="$?"
set -e

end_ns="$(date +%s%N)"
end_epoch="$(date +%s)"
duration_ns="$((end_ns - start_ns))"
duration_seconds="$(awk "BEGIN { print ${duration_ns} / 1000000000 }")"

cat /proc/self/io > "$OUT_DIR/proc-self-io-after.txt" || true
du -sb "$TARGET_DIR" > "$OUT_DIR/repo-size-bytes.txt" 2>/dev/null || printf '0\t%s\n' "$TARGET_DIR" > "$OUT_DIR/repo-size-bytes.txt"
find "$TARGET_DIR" -xdev -type f 2>/dev/null | wc -l > "$OUT_DIR/file-count.txt" || echo 0 > "$OUT_DIR/file-count.txt"
df -h "$SAMPLE_WORK_DIR" > "$OUT_DIR/df-after.txt" || true

repo_size_bytes="$(awk '{print $1}' "$OUT_DIR/repo-size-bytes.txt")"
file_count="$(tr -d ' ' < "$OUT_DIR/file-count.txt")"

cat > "$OUT_DIR/summary.prom" <<EOF
# TYPE git_clone_duration_seconds gauge
git_clone_duration_seconds{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $duration_seconds
# TYPE git_clone_exit_code gauge
git_clone_exit_code{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $exit_code
# TYPE git_clone_start_time_seconds gauge
git_clone_start_time_seconds{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $start_epoch
# TYPE git_clone_end_time_seconds gauge
git_clone_end_time_seconds{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $end_epoch
# TYPE git_clone_repo_size_bytes gauge
git_clone_repo_size_bytes{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $repo_size_bytes
# TYPE git_clone_file_count gauge
git_clone_file_count{run_id="$RUN_ID",scenario="$SCENARIO",clone_mode="$CLONE_MODE"} $file_count
EOF

cat "$OUT_DIR/summary.prom"
exit "$exit_code"
```

- [ ] **Step 6: Create fio profiles**

Create `suites/kata-io/images/benchmark/fio-profiles/randread-4k.fio`:

```ini
[global]
ioengine=libaio
direct=1
time_based=1
runtime=60
size=1G
bs=4k
iodepth=32
numjobs=4
group_reporting=1

[randread-4k]
rw=randread
```

Create `suites/kata-io/images/benchmark/fio-profiles/randwrite-4k.fio`:

```ini
[global]
ioengine=libaio
direct=1
time_based=1
runtime=60
size=1G
bs=4k
iodepth=32
numjobs=4
group_reporting=1

[randwrite-4k]
rw=randwrite
```

Create `suites/kata-io/images/benchmark/fio-profiles/seqread.fio`:

```ini
[global]
ioengine=libaio
direct=1
time_based=1
runtime=60
size=2G
bs=1M
iodepth=16
numjobs=2
group_reporting=1

[seqread]
rw=read
```

Create `suites/kata-io/images/benchmark/fio-profiles/seqwrite.fio`:

```ini
[global]
ioengine=libaio
direct=1
time_based=1
runtime=60
size=2G
bs=1M
iodepth=16
numjobs=2
group_reporting=1

[seqwrite]
rw=write
```

Create `suites/kata-io/images/benchmark/fio-profiles/fsync-heavy.fio`:

```ini
[global]
ioengine=sync
direct=0
time_based=1
runtime=60
size=512M
bs=4k
numjobs=4
fsync=1
group_reporting=1

[fsync-heavy]
rw=write
```

- [ ] **Step 7: Run examples test**

Run: `go test ./internal/examples -run TestKataIOBenchmarkImageFilesExist`

Expected: PASS.

- [ ] **Step 8: Approved commit command**

If commits are authorized, run:

```bash
git add suites/kata-io/images internal/examples/examples_test.go
git commit -m "feat: add kata io benchmark image scripts"
```

---

### Task 7: `kata-io` Suite Manifests, Modes, Metrics, And Templates

**Files:**
- Create: `suites/kata-io/suite.yml`
- Create: `suites/kata-io/requirements.yml`
- Create: `suites/kata-io/infra.bicepparam`
- Create: `suites/kata-io/workload.yml`
- Create: `suites/kata-io/workload-smoke.yml`
- Create: `suites/kata-io/workload-full.yml`
- Create: `suites/kata-io/metrics.yml`
- Create: `suites/kata-io/templates/namespace.yml`
- Create: `suites/kata-io/templates/fio-emptydir-standard-job.yml`
- Create: `suites/kata-io/templates/fio-emptydir-kata-job.yml`
- Create: `suites/kata-io/templates/fio-pvc-standard-job.yml`
- Create: `suites/kata-io/templates/fio-pvc-kata-job.yml`
- Create: `suites/kata-io/templates/git-emptydir-standard-job.yml`
- Create: `suites/kata-io/templates/git-emptydir-kata-job.yml`
- Create: `suites/kata-io/templates/git-pvc-standard-job.yml`
- Create: `suites/kata-io/templates/git-pvc-kata-job.yml`
- Create: `suites/kata-io/templates/work-pvc.yml`
- Create: `suites/kata-io/templates/results-pvc.yml`
- Create: `suites/kata-io/vars/smoke.yml`
- Create: `suites/kata-io/vars/full.yml`
- Modify: `internal/examples/examples_test.go`

**Interfaces:**
- Consumes: benchmark image key `benchmark`, mode `workloadFile`, template vars, and artifact config.
- Produces: schema-valid suite that renders standard and Kata fio/Git jobs with all three storage backends in full mode.

- [ ] **Step 1: Add failing contract validation tests**

Add this test to `internal/examples/examples_test.go`:

```go
func TestKataIOContractsValidate(t *testing.T) {
	root := filepath.Join("..", "..")
	cases := []struct {
		schema string
		file   string
	}{
		{"schemas/suite.schema.json", "suites/kata-io/suite.yml"},
		{"schemas/requirements.schema.json", "suites/kata-io/requirements.yml"},
		{"schemas/mode.schema.json", "suites/kata-io/vars/smoke.yml"},
		{"schemas/mode.schema.json", "suites/kata-io/vars/full.yml"},
	}
	for _, tc := range cases {
		if err := config.ValidateYAML(filepath.Join(root, tc.schema), filepath.Join(root, tc.file)); err != nil {
			t.Fatalf("%s failed validation against %s: %v", tc.file, tc.schema, err)
		}
	}
}

func TestKataIOFullWorkloadCoversRequiredScenarios(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", "workload-full.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	runtimes := []string{"standard", "kata"}
	storages := []string{"emptydir", "azure-disk", "azure-files"}
	profiles := []string{"randread-4k", "randwrite-4k", "seqread", "seqwrite", "fsync-heavy"}
	cloneModes := []string{"full", "blobless"}
	concurrencies := []string{"1", "10"}
	for _, runtime := range runtimes {
		for _, storage := range storages {
			for _, concurrency := range concurrencies {
				for _, profile := range profiles {
					scenario := "runtime-" + runtime + "-storage-" + storage + "-fio-" + profile + "-concurrency-" + concurrency
					if !strings.Contains(text, scenario) {
						t.Fatalf("workload-full.yml missing scenario %q", scenario)
					}
				}
				for _, cloneMode := range cloneModes {
					scenario := "runtime-" + runtime + "-storage-" + storage + "-git-" + cloneMode + "-concurrency-" + concurrency
					if !strings.Contains(text, scenario) {
						t.Fatalf("workload-full.yml missing scenario %q", scenario)
					}
				}
			}
		}
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/examples -run 'TestKataIOContractsValidate|TestKataIOFullWorkloadCoversRequiredScenarios'`

Expected: FAIL because the suite files do not exist.

- [ ] **Step 3: Create suite metadata**

Create `suites/kata-io/suite.yml`:

```yaml
name: kata-io
description: Exploratory AKS Kata Pod Sandboxing I/O and Git clone benchmark suite.
tests:
  - kata-io-smoke
  - kata-io-full
```

- [ ] **Step 4: Create requirements**

Create `suites/kata-io/requirements.yml`:

```yaml
suite: kata-io
requires:
  infrastructure:
    provider: aks
    bicep:
      template: infra/aks/main.bicep
      parameters: suites/kata-io/infra.bicepparam
  kubernetes:
    minVersion: "1.36"
  nodeSelectors:
    - name: workload
      required: true
      minNodes: 1
      labels:
        perf.azure.com/node-role: workload
  images:
    registry:
      nameParameter: containerRegistryName
    builds:
      - key: benchmark
        repository: kata-io/benchmark
        context: images/benchmark
        dockerfile: Dockerfile
        platform: linux/amd64
        timeoutSeconds: 3600
  observability:
    prometheus:
      required: true
      install: true
      namespace: perf-monitoring
      imageKey: prometheus
      serviceName: prometheus
      servicePort: 9090
      localPort: 9090
      requiredMetrics:
        - container_cpu_usage_seconds_total
        - container_memory_working_set_bytes
        - container_fs_reads_bytes_total
        - container_fs_writes_bytes_total
        - container_network_receive_bytes_total
        - container_network_transmit_bytes_total
    kubeStateMetrics:
      required: true
      install: true
      namespace: perf-monitoring
      imageKey: kube-state-metrics
      serviceName: kube-state-metrics
      servicePort: 8080
      requiredMetrics:
        - kube_pod_created
        - kube_pod_status_scheduled_time
        - kube_pod_status_initialized_time
        - kube_pod_container_state_started
        - kube_pod_status_ready_time
        - kube_pod_runtimeclass_name_info
  artifacts:
    enabled: true
    namespace: kata-io
    pvcName: kata-io-results
    mountPath: /results
    copyImage: artifact-copy
```

- [ ] **Step 5: Create infra params**

Create `suites/kata-io/infra.bicepparam`:

```bicep
using '../../infra/aks/main.bicep'

param clusterName = 'akskataio'
param kubernetesVersion = ''
param userNodeCount = 3
param userNodeVmSize = 'Standard_D8s_v5'
param userNodeOsSKU = 'AzureLinux'
param userNodeWorkloadRuntime = 'KataVmIsolation'
param userNodeLabels = {
  'perf.azure.com/node-role': 'workload'
}
```

- [ ] **Step 6: Create mode files**

Create `suites/kata-io/vars/smoke.yml`:

```yaml
iterations: 1
iterationsPerNamespace: 1
qps: 1
burst: 1
cleanup: true
waitWhenFinished: true
preLoadImages: true
workloadFile: workload-smoke.yml
templateVars:
  namespace: kata-io
  runID: kata-io-smoke
  repoURL: https://github.com/kubernetes/kubernetes
  kataRuntimeClassName: kata-vm-isolation
  azureDiskStorageClass: managed-csi
  azureFilesStorageClass: azurefile-csi
  resultsStorageClass: azurefile-csi
  workVolumeSize: 128Gi
  resultsVolumeSize: 128Gi
imageVars:
  benchmarkImage: benchmark
```

Create `suites/kata-io/vars/full.yml`:

```yaml
iterations: 1
iterationsPerNamespace: 1
qps: 1
burst: 1
cleanup: true
waitWhenFinished: true
preLoadImages: true
workloadFile: workload-full.yml
templateVars:
  namespace: kata-io
  runID: kata-io-full
  repoURL: https://github.com/kubernetes/kubernetes
  kataRuntimeClassName: kata-vm-isolation
  azureDiskStorageClass: managed-csi
  azureFilesStorageClass: azurefile-csi
  resultsStorageClass: azurefile-csi
  workVolumeSize: 128Gi
  resultsVolumeSize: 128Gi
imageVars:
  benchmarkImage: benchmark
```

- [ ] **Step 7: Create PVC templates**

Create `suites/kata-io/templates/namespace.yml`:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: {{.namespace}}
  labels:
    app: kata-io
    benchmark: io
```

Create `suites/kata-io/templates/results-pvc.yml`:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: kata-io-results
  namespace: {{.namespace}}
  labels:
    app: kata-io
    benchmark: io
    storage-type: results
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: {{.resultsStorageClass}}
  resources:
    requests:
      storage: {{.resultsVolumeSize}}
```

Create `suites/kata-io/templates/work-pvc.yml`. The PVC name includes `{{.jobName}}` and `{{.Iteration}}` so concurrency-10 Azure Disk and Azure Files jobs each mount their own work volume:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{.jobName}}-work-{{.Iteration}}
  namespace: {{.namespace}}
  labels:
    app: kata-io
    benchmark: io
    storage-type: {{.storageType}}
spec:
  accessModes:
    - {{.workAccessMode}}
  storageClassName: {{.workStorageClass}}
  resources:
    requests:
      storage: {{.workVolumeSize}}
```

- [ ] **Step 8: Create job templates**

Create these eight template files so standard/Kata and `emptyDir`/PVC differences stay explicit and Kubernetes manifests do not need unsupported conditional rendering:

```text
suites/kata-io/templates/fio-emptydir-standard-job.yml
suites/kata-io/templates/fio-emptydir-kata-job.yml
suites/kata-io/templates/fio-pvc-standard-job.yml
suites/kata-io/templates/fio-pvc-kata-job.yml
suites/kata-io/templates/git-emptydir-standard-job.yml
suites/kata-io/templates/git-emptydir-kata-job.yml
suites/kata-io/templates/git-pvc-standard-job.yml
suites/kata-io/templates/git-pvc-kata-job.yml
```

Use this complete content for `suites/kata-io/templates/fio-pvc-standard-job.yml`:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: {{.jobName}}-{{.Iteration}}
  namespace: {{.namespace}}
  labels:
    app: kata-io
    benchmark: io
    runtime: {{.runtime}}
    storage-type: {{.storageType}}
    workload-type: fio
    profile: {{.fioProfileName}}
    concurrency: "{{.concurrency}}"
  annotations:
    scenario: {{.scenario}}
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app: kata-io
        benchmark: io
        runtime: {{.runtime}}
        storage-type: {{.storageType}}
        workload-type: fio
        profile: {{.fioProfileName}}
        concurrency: "{{.concurrency}}"
      annotations:
        scenario: {{.scenario}}
    spec:
      restartPolicy: Never
      nodeSelector:
        perf.azure.com/node-role: workload
      containers:
        - name: fio
          image: {{.benchmarkImage}}
          imagePullPolicy: IfNotPresent
          command: [/usr/local/bin/run-fio.sh]
          env:
            - name: RUN_ID
              value: {{.runID}}
            - name: SCENARIO
              value: {{.scenario}}
            - name: SAMPLE_ID
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: FIO_PROFILE
              value: {{.fioProfile}}
            - name: FIO_PROFILE_NAME
              value: {{.fioProfileName}}
            - name: WORK_DIR
              value: /work
            - name: RESULTS_DIR
              value: /results
          resources:
            requests:
              cpu: "4"
              memory: 8Gi
            limits:
              cpu: "4"
              memory: 8Gi
          volumeMounts:
            - name: work
              mountPath: /work
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

Create `suites/kata-io/templates/fio-pvc-kata-job.yml` by copying `fio-pvc-standard-job.yml` and adding this line under `spec.template.spec`, aligned with `restartPolicy`:

```yaml
      runtimeClassName: {{.kataRuntimeClassName}}
```

Create `suites/kata-io/templates/fio-emptydir-standard-job.yml` by copying `fio-pvc-standard-job.yml` and replacing the `work` volume with:

```yaml
        - name: work
          emptyDir:
            sizeLimit: {{.workVolumeSize}}
```

Create `suites/kata-io/templates/fio-emptydir-kata-job.yml` by copying `fio-emptydir-standard-job.yml` and adding this line under `spec.template.spec`, aligned with `restartPolicy`:

```yaml
      runtimeClassName: {{.kataRuntimeClassName}}
```

Use the same metadata, resources, and volume contract for Git templates. The container block in all four Git templates must be:

Git template labels must use short bounded values only:

```yaml
    runtime: {{.runtime}}
    storage-type: {{.storageType}}
    workload-type: git
    clone-mode: {{.cloneMode}}
    concurrency: "{{.concurrency}}"
```

The full scenario name must be stored as an annotation:

```yaml
  annotations:
    scenario: {{.scenario}}
```

The pod template metadata must repeat the same short labels and scenario annotation.

```yaml
        - name: git-clone
          image: {{.benchmarkImage}}
          imagePullPolicy: IfNotPresent
          command: [/usr/local/bin/run-git-clone.sh]
          env:
            - name: RUN_ID
              value: {{.runID}}
            - name: SCENARIO
              value: {{.scenario}}
            - name: SAMPLE_ID
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: REPO_URL
              value: {{.repoURL}}
            - name: CLONE_MODE
              value: {{.cloneMode}}
            - name: WORK_DIR
              value: /work
            - name: RESULTS_DIR
              value: /results
```

Create `git-pvc-standard-job.yml` with a PVC `work` volume, `git-pvc-kata-job.yml` with the same PVC volume plus `runtimeClassName`, `git-emptydir-standard-job.yml` with `emptyDir`, and `git-emptydir-kata-job.yml` with `emptyDir` plus `runtimeClassName`. Do not render `runtimeClassName` in any standard template.

- [ ] **Step 9: Create workload files**

Create `suites/kata-io/workload.yml` as a smoke-compatible default:

```yaml
global:
  measurements:
    - name: podLatency
jobs:
  - name: kio-namespace
    jobType: create
    namespace: default
    jobIterations: 1
    qps: 1
    burst: 1
    cleanup: false
    waitWhenFinished: true
    preLoadImages: true
    objects:
      - objectTemplate: templates/namespace.yml
        replicas: 1
        inputVars: {}
  - name: kio-results-pvc
    jobType: create
    namespace: kata-io
    jobIterations: 1
    qps: 1
    burst: 1
    cleanup: false
    waitWhenFinished: true
    preLoadImages: true
    objects:
      - objectTemplate: templates/results-pvc.yml
        replicas: 1
        inputVars: {}
```

Create `suites/kata-io/workload-smoke.yml` with this structure:

```yaml
global:
  measurements:
    - name: podLatency
jobs:
  - name: kio-namespace
    jobType: create
    namespace: default
    jobIterations: 1
    qps: 1
    burst: 1
    cleanup: false
    waitWhenFinished: true
    objects:
      - objectTemplate: templates/namespace.yml
        replicas: 1
        inputVars: {}
  - name: kio-results-pvc
    jobType: create
    namespace: kata-io
    jobIterations: 1
    qps: 1
    burst: 1
    cleanup: false
    waitWhenFinished: true
    objects:
      - objectTemplate: templates/results-pvc.yml
        replicas: 1
        inputVars: {}
  - name: kio-smoke-1
    jobType: create
    namespace: kata-io
    jobIterations: 1
    qps: 1
    burst: 1
    cleanup: true
    waitWhenFinished: true
    preLoadImages: true
    objects:
      - objectTemplate: templates/fio-emptydir-standard-job.yml
        replicas: 1
        inputVars:
          jobName: standard-emptydir-fio-randread
          scenario: runtime-standard-storage-emptydir-fio-randread-4k-concurrency-1
          runtime: runtime-standard
          storageType: storage-emptydir
          workloadType: fio
          fioProfile: /profiles/randread-4k.fio
          fioProfileName: randread-4k
          concurrency: "1"
  - name: kio-smoke-2
    jobType: create
    namespace: kata-io
    jobIterations: 1
    qps: 1
    burst: 1
    cleanup: true
    waitWhenFinished: true
    preLoadImages: true
    objects:
      - objectTemplate: templates/fio-emptydir-kata-job.yml
        replicas: 1
        inputVars:
          jobName: kata-emptydir-fio-randread
          scenario: runtime-kata-storage-emptydir-fio-randread-4k-concurrency-1
          runtime: runtime-kata
          storageType: storage-emptydir
          workloadType: fio
          fioProfile: /profiles/randread-4k.fio
          fioProfileName: randread-4k
          concurrency: "1"
  - name: kio-smoke-3
    jobType: create
    namespace: kata-io
    jobIterations: 1
    qps: 1
    burst: 1
    cleanup: true
    waitWhenFinished: true
    preLoadImages: true
    objects:
      - objectTemplate: templates/git-emptydir-standard-job.yml
        replicas: 1
        inputVars:
          jobName: standard-emptydir-git-blobless
          scenario: runtime-standard-storage-emptydir-git-blobless-concurrency-1
          runtime: runtime-standard
          storageType: storage-emptydir
          workloadType: git
          cloneMode: blobless
          concurrency: "1"
  - name: kio-smoke-4
    jobType: create
    namespace: kata-io
    jobIterations: 1
    qps: 1
    burst: 1
    cleanup: true
    waitWhenFinished: true
    preLoadImages: true
    objects:
      - objectTemplate: templates/git-emptydir-kata-job.yml
        replicas: 1
        inputVars:
          jobName: kata-emptydir-git-blobless
          scenario: runtime-kata-storage-emptydir-git-blobless-concurrency-1
          runtime: runtime-kata
          storageType: storage-emptydir
          workloadType: git
          cloneMode: blobless
          concurrency: "1"
```

Create `suites/kata-io/workload-full.yml` by expanding explicit scenario jobs covering:

```text
runtime-standard, runtime-kata
storage-emptydir, storage-azure-disk, storage-azure-files
fio-randread-4k, fio-randwrite-4k, fio-seqread, fio-seqwrite, fio-fsync-heavy
git-full, git-blobless
concurrency-1, concurrency-10
```

Use `jobIterations: 1, qps: 1, burst: 1` for `concurrency-1` jobs and `jobIterations: 10, qps: 10, burst: 10` for `concurrency-10` jobs.

To avoid omissions while still checking in plain YAML, generate the first draft with this temporary shell loop, then inspect and keep the resulting `workload-full.yml`:

```bash
tmp="suites/kata-io/workload-full.yml"
printf '%s\n' 'global:' '  measurements:' '    - name: podLatency' 'jobs:' > "$tmp"
idx=0
printf '  - name: kio-namespace\n    jobType: create\n    namespace: default\n    jobIterations: 1\n    qps: 1\n    burst: 1\n    cleanup: false\n    waitWhenFinished: true\n    objects:\n      - objectTemplate: templates/namespace.yml\n        replicas: 1\n        inputVars: {}\n' >> "$tmp"
for setup in results-pvc; do
  template="templates/${setup}.yml"
  name="kata-io-${setup}"
  printf '  - name: %s\n    jobType: create\n    namespace: kata-io\n    jobIterations: 1\n    qps: 1\n    burst: 1\n    cleanup: false\n    waitWhenFinished: true\n    objects:\n      - objectTemplate: %s\n        replicas: 1\n        inputVars: {}\n' "$name" "$template" >> "$tmp"
done
for runtime in standard kata; do
  for storage in emptydir azure-disk azure-files; do
    for concurrency in 1 10; do
      if [ "$concurrency" = 10 ]; then iterations=10; else iterations=1; fi
      if [ "$storage" = emptydir ]; then storage_template="emptydir"; work_storage_class=""; work_access_mode=""; elif [ "$storage" = azure-disk ]; then storage_template="pvc"; work_storage_class="managed-csi"; work_access_mode="ReadWriteOnce"; else storage_template="pvc"; work_storage_class="azurefile-csi"; work_access_mode="ReadWriteMany"; fi
      for profile in randread-4k randwrite-4k seqread seqwrite fsync-heavy; do
        scenario="runtime-${runtime}-storage-${storage}-fio-${profile}-concurrency-${concurrency}"
        template="templates/fio-${storage_template}-${runtime}-job.yml"
        idx=$((idx + 1))
        short_name="kio-${idx}"
        if [ "$storage" != emptydir ]; then
          printf '  - name: %s\n    jobType: create\n    namespace: kata-io\n    jobIterations: %s\n    qps: %s\n    burst: %s\n    cleanup: true\n    waitWhenFinished: true\n    preLoadImages: true\n    objects:\n      - objectTemplate: templates/work-pvc.yml\n        replicas: 1\n        inputVars:\n          jobName: %s\n          storageType: storage-%s\n          workStorageClass: %s\n          workAccessMode: %s\n      - objectTemplate: %s\n        replicas: 1\n        inputVars:\n          jobName: %s\n          scenario: %s\n          runtime: runtime-%s\n          storageType: storage-%s\n          workloadType: fio\n          fioProfile: /profiles/%s.fio\n          fioProfileName: %s\n          concurrency: "%s"\n' "$short_name" "$iterations" "$iterations" "$iterations" "$short_name" "$storage" "$work_storage_class" "$work_access_mode" "$template" "$short_name" "$scenario" "$runtime" "$storage" "$profile" "$profile" "$concurrency" >> "$tmp"
        else
          printf '  - name: %s\n    jobType: create\n    namespace: kata-io\n    jobIterations: %s\n    qps: %s\n    burst: %s\n    cleanup: true\n    waitWhenFinished: true\n    preLoadImages: true\n    objects:\n      - objectTemplate: %s\n        replicas: 1\n        inputVars:\n          jobName: %s\n          scenario: %s\n          runtime: runtime-%s\n          storageType: storage-%s\n          workloadType: fio\n          fioProfile: /profiles/%s.fio\n          fioProfileName: %s\n          concurrency: "%s"\n' "$short_name" "$iterations" "$iterations" "$iterations" "$template" "$short_name" "$scenario" "$runtime" "$storage" "$profile" "$profile" "$concurrency" >> "$tmp"
        fi
      done
      for clone_mode in full blobless; do
        scenario="runtime-${runtime}-storage-${storage}-git-${clone_mode}-concurrency-${concurrency}"
        template="templates/git-${storage_template}-${runtime}-job.yml"
        idx=$((idx + 1))
        short_name="kio-${idx}"
        if [ "$storage" != emptydir ]; then
          printf '  - name: %s\n    jobType: create\n    namespace: kata-io\n    jobIterations: %s\n    qps: %s\n    burst: %s\n    cleanup: true\n    waitWhenFinished: true\n    preLoadImages: true\n    objects:\n      - objectTemplate: templates/work-pvc.yml\n        replicas: 1\n        inputVars:\n          jobName: %s\n          storageType: storage-%s\n          workStorageClass: %s\n          workAccessMode: %s\n      - objectTemplate: %s\n        replicas: 1\n        inputVars:\n          jobName: %s\n          scenario: %s\n          runtime: runtime-%s\n          storageType: storage-%s\n          workloadType: git\n          cloneMode: %s\n          concurrency: "%s"\n' "$short_name" "$iterations" "$iterations" "$iterations" "$short_name" "$storage" "$work_storage_class" "$work_access_mode" "$template" "$short_name" "$scenario" "$runtime" "$storage" "$clone_mode" "$concurrency" >> "$tmp"
        else
          printf '  - name: %s\n    jobType: create\n    namespace: kata-io\n    jobIterations: %s\n    qps: %s\n    burst: %s\n    cleanup: true\n    waitWhenFinished: true\n    preLoadImages: true\n    objects:\n      - objectTemplate: %s\n        replicas: 1\n        inputVars:\n          jobName: %s\n          scenario: %s\n          runtime: runtime-%s\n          storageType: storage-%s\n          workloadType: git\n          cloneMode: %s\n          concurrency: "%s"\n' "$short_name" "$iterations" "$iterations" "$iterations" "$template" "$short_name" "$scenario" "$runtime" "$storage" "$clone_mode" "$concurrency" >> "$tmp"
        fi
      done
    done
  done
done
```

After generation, open `workload-full.yml` and verify no template path contains `standard` with `runtimeClassName`, and every `runtime-kata` scenario uses a `*-kata-job.yml` template.

- [ ] **Step 10: Create metrics profile**

Create `suites/kata-io/metrics.yml`:

```yaml
- query: sum(rate(container_cpu_usage_seconds_total[2m])) by (pod, namespace)
  metricName: podCPUUsage
- query: sum(container_memory_working_set_bytes) by (pod, namespace)
  metricName: podMemoryWorkingSet
- query: sum(increase(container_fs_reads_bytes_total[{{ .elapsed }}s])) by (namespace, pod)
  metricName: containerFsReadBytes
- query: sum(increase(container_fs_writes_bytes_total[{{ .elapsed }}s])) by (namespace, pod)
  metricName: containerFsWriteBytes
- query: sum(increase(container_fs_io_time_seconds_total[{{ .elapsed }}s])) by (namespace, pod)
  metricName: containerFsIoTime
- query: sum(increase(container_network_receive_bytes_total[{{ .elapsed }}s])) by (namespace, pod)
  metricName: containerNetworkReceiveBytes
- query: sum(increase(container_network_transmit_bytes_total[{{ .elapsed }}s])) by (namespace, pod)
  metricName: containerNetworkTransmitBytes
- query: sum(increase(node_disk_read_bytes_total[{{ .elapsed }}s])) by (instance, device)
  metricName: nodeDiskReadBytes
- query: sum(increase(node_disk_written_bytes_total[{{ .elapsed }}s])) by (instance, device)
  metricName: nodeDiskWrittenBytes
- query: sum(increase(node_disk_read_time_seconds_total[{{ .elapsed }}s])) by (instance, device)
  metricName: nodeDiskReadTime
- query: sum(increase(node_disk_write_time_seconds_total[{{ .elapsed }}s])) by (instance, device)
  metricName: nodeDiskWriteTime
- query: sum(increase(node_disk_io_time_seconds_total[{{ .elapsed }}s])) by (instance, device)
  metricName: nodeDiskIoTime
- query: kube_pod_created{namespace="kata-io"}
  metricName: kubePodCreated
- query: kube_pod_status_scheduled_time{namespace="kata-io"}
  metricName: kubePodScheduledTime
- query: kube_pod_status_initialized_time{namespace="kata-io"}
  metricName: kubePodInitializedTime
- query: kube_pod_container_state_started{namespace="kata-io"}
  metricName: kubePodContainerStarted
- query: kube_pod_status_ready_time{namespace="kata-io"}
  metricName: kubePodReadyTime
- query: kube_pod_runtimeclass_name_info{namespace="kata-io"}
  metricName: kubePodRuntimeClass
- query: histogram_quantile(0.95, sum by (le, runtime_handler) (increase(kubelet_run_podsandbox_duration_seconds_bucket[{{ .elapsed }}s])))
  metricName: runPodSandboxP95
- query: histogram_quantile(0.99, sum by (le, runtime_handler) (increase(kubelet_run_podsandbox_duration_seconds_bucket[{{ .elapsed }}s])))
  metricName: runPodSandboxP99
- query: histogram_quantile(0.95, sum by (le) (increase(kubelet_pod_start_total_duration_seconds_bucket[{{ .elapsed }}s])))
  metricName: podStartTotalP95
```

- [ ] **Step 11: Run schema and content tests**

Run: `go test ./internal/examples -run 'TestKataIOContractsValidate|TestKataIOFullWorkloadCoversRequiredScenarios|TestKataIOBenchmarkImageFilesExist'`

Expected: PASS.

- [ ] **Step 12: Approved commit command**

If commits are authorized, run:

```bash
git add suites/kata-io internal/examples/examples_test.go
git commit -m "feat: add kata io benchmark suite"
```

---

### Task 8: Documentation And End-To-End Validation

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-09-kata-io-suite-design.md` only if validation discovers a required design correction.
- Create: local run output under `results/` during validation.

**Interfaces:**
- Consumes: complete implementation from Tasks 1-7.
- Produces: documented workflow and evidence that the smoke benchmark runs end to end.

- [ ] **Step 1: Update README commands**

Add these examples to `README.md` under common commands or suite lifecycle:

```bash
TEST_SUITE=kata-io make provision
TEST_SUITE=kata-io TEST_MODE=smoke make run-suite
TEST_SUITE=kata-io TEST_MODE=full make run-suite
TEST_SUITE=kata-io make destroy
```

Add this note:

```markdown
`kata-io` provisions a Kata Pod Sandboxing-capable AKS workload pool, builds a benchmark image, installs Prometheus and kube-state-metrics, runs fio and Git clone workloads, and copies raw artifacts from the results PVC into the local run directory.
```

- [ ] **Step 2: Run unit and render validation**

Run: `make test`

Expected: PASS.

Run: `make build`

Expected: PASS.

Run: `make list-suites`

Expected: PASS and output includes `kata-io`.

- [ ] **Step 3: Provision real AKS infrastructure**

Run: `TEST_SUITE=kata-io make provision`

Expected: PASS. The Azure resource group `rg-aks-burner-kata-io` exists, the AKS cluster exists, and the workload pool has `workloadRuntime: KataVmIsolation`.

- [ ] **Step 4: Run smoke suite end to end**

Run: `TEST_SUITE=kata-io TEST_MODE=smoke make run-suite`

Expected: PASS. The runner builds and pushes the benchmark image, installs Prometheus, installs kube-state-metrics, runs standard and Kata smoke jobs, waits for kube-burner completion, and copies artifacts.

- [ ] **Step 5: Verify cluster-side smoke requirements**

Run: `kubectl get runtimeclass kata-vm-isolation`

Expected: PASS and output includes `kata-vm-isolation`.

Run: `kubectl get pods -n perf-monitoring`

Expected: PASS and output includes running `prometheus` and `kube-state-metrics` pods.

- [ ] **Step 6: Verify local artifacts**

Inspect the newest smoke run directory under `results/`. Required files must exist under `artifacts/`:

```text
summary.prom
fio.json
git-trace2-event.json
git-trace2-perf.log
time.txt
```

Run: `make list-suites`

Expected: PASS. This confirms local repo commands still work after validation output was created.

- [ ] **Step 7: Destroy real AKS infrastructure**

Run: `TEST_SUITE=kata-io make destroy`

Expected: PASS. The default resource group `rg-aks-burner-kata-io` is deleted.

- [ ] **Step 8: Optional full exploratory run**

When time and cost allow, run:

```bash
TEST_SUITE=kata-io make provision
TEST_SUITE=kata-io TEST_MODE=full make run-suite
TEST_SUITE=kata-io make destroy
```

Expected: PASS. Full mode covers standard/Kata, `emptyDir`/Azure Disk/Azure Files, all five fio profiles, both Git clone modes, and concurrency 1/10.

- [ ] **Step 9: Approved commit command**

If commits are authorized, run:

```bash
git add README.md docs/superpowers/specs/2026-07-09-kata-io-suite-design.md
git commit -m "docs: document kata io validation workflow"
```

---

## Self-Review

Spec coverage: Tasks cover the new `kata-io` suite, Kata-capable AKS provisioning, all required storage types, focused fio and Git workloads, remote GitHub default, concurrency 1 and 10, kube-state-metrics, required raw artifacts, minimal runner changes, and end-to-end smoke validation.

Placeholder scan: The plan avoids open-ended implementation markers. Where full mode requires many explicit scenario jobs, the plan defines the exact required dimensions and labels, and tests assert those dimensions are present.

Type consistency: `Mode.WorkloadFile`, `Mode.SelectedWorkloadFile`, `kubestatemetrics.Config`, and `artifacts.Config` are defined before later tasks consume them. Requirement field names match the design: `kubeStateMetrics`, `artifacts.enabled`, `artifacts.namespace`, `artifacts.pvcName`, and `artifacts.mountPath`.
