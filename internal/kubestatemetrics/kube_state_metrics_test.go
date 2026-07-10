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
