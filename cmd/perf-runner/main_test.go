package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/acr"
	"github.com/Azure/aks-burner/internal/artifacts"
	"github.com/Azure/aks-burner/internal/config"
	"github.com/Azure/aks-burner/internal/kubestatemetrics"
)

var testSourceRoot = mustTestSourceRoot()

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

func TestRunDispatchesRunSuite(t *testing.T) {
	err := run([]string{"run-suite"})
	if err == nil || !strings.Contains(err.Error(), "usage: perf-runner run-suite") {
		t.Fatalf("run-suite dispatch error = %v", err)
	}
}

func TestRunSuiteRejectsInvalidSuiteSetupSchema(t *testing.T) {
	root := testRepoRoot(t)
	suiteDir := filepath.Join(root, "suites", "bad-setup")
	if err := os.MkdirAll(filepath.Join(suiteDir, "vars"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.yml"), []byte(`name: bad-setup
description: Bad setup suite
tests:
  - startup
setup:
  resources:
    - name: node-prep
      path: setup/node-prep.yml
      wait:
        - kind: sleep
          resource: daemonset/node-prep
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "requirements.yml"), []byte(`suite: bad-setup
requires:
  infrastructure:
    provider: aks
    bicep:
      template: infra/aks/main.bicep
      parameters: suites/bad-setup/infra.bicepparam
  kubernetes:
    minVersion: "1.30"
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
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "infra.bicepparam"), []byte("param clusterName = 'aksbadsetup'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "infra", "aks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "infra", "aks", "main.bicep"), []byte("param clusterName string\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "images.yml"), []byte("pause: mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2\nprometheus: prom/prometheus:v2.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "workload.yml"), []byte("jobs: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "vars", "smoke.yml"), []byte(`iterations: 1
iterationsPerNamespace: 1
qps: 1
burst: 1
cleanup: true
waitWhenFinished: true
preLoadImages: false
templateVars: {}
imageVars: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	withWorkingDir(t, root)

	err := run([]string{"run-suite", "--suite", "bad-setup", "--mode", "smoke", "--resource-group", "rg-aks-burner-bad-setup"})
	if err == nil || !strings.Contains(err.Error(), "suite.schema.json") || !strings.Contains(err.Error(), "/setup/resources/0/wait/0/kind") {
		t.Fatalf("run-suite error = %v, want suite setup schema validation error", err)
	}
}

func TestListSuitesPrintsModes(t *testing.T) {
	root := testRepoRoot(t)
	suiteDir := filepath.Join(root, "suites", "demo")
	if err := os.MkdirAll(filepath.Join(suiteDir, "vars"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.yml"), []byte("name: demo\ndescription: Demo suite\ntests:\n  - startup-smoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "vars", "smoke.yml"), []byte("iterations: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "vars", "full.yml"), []byte("iterations: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)

	var out bytes.Buffer
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	os.Stdout = w
	err = run([]string{"list-suites"})
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	os.Stdout = oldStdout
	if _, copyErr := io.Copy(&out, r); copyErr != nil {
		t.Fatalf("Copy() error = %v", copyErr)
	}
	if err != nil {
		t.Fatalf("run(list-suites) error = %v", err)
	}

	if got, want := out.String(), "demo\tsmoke, full\tDemo suite\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestShouldWaitPrometheusRolloutOnlyWhenInstalledByRunner(t *testing.T) {
	cases := []struct {
		name     string
		required bool
		install  bool
		want     bool
	}{
		{name: "required and installed", required: true, install: true, want: true},
		{name: "required existing service", required: true, install: false, want: false},
		{name: "not required", required: false, install: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldWaitPrometheusRollout(tc.required, tc.install); got != tc.want {
				t.Fatalf("shouldWaitPrometheusRollout(%v, %v) = %v, want %v", tc.required, tc.install, got, tc.want)
			}
		})
	}
}

func TestKubeStateMetricsScrapeTargetOnlyWhenRequired(t *testing.T) {
	if got := kubeStateMetricsScrapeTarget(kubestatemetrics.Config{}); got != "" {
		t.Fatalf("kubeStateMetricsScrapeTarget() = %q, want empty for existing kata-perf requirements", got)
	}
	got := kubeStateMetricsScrapeTarget(kubestatemetrics.Config{Required: true, Namespace: "perf-monitoring", ServiceName: "kube-state-metrics", ServicePort: 8080})
	want := "kube-state-metrics.perf-monitoring.svc:8080"
	if got != want {
		t.Fatalf("kubeStateMetricsScrapeTarget() = %q, want %q", got, want)
	}
}

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

func TestValidateDestroyTargetRequiresDefaultResourceGroup(t *testing.T) {
	err := validateDestroyTarget("kata-perf", "rg-not-owned", false)
	if err == nil || !strings.Contains(err.Error(), "rg-aks-burner-kata-perf") {
		t.Fatalf("validateDestroyTarget() error = %v, want default resource group error", err)
	}
	if err := validateDestroyTarget("kata-perf", "rg-not-owned", true); err != nil {
		t.Fatalf("validateDestroyTarget() with override returned error: %v", err)
	}
}

func TestResolveSuitePathRejectsOutsideSuite(t *testing.T) {
	root := t.TempDir()
	_, err := resolveSuitePath(root, "kata-perf", "../outside.bicepparam")
	if err == nil || !strings.Contains(err.Error(), "outside suite directory") {
		t.Fatalf("resolveSuitePath() error = %v, want outside suite directory", err)
	}
}

func TestResolveSuitePathAcceptsRepoRelativeSuitePath(t *testing.T) {
	root := t.TempDir()
	got, err := resolveSuitePath(root, "kata-perf", "suites/kata-perf/infra.bicepparam")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "suites", "kata-perf", "infra.bicepparam")
	if got != want {
		t.Fatalf("resolveSuitePath() = %q, want %q", got, want)
	}
}

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

func TestResolveRepoPathRejectsOutsideRepo(t *testing.T) {
	root := t.TempDir()
	_, err := resolveRepoPath(root, "../outside/main.bicep")
	if err == nil || !strings.Contains(err.Error(), "outside repo") {
		t.Fatalf("resolveRepoPath() error = %v, want outside repo", err)
	}
}

func TestAddSuiteFastModeWritesDummySuite(t *testing.T) {
	root := testRepoRoot(t)
	withWorkingDir(t, root)

	if err := addSuiteWithIO([]string{"--suite", "demo-suite"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("addSuiteWithIO() returned error: %v", err)
	}

	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "suite.yml"), "name: demo-suite")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "requirements.yml"), "suite: demo-suite")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "requirements.yml"), "parameters: suites/demo-suite/infra.bicepparam")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "infra.bicepparam"), "param clusterName = 'aksdemosuite'")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "workload.yml"), "name: startup-smoke")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "templates", "pod.yml"), "app: demo-suite")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "vars", "smoke.yml"), "iterations: 20")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "vars", "full.yml"), "iterations: 500")
	assertGeneratedSuiteSchemas(t, root, "demo-suite")
}

