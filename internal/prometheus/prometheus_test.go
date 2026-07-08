package prometheus

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

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

func TestRolloutStatusArgs(t *testing.T) {
	args := RolloutStatusArgs(Config{Namespace: "perf-monitoring"})
	want := []string{"kubectl", "rollout", "status", "deployment/prometheus", "-n", "perf-monitoring", "--timeout=2m"}
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
