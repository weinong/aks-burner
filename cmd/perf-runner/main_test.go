package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/acr"
	"github.com/Azure/aks-burner/internal/artifacts"
	"github.com/Azure/aks-burner/internal/config"
	"github.com/Azure/aks-burner/internal/infra"
	"github.com/Azure/aks-burner/internal/kubestatemetrics"
	"github.com/Azure/aks-burner/internal/kubetarget"
	runpkg "github.com/Azure/aks-burner/internal/run"
)

var testSourceRoot = mustTestSourceRoot()

func TestResolveAzureResourceNames(t *testing.T) {
	tests := []struct {
		name                string
		resourceGroup       string
		clusterName         string
		resourceGroupNeeded bool
		wantResourceGroup   string
		wantClusterName     string
		wantIdentityLookups int
	}{
		{name: "default names include alias", resourceGroupNeeded: true, wantResourceGroup: "rg-aks-burner-kata-perf-jane-doe", wantClusterName: "akskataperfjanedoe", wantIdentityLookups: 1},
		{name: "explicit resource group keeps suite-only cluster", resourceGroup: "rg-custom", resourceGroupNeeded: true, wantResourceGroup: "rg-custom", wantClusterName: "akskataperf"},
		{name: "explicit cluster is unchanged with default resource group", clusterName: "existing-aks", resourceGroupNeeded: true, wantResourceGroup: "rg-aks-burner-kata-perf-jane-doe", wantClusterName: "existing-aks", wantIdentityLookups: 1},
		{name: "Azure-independent run skips identity", clusterName: "existing-aks", wantClusterName: "existing-aks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lookups := 0
			got, err := resolveAzureResourceNames(context.Background(), "kata-perf", test.resourceGroup, test.clusterName, test.resourceGroupNeeded, func(context.Context) (string, error) {
				lookups++
				return "jane-doe", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.ResourceGroup != test.wantResourceGroup || got.ClusterName != test.wantClusterName {
				t.Fatalf("resolveAzureResourceNames() = %#v, want resource group %q and cluster %q", got, test.wantResourceGroup, test.wantClusterName)
			}
			if lookups != test.wantIdentityLookups {
				t.Fatalf("identity lookups = %d, want %d", lookups, test.wantIdentityLookups)
			}
		})
	}
}

func TestMakeRunSuiteExplicitContextOmitsDefaultResourceGroup(t *testing.T) {
	output := makeDryRun(t, "run-suite", "TEST_SUITE=kata-perf", "KUBE_CONTEXT=preview", "CLUSTER_NAME=existing-aks")
	if !strings.Contains(output, `--kube-context "preview"`) {
		t.Fatalf("make output missing context: %s", output)
	}
	if !strings.Contains(output, `--cluster-name "existing-aks"`) {
		t.Fatalf("make output missing cluster name: %s", output)
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

func TestMakeLifecycleTargetsOmitImplicitResourceGroup(t *testing.T) {
	for _, target := range []string{"run-suite", "provision", "destroy"} {
		output := makeDryRun(t, target, "TEST_SUITE=kata-perf")
		if strings.Contains(output, "--resource-group") || strings.Contains(output, "rg-aks-burner-kata-perf") {
			t.Fatalf("%s materialized an implicit resource group: %s", target, output)
		}
	}
}

func TestMakeLifecycleTargetsForwardExplicitResourceGroup(t *testing.T) {
	for _, target := range []string{"run-suite", "provision", "destroy"} {
		output := makeDryRun(t, target, "TEST_SUITE=kata-perf", "RESOURCE_GROUP=rg-custom")
		if !strings.Contains(output, `--resource-group "rg-custom"`) {
			t.Fatalf("%s did not forward explicit resource group: %s", target, output)
		}
	}
}

func TestMakeLifecycleTargetsForwardExplicitEmptyResourceGroup(t *testing.T) {
	for _, target := range []string{"run-suite", "provision", "destroy"} {
		output := makeDryRun(t, target, "TEST_SUITE=kata-perf", "RESOURCE_GROUP=")
		if !strings.Contains(output, `--resource-group ""`) {
			t.Fatalf("%s did not forward explicit empty resource group: %s", target, output)
		}
	}
}

func TestMakeProvisionForwardsClusterName(t *testing.T) {
	output := makeDryRun(t, "provision", "TEST_SUITE=kata-perf", "CLUSTER_NAME=custom-aks")
	if !strings.Contains(output, `--cluster-name "custom-aks"`) {
		t.Fatalf("make output missing cluster name: %s", output)
	}
}

func TestInfraBicepSupportsKataWorkloadRuntimeParameters(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testSourceRoot, "infra", "aks", "main.bicep"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"param nodePools NodePool[]",
		"Microsoft.ContainerService/managedClusters@2025-09-02-preview",
		"'KataMshvVmIsolation'",
		"'KataVmIsolation'",
		"osSKU: pool.osSKU",
		"workloadRuntime: pool.workloadRuntime",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("main.bicep missing %q", want)
		}
	}
}

func TestAzureResourceDerivationInputsRemainStable(t *testing.T) {
	if infra.DeploymentName != "aks-burner" {
		t.Fatalf("DeploymentName = %q, want aks-burner", infra.DeploymentName)
	}
	data, err := os.ReadFile(filepath.Join(testSourceRoot, "infra", "aks", "main.bicep"))
	if err != nil {
		t.Fatal(err)
	}
	want := "'acr${take(uniqueString(resourceGroup().id, clusterName), 18)}'"
	if !strings.Contains(string(data), want) {
		t.Fatalf("main.bicep missing unchanged ACR derivation %s", want)
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
    nodePools:
      - name: systempool
        mode: System
        count: 1
        vmSize: Standard_D4s_v5
        osType: Linux
        osSKU: Ubuntu
        workloadRuntime: OCIContainer
        labels: {}
        taints: []
  kubernetes:
    minVersion: "1.30"
  nodeSelectors: []
  reporting:
    sources:
      standardSummary: false
      kubeBurner: true
    prometheusMetricUnits: {}
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
    nodePools:
      - name: systempool
        mode: System
        count: 1
        vmSize: Standard_D4s_v5
        osType: Linux
        osSKU: Ubuntu
        workloadRuntime: OCIContainer
        labels: {}
        taints: []
  kubernetes:
    minVersion: "1.36"
  reporting:
    sources:
      standardSummary: true
      kubeBurner: true
    prometheusMetricUnits:
      podCPUUsage: cores
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

func TestRequirementsSchemaRejectsMissingReporting(t *testing.T) {
	root := testRepoRoot(t)
	path := filepath.Join(root, "requirements.yml")
	data := []byte(`suite: demo
requires:
  infrastructure:
    provider: aks
    nodePools:
      - name: systempool
        mode: System
        count: 1
        vmSize: Standard_D4s_v5
        osType: Linux
        osSKU: Ubuntu
        workloadRuntime: OCIContainer
        labels: {}
        taints: []
  kubernetes:
    minVersion: "1.36"
  observability:
    prometheus:
      required: false
      install: false
      namespace: monitoring
      imageKey: prometheus
      serviceName: prometheus
      servicePort: 9090
      localPort: 9090
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), path); err == nil {
		t.Fatal("requirements schema accepted missing reporting block")
	}
}

func TestRequirementsSchemaRejectsUnknownReportingSource(t *testing.T) {
	root := testRepoRoot(t)
	path := filepath.Join(root, "requirements.yml")
	data := []byte(`suite: demo
requires:
  infrastructure:
    provider: aks
    nodePools:
      - name: systempool
        mode: System
        count: 1
        vmSize: Standard_D4s_v5
        osType: Linux
        osSKU: Ubuntu
        workloadRuntime: OCIContainer
        labels: {}
        taints: []
  kubernetes:
    minVersion: "1.36"
  reporting:
    sources:
      standardSummary: false
      kubeBurner: true
      unknown: true
    prometheusMetricUnits: {}
  observability:
    prometheus:
      required: false
      install: false
      namespace: monitoring
      imageKey: prometheus
      serviceName: prometheus
      servicePort: 9090
      localPort: 9090
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), path); err == nil {
		t.Fatal("requirements schema accepted unknown reporting source")
	}
}

func TestSuiteRequirementsDeclaresReporting(t *testing.T) {
	requires := suiteRequirements(addSuiteOptions{Prometheus: true})["requires"].(map[string]any)
	reporting := requires["reporting"].(map[string]any)
	sources := reporting["sources"].(map[string]any)
	if sources["kubeBurner"] != true {
		t.Fatalf("kubeBurner source = %#v, want true", sources["kubeBurner"])
	}
	units := reporting["prometheusMetricUnits"].(map[string]string)
	if units["podCPUUsage"] != "cores" || units["podMemoryWorkingSet"] != "bytes" {
		t.Fatalf("prometheusMetricUnits = %#v", units)
	}
}

func TestValidateDestroyTargetRequiresDefaultResourceGroup(t *testing.T) {
	err := validateDestroyTarget("rg-not-owned", false, false)
	if err == nil || !strings.Contains(err.Error(), "--allow-non-default-resource-group") {
		t.Fatalf("validateDestroyTarget() error = %v, want override guidance", err)
	}
	if err := validateDestroyTarget("rg-not-owned", false, true); err != nil {
		t.Fatalf("validateDestroyTarget() with override returned error: %v", err)
	}
	if err := validateDestroyTarget("rg-aks-burner-kata-perf-jane", true, false); err != nil {
		t.Fatalf("validateDestroyTarget() rejected derived default: %v", err)
	}
}

func TestDestroyDerivesCurrentUserResourceGroup(t *testing.T) {
	root := testRepoRoot(t)
	suiteDir := filepath.Join(root, "suites", "demo", "vars")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "suites", "demo", "suite.yml"), []byte("name: demo\ndescription: Demo\ntests: [smoke]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)
	stubAzureUserAlias(t, func(context.Context) (string, error) { return "jane-doe", nil })
	oldDestroy := destroyInfra
	var got string
	destroyInfra = func(_ context.Context, resourceGroup string) error { got = resourceGroup; return nil }
	t.Cleanup(func() { destroyInfra = oldDestroy })

	if err := destroy([]string{"--suite", "demo"}); err != nil {
		t.Fatal(err)
	}
	if got != "rg-aks-burner-demo-jane-doe" {
		t.Fatalf("destroy resource group = %q", got)
	}
}

func TestDestroyExplicitResourceGroupSkipsIdentityAndRequiresOverride(t *testing.T) {
	root := testRepoRoot(t)
	suiteDir := filepath.Join(root, "suites", "demo", "vars")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "suites", "demo", "suite.yml"), []byte("name: demo\ndescription: Demo\ntests: [smoke]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)
	stubAzureUserAlias(t, func(context.Context) (string, error) { return "", errors.New("identity lookup must not run") })
	oldDestroy := destroyInfra
	called := false
	destroyInfra = func(context.Context, string) error { called = true; return nil }
	t.Cleanup(func() { destroyInfra = oldDestroy })

	err := destroy([]string{"--suite", "demo", "--resource-group", "rg-custom"})
	if err == nil || !strings.Contains(err.Error(), "--allow-non-default-resource-group") {
		t.Fatalf("destroy error = %v, want override guidance", err)
	}
	if called {
		t.Fatal("destroy ran without explicit resource group override")
	}
}

func TestDestroyRejectsExplicitEmptyResourceGroupBeforeIdentityOrDelete(t *testing.T) {
	root := testRepoRoot(t)
	suiteDir := filepath.Join(root, "suites", "demo", "vars")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "suites", "demo", "suite.yml"), []byte("name: demo\ndescription: Demo\ntests: [smoke]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)
	identityLookups := 0
	stubAzureUserAlias(t, func(context.Context) (string, error) {
		identityLookups++
		return "jane-doe", nil
	})
	oldDestroy := destroyInfra
	destroyCalled := false
	destroyInfra = func(context.Context, string) error { destroyCalled = true; return nil }
	t.Cleanup(func() { destroyInfra = oldDestroy })

	err := destroy([]string{"--suite", "demo", "--resource-group", ""})
	if err == nil || !strings.Contains(err.Error(), "resource-group must not be empty") {
		t.Fatalf("destroy error = %v, want explicit-empty validation", err)
	}
	if identityLookups != 0 {
		t.Fatalf("identity lookups = %d, want 0", identityLookups)
	}
	if destroyCalled {
		t.Fatal("destroy ran for an explicitly empty resource group")
	}
}

func TestDestroyExplicitResourceGroupWithOverrideSkipsIdentityAndDeletesExactTarget(t *testing.T) {
	root := testRepoRoot(t)
	suiteDir := filepath.Join(root, "suites", "demo", "vars")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "suites", "demo", "suite.yml"), []byte("name: demo\ndescription: Demo\ntests: [smoke]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)
	stubAzureUserAlias(t, func(context.Context) (string, error) {
		return "", errors.New("identity lookup must not run")
	})
	oldDestroy := destroyInfra
	var deletedResourceGroup string
	destroyInfra = func(_ context.Context, resourceGroup string) error {
		deletedResourceGroup = resourceGroup
		return nil
	}
	t.Cleanup(func() { destroyInfra = oldDestroy })

	if err := destroy([]string{"--suite", "demo", "--resource-group", "rg-custom", "--allow-non-default-resource-group"}); err != nil {
		t.Fatal(err)
	}
	if deletedResourceGroup != "rg-custom" {
		t.Fatalf("deleted resource group = %q, want rg-custom", deletedResourceGroup)
	}
}

func TestDestroyIdentityFailurePreventsDelete(t *testing.T) {
	root := testRepoRoot(t)
	suiteDir := filepath.Join(root, "suites", "demo", "vars")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "suites", "demo", "suite.yml"), []byte("name: demo\ndescription: Demo\ntests: [smoke]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)
	identityErr := errors.New("identity unavailable")
	stubAzureUserAlias(t, func(context.Context) (string, error) { return "", identityErr })
	oldDestroy := destroyInfra
	destroyCalled := false
	destroyInfra = func(context.Context, string) error { destroyCalled = true; return nil }
	t.Cleanup(func() { destroyInfra = oldDestroy })

	err := destroy([]string{"--suite", "demo"})
	if !errors.Is(err, identityErr) {
		t.Fatalf("destroy error = %v, want identity failure", err)
	}
	if destroyCalled {
		t.Fatal("destroy ran after identity lookup failed")
	}
}

func TestRunSuiteExplicitContextNoBuildSkipsAzure(t *testing.T) {
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

	err := run([]string{"run-suite", "--suite", "existing", "--mode", "smoke", "--kube-context", "preview"})
	if err != nil {
		t.Fatalf("run-suite error = %v", err)
	}
	if data, _ := os.ReadFile(azMarker); len(data) != 0 {
		t.Fatalf("explicit run invoked az: %s", data)
	}
	assertFileContains(t, kubectlMarker, "--context preview version -o json")
	assertFileContains(t, kubectlMarker, "--context preview get nodes -l kubernetes.azure.com/os-sku=AzureLinux -o name")
	runMetadata := singleRunMetadataPath(t, filepath.Join(root, "results"))
	data, err := os.ReadFile(runMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "kubeContext: preview") || strings.Contains(string(data), "clusterName:") {
		t.Fatalf("metadata = %s", data)
	}
}

func TestRunSuiteExplicitContextValidatesNodePoolsBeforeSideEffects(t *testing.T) {
	root := testRepoRoot(t)
	writeNoBuildContextSuite(t, root)
	requirementsPath := filepath.Join(root, "suites", "existing", "requirements.yml")
	data, err := os.ReadFile(requirementsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requirementsPath, bytes.Replace(data, []byte("pool: userpool"), []byte("pool: missing"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	markers := []string{filepath.Join(t.TempDir(), "az.log"), filepath.Join(t.TempDir(), "kubectl.log")}
	writeRecordingCommand(t, binDir, "az", markers[0], "")
	writeRecordingCommand(t, binDir, "kubectl", markers[1], "")
	t.Setenv("PATH", binDir)
	withWorkingDir(t, root)

	err = run([]string{"run-suite", "--suite", "existing", "--mode", "smoke", "--kube-context", "preview"})
	if err == nil || !strings.Contains(err.Error(), "references missing pool") {
		t.Fatalf("run-suite error = %v, want pool relationship validation", err)
	}
	for _, marker := range markers {
		if data, _ := os.ReadFile(marker); len(data) != 0 {
			t.Fatalf("side effect before node-pool validation in %s: %s", marker, data)
		}
	}
}

func TestRunSuiteLegacyRefreshesCredentials(t *testing.T) {
	root := testRepoRoot(t)
	writeLegacyContextSuite(t, root)
	binDir := t.TempDir()
	azMarker := filepath.Join(t.TempDir(), "az.log")
	kubectlMarker := filepath.Join(t.TempDir(), "kubectl.log")
	writeRecordingCommand(t, binDir, "az", azMarker, "")
	writeRecordingCommand(t, binDir, "kubectl", kubectlMarker, `{"serverVersion":{"gitVersion":"v9.99.0"}}`)
	writeRecordingCommand(t, binDir, "kube-burner", filepath.Join(t.TempDir(), "kube-burner.log"), "")
	t.Setenv("PATH", binDir)
	withWorkingDir(t, root)

	if err := run([]string{"run-suite", "--suite", "existing", "--mode", "smoke", "--resource-group", "rg-test"}); err != nil {
		t.Fatalf("run-suite error = %v", err)
	}
	assertFileContains(t, azMarker, "aks get-credentials --resource-group rg-test --name aksexisting --overwrite-existing")
	assertFileContains(t, kubectlMarker, "version -o json")
	if data, err := os.ReadFile(kubectlMarker); err != nil || strings.Contains(string(data), "--context") {
		t.Fatalf("legacy kubectl marker = %q, error = %v", data, err)
	}
	data, err := os.ReadFile(singleRunMetadataPath(t, filepath.Join(root, "results")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "clusterName: aksexisting") || strings.Contains(string(data), "kubeContext:") {
		t.Fatalf("legacy metadata = %s", data)
	}
}

func TestManagedRunSuiteOmittedResourceGroupUsesAliasQualifiedNamesEndToEnd(t *testing.T) {
	root := testRepoRoot(t)
	writeLegacyContextSuite(t, root)
	binDir := t.TempDir()
	azMarker := filepath.Join(t.TempDir(), "az.log")
	kubectlMarker := filepath.Join(t.TempDir(), "kubectl.log")
	writeAzureIdentityAndCredentialsCommand(t, binDir, azMarker, "Jane.Doe@contoso.com")
	writeRecordingCommand(t, binDir, "kubectl", kubectlMarker, `{"serverVersion":{"gitVersion":"v9.99.0"}}`)
	writeRecordingCommand(t, binDir, "kube-burner", filepath.Join(t.TempDir(), "kube-burner.log"), "")
	t.Setenv("PATH", binDir)
	withWorkingDir(t, root)

	if err := run([]string{"run-suite", "--suite", "existing", "--mode", "smoke"}); err != nil {
		t.Fatalf("run-suite error = %v", err)
	}
	assertFileContains(t, azMarker, "account show --query user.name --output tsv")
	assertFileContains(t, azMarker, "aks get-credentials --resource-group rg-aks-burner-existing-jane-doe --name aksexistingjanedoe --overwrite-existing")
	data, err := os.ReadFile(singleRunMetadataPath(t, filepath.Join(root, "results")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "resourceGroup: rg-aks-burner-existing-jane-doe") || !strings.Contains(string(data), "clusterName: aksexistingjanedoe") {
		t.Fatalf("managed metadata = %s", data)
	}
}

func TestRunSuiteExplicitContextBuildsDeriveResourceNames(t *testing.T) {
	root := testRepoRoot(t)
	writeBuildContextSuite(t, root)
	withWorkingDir(t, root)
	stubAzureUserAlias(t, func(context.Context) (string, error) { return "jane-doe", nil })
	stop := errors.New("stop after derived deployment lookup")
	var gotResourceGroup string
	var gotClusterName string

	err := runSuiteWithDependencies([]string{"--suite", "existing", "--mode", "smoke", "--kube-context", "preview"}, runSuiteDependencies{
		DeploymentOutput: func(_ context.Context, resourceGroup, _, output string) (string, error) {
			gotResourceGroup = resourceGroup
			if output == "clusterName" {
				gotClusterName = "aksexistingjanedoe"
				return gotClusterName, nil
			}
			return "", stop
		},
	})
	if !errors.Is(err, stop) {
		t.Fatalf("run-suite error = %v, want sentinel", err)
	}
	if gotResourceGroup != "rg-aks-burner-existing-jane-doe" || gotClusterName != "aksexistingjanedoe" {
		t.Fatalf("derived names = %q/%q", gotResourceGroup, gotClusterName)
	}
}

func TestRunSuiteExplicitContextWithBuildsUsesAzureWithoutCredentials(t *testing.T) {
	root := testRepoRoot(t)
	writeBuildContextSuite(t, root)
	binDir := t.TempDir()
	azMarker := filepath.Join(t.TempDir(), "az.log")
	kubectlMarker := filepath.Join(t.TempDir(), "kubectl.log")
	kubeBurnerMarker := filepath.Join(t.TempDir(), "kube-burner.log")
	writeAzureBuildCommand(t, binDir, azMarker)
	writeRecordingCommand(t, binDir, "kubectl", kubectlMarker, `{"serverVersion":{"gitVersion":"v9.99.0"}}`)
	writeRecordingCommand(t, binDir, "kube-burner", kubeBurnerMarker, "")
	t.Setenv("PATH", binDir)
	withWorkingDir(t, root)

	if err := run([]string{"run-suite", "--suite", "existing", "--mode", "smoke", "--kube-context", "preview", "--resource-group", "rg-build"}); err != nil {
		t.Fatalf("run-suite error = %v", err)
	}
	assertFileContains(t, azMarker, "deployment group show --resource-group rg-build")
	assertFileContains(t, azMarker, "properties.outputs.containerRegistryName.value")
	assertFileContains(t, azMarker, "properties.outputs.containerRegistryLoginServer.value")
	assertFileContains(t, azMarker, "acr build --registry acrbuild --resource-group rg-build")
	if data, err := os.ReadFile(azMarker); err != nil || strings.Contains(string(data), "aks get-credentials") {
		t.Fatalf("explicit build Azure marker = %q, error = %v", data, err)
	}
	assertEveryLineContains(t, kubectlMarker, "--context preview")
	assertEveryLineContains(t, kubeBurnerMarker, "--kube-context preview")
	data, err := os.ReadFile(singleRunMetadataPath(t, filepath.Join(root, "results")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "kubeContext: preview") || strings.Contains(string(data), "clusterName:") {
		t.Fatalf("explicit build metadata = %s", data)
	}
}

func TestValidateRequirementsUsesOneRunnerForVersionAndNodeSelector(t *testing.T) {
	var got [][]string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		got = append(got, append([]string(nil), args...))
		if args[0] == "version" {
			return []byte(`{"serverVersion":{"gitVersion":"v9.99.0"}}`), nil
		}
		return []byte("node/test\n"), nil
	}
	req := runpkg.Requirements{
		Kubernetes: runpkg.KubernetesRequirements{MinVersion: "9.99"},
		NodeSelectors: []runpkg.NodeSelectorRequirement{{
			Name: "azure-linux", Required: true, MinNodes: 1,
			Labels: map[string]string{"kubernetes.azure.com/os-sku": "AzureLinux"},
		}},
	}
	if err := runpkg.ValidateRequirements(context.Background(), req, runner); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"version", "-o", "json"},
		{"get", "nodes", "-l", "kubernetes.azure.com/os-sku=AzureLinux", "-o", "name"},
	}
	if len(got) != len(want) {
		t.Fatalf("runner calls = %#v, want %#v", got, want)
	}
	for i := range want {
		if strings.Join(got[i], "\x00") != strings.Join(want[i], "\x00") {
			t.Fatalf("runner call %d = %#v, want %#v", i, got[i], want[i])
		}
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
	got, err := resolveSuitePath(root, "kata-perf", "suites/kata-perf/requirements.yml")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "suites", "kata-perf", "requirements.yml")
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

func TestAddSuiteFastModeWritesDummySuite(t *testing.T) {
	root := testRepoRoot(t)
	withWorkingDir(t, root)

	if err := addSuiteWithIO([]string{"--suite", "demo-suite"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("addSuiteWithIO() returned error: %v", err)
	}

	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "suite.yml"), "name: demo-suite")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "requirements.yml"), "suite: demo-suite")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "requirements.yml"), "name: systempool")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "requirements.yml"), "name: userpool")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "requirements.yml"), "pool: userpool")
	if _, err := os.Stat(filepath.Join(root, "suites", "demo-suite", "infra.bicepparam")); !os.IsNotExist(err) {
		t.Fatalf("infra.bicepparam exists: %v", err)
	}
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "workload.yml"), "name: startup-smoke")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "templates", "pod.yml"), "app: demo-suite")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "vars", "smoke.yml"), "iterations: 20")
	assertFileContains(t, filepath.Join(root, "suites", "demo-suite", "vars", "full.yml"), "iterations: 500")
	assertGeneratedSuiteSchemas(t, root, "demo-suite")
}

