package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Azure/aks-burner/internal/acr"
	"github.com/Azure/aks-burner/internal/suite"
)

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

func TestRenderWorkloadInjectsPrometheusEndpoint(t *testing.T) {
	workload := map[string]any{"global": map[string]any{}, "jobs": []any{map[string]any{"objects": []any{map[string]any{"inputVars": map[string]any{}}}}}}
	mode := Mode{Iterations: 20, IterationsPerNamespace: 20, QPS: 20, Burst: 20, Cleanup: true, WaitWhenFinished: true, PreLoadImages: true, TemplateVars: map[string]any{"app": "test"}, ImageVars: map[string]string{"image": "pause"}}
	rendered, err := RenderWorkload(workload, mode, map[string]string{"pause": "mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2"}, "http://127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	endpoints := rendered["metricsEndpoints"].([]any)
	endpoint := endpoints[0].(map[string]any)
	if endpoint["endpoint"] != "http://127.0.0.1:9090" {
		t.Fatalf("endpoint not injected: %#v", endpoint)
	}
	objects := rendered["jobs"].([]any)[0].(map[string]any)["objects"].([]any)
	inputVars := objects[0].(map[string]any)["inputVars"].(map[string]any)
	if inputVars["image"] != "mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2" {
		t.Fatalf("image key was not resolved: %#v", inputVars)
	}
}

func TestRenderWorkloadWritesPrometheusMetricsToRunRoot(t *testing.T) {
	workload := map[string]any{"global": map[string]any{}, "jobs": []any{map[string]any{"objects": []any{map[string]any{"inputVars": map[string]any{}}}}}}
	mode := Mode{Iterations: 1, IterationsPerNamespace: 1, QPS: 1, Burst: 1, Cleanup: true, WaitWhenFinished: true, PreLoadImages: true}

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "http://127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}

	endpoints := rendered["metricsEndpoints"].([]any)
	endpoint := endpoints[0].(map[string]any)
	indexer := endpoint["indexer"].(map[string]any)
	if got, want := indexer["metricsDirectory"], "../raw/metrics"; got != want {
		t.Fatalf("metricsDirectory = %#v, want %#v", got, want)
	}
}

func TestRenderWorkloadSkipsPrometheusEndpointWhenEmpty(t *testing.T) {
	workload := map[string]any{"global": map[string]any{}, "jobs": []any{map[string]any{"objects": []any{map[string]any{"inputVars": map[string]any{}}}}}}
	mode := Mode{Iterations: 20, IterationsPerNamespace: 20, QPS: 20, Burst: 20, Cleanup: true, WaitWhenFinished: true, PreLoadImages: true, TemplateVars: map[string]any{"app": "test"}, ImageVars: map[string]string{"image": "pause"}}

	rendered, err := RenderWorkload(workload, mode, map[string]string{"pause": "mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rendered["metricsEndpoints"]; ok {
		t.Fatalf("metrics endpoint injected for empty prometheus endpoint: %#v", rendered["metricsEndpoints"])
	}
}

func TestRenderWorkloadKeepsWaitWhenFinishedJobScoped(t *testing.T) {
	workload := map[string]any{"global": map[string]any{}, "jobs": []any{map[string]any{"objects": []any{map[string]any{"inputVars": map[string]any{}}}}}}
	mode := Mode{Iterations: 20, IterationsPerNamespace: 20, QPS: 20, Burst: 20, Cleanup: true, WaitWhenFinished: true, PreLoadImages: true, TemplateVars: map[string]any{"app": "test"}, ImageVars: map[string]string{"image": "pause"}}

	rendered, err := RenderWorkload(workload, mode, map[string]string{"pause": "mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	global := rendered["global"].(map[string]any)
	if _, ok := global["waitWhenFinished"]; ok {
		t.Fatalf("global waitWhenFinished disables useful podLatency waiting: %#v", global)
	}
	job := rendered["jobs"].([]any)[0].(map[string]any)
	if job["waitWhenFinished"] != true {
		t.Fatalf("job waitWhenFinished = %#v, want true", job["waitWhenFinished"])
	}
}

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

func TestRenderWorkloadReplacesRunTimestampPlaceholder(t *testing.T) {
	workload := map[string]any{
		"jobs": []any{
			map[string]any{
				"objects": []any{
					map[string]any{"objectTemplate": "templates/job.yml", "replicas": 1, "inputVars": map[string]any{}},
				},
			},
		},
	}
	mode := Mode{TemplateVars: map[string]any{"runID": "kata-io-full-{{.runTimestamp}}"}}

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}

	objects := rendered["jobs"].([]any)[0].(map[string]any)["objects"].([]any)
	inputVars := objects[0].(map[string]any)["inputVars"].(map[string]any)
	runID, ok := inputVars["runID"].(string)
	if !ok {
		t.Fatalf("runID = %#v, want string", inputVars["runID"])
	}
	if !strings.HasPrefix(runID, "kata-io-full-") {
		t.Fatalf("runID = %q, want kata-io-full prefix", runID)
	}
	timestamp := strings.TrimPrefix(runID, "kata-io-full-")
	if _, err := time.Parse("20060102T150405.000000000Z", timestamp); err != nil {
		t.Fatalf("runID timestamp %q is not parseable: %v", timestamp, err)
	}
}

func TestRenderWorkloadUsesModeRunTimestampForPlaceholder(t *testing.T) {
	workload := map[string]any{
		"jobs": []any{
			map[string]any{
				"objects": []any{
					map[string]any{"objectTemplate": "templates/job.yml", "replicas": 1, "inputVars": map[string]any{}},
				},
			},
		},
	}
	mode := Mode{
		RunTimestamp: time.Date(2026, 7, 9, 1, 2, 3, 4, time.UTC),
		TemplateVars: map[string]any{"runID": "kata-io-full-{{.runTimestamp}}"},
	}

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}

	objects := rendered["jobs"].([]any)[0].(map[string]any)["objects"].([]any)
	inputVars := objects[0].(map[string]any)["inputVars"].(map[string]any)
	if got, want := inputVars["runID"], "kata-io-full-20260709T010203.000000004Z"; got != want {
		t.Fatalf("runID = %q, want %q", got, want)
	}
}

func TestRenderWorkloadUsesDNSRunTimestampPlaceholder(t *testing.T) {
	workload := map[string]any{
		"jobs": []any{
			map[string]any{
				"objects": []any{
					map[string]any{"objectTemplate": "templates/job.yml", "replicas": 1, "inputVars": map[string]any{}},
				},
			},
		},
	}
	mode := Mode{
		RunTimestamp: time.Date(2026, 7, 9, 1, 2, 3, 4, time.UTC),
		TemplateVars: map[string]any{"k8sRunID": "kio-smoke-{{.runTimestampDNS}}"},
	}

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}

	objects := rendered["jobs"].([]any)[0].(map[string]any)["objects"].([]any)
	inputVars := objects[0].(map[string]any)["inputVars"].(map[string]any)
	if got, want := inputVars["k8sRunID"], "kio-smoke-20260709t010203000000004"; got != want {
		t.Fatalf("k8sRunID = %q, want %q", got, want)
	}
}

func TestRenderWorkloadReplacesInputVarTemplateVars(t *testing.T) {
	workload := map[string]any{
		"jobs": []any{
			map[string]any{
				"objects": []any{
					map[string]any{"objectTemplate": "templates/job.yml", "replicas": 1, "inputVars": map[string]any{"jobName": "{{.runID}}-fio"}},
				},
			},
		},
	}
	mode := Mode{
		RunTimestamp: time.Date(2026, 7, 9, 1, 2, 3, 4, time.UTC),
		TemplateVars: map[string]any{"runID": "kata-io-smoke-{{.runTimestamp}}"},
	}

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}

	objects := rendered["jobs"].([]any)[0].(map[string]any)["objects"].([]any)
	inputVars := objects[0].(map[string]any)["inputVars"].(map[string]any)
	if got, want := inputVars["jobName"], "kata-io-smoke-20260709T010203.000000004Z-fio"; got != want {
		t.Fatalf("jobName = %q, want %q", got, want)
	}
}

