package prometheus

import "testing"

func TestEndpointURL(t *testing.T) {
	cfg := Config{LocalPort: 9090}
	if got := EndpointURL(cfg); got != "http://127.0.0.1:9090" {
		t.Fatalf("EndpointURL() = %q", got)
	}
}

func TestPortForwardArgs(t *testing.T) {
	cfg := Config{Namespace: "perf-monitoring", ServiceName: "prometheus", ServicePort: 9090, LocalPort: 19090}
	args := PortForwardArgs(cfg)
	want := []string{"kubectl", "-n", "perf-monitoring", "port-forward", "service/prometheus", "19090:9090"}
	if len(args) != len(want) {
		t.Fatalf("args length = %d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestRenderManifestReplacesPrometheusImage(t *testing.T) {
	manifest := "image: {{PROMETHEUS_IMAGE}}\n"
	rendered := RenderManifest(manifest, "mcr.microsoft.com/oss/v2/prometheus/prometheus:v3.11.3")
	if rendered != "image: mcr.microsoft.com/oss/v2/prometheus/prometheus:v3.11.3\n" {
		t.Fatalf("unexpected manifest: %q", rendered)
	}
}