func TestAddSuiteRejectsInvalidName(t *testing.T) {
	root := testRepoRoot(t)
	withWorkingDir(t, root)

	err := addSuiteWithIO([]string{"--suite", "Demo_Suite"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid suite name") {
		t.Fatalf("addSuiteWithIO() error = %v, want invalid suite name", err)
	}
}

func TestAddSuiteRefusesOverwrite(t *testing.T) {
	root := testRepoRoot(t)
	withWorkingDir(t, root)
	if err := os.MkdirAll(filepath.Join(root, "suites", "demo-suite"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := addSuiteWithIO([]string{"--suite", "demo-suite"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("addSuiteWithIO() error = %v, want already exists", err)
	}
}

func TestAddSuiteRejectsUnsafeBicepParameterText(t *testing.T) {
	root := testRepoRoot(t)
	withWorkingDir(t, root)

	err := addSuiteWithIO([]string{"--suite", "demo-suite", "--cluster-name", "aksdemo'\nparam extra = 'x"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cluster name") {
		t.Fatalf("addSuiteWithIO() error = %v, want cluster name validation", err)
	}
}

func TestAddSuiteRejectsNonPositiveNumbers(t *testing.T) {
	root := testRepoRoot(t)
	withWorkingDir(t, root)

	err := addSuiteWithIO([]string{"--suite", "demo-suite", "--smoke-iterations", "-1"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "smoke iterations") {
		t.Fatalf("addSuiteWithIO() error = %v, want smoke iterations validation", err)
	}
	err = addSuiteWithIO([]string{"--suite", "demo-suite", "--node-count", "0"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "node count") {
		t.Fatalf("addSuiteWithIO() error = %v, want node count validation", err)
	}
}

func TestAddSuiteGuidedUsesDefaultsForBlankAnswers(t *testing.T) {
	root := testRepoRoot(t)
	withWorkingDir(t, root)
	input := strings.NewReader("guided-suite\n\n\n\n\n\n\n\n\n")

	if err := addSuiteWithIO([]string{"--guided"}, input, io.Discard); err != nil {
		t.Fatalf("addSuiteWithIO() returned error: %v", err)
	}

	assertFileContains(t, filepath.Join(root, "suites", "guided-suite", "suite.yml"), "description: Dummy guided-suite performance suite.")
	assertFileContains(t, filepath.Join(root, "suites", "guided-suite", "requirements.yml"), "required: true")
	assertFileContains(t, filepath.Join(root, "suites", "guided-suite", "infra.bicepparam"), "param userNodeCount = 1")
	assertGeneratedSuiteSchemas(t, root, "guided-suite")
}

func TestReadBicepParamStringReadsContainerRegistryName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "infra.bicepparam")
	if err := os.WriteFile(path, []byte("param clusterName = 'akstest'\nparam containerRegistryName = 'acrtest'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readBicepParamString(path, "containerRegistryName")
	if err != nil {
		t.Fatal(err)
	}
	if got != "acrtest" {
		t.Fatalf("readBicepParamString() = %q, want acrtest", got)
	}
}

func TestRegistryNameFromRequirementsUsesParameterWhenPresent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "infra.bicepparam")
	if err := os.WriteFile(path, []byte("param clusterName = 'akstest'\nparam containerRegistryName = 'acrtest'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := registryNameFromRequirements(path, acr.Requirements{
		Registry: acr.RegistryConfig{NameParameter: "containerRegistryName"},
		Builds:   []acr.ImageBuild{{Key: "image", Repository: "repo/image", Context: ".", Dockerfile: "Dockerfile"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "acrtest" {
		t.Fatalf("registryNameFromRequirements() = %q, want acrtest", got)
	}
}

func TestRegistryNameFromRequirementsAllowsGeneratedRegistryName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "infra.bicepparam")
	if err := os.WriteFile(path, []byte("param clusterName = 'akstest'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := registryNameFromRequirements(path, acr.Requirements{
		Registry: acr.RegistryConfig{NameParameter: "containerRegistryName"},
		Builds:   []acr.ImageBuild{{Key: "image", Repository: "repo/image", Context: ".", Dockerfile: "Dockerfile"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("registryNameFromRequirements() = %q, want empty fallback marker", got)
	}
}

func TestShouldDeployContainerRegistryRequiresImages(t *testing.T) {
	if shouldDeployContainerRegistry(nil) {
		t.Fatal("shouldDeployContainerRegistry(nil) = true, want false")
	}
	images := &acr.Requirements{
		Registry: acr.RegistryConfig{NameParameter: "containerRegistryName"},
		Builds:   []acr.ImageBuild{{Key: "image", Repository: "repo/image", Context: ".", Dockerfile: "Dockerfile"}},
	}
	if !shouldDeployContainerRegistry(images) {
		t.Fatal("shouldDeployContainerRegistry(images) = false, want true")
	}
}

func TestMergeImagesOverlaysBuiltImages(t *testing.T) {
	got := mergeImages(map[string]string{"pause": "mcr/pause", "app": "old"}, map[string]string{"app": "acr/app:run"})
	if got["pause"] != "mcr/pause" || got["app"] != "acr/app:run" {
		t.Fatalf("mergeImages() = %#v", got)
	}
}

func TestValidateModeImageVarsAllowsDeclaredBuildKeys(t *testing.T) {
	err := validateModeImageVars(map[string]string{"image": "kata-pause"}, map[string]string{"prometheus": "mcr/prometheus"}, []acr.ImageBuild{{Key: "kata-pause"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateModeImageVarsRejectsUnknownImageKeyBeforeBuild(t *testing.T) {
	err := validateModeImageVars(map[string]string{"image": "missing"}, map[string]string{"prometheus": "mcr/prometheus"}, []acr.ImageBuild{{Key: "kata-pause"}})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("validateModeImageVars() error = %v, want missing image key", err)
	}
}

func TestExecuteRunAndCopyArtifactsResolvesCopyImageBeforeExecute(t *testing.T) {
	order := []string{}
	execute := func(workloadPath string, logPath string) error {
		order = append(order, "execute")
		return nil
	}
	copyArtifacts := func(ctx context.Context, cfg artifacts.Config, destination string, subpath string) error {
		order = append(order, "copy:"+cfg.CopyImage)
		return nil
	}
	waitArtifacts := func(ctx context.Context, cfg artifacts.Config) error {
		order = append(order, "wait:"+cfg.Namespace)
		return nil
	}

	err := executeRunAndCopyArtifacts(context.Background(), "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, Namespace: "kata-io", CopyImage: "artifact-copy"}, map[string]string{"artifact-copy": "busybox:test"}, "artifacts", "", execute, waitArtifacts, copyArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"execute", "wait:kata-io", "copy:busybox:test"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
}

func TestExecuteRunAndCopyArtifactsSkipsJobWaitWhenArtifactsDisabled(t *testing.T) {
	waited := false
	err := executeRunAndCopyArtifacts(context.Background(), "workload.yml", "kube-burner.log", artifacts.Config{}, map[string]string{}, "artifacts", "", func(workloadPath string, logPath string) error {
		return nil
	}, func(ctx context.Context, cfg artifacts.Config) error {
		waited = true
		return nil
	}, func(ctx context.Context, cfg artifacts.Config, destination string, subpath string) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if waited {
		t.Fatalf("waitArtifactJobs called when artifacts are disabled")
	}
}

func TestExecuteRunAndCopyArtifactsCopiesCurrentRunIDSubpath(t *testing.T) {
	copiedSubpath := ""
	err := executeRunAndCopyArtifacts(context.Background(), "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, CopyImage: "artifact-copy"}, map[string]string{"artifact-copy": "busybox:test"}, "artifacts", "kata-io-full-20260709T010203.000000004Z", func(workloadPath string, logPath string) error {
		return nil
	}, func(ctx context.Context, cfg artifacts.Config) error {
		return nil
	}, func(ctx context.Context, cfg artifacts.Config, destination string, subpath string) error {
		copiedSubpath = subpath
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if copiedSubpath != "kata-io-full-20260709T010203.000000004Z" {
		t.Fatalf("copied subpath = %q, want current runID", copiedSubpath)
	}
}

func TestExecuteRunAndCopyArtifactsKeepsLegacyCopyWhenRunIDEmpty(t *testing.T) {
	copiedSubpath := "not-called"
	err := executeRunAndCopyArtifacts(context.Background(), "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, CopyImage: "artifact-copy"}, map[string]string{"artifact-copy": "busybox:test"}, "artifacts", "", func(workloadPath string, logPath string) error {
		return nil
	}, func(ctx context.Context, cfg artifacts.Config) error {
		return nil
	}, func(ctx context.Context, cfg artifacts.Config, destination string, subpath string) error {
		copiedSubpath = subpath
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if copiedSubpath != "" {
		t.Fatalf("copied subpath = %q, want empty legacy subpath", copiedSubpath)
	}
}

func TestExecuteRunAndCopyArtifactsRejectsUnsafeRunIDBeforeExecute(t *testing.T) {
	executed := false
	err := executeRunAndCopyArtifacts(context.Background(), "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, CopyImage: "artifact-copy"}, map[string]string{"artifact-copy": "busybox:test"}, "artifacts", "../old-run", func(workloadPath string, logPath string) error {
		executed = true
		return nil
	}, func(ctx context.Context, cfg artifacts.Config) error {
		return nil
	}, func(ctx context.Context, cfg artifacts.Config, destination string, subpath string) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "invalid artifact subpath") {
		t.Fatalf("executeRunAndCopyArtifacts() error = %v, want invalid artifact subpath", err)
	}
	if executed {
		t.Fatalf("ExecuteKubeBurner ran after unsafe runID")
	}
}

func TestArtifactSubpathFromRenderedWorkloadUsesFirstRunID(t *testing.T) {
	rendered := map[string]any{"jobs": []any{map[string]any{"objects": []any{map[string]any{"inputVars": map[string]any{"runID": "kata-io-smoke-20260709T010203.000000004Z"}}}}}}
	if got := artifactSubpathFromRenderedWorkload(rendered); got != "kata-io-smoke-20260709T010203.000000004Z" {
		t.Fatalf("artifactSubpathFromRenderedWorkload() = %q, want runID", got)
	}
}

func TestArtifactSubpathFromRenderedWorkloadAllowsMissingRunID(t *testing.T) {
	rendered := map[string]any{"jobs": []any{map[string]any{"objects": []any{map[string]any{"inputVars": map[string]any{"app": "kata-perf"}}}}}}
	if got := artifactSubpathFromRenderedWorkload(rendered); got != "" {
		t.Fatalf("artifactSubpathFromRenderedWorkload() = %q, want empty legacy subpath", got)
	}
}

func TestExecuteRunAndCopyArtifactsReturnsResolveErrorBeforeExecute(t *testing.T) {
	executed := false
	err := executeRunAndCopyArtifacts(context.Background(), "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, CopyImage: "missing"}, map[string]string{}, "artifacts", "", func(workloadPath string, logPath string) error {
		executed = true
		return nil
	}, func(ctx context.Context, cfg artifacts.Config) error {
		return nil
	}, func(ctx context.Context, cfg artifacts.Config, destination string, subpath string) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("executeRunAndCopyArtifacts() error = %v, want missing image error", err)
	}
	if executed {
		t.Fatalf("ExecuteKubeBurner ran after artifact image resolution failed")
	}
}

func TestExecuteRunAndCopyArtifactsPrefersKubeBurnerFailureOverArtifactCopy(t *testing.T) {
	executeErr := errors.New("kube-burner failed")
	copyErr := errors.New("artifact copy failed")
	err := executeRunAndCopyArtifacts(context.Background(), "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, CopyImage: "artifact-copy"}, map[string]string{"artifact-copy": "busybox:test"}, "artifacts", "", func(workloadPath string, logPath string) error {
		return executeErr
	}, func(ctx context.Context, cfg artifacts.Config) error {
		return nil
	}, func(ctx context.Context, cfg artifacts.Config, destination string, subpath string) error {
		return copyErr
	})
	if !errors.Is(err, executeErr) || !strings.Contains(err.Error(), "artifact copy also failed") {
		t.Fatalf("executeRunAndCopyArtifacts() error = %v, want kube-burner error with artifact copy context", err)
	}
}

func testRepoRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/Azure/aks-burner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "suites"), 0o755); err != nil {
		t.Fatal(err)
	}
	copySchema(t, root, "suite.schema.json")
	copySchema(t, root, "requirements.schema.json")
	copySchema(t, root, "mode.schema.json")
	return root
}

func copySchema(t *testing.T, root string, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testSourceRoot, "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "schemas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustTestSourceRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
}

func assertFileContains(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s does not contain %q; content:\n%s", path, want, data)
	}
}

func assertGeneratedSuiteSchemas(t *testing.T, root string, name string) {
	t.Helper()
	paths := []struct {
		schema string
		file   string
	}{
		{schema: "suite.schema.json", file: filepath.Join("suites", name, "suite.yml")},
		{schema: "requirements.schema.json", file: filepath.Join("suites", name, "requirements.yml")},
		{schema: "mode.schema.json", file: filepath.Join("suites", name, "vars", "smoke.yml")},
		{schema: "mode.schema.json", file: filepath.Join("suites", name, "vars", "full.yml")},
	}
	for _, path := range paths {
		if err := config.ValidateYAML(filepath.Join(root, "schemas", path.schema), filepath.Join(root, path.file)); err != nil {
			t.Fatalf("ValidateYAML(%s) returned error: %v", path.file, err)
		}
	}
}