func TestRunDirNameIncludesNanoseconds(t *testing.T) {
	base := time.Date(2026, 7, 8, 1, 2, 3, 4, time.UTC)
	first := runDirName("kata-perf", "smoke", base)
	second := runDirName("kata-perf", "smoke", base.Add(time.Nanosecond))
	if first == second {
		t.Fatalf("runDirName returned duplicate names for distinct nanoseconds: %q", first)
	}
	if !strings.Contains(first, "kata-perf_smoke") {
		t.Fatalf("runDirName() = %q, want suite and mode suffix", first)
	}
}

func TestCopyRenderAssetsCopiesTemplatesAndMetrics(t *testing.T) {
	suiteDir := t.TempDir()
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(suiteDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "templates", "pod.yml"), []byte("kind: Pod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "metrics.yml"), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CopyRenderAssets(suiteDir, runDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "rendered", "templates", "pod.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "rendered", "metrics.yml")); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteKubeBurnerPrefersRepoLocalBinary(t *testing.T) {
	repoDir := t.TempDir()
	writeFakeRepoRoot(t, repoDir)

	writeFakeKubeBurner(t, filepath.Join(repoDir, "bin", "kube-burner"), "repo-local")
	pathDir := t.TempDir()
	writeFakeKubeBurner(t, filepath.Join(pathDir, "kube-burner"), "path-binary")
	t.Setenv("PATH", pathDir)

	workloadPath := filepath.Join(repoDir, "results", "run", "rendered", "workload.yml")
	logPath := filepath.Join(repoDir, "results", "run", "logs", "kube-burner.log")
	if err := os.MkdirAll(filepath.Dir(workloadPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workloadPath, []byte("jobs: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ExecuteKubeBurner(workloadPath, logPath); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "repo-local init -c workload.yml") {
		t.Fatalf("log missing repo-local kube-burner output: %s", logText)
	}
	if strings.Contains(logText, "path-binary") {
		t.Fatalf("log used PATH kube-burner instead of repo-local binary: %s", logText)
	}
}

func TestExecuteKubeBurnerFallsBackToPathBinary(t *testing.T) {
	repoDir := t.TempDir()
	writeFakeRepoRoot(t, repoDir)

	pathDir := t.TempDir()
	writeFakeKubeBurner(t, filepath.Join(pathDir, "kube-burner"), "path-binary")
	t.Setenv("PATH", pathDir)

	workloadPath := filepath.Join(repoDir, "results", "run", "rendered", "workload.yml")
	logPath := filepath.Join(repoDir, "results", "run", "logs", "kube-burner.log")
	if err := os.MkdirAll(filepath.Dir(workloadPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workloadPath, []byte("jobs: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ExecuteKubeBurner(workloadPath, logPath); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "path-binary init -c workload.yml") {
		t.Fatalf("log missing PATH kube-burner output: %s", logText)
	}
}

func TestExecuteKubeBurnerFallsBackWhenRepoLocalBinaryIsNotExecutable(t *testing.T) {
	repoDir := t.TempDir()
	writeFakeRepoRoot(t, repoDir)

	writeFakeKubeBurnerWithMode(t, filepath.Join(repoDir, "bin", "kube-burner"), "repo-local", 0o644)
	pathDir := t.TempDir()
	writeFakeKubeBurner(t, filepath.Join(pathDir, "kube-burner"), "path-binary")
	t.Setenv("PATH", pathDir)

	workloadPath := filepath.Join(repoDir, "results", "run", "rendered", "workload.yml")
	logPath := filepath.Join(repoDir, "results", "run", "logs", "kube-burner.log")
	if err := os.MkdirAll(filepath.Dir(workloadPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workloadPath, []byte("jobs: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ExecuteKubeBurner(workloadPath, logPath); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "path-binary init -c workload.yml") {
		t.Fatalf("log missing PATH kube-burner output: %s", logText)
	}
	if strings.Contains(logText, "repo-local") {
		t.Fatalf("log used non-executable repo-local binary: %s", logText)
	}
}

func TestValidateRequirementsFailsWhenKubernetesVersionTooLow(t *testing.T) {
	req := Requirements{Kubernetes: KubernetesRequirements{MinVersion: "1.30"}}
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		return []byte(`{"serverVersion":{"gitVersion":"v1.29.9"}}`), nil
	}
	err := ValidateRequirements(context.Background(), req, runner)
	if err == nil || !strings.Contains(err.Error(), "Kubernetes version") {
		t.Fatalf("ValidateRequirements() error = %v, want Kubernetes version error", err)
	}
}

func writeFakeRepoRoot(t *testing.T, repoDir string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFakeKubeBurner(t *testing.T, path string, marker string) {
	t.Helper()
	writeFakeKubeBurnerWithMode(t, path, marker, 0o755)
}

func writeFakeKubeBurnerWithMode(t *testing.T, path string, marker string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '" + marker + " %s\\n' \"$*\"\n"
	if err := os.WriteFile(path, []byte(script), mode); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRequirementsFailsWhenRequiredNodeSelectorHasTooFewNodes(t *testing.T) {
	req := Requirements{NodeSelectors: []NodeSelectorRequirement{{Name: "workload", Required: true, MinNodes: 2, Labels: map[string]string{"perf.azure.com/node-role": "workload"}}}}
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "get" && args[1] == "nodes" {
			return []byte("node/node-1\n"), nil
		}
		return []byte(`{"serverVersion":{"gitVersion":"v1.30.1"}}`), nil
	}
	err := ValidateRequirements(context.Background(), req, runner)
	if err == nil || !strings.Contains(err.Error(), "node selector workload") {
		t.Fatalf("ValidateRequirements() error = %v, want node selector error", err)
	}
}

func TestNodeSelectorArgsSortsLabels(t *testing.T) {
	args := NodeSelectorArgs(map[string]string{"z": "last", "a": "first"})
	want := []string{"get", "nodes", "-l", "a=first,z=last", "-o", "name"}
	if len(args) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestWriteMetadataWritesSafeRunMetadata(t *testing.T) {
	runDir := t.TempDir()
	err := WriteMetadata(runDir, Metadata{
		Suite:         "kata-perf",
		Mode:          "smoke",
		Timestamp:     "2026-07-08T00:00:00Z",
		ResourceGroup: "rg-aks-burner-kata-perf",
		ClusterName:   "akskataperf",
		Images:        map[string]string{"pause": "mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2"},
		BuiltImages: []acr.BuiltImage{{
			Key:        "kata-pause",
			Image:      "acrakskataperf.azurecr.io/kata-perf/pause:kata-perf-smoke-20260709T010203Z",
			Repository: "kata-perf/pause",
			Tag:        "kata-perf-smoke-20260709T010203Z",
			Context:    "images/pause",
			Dockerfile: "Dockerfile",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "metadata", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"suite: kata-perf", "mode: smoke", "clusterName: akskataperf", "pause:", "builtImages:", "kata-pause", "acrakskataperf.azurecr.io/kata-perf/pause:kata-perf-smoke-20260709T010203Z"} {
		if !strings.Contains(text, want) {
			t.Fatalf("metadata missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"kubeconfig", "bearer", "token", "Authorization"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("metadata contains forbidden auth material %q: %s", forbidden, text)
		}
	}
}

func TestWriteMetadataIncludesSetup(t *testing.T) {
	runDir := t.TempDir()
	metadata := Metadata{
		Suite: "kata-perf",
		Mode:  "smoke",
		Setup: suite.Setup{Resources: []suite.SetupResource{{
			Name: "node-prep",
			Path: "setup/node-prep-daemonset.yml",
			Wait: []suite.WaitRule{{Kind: "rollout", Resource: "daemonset/node-prep", Namespace: "kube-system", Timeout: "10m"}},
		}}},
	}
	if err := WriteMetadata(runDir, metadata); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "metadata", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"setup:", "node-prep", "setup/node-prep-daemonset.yml", "daemonset/node-prep", "kube-system"} {
		if !strings.Contains(text, want) {
			t.Fatalf("metadata missing %q:\n%s", want, text)
		}
	}
}
