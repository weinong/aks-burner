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

func TestValidateConfigRejectsPodReadyReportingWithoutKubeBurner(t *testing.T) {
	cfg := Config{Sources: Sources{StandardSummary: true}, ReportPodReadyMetrics: true}
	err := ValidateConfig(&cfg, true, false, map[string]any{"jobs": []any{}}, nil)
	if err == nil || !strings.Contains(err.Error(), "requires kubeBurner reporting") {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestValidateConfigRejectsPodReadyReportingWithoutPodLatency(t *testing.T) {
	cfg := Config{Sources: Sources{KubeBurner: true}, ReportPodReadyMetrics: true, PrometheusMetricUnits: map[string]string{"up": "count"}}
	err := ValidateConfig(&cfg, false, true, map[string]any{"jobs": []any{}}, []string{"up"})
	if err == nil || !strings.Contains(err.Error(), "requires podLatency") {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestValidateConfigRejectsPodReadyReportingWhenAnyJobLacksPodLatency(t *testing.T) {
	workload := map[string]any{
		"jobs": []any{
			map[string]any{
				"name":         "measured",
				"measurements": []any{map[string]any{"name": "podLatency"}},
				"objects":      []any{map[string]any{"replicas": 1}},
			},
			map[string]any{
				"name":    "unmeasured",
				"objects": []any{map[string]any{"replicas": 1}},
			},
		},
	}
	cfg := Config{Sources: Sources{KubeBurner: true}, ReportPodReadyMetrics: true}
	err := ValidateConfig(&cfg, false, false, workload, nil)
	if err == nil || !strings.Contains(err.Error(), `job "unmeasured" requires podLatency`) {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestValidateConfigAcceptsPodReadyReportingWithPodLatencyOnEveryJob(t *testing.T) {
	workload := map[string]any{
		"jobs": []any{
			map[string]any{
				"name":         "kata",
				"measurements": []any{map[string]any{"name": "podLatency"}},
				"objects":      []any{map[string]any{"replicas": 1}},
			},
			map[string]any{
				"name":         "default",
				"measurements": []any{map[string]any{"name": "podLatency"}},
				"objects":      []any{map[string]any{"replicas": 1}},
			},
		},
	}
	cfg := Config{Sources: Sources{KubeBurner: true}, ReportPodReadyMetrics: true}
	if err := ValidateConfig(&cfg, false, false, workload, nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateConfigRejectsPodReadyReportingWithoutOnePodPerIteration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		job     map[string]any
		objects []any
	}{
		{name: "no objects", objects: []any{}},
		{name: "multiple objects", objects: []any{map[string]any{"replicas": 1}, map[string]any{"replicas": 1}}},
		{name: "multiple replicas", objects: []any{map[string]any{"replicas": 2}}},
		{name: "non-create job", job: map[string]any{"jobType": "read"}, objects: []any{map[string]any{"replicas": 1}}},
		{name: "run once", objects: []any{map[string]any{"replicas": 1, "runOnce": true}}},
		{name: "repeated object", objects: []any{map[string]any{"replicas": 1, "repeatEveryNIterations": 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := map[string]any{"name": "load", "objects": tc.objects}
			for key, value := range tc.job {
				job[key] = value
			}
			workload := map[string]any{
				"global": map[string]any{"measurements": []any{map[string]any{"name": "podLatency"}}},
				"jobs":   []any{job},
			}
			cfg := Config{Sources: Sources{KubeBurner: true}, ReportPodReadyMetrics: true}
			err := ValidateConfig(&cfg, false, false, workload, nil)
			if err == nil || !strings.Contains(err.Error(), "exactly one object with one replica") {
				t.Fatalf("ValidateConfig() error = %v", err)
			}
		})
	}
}

func TestValidateConfigRejectsPodReadyReportingWithoutJobs(t *testing.T) {
	workload := map[string]any{
		"global": map[string]any{"measurements": []any{map[string]any{"name": "podLatency"}}},
		"jobs":   []any{},
	}
	cfg := Config{Sources: Sources{KubeBurner: true}, ReportPodReadyMetrics: true}
	err := ValidateConfig(&cfg, false, false, workload, nil)
	if err == nil || !strings.Contains(err.Error(), "requires at least one job") {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
}

func TestValidateConfigRejectsPodReadyReportingWithStandardSummaries(t *testing.T) {
	workload := map[string]any{
		"global": map[string]any{"measurements": []any{map[string]any{"name": "podLatency"}}},
		"jobs":   []any{map[string]any{"name": "load", "objects": []any{map[string]any{"replicas": 1}}}},
	}
	cfg := Config{Sources: Sources{StandardSummary: true, KubeBurner: true}, ReportPodReadyMetrics: true}
	err := ValidateConfig(&cfg, true, false, workload, nil)
	if err == nil || !strings.Contains(err.Error(), "does not support standardSummary") {
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
