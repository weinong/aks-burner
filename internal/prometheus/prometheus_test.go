package prometheus

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Azure/aks-burner/internal/kubetarget"
)

func TestEndpointURL(t *testing.T) {
	cfg := Config{LocalPort: 9090}
	if got := EndpointURL(cfg); got != "http://127.0.0.1:9090" {
		t.Fatalf("EndpointURL() = %q", got)
	}
}

func TestPortForwardArgs(t *testing.T) {
	cfg := Config{Namespace: "perf-monitoring", ServiceName: "prometheus", ServicePort: 9090, LocalPort: 19090}
	args := PortForwardArgs(kubetarget.Target{Context: "preview"}, cfg)
	want := []string{"kubectl", "--context", "preview", "-n", "perf-monitoring", "port-forward", "service/prometheus", "19090:9090"}
	if len(args) != len(want) {
		t.Fatalf("args length = %d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestPortForwardArgsWithEmptyTargetPreservesCommand(t *testing.T) {
	cfg := Config{Namespace: "perf-monitoring", ServiceName: "prometheus", ServicePort: 9090, LocalPort: 19090}
	got := PortForwardArgs(kubetarget.Target{}, cfg)
	want := []string{"kubectl", "-n", "perf-monitoring", "port-forward", "service/prometheus", "19090:9090"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PortForwardArgs() = %#v, want %#v", got, want)
	}
}

func TestRolloutStatusArgs(t *testing.T) {
	args := RolloutStatusArgs(kubetarget.Target{Context: "preview"}, Config{Namespace: "perf-monitoring"})
	want := []string{"kubectl", "--context", "preview", "rollout", "status", "deployment/prometheus", "-n", "perf-monitoring", "--timeout=2m"}
	if len(args) != len(want) {
		t.Fatalf("args length = %d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestInstallTargetsKubectlApply(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "manifest.yml")
	if err := os.WriteFile(manifestPath, []byte("image: {{PROMETHEUS_IMAGE}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var command []string
	var stdin string
	runner := func(_ context.Context, input string, args ...string) error {
		stdin = input
		command = append([]string(nil), args...)
		return nil
	}
	if err := installWithRunner(context.Background(), kubetarget.Target{Context: "preview"}, manifestPath, "prometheus:test", "", runner); err != nil {
		t.Fatal(err)
	}
	want := []string{"kubectl", "--context", "preview", "apply", "-f", "-"}
	if !reflect.DeepEqual(command, want) || !strings.Contains(stdin, "image: prometheus:test") {
		t.Fatalf("command = %#v, stdin = %q", command, stdin)
	}
}

func TestRenderManifestReplacesPrometheusImage(t *testing.T) {
	manifest := "image: {{PROMETHEUS_IMAGE}}\n"
	rendered := RenderManifest(manifest, "mcr.microsoft.com/oss/v2/prometheus/prometheus:v3.11.3")
	if rendered != "image: mcr.microsoft.com/oss/v2/prometheus/prometheus:v3.11.3\n" {
		t.Fatalf("unexpected manifest: %q", rendered)
	}
}

func TestRenderManifestOmitsKubeStateMetricsByDefault(t *testing.T) {
	manifest := `scrape_configs:
      - job_name: kubernetes-nodes
{{KUBE_STATE_METRICS_SCRAPE_CONFIG}}image: {{PROMETHEUS_IMAGE}}
`
	rendered := RenderManifest(manifest, "prometheus:test")
	if strings.Contains(rendered, "kube-state-metrics") || strings.Contains(rendered, "{{KUBE_STATE_METRICS_SCRAPE_CONFIG}}") {
		t.Fatalf("default manifest should not include kube-state-metrics scrape config:\n%s", rendered)
	}
}

func TestRenderManifestWithScrapeTargetIncludesKubeStateMetrics(t *testing.T) {
	manifest := `scrape_configs:
      - job_name: kubernetes-nodes
{{KUBE_STATE_METRICS_SCRAPE_CONFIG}}image: {{PROMETHEUS_IMAGE}}
`
	rendered := RenderManifestWithScrapeTarget(manifest, "prometheus:test", "kube-state-metrics.perf-monitoring.svc:8080")
	for _, want := range []string{
		"job_name: kube-state-metrics",
		"kube-state-metrics.perf-monitoring.svc:8080",
		"image: prometheus:test",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("manifest missing %q:\n%s", want, rendered)
		}
	}
}

func TestWaitReadyWithTimeoutReturnsDeadline(t *testing.T) {
	start := time.Now()
	err := WaitReadyWithTimeout(context.Background(), "http://127.0.0.1:1", 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReadyWithTimeout() error = %v, want deadline exceeded", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("WaitReadyWithTimeout() did not return promptly")
	}
}

func TestStopPortForwardCancelsAndWaits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := StopPortForward(cancel, cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.ProcessState == nil {
		t.Fatalf("StopPortForward() did not wait for process")
	}
}
