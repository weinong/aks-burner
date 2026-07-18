package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Azure/aks-burner/internal/acr"
	"github.com/Azure/aks-burner/internal/config"
	"github.com/Azure/aks-burner/internal/kubetarget"
	"github.com/Azure/aks-burner/internal/suite"
	"gopkg.in/yaml.v3"
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

func TestModeRenderedArtifactSubpathReplacesTimestamp(t *testing.T) {
	mode := Mode{
		ArtifactSubpath: "kata-io-fio-{{.runTimestamp}}",
		RunTimestamp:    time.Date(2026, 7, 9, 1, 2, 3, 4, time.UTC),
	}
	if got, want := mode.RenderedArtifactSubpath(), "kata-io-fio-20260709T010203.000000004Z"; got != want {
		t.Fatalf("RenderedArtifactSubpath() = %q, want %q", got, want)
	}
}

func TestModeSchemaRejectsUnsupportedArtifactPlaceholder(t *testing.T) {
	root := filepath.Join("..", "..")
	path := filepath.Join(t.TempDir(), "mode.yml")
	if err := os.WriteFile(path, []byte(`iterations: 1
iterationsPerNamespace: 1
qps: 1
burst: 1
cleanup: true
waitWhenFinished: true
preLoadImages: false
artifactSubpath: run-{{.runID}}
reporting:
  scheme: standard-summary
templateVars: {}
imageVars: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "mode.schema.json"), path); err == nil {
		t.Fatal("mode schema accepted an unsupported artifact placeholder")
	}
}

func TestModeSchemaRejectsStaticArtifactSubpath(t *testing.T) {
	root := filepath.Join("..", "..")
	path := filepath.Join(t.TempDir(), "mode.yml")
	if err := os.WriteFile(path, []byte(`iterations: 1
iterationsPerNamespace: 1
qps: 1
burst: 1
cleanup: true
waitWhenFinished: true
preLoadImages: false
artifactSubpath: reused-results
reporting:
  scheme: standard-summary
templateVars: {}
imageVars: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "mode.schema.json"), path); err == nil {
		t.Fatal("mode schema accepted a static artifact subpath")
	}
}

func TestRenderWorkloadInjectsPrometheusEndpoint(t *testing.T) {
	workload := map[string]any{"global": map[string]any{}, "jobs": []any{map[string]any{"objects": []any{map[string]any{"inputVars": map[string]any{}}}}}}
	mode := Mode{Iterations: 20, IterationsPerNamespace: 20, QPS: 20, Burst: 20, Cleanup: true, WaitWhenFinished: true, PreLoadImages: true, TemplateVars: map[string]any{"app": "test"}, ImageVars: map[string]string{"image": "pause"}}
	rendered, err := RenderWorkload(workload, mode, map[string]string{"pause": "mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2"}, "http://127.0.0.1:9090", true)
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

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "http://127.0.0.1:9090", true)
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

func TestRenderWorkloadAddsLocalIndexerWithoutPrometheus(t *testing.T) {
	workload := map[string]any{"jobs": []any{}}
	rendered, err := RenderWorkload(workload, Mode{}, nil, "", true)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := rendered["metricsEndpoints"].([]any)[0].(map[string]any)
	if _, exists := endpoint["endpoint"]; exists {
		t.Fatalf("unexpected endpoint: %#v", endpoint)
	}
	indexer := endpoint["indexer"].(map[string]any)
	if indexer["type"] != "local" || indexer["metricsDirectory"] != "../raw/metrics" {
		t.Fatalf("indexer = %#v", indexer)
	}
}

func TestRenderWorkloadOmitsMetricsEndpointsWhenReportingDisabled(t *testing.T) {
	rendered, err := RenderWorkload(map[string]any{"jobs": []any{}}, Mode{}, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := rendered["metricsEndpoints"]; exists {
		t.Fatalf("unexpected endpoint: %#v", rendered)
	}
}

func TestRenderWorkloadKeepsWaitWhenFinishedJobScoped(t *testing.T) {
	workload := map[string]any{"global": map[string]any{}, "jobs": []any{map[string]any{"objects": []any{map[string]any{"inputVars": map[string]any{}}}}}}
	mode := Mode{Iterations: 20, IterationsPerNamespace: 20, QPS: 20, Burst: 20, Cleanup: true, WaitWhenFinished: true, PreLoadImages: true, TemplateVars: map[string]any{"app": "test"}, ImageVars: map[string]string{"image": "pause"}}

	rendered, err := RenderWorkload(workload, mode, map[string]string{"pause": "mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2"}, "", false)
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
				"jobPause":               "30s",
				"metricsClosing":         "afterJobPause",
				"objects": []any{
					map[string]any{"objectTemplate": "templates/job.yml", "replicas": 1, "inputVars": map[string]any{}},
				},
			},
		},
	}
	mode := Mode{Iterations: 1, IterationsPerNamespace: 1, QPS: 1, Burst: 1, Cleanup: true, WaitWhenFinished: true, PreLoadImages: true, JobPause: "6m", MetricsClosing: "afterJob"}
	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "", false)
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
		"jobPause":               "30s",
		"metricsClosing":         "afterJobPause",
	}
	for key, want := range checks {
		if got := job[key]; got != want {
			t.Fatalf("job[%s] = %#v, want %#v", key, got, want)
		}
	}
}

