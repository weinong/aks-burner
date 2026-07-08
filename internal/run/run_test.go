package run

import (
	"os"
	"path/filepath"
	"testing"
)

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
