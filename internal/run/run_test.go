package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "metadata", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"suite: kata-perf", "mode: smoke", "clusterName: akskataperf", "pause:"} {
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