func TestRenderWorkloadAppliesOptionalMeasurementDrainDefaults(t *testing.T) {
	workload := map[string]any{
		"jobs": []any{
			map[string]any{
				"name": "startup",
				"objects": []any{
					map[string]any{"objectTemplate": "templates/pod.yml", "replicas": 1, "inputVars": map[string]any{}},
				},
			},
		},
	}
	mode := Mode{JobPause: "6m", MetricsClosing: "afterJob"}

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "", false)
	if err != nil {
		t.Fatal(err)
	}

	job := rendered["jobs"].([]any)[0].(map[string]any)
	if got, want := job["jobPause"], "6m"; got != want {
		t.Fatalf("jobPause = %#v, want %#v", got, want)
	}
	if got, want := job["metricsClosing"], "afterJob"; got != want {
		t.Fatalf("metricsClosing = %#v, want %#v", got, want)
	}
}

func TestRenderWorkloadOmitsUnsetMeasurementDrainDefaults(t *testing.T) {
	workload := map[string]any{
		"jobs": []any{
			map[string]any{
				"name": "startup",
				"objects": []any{
					map[string]any{"objectTemplate": "templates/pod.yml", "replicas": 1, "inputVars": map[string]any{}},
				},
			},
		},
	}

	rendered, err := RenderWorkload(workload, Mode{}, map[string]string{}, "", false)
	if err != nil {
		t.Fatal(err)
	}

	job := rendered["jobs"].([]any)[0].(map[string]any)
	if _, exists := job["jobPause"]; exists {
		t.Fatalf("unexpected jobPause default: %#v", job)
	}
	if _, exists := job["metricsClosing"]; exists {
		t.Fatalf("unexpected metricsClosing default: %#v", job)
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

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "", false)
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

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "", false)
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

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "", false)
	if err != nil {
		t.Fatal(err)
	}

	objects := rendered["jobs"].([]any)[0].(map[string]any)["objects"].([]any)
	inputVars := objects[0].(map[string]any)["inputVars"].(map[string]any)
	if got, want := inputVars["k8sRunID"], "kio-smoke-20260709t010203000000004"; got != want {
		t.Fatalf("k8sRunID = %q, want %q", got, want)
	}
}

