package reporting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateConfigAcceptsStandardSummaryWithArtifacts(t *testing.T) {
	cfg := Config{Sources: Sources{StandardSummary: true}}
	if err := ValidateConfig(&cfg, true, false, map[string]any{"jobs": []any{}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConfigAcceptsSupportedKubeBurnerMeasurement(t *testing.T) {
	workload := map[string]any{"global": map[string]any{"measurements": []any{map[string]any{"name": "podLatency"}}}}
	cfg := Config{Sources: Sources{KubeBurner: true}}
	if err := ValidateConfig(&cfg, false, false, workload, nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConfigRejectsNoViableSource(t *testing.T) {
	cfg := Config{}
	err := ValidateConfig(&cfg, false, false, map[string]any{"jobs": []any{}}, nil)
	if err == nil || !strings.Contains(err.Error(), "viable reporting source") {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestValidateConfigRejectsStandardSummaryWithoutArtifacts(t *testing.T) {
	cfg := Config{Sources: Sources{StandardSummary: true}}
	err := ValidateConfig(&cfg, false, false, map[string]any{"jobs": []any{}}, nil)
	if err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestValidateConfigRejectsPrometheusMetricWithoutUnit(t *testing.T) {
	cfg := Config{Sources: Sources{KubeBurner: true}}
	err := ValidateConfig(&cfg, false, true, map[string]any{"jobs": []any{}}, []string{"podCPUUsage"})
	if err == nil || !strings.Contains(err.Error(), "podCPUUsage") {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestValidateConfigRejectsUnitForUndeclaredPrometheusMetric(t *testing.T) {
	cfg := Config{Sources: Sources{KubeBurner: true}, PrometheusMetricUnits: map[string]string{"other": "cores"}}
	err := ValidateConfig(&cfg, false, true, map[string]any{"jobs": []any{}}, []string{"podCPUUsage"})
	if err == nil || !strings.Contains(err.Error(), "other") {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestPrometheusMetricNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.yml")
	if err := os.WriteFile(path, []byte("- query: up\n  metricName: availability\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := PrometheusMetricNames(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "availability" {
		t.Fatalf("PrometheusMetricNames() = %#v", got)
	}
}