func TestAddSuiteVPrefixedKubernetesVersionProducesNormalizedARMParameters(t *testing.T) {
	root := testRepoRoot(t)
	withWorkingDir(t, root)
	if err := addSuiteWithIO([]string{"--suite", "demo-suite", "--kubernetes-version", "v1.36"}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "infra", "aks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "infra", "aks", "main.bicep"), []byte("param clusterName string\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := provisionWithIO([]string{"--suite", "demo-suite", "--resource-group", "rg-demo", "--location", "westus2", "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"value": "v1.36"`) || !strings.Contains(out.String(), `"value": "1.36"`) {
		t.Fatalf("dry-run kubernetesVersion was not normalized:\n%s", out.String())
	}
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

func TestAddSuiteRejectsClusterNameFlag(t *testing.T) {
	root := testRepoRoot(t)
	withWorkingDir(t, root)

	err := addSuiteWithIO([]string{"--suite", "demo-suite", "--cluster-name", "custom-aks"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("addSuiteWithIO() error = %v, want unknown flag", err)
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
	input := strings.NewReader("guided-suite\n\n\n\n\n\n\n\n")
	var out bytes.Buffer

	if err := addSuiteWithIO([]string{"--guided"}, input, &out); err != nil {
		t.Fatalf("addSuiteWithIO() returned error: %v", err)
	}

	assertFileContains(t, filepath.Join(root, "suites", "guided-suite", "suite.yml"), "description: Dummy guided-suite performance suite.")
	assertFileContains(t, filepath.Join(root, "suites", "guided-suite", "requirements.yml"), "required: true")
	assertFileContains(t, filepath.Join(root, "suites", "guided-suite", "requirements.yml"), "count: 1")
	if strings.Contains(out.String(), "Cluster name") {
		t.Fatalf("guided prompts include cluster name: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "suites", "guided-suite", "infra.bicepparam")); !os.IsNotExist(err) {
		t.Fatalf("infra.bicepparam exists: %v", err)
	}
	assertGeneratedSuiteSchemas(t, root, "guided-suite")
}

func TestShouldDeployContainerRegistryRequiresImages(t *testing.T) {
	if shouldDeployContainerRegistry(nil) {
		t.Fatal("shouldDeployContainerRegistry(nil) = true, want false")
	}
	images := &acr.Requirements{
		Builds: []acr.ImageBuild{{Key: "image", Repository: "repo/image", Context: ".", Dockerfile: "Dockerfile"}},
	}
	if !shouldDeployContainerRegistry(images) {
		t.Fatal("shouldDeployContainerRegistry(images) = false, want true")
	}
}

func TestProvisionDryRunPrintsGeneratedParameters(t *testing.T) {
	root := provisionTestRepo(t)
	withWorkingDir(t, root)
	oldProvision := provisionInfra
	called := false
	provisionInfra = func(context.Context, infra.ProvisionOptions) error { called = true; return nil }
	t.Cleanup(func() { provisionInfra = oldProvision })
	var out bytes.Buffer
	if err := provisionWithIO([]string{"--suite", "demo", "--resource-group", "rg-demo", "--location", "westus2", "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Parameters map[string]struct {
			Value any `json:"value"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("dry-run output is not JSON: %v\n%s", err, out.String())
	}
	if got := doc.Parameters["clusterName"].Value; got != "aksdemo" {
		t.Fatalf("clusterName = %#v, want aksdemo", got)
	}
	if called {
		t.Fatal("provision command executed during dry-run")
	}
}

func TestProvisionClusterNameOverrideAndValidation(t *testing.T) {
	root := provisionTestRepo(t)
	withWorkingDir(t, root)
	var out bytes.Buffer
	if err := provisionWithIO([]string{"--suite", "demo", "--resource-group", "rg-demo", "--location", "westus2", "--cluster-name", "custom-aks", "--dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"value": "custom-aks"`) {
		t.Fatalf("output missing cluster override: %s", out.String())
	}
	if err := provisionWithIO([]string{"--suite", "demo", "--resource-group", "rg-demo", "--location", "westus2", "--cluster-name", "Invalid", "--dry-run"}, io.Discard); err == nil || !strings.Contains(err.Error(), "invalid cluster name") {
		t.Fatalf("invalid override error = %v", err)
	}
}

func TestProvisionPassesGeneratedParametersToInfra(t *testing.T) {
	root := provisionTestRepo(t)
	withWorkingDir(t, root)
	oldProvision := provisionInfra
	var got infra.ProvisionOptions
	provisionInfra = func(_ context.Context, opts infra.ProvisionOptions) error { got = opts; return nil }
	t.Cleanup(func() { provisionInfra = oldProvision })
	if err := provisionWithIO([]string{"--suite", "demo", "--resource-group", "rg-demo", "--location", "westus2"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got.ResourceGroup != "rg-demo" || got.Location != "westus2" || got.ClusterName != "aksdemo" {
		t.Fatalf("provision options = %#v", got)
	}
	if got.TemplateFile != filepath.Join(root, "infra", "aks", "main.bicep") {
		t.Fatalf("template file = %q", got.TemplateFile)
	}
	if !json.Valid(got.ParametersJSON) || !bytes.Contains(got.ParametersJSON, []byte(`"nodePools"`)) {
		t.Fatalf("parameters JSON = %s", got.ParametersJSON)
	}
}

func TestProvisionDerivesPerUserResourceNames(t *testing.T) {
	root := provisionTestRepo(t)
	withWorkingDir(t, root)
	stubAzureUserAlias(t, func(context.Context) (string, error) { return "jane-doe", nil })
	oldProvision := provisionInfra
	var got infra.ProvisionOptions
	provisionInfra = func(_ context.Context, opts infra.ProvisionOptions) error { got = opts; return nil }
	t.Cleanup(func() { provisionInfra = oldProvision })

	if err := provisionWithIO([]string{"--suite", "demo", "--location", "westus2"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got.ResourceGroup != "rg-aks-burner-demo-jane-doe" || got.ClusterName != "aksdemojanedoe" {
		t.Fatalf("provision options = %#v", got)
	}
}

func TestProvisionExplicitResourceGroupSkipsIdentityLookup(t *testing.T) {
	root := provisionTestRepo(t)
	withWorkingDir(t, root)
	stubAzureUserAlias(t, func(context.Context) (string, error) { return "", errors.New("identity lookup must not run") })
	oldProvision := provisionInfra
	var got infra.ProvisionOptions
	provisionInfra = func(_ context.Context, opts infra.ProvisionOptions) error { got = opts; return nil }
	t.Cleanup(func() { provisionInfra = oldProvision })

	if err := provisionWithIO([]string{"--suite", "demo", "--resource-group", "rg-custom", "--location", "westus2"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got.ResourceGroup != "rg-custom" || got.ClusterName != "aksdemo" {
		t.Fatalf("provision options = %#v", got)
	}
}

func TestProvisionAllowsGeneratedResourceGroupAtLengthLimit(t *testing.T) {
	root := provisionTestRepo(t)
	withWorkingDir(t, root)
	alias := strings.Repeat("a", 71)
	wantResourceGroup := "rg-aks-burner-demo-" + alias
	if len(wantResourceGroup) != 90 {
		t.Fatalf("test resource group length = %d, want 90", len(wantResourceGroup))
	}
	var gotResourceGroup string

	err := provisionWithDependencies([]string{"--suite", "demo", "--location", "westus2"}, io.Discard, provisionDependencies{
		AzureUserAlias: func(context.Context) (string, error) { return alias, nil },
		Provision: func(_ context.Context, opts infra.ProvisionOptions) error {
			gotResourceGroup = opts.ResourceGroup
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotResourceGroup != wantResourceGroup {
		t.Fatalf("provision resource group = %q, want %q", gotResourceGroup, wantResourceGroup)
	}
}

func TestProvisionRejectsGeneratedResourceGroupOverLengthLimitBeforeProvision(t *testing.T) {
	root := provisionTestRepo(t)
	withWorkingDir(t, root)
	alias := strings.Repeat("a", 72)
	generatedResourceGroup := "rg-aks-burner-demo-" + alias
	if len(generatedResourceGroup) != 91 {
		t.Fatalf("test resource group length = %d, want 91", len(generatedResourceGroup))
	}
	provisionCalled := false

	err := provisionWithDependencies([]string{"--suite", "demo", "--location", "westus2"}, io.Discard, provisionDependencies{
		AzureUserAlias: func(context.Context) (string, error) { return alias, nil },
		Provision: func(context.Context, infra.ProvisionOptions) error {
			provisionCalled = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "90 characters") || !strings.Contains(err.Error(), "--resource-group") {
		t.Fatalf("provision error = %v, want length limit and explicit --resource-group guidance", err)
	}
	if provisionCalled {
		t.Fatal("provision ran for an overlength generated resource group")
	}
}

func TestProvisionKeepsOverlengthExplicitResourceGroupVerbatim(t *testing.T) {
	root := provisionTestRepo(t)
	withWorkingDir(t, root)
	explicitResourceGroup := strings.Repeat("x", 91)
	var gotResourceGroup string

	err := provisionWithDependencies([]string{"--suite", "demo", "--resource-group", explicitResourceGroup, "--location", "westus2"}, io.Discard, provisionDependencies{
		AzureUserAlias: func(context.Context) (string, error) { return "", errors.New("identity lookup must not run") },
		Provision: func(_ context.Context, opts infra.ProvisionOptions) error {
			gotResourceGroup = opts.ResourceGroup
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotResourceGroup != explicitResourceGroup {
		t.Fatalf("provision resource group = %q, want explicit value verbatim", gotResourceGroup)
	}
}

func TestProvisionRejectsExplicitEmptyResourceGroupBeforeIdentityOrProvision(t *testing.T) {
	root := provisionTestRepo(t)
	withWorkingDir(t, root)
	identityLookups := 0
	stubAzureUserAlias(t, func(context.Context) (string, error) {
		identityLookups++
		return "jane-doe", nil
	})
	oldProvision := provisionInfra
	provisionCalled := false
	provisionInfra = func(context.Context, infra.ProvisionOptions) error { provisionCalled = true; return nil }
	t.Cleanup(func() { provisionInfra = oldProvision })

	err := provisionWithIO([]string{"--suite", "demo", "--resource-group", "", "--location", "westus2"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "resource-group must not be empty") {
		t.Fatalf("provision error = %v, want explicit-empty validation", err)
	}
	if identityLookups != 0 {
		t.Fatalf("identity lookups = %d, want 0", identityLookups)
	}
	if provisionCalled {
		t.Fatal("provision ran for an explicitly empty resource group")
	}
}

func TestProvisionIdentityFailurePreventsProvisionAndOutput(t *testing.T) {
	root := provisionTestRepo(t)
	withWorkingDir(t, root)
	identityErr := errors.New("identity unavailable")
	provisionCalled := false
	var out bytes.Buffer

	err := provisionWithDependencies([]string{"--suite", "demo", "--location", "westus2"}, &out, provisionDependencies{
		AzureUserAlias: func(context.Context) (string, error) { return "", identityErr },
		Provision: func(context.Context, infra.ProvisionOptions) error {
			provisionCalled = true
			return nil
		},
	})
	if !errors.Is(err, identityErr) {
		t.Fatalf("provision error = %v, want identity failure", err)
	}
	if provisionCalled {
		t.Fatal("provision ran after identity lookup failed")
	}
	if out.Len() != 0 {
		t.Fatalf("provision output = %q, want empty", out.String())
	}
}

func TestProvisionRejectsPoolRelationshipsBeforeCommand(t *testing.T) {
	root := provisionTestRepo(t)
	path := filepath.Join(root, "suites", "demo", "requirements.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(data, []byte("pool: userpool"), []byte("pool: missing"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)
	oldProvision := provisionInfra
	called := false
	provisionInfra = func(context.Context, infra.ProvisionOptions) error { called = true; return nil }
	t.Cleanup(func() { provisionInfra = oldProvision })
	err = provisionWithIO([]string{"--suite", "demo", "--resource-group", "rg-demo", "--location", "westus2"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "references missing pool") {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("provision command ran after semantic validation failure")
	}
}

func TestRunSuiteClusterOverrideUsesOverrideForCredentials(t *testing.T) {
	root := provisionTestRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "images.yml"), []byte("images:\n  pause: mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)
	stop := errors.New("stop after credentials")
	var got string
	err := runSuiteWithDependencies([]string{"--suite", "demo", "--mode", "smoke", "--resource-group", "rg-demo", "--cluster-name", "existing-aks"}, runSuiteDependencies{
		GetCredentials: func(_ context.Context, _, cluster string) error { got = cluster; return stop },
	})
	if !errors.Is(err, stop) {
		t.Fatalf("runSuiteWithDependencies() error = %v, want sentinel", err)
	}
	if got != "existing-aks" {
		t.Fatalf("credentials cluster = %q, want existing-aks", got)
	}
}

func TestManagedRunSuiteRejectsExplicitEmptyResourceGroupBeforeIdentityOrAzureAccess(t *testing.T) {
	root := provisionTestRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "images.yml"), []byte("images:\n  pause: mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)
	identityLookups := 0
	stubAzureUserAlias(t, func(context.Context) (string, error) {
		identityLookups++
		return "jane-doe", nil
	})
	credentialsCalled := false

	err := runSuiteWithDependencies([]string{"--suite", "demo", "--mode", "smoke", "--resource-group", ""}, runSuiteDependencies{
		GetCredentials: func(context.Context, string, string) error { credentialsCalled = true; return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "resource-group must not be empty") {
		t.Fatalf("run-suite error = %v, want explicit-empty validation", err)
	}
	if identityLookups != 0 {
		t.Fatalf("identity lookups = %d, want 0", identityLookups)
	}
	if credentialsCalled {
		t.Fatal("run-suite refreshed credentials for an explicitly empty resource group")
	}
}

func TestManagedRunSuiteIdentityFailurePreventsAzureAndResultMutations(t *testing.T) {
	root := provisionTestRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "images.yml"), []byte("images:\n  pause: mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)
	identityErr := errors.New("identity unavailable")
	deploymentOutputCalled := false
	credentialsCalled := false

	err := runSuiteWithDependencies([]string{"--suite", "demo", "--mode", "smoke"}, runSuiteDependencies{
		AzureUserAlias: func(context.Context) (string, error) { return "", identityErr },
		DeploymentOutput: func(context.Context, string, string, string) (string, error) {
			deploymentOutputCalled = true
			return "", nil
		},
		GetCredentials: func(context.Context, string, string) error { credentialsCalled = true; return nil },
	})
	if !errors.Is(err, identityErr) {
		t.Fatalf("run-suite error = %v, want identity failure", err)
	}
	if deploymentOutputCalled {
		t.Fatal("run-suite read deployment output after identity lookup failed")
	}
	if credentialsCalled {
		t.Fatal("run-suite refreshed credentials after identity lookup failed")
	}
	if _, err := os.Stat(filepath.Join(root, "results")); !os.IsNotExist(err) {
		t.Fatalf("results directory exists after identity lookup failed: %v", err)
	}
}

func TestManagedRunSuiteOmittedResourceGroupDerivesAliasQualifiedNames(t *testing.T) {
	root := provisionTestRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "images.yml"), []byte("images:\n  pause: mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)
	stop := errors.New("stop after credentials")
	var gotResourceGroup string
	var gotClusterName string

	err := runSuiteWithDependencies([]string{"--suite", "demo", "--mode", "smoke"}, runSuiteDependencies{
		AzureUserAlias: func(context.Context) (string, error) { return "jane-doe", nil },
		GetCredentials: func(_ context.Context, resourceGroup, clusterName string) error {
			gotResourceGroup = resourceGroup
			gotClusterName = clusterName
			return stop
		},
	})
	if !errors.Is(err, stop) {
		t.Fatalf("run-suite error = %v, want credential sentinel", err)
	}
	if gotResourceGroup != "rg-aks-burner-demo-jane-doe" || gotClusterName != "aksdemojanedoe" {
		t.Fatalf("managed names = %q/%q, want alias-qualified resource group and cluster", gotResourceGroup, gotClusterName)
	}
}

func TestRunSuiteRegistryOutputsPrecedeCredentials(t *testing.T) {
	order := []string{}
	registryName, registryServer, err := prepareRunSuiteCluster(context.Background(), "rg-demo", "aksdemo", &acr.Requirements{Builds: []acr.ImageBuild{{Key: "app"}}}, true, runSuiteDependencies{
		DeploymentOutput: func(_ context.Context, _, _, output string) (string, error) {
			order = append(order, "output:"+output)
			switch output {
			case "clusterName":
				return "aksdemo", nil
			case "containerRegistryName":
				return "acrdemo", nil
			default:
				return "acrdemo.azurecr.io", nil
			}
		},
		GetCredentials: func(context.Context, string, string) error { order = append(order, "credentials"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if registryName != "acrdemo" || registryServer != "acrdemo.azurecr.io" {
		t.Fatalf("registry = %q/%q", registryName, registryServer)
	}
	want := "output:clusterName,output:containerRegistryName,output:containerRegistryLoginServer,credentials"
	if strings.Join(order, ",") != want {
		t.Fatalf("order = %q, want %q", strings.Join(order, ","), want)
	}
}

func TestRunSuiteRegistryOutputsCanSkipCredentialRefresh(t *testing.T) {
	outputs := []string{}
	registryName, registryServer, err := prepareRunSuiteCluster(context.Background(), "rg-demo", "aksdemo", &acr.Requirements{Builds: []acr.ImageBuild{{Key: "app"}}}, false, runSuiteDependencies{
		DeploymentOutput: func(_ context.Context, _, _, output string) (string, error) {
			outputs = append(outputs, output)
			switch output {
			case "clusterName":
				return "aksdemo", nil
			case "containerRegistryName":
				return "acrdemo", nil
			default:
				return "acrdemo.azurecr.io", nil
			}
		},
		GetCredentials: func(context.Context, string, string) error {
			t.Fatal("credentials called when refresh disabled")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if registryName != "acrdemo" || registryServer != "acrdemo.azurecr.io" {
		t.Fatalf("registry = %q/%q", registryName, registryServer)
	}
	if got, want := strings.Join(outputs, ","), "clusterName,containerRegistryName,containerRegistryLoginServer"; got != want {
		t.Fatalf("deployment outputs = %q, want %q", got, want)
	}
}

func TestRunSuiteRejectsImageBuildClusterMismatchBeforeRegistryAndCredentials(t *testing.T) {
	outputs := []string{}
	_, _, err := prepareRunSuiteCluster(context.Background(), "rg-demo", "requested-aks", &acr.Requirements{Builds: []acr.ImageBuild{{Key: "app"}}}, true, runSuiteDependencies{
		DeploymentOutput: func(_ context.Context, _, _, output string) (string, error) {
			outputs = append(outputs, output)
			return "deployed-aks", nil
		},
		GetCredentials: func(context.Context, string, string) error {
			t.Fatal("credentials called after cluster mismatch")
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "managed aks-burner deployment") || !strings.Contains(err.Error(), "AcrPull") {
		t.Fatalf("error = %v, want managed deployment and AcrPull guidance", err)
	}
	if got := strings.Join(outputs, ","); got != "clusterName" {
		t.Fatalf("deployment outputs = %q, want only clusterName", got)
	}
}

func TestRunSuiteRegistryOutputFailureExplainsManagedDeployment(t *testing.T) {
	_, _, err := prepareRunSuiteCluster(context.Background(), "rg-demo", "aksdemo", &acr.Requirements{Builds: []acr.ImageBuild{{Key: "app"}}}, true, runSuiteDependencies{
		DeploymentOutput: func(context.Context, string, string, string) (string, error) { return "", errors.New("missing") },
		GetCredentials:   func(context.Context, string, string) error { t.Fatal("credentials called"); return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "requires an aks-burner deployment with container registry outputs") {
		t.Fatalf("error = %v", err)
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
	target := kubetarget.Target{Context: "preview"}
	execute := func(workloadPath string, logPath string, gotTarget kubetarget.Target) error {
		if gotTarget != target {
			t.Fatalf("executor target = %#v, want %#v", gotTarget, target)
		}
		order = append(order, "execute")
		return nil
	}
	copyArtifacts := func(ctx context.Context, gotTarget kubetarget.Target, cfg artifacts.Config, destination string, subpath string) error {
		if gotTarget != target {
			t.Fatalf("copy target = %#v, want %#v", gotTarget, target)
		}
		order = append(order, "copy:"+cfg.CopyImage)
		return nil
	}
	waitArtifacts := func(ctx context.Context, gotTarget kubetarget.Target, cfg artifacts.Config) error {
		if gotTarget != target {
			t.Fatalf("wait target = %#v, want %#v", gotTarget, target)
		}
		order = append(order, "wait:"+cfg.Namespace)
		return nil
	}

	err := executeRunAndCopyArtifacts(context.Background(), target, "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, Namespace: "kata-io", CopyImage: "artifact-copy"}, map[string]string{"artifact-copy": "busybox:test"}, "artifacts", "", execute, waitArtifacts, copyArtifacts)
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
	err := executeRunAndCopyArtifacts(context.Background(), kubetarget.Target{}, "workload.yml", "kube-burner.log", artifacts.Config{}, map[string]string{}, "artifacts", "", func(workloadPath string, logPath string, target kubetarget.Target) error {
		return nil
	}, func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config) error {
		waited = true
		return nil
	}, func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config, destination string, subpath string) error {
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
	err := executeRunAndCopyArtifacts(context.Background(), kubetarget.Target{}, "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, CopyImage: "artifact-copy"}, map[string]string{"artifact-copy": "busybox:test"}, "artifacts", "kata-io-full-20260709T010203.000000004Z", func(workloadPath string, logPath string, target kubetarget.Target) error {
		return nil
	}, func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config) error {
		return nil
	}, func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config, destination string, subpath string) error {
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
	err := executeRunAndCopyArtifacts(context.Background(), kubetarget.Target{}, "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, CopyImage: "artifact-copy"}, map[string]string{"artifact-copy": "busybox:test"}, "artifacts", "", func(workloadPath string, logPath string, target kubetarget.Target) error {
		return nil
	}, func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config) error {
		return nil
	}, func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config, destination string, subpath string) error {
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
	err := executeRunAndCopyArtifacts(context.Background(), kubetarget.Target{}, "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, CopyImage: "artifact-copy"}, map[string]string{"artifact-copy": "busybox:test"}, "artifacts", "../old-run", func(workloadPath string, logPath string, target kubetarget.Target) error {
		executed = true
		return nil
	}, func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config) error {
		return nil
	}, func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config, destination string, subpath string) error {
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
	err := executeRunAndCopyArtifacts(context.Background(), kubetarget.Target{}, "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, CopyImage: "missing"}, map[string]string{}, "artifacts", "", func(workloadPath string, logPath string, target kubetarget.Target) error {
		executed = true
		return nil
	}, func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config) error {
		return nil
	}, func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config, destination string, subpath string) error {
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
	target := kubetarget.Target{Context: "preview"}
	err := executeRunAndCopyArtifacts(context.Background(), target, "workload.yml", "kube-burner.log", artifacts.Config{Enabled: true, CopyImage: "artifact-copy"}, map[string]string{"artifact-copy": "busybox:test"}, "artifacts", "", func(workloadPath string, logPath string, gotTarget kubetarget.Target) error {
		if gotTarget != target {
			t.Fatalf("executor target = %#v", gotTarget)
		}
		return executeErr
	}, func(ctx context.Context, gotTarget kubetarget.Target, cfg artifacts.Config) error {
		return nil
	}, func(ctx context.Context, gotTarget kubetarget.Target, cfg artifacts.Config, destination string, subpath string) error {
		if gotTarget != target {
			t.Fatalf("copy target = %#v", gotTarget)
		}
		return copyErr
	})
	if !errors.Is(err, executeErr) || !strings.Contains(err.Error(), "artifact copy also failed") {
		t.Fatalf("executeRunAndCopyArtifacts() error = %v, want kube-burner error with artifact copy context", err)
	}
}

func provisionTestRepo(t *testing.T) string {
	t.Helper()
	root := testRepoRoot(t)
	suiteDir := filepath.Join(root, "suites", "demo")
	if err := os.MkdirAll(filepath.Join(suiteDir, "vars"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"suite.yml": "name: demo\ndescription: Demo suite\ntests: [startup]\n",
		"requirements.yml": `suite: demo
requires:
  infrastructure:
    provider: aks
    nodePools:
      - name: systempool
        mode: System
        count: 1
        vmSize: Standard_D4s_v5
        osType: Linux
        osSKU: Ubuntu
        workloadRuntime: OCIContainer
        labels: {}
        taints: []
      - name: userpool
        mode: User
        count: 1
        vmSize: Standard_D8s_v5
        osType: Linux
        osSKU: AzureLinux
        workloadRuntime: KataMshvVmIsolation
        labels:
          perf.azure.com/node-role: workload
        taints: []
  kubernetes:
    minVersion: "1.36"
  nodeSelectors:
    - name: workload
      pool: userpool
      required: true
      minNodes: 1
      labels:
        perf.azure.com/node-role: workload
  reporting:
    sources:
      standardSummary: false
      kubeBurner: true
    prometheusMetricUnits: {}
  observability:
    prometheus:
      required: false
      install: false
      namespace: monitoring
      imageKey: prometheus
      serviceName: prometheus
      servicePort: 9090
      localPort: 9090
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(suiteDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "infra", "aks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "infra", "aks", "main.bicep"), []byte("param clusterName string\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestWaitArtifactJobsCompleteUsesTargetContext(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "kubectl.log")
	writeRecordingCommand(t, binDir, "kubectl", marker, "")
	t.Setenv("PATH", binDir)

	err := waitArtifactJobsComplete(context.Background(), kubetarget.Target{Context: "preview"}, artifacts.Config{Enabled: true, Namespace: "kata-io"})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(data)) != "--context preview wait --for=condition=complete job --all -n kata-io --timeout=15m" {
		t.Fatalf("kubectl marker = %q, error = %v", data, err)
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

func writeNoBuildContextSuite(t *testing.T, root string) {
	t.Helper()
	writeContextSuite(t, root, `  nodeSelectors:
    - name: azure-linux
      pool: userpool
      required: true
      minNodes: 1
      labels:
        kubernetes.azure.com/os-sku: AzureLinux
`, "")
}

func writeLegacyContextSuite(t *testing.T, root string) {
	t.Helper()
	writeContextSuite(t, root, "  nodeSelectors: []\n", "")
}

func writeBuildContextSuite(t *testing.T, root string) {
	t.Helper()
	images := `  images:
    builds:
      - key: benchmark
        repository: benchmark/app
        context: build
        dockerfile: Dockerfile
`
	writeContextSuite(t, root, "  nodeSelectors: []\n", images)
	suiteDir := filepath.Join(root, "suites", "existing")
	buildDir := filepath.Join(suiteDir, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeContextSuite(t *testing.T, root string, nodeSelectors string, images string) {
	t.Helper()
	suiteDir := filepath.Join(root, "suites", "existing")
	for _, dir := range []string{filepath.Join(suiteDir, "vars"), filepath.Join(suiteDir, "templates")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"suite.yml": "name: existing\ndescription: Existing cluster suite\ntests:\n  - startup\n",
		"requirements.yml": `suite: existing
requires:
  infrastructure:
    provider: aks
    nodePools:
      - name: systempool
        mode: System
        count: 1
        vmSize: Standard_D4s_v5
        osType: Linux
        osSKU: Ubuntu
        workloadRuntime: OCIContainer
        labels: {}
        taints: []
      - name: userpool
        mode: User
        count: 1
        vmSize: Standard_D8s_v5
        osType: Linux
        osSKU: AzureLinux
        workloadRuntime: OCIContainer
        labels: {}
        taints: []
  kubernetes:
    minVersion: "9.99"
` + nodeSelectors + images + `  reporting:
    sources:
      standardSummary: false
      kubeBurner: true
    prometheusMetricUnits: {}
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
		"metrics.yml":  "[]\n",
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
		if err := os.WriteFile(filepath.Join(suiteDir, name), []byte(content), 0o644); err != nil {
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

func writeAzureBuildCommand(t *testing.T, dir, marker string) {
	t.Helper()
	content := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(marker) + `
case "$*" in
  *clusterName.value*) printf '%s' aksexisting ;;
  *containerRegistryName.value*) printf '%s' acrbuild ;;
  *containerRegistryLoginServer.value*) printf '%s' acrbuild.azurecr.io ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "az"), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeAzureIdentityAndCredentialsCommand(t *testing.T, dir, marker, accountName string) {
	t.Helper()
	content := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + strconv.Quote(marker) + `
case "$*" in
  "account show --query user.name --output tsv") printf '%s' ` + strconv.Quote(accountName) + ` ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "az"), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func singleRunMetadataPath(t *testing.T, resultsDir string) string {
	t.Helper()
	entries, err := os.ReadDir(resultsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("results entries = %#v, want one run directory", entries)
	}
	return filepath.Join(resultsDir, entries[0].Name(), "metadata", "run.yml")
}

func assertEveryLineContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("%s has no recorded commands", path)
	}
	for _, line := range lines {
		if !strings.Contains(line, want) {
			t.Fatalf("%s line %q does not contain %q", path, line, want)
		}
	}
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

func stubAzureUserAlias(t *testing.T, stub azureUserAliasFunc) {
	t.Helper()
	old := currentAzureUserAlias
	currentAzureUserAlias = stub
	t.Cleanup(func() { currentAzureUserAlias = old })
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