func TestModeCarriesReportingScheme(t *testing.T) {
	var mode Mode
	if err := yaml.Unmarshal([]byte("reporting:\n  scheme: storage-startup\n"), &mode); err != nil {
		t.Fatal(err)
	}
	if !mode.Reporting.Scheme.ReportsStorageStartup() {
		t.Fatal("storage-startup reporting scheme was not decoded")
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

	rendered, err := RenderWorkload(workload, mode, map[string]string{}, "", false)
	if err != nil {
		t.Fatal(err)
	}

	objects := rendered["jobs"].([]any)[0].(map[string]any)["objects"].([]any)
	inputVars := objects[0].(map[string]any)["inputVars"].(map[string]any)
	if got, want := inputVars["jobName"], "kata-io-smoke-20260709T010203.000000004Z-fio"; got != want {
		t.Fatalf("jobName = %q, want %q", got, want)
	}
}

func TestRenderWorkloadReplacesTimestampPlaceholderFromRawTemplate(t *testing.T) {
	workload := map[string]any{
		"jobs": []any{
			map[string]any{
				"objects": []any{
					map[string]any{"objectTemplate": "templates/job.yml", "replicas": 1, "inputVars": map[string]any{"jobName": "kio-fio-{{.runTimestampDNS}}-job"}},
				},
			},
		},
	}
	mode := Mode{
		RunTimestamp: time.Date(2026, 7, 9, 1, 2, 3, 4, time.UTC),
		TemplateVars: map[string]any{"k8sRunID": "kio-fio-{{.runTimestampDNS}}"},
	}

	rendered, err := RenderWorkload(workload, mode, nil, "", false)
	if err != nil {
		t.Fatal(err)
	}

	objects := rendered["jobs"].([]any)[0].(map[string]any)["objects"].([]any)
	inputVars := objects[0].(map[string]any)["inputVars"].(map[string]any)
	if got, want := inputVars["jobName"], "kio-fio-20260709t010203000000004-job"; got != want {
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
	if err := os.MkdirAll(filepath.Join(suiteDir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "hooks", "cleanup.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
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
	if _, err := os.Stat(filepath.Join(runDir, "rendered", "hooks", "cleanup.sh")); err != nil {
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

	if err := ExecuteKubeBurner(workloadPath, logPath, kubetarget.Target{Context: "preview"}); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "repo-local init -c workload.yml --kube-context preview") {
		t.Fatalf("log missing repo-local kube-burner output: %s", logText)
	}
	if strings.Contains(logText, "path-binary") {
		t.Fatalf("log used PATH kube-burner instead of repo-local binary: %s", logText)
	}
}

func TestExecuteKubeBurnerExportsKubeContextForHooks(t *testing.T) {
	repoDir := t.TempDir()
	writeFakeRepoRoot(t, repoDir)
	pathDir := t.TempDir()
	path := filepath.Join(pathDir, "kube-burner")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'context=%s\\n' \"$AKS_BURNER_KUBE_CONTEXT\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
	if err := ExecuteKubeBurner(workloadPath, logPath, kubetarget.Target{Context: "preview"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "context=preview") {
		t.Fatalf("kube-burner environment = %s", data)
	}
}

func TestValidateStorageClassesReturnsMetadata(t *testing.T) {
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		if ctx.Value("request") != "storage" {
			t.Fatal("context was not propagated")
		}
		return []byte(`{"items":[{"metadata":{"name":"managed-csi"},"provisioner":"disk.csi.azure.com","reclaimPolicy":"Delete","volumeBindingMode":"WaitForFirstConsumer","parameters":{"skuName":"Premium_LRS"}},{"metadata":{"name":"azurefile-csi"},"provisioner":"file.csi.azure.com","reclaimPolicy":"Delete","volumeBindingMode":"Immediate","parameters":{"skuName":"Standard_LRS"}}]}`), nil
	}
	ctx := context.WithValue(context.Background(), "request", "storage")
	got, err := ValidateStorageClasses(ctx, []StorageClassRequirement{{Name: "managed-csi", Provisioner: "disk.csi.azure.com"}, {Name: "azurefile-csi", Provisioner: "file.csi.azure.com"}}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].VolumeBindingMode != "WaitForFirstConsumer" || got[0].Parameters["skuName"] != "Premium_LRS" {
		t.Fatalf("storage metadata = %#v", got)
	}
}

func TestValidateStorageClassesRejectsUnsafeClass(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
		want string
	}{
		{name: "wrong provisioner", json: `{"items":[{"metadata":{"name":"managed-csi"},"provisioner":"kubernetes.io/azure-disk","reclaimPolicy":"Delete"}]}`, want: "disk.csi.azure.com"},
		{name: "retained volumes", json: `{"items":[{"metadata":{"name":"managed-csi"},"provisioner":"disk.csi.azure.com","reclaimPolicy":"Retain"}]}`, want: "Delete"},
		{name: "missing class", json: `{"items":[]}`, want: "managed-csi"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateStorageClasses(context.Background(), []StorageClassRequirement{{Name: "managed-csi", Provisioner: "disk.csi.azure.com"}}, func(context.Context, ...string) ([]byte, error) {
				return []byte(tc.json), nil
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateStorageClasses() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateStorageClassesRejectsMissingRequirementsBeforeAPI(t *testing.T) {
	called := false
	_, err := ValidateStorageClasses(context.Background(), nil, func(context.Context, ...string) ([]byte, error) {
		called = true
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "declared StorageClasses") || called {
		t.Fatalf("ValidateStorageClasses() error/called = %v/%v", err, called)
	}
}

func TestAcquireStorageRunLockCreatesAndReleasesAtomically(t *testing.T) {
	var calls [][]string
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		if ctx.Err() != nil {
			t.Fatalf("runner received canceled context: %v", ctx.Err())
		}
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "create" {
			return []byte(`{"metadata":{"uid":"lock-uid"},"data":{"holder":"storage-smoke-20260717"}}`), nil
		}
		return nil, nil
	}
	release, err := AcquireStorageRunLock(context.Background(), "storage-smoke-20260717", runner)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !strings.Contains(strings.Join(calls[0], " "), "create configmap aks-burner-storage-startup-lock") || !strings.Contains(strings.Join(calls[0], " "), "--labels=aks-burner.azure.com/storage-lock=") || !strings.Contains(strings.Join(calls[1], " "), "delete configmap -l aks-burner.azure.com/storage-lock=") || strings.Contains(strings.Join(calls[1], " "), "aks-burner-storage-startup-lock") {
		t.Fatalf("lock calls = %#v", calls)
	}
}

func TestStorageRunLockReleaseDoesNotDeleteReplacementOwner(t *testing.T) {
	var createArgs, releaseArgs []string
	release, err := AcquireStorageRunLock(context.Background(), "original", func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "create" {
			createArgs = append([]string(nil), args...)
			return []byte(`{"metadata":{"uid":"original-uid"},"data":{"holder":"original"}}`), nil
		}
		releaseArgs = append([]string(nil), args...)
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	create := strings.Join(createArgs, " ")
	releaseCommand := strings.Join(releaseArgs, " ")
	createToken := strings.TrimPrefix(create[strings.Index(create, "--labels="):], "--labels=aks-burner.azure.com/storage-lock=")
	if index := strings.IndexByte(createToken, ' '); index >= 0 {
		createToken = createToken[:index]
	}
	if !strings.Contains(releaseCommand, "-l aks-burner.azure.com/storage-lock="+createToken) || strings.Contains(releaseCommand, "aks-burner-storage-startup-lock") {
		t.Fatalf("create/release = %q/%q, want holder-scoped selector deletion", create, releaseCommand)
	}
}

func TestAcquireStorageRunLockReportsContentionAndManualRecovery(t *testing.T) {
	_, err := AcquireStorageRunLock(context.Background(), "new-run", func(context.Context, ...string) ([]byte, error) {
		return nil, fmt.Errorf("AlreadyExists")
	})
	if err == nil || !strings.Contains(err.Error(), "another storage run") || !strings.Contains(err.Error(), "delete configmap") || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("AcquireStorageRunLock() error = %v", err)
	}
}

func TestAcquireStorageRunLockPropagatesContext(t *testing.T) {
	type key string
	ctx := context.WithValue(context.Background(), key("request"), "storage-lock")
	release, err := AcquireStorageRunLock(ctx, "holder", func(got context.Context, args ...string) ([]byte, error) {
		if args[0] == "create" && got.Value(key("request")) != "storage-lock" {
			t.Fatal("acquire context was not propagated")
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireStorageRunLockHonorsExplicitKubeContext(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "kubectl.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MARKER\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MARKER", marker)
	target := kubetarget.Target{Context: "preview"}
	release, err := AcquireStorageRunLock(context.Background(), "holder", target.Output)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.HasPrefix(line, "--context preview ") {
			t.Fatalf("kubectl call omitted explicit context: %q", line)
		}
	}
}

func TestWithStorageRunLockScopesLifecycleToStorageModes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		want    string
	}{
		{name: "storage", enabled: true, want: "create,execute,delete"},
		{name: "ordinary", enabled: false, want: "execute"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			runner := func(_ context.Context, args ...string) ([]byte, error) {
				order = append(order, args[0])
				return nil, nil
			}
			if err := WithStorageRunLock(context.Background(), tc.enabled, "holder", runner, func() error {
				order = append(order, "execute")
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(order, ","); got != tc.want {
				t.Fatalf("order = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWithStorageRunLockReleasesAfterWorkloadFailure(t *testing.T) {
	workloadErr := fmt.Errorf("workload failed")
	deleted := false
	err := WithStorageRunLock(context.Background(), true, "holder", func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "delete" {
			deleted = true
		}
		return nil, nil
	}, func() error { return workloadErr })
	if err != workloadErr || !deleted {
		t.Fatalf("WithStorageRunLock() error/deleted = %v/%v", err, deleted)
	}
}

func TestWriteMetadataIncludesStorageClasses(t *testing.T) {
	runDir := t.TempDir()
	metadata := Metadata{StorageClasses: []StorageClassMetadata{{Name: "managed-csi", Provisioner: "disk.csi.azure.com", ReclaimPolicy: "Delete", VolumeBindingMode: "WaitForFirstConsumer", Parameters: map[string]string{"skuName": "Premium_LRS"}}}}
	if err := WriteMetadata(runDir, metadata); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "metadata", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"storageClasses:", "managed-csi", "disk.csi.azure.com", "WaitForFirstConsumer", "Premium_LRS"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("metadata missing %q: %s", want, data)
		}
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

	if err := ExecuteKubeBurner(workloadPath, logPath, kubetarget.Target{}); err != nil {
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
	if strings.Contains(logText, "--kube-context") {
		t.Fatalf("legacy kube-burner output contains context flag: %s", logText)
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

	if err := ExecuteKubeBurner(workloadPath, logPath, kubetarget.Target{}); err != nil {
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

func TestNodeSelectorRequirementLoadsPool(t *testing.T) {
	var selector NodeSelectorRequirement
	if err := yaml.Unmarshal([]byte("name: workload\npool: userpool\n"), &selector); err != nil {
		t.Fatal(err)
	}
	if selector.Pool != "userpool" {
		t.Fatalf("Pool = %q, want userpool", selector.Pool)
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
