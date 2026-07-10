package kubestatemetrics

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/kubetarget"
)

func TestRenderManifestReplacesImage(t *testing.T) {
	manifest := "image: {{KUBE_STATE_METRICS_IMAGE}}\n"
	rendered := RenderManifest(manifest, "mcr.microsoft.com/oss/v2/kubernetes/kube-state-metrics:v2.19.0")
	want := "image: mcr.microsoft.com/oss/v2/kubernetes/kube-state-metrics:v2.19.0\n"
	if rendered != want {
		t.Fatalf("RenderManifest() = %q, want %q", rendered, want)
	}
}

func TestRolloutStatusArgs(t *testing.T) {
	args := RolloutStatusArgs(kubetarget.Target{Context: "preview"}, Config{Namespace: "perf-monitoring"})
	want := []string{"kubectl", "--context", "preview", "rollout", "status", "deployment/kube-state-metrics", "-n", "perf-monitoring", "--timeout=2m"}
	if len(args) != len(want) {
		t.Fatalf("args length = %d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestRolloutStatusArgsWithEmptyTargetPreservesCommand(t *testing.T) {
	got := RolloutStatusArgs(kubetarget.Target{}, Config{Namespace: "perf-monitoring"})
	want := []string{"kubectl", "rollout", "status", "deployment/kube-state-metrics", "-n", "perf-monitoring", "--timeout=2m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RolloutStatusArgs() = %#v, want %#v", got, want)
	}
}

func TestInstallTargetsKubectlApply(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "manifest.yml")
	if err := os.WriteFile(manifestPath, []byte("image: {{KUBE_STATE_METRICS_IMAGE}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var command []string
	var stdin string
	runner := func(_ context.Context, input string, args ...string) error {
		stdin = input
		command = append([]string(nil), args...)
		return nil
	}
	if err := installWithRunner(context.Background(), kubetarget.Target{Context: "preview"}, manifestPath, "kube-state-metrics:test", runner); err != nil {
		t.Fatal(err)
	}
	want := []string{"kubectl", "--context", "preview", "apply", "-f", "-"}
	if !reflect.DeepEqual(command, want) || !strings.Contains(stdin, "image: kube-state-metrics:test") {
		t.Fatalf("command = %#v, stdin = %q", command, stdin)
	}
}
