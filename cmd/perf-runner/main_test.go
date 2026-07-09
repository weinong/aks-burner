package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/acr"
	"github.com/Azure/aks-burner/internal/config"
)

var testSourceRoot = mustTestSourceRoot()

func TestRunDispatchesRunSuite(t *testing.T) {
	err := run([]string{"run-suite"})
	if err == nil || !strings.Contains(err.Error(), "usage: perf-runner run-suite") {
		t.Fatalf("run-suite dispatch error = %v", err)
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
