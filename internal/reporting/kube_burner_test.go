package reporting

// Fixture shapes are from kube-burner v2.7.3
// (9091baef070ca04e1116cc4d07e53d08490e6896) measurement and local-indexer
// output. To refresh them, run that exact version against the kata-perf smoke
// suite, copy the corresponding raw/metrics arrays, remove environment-specific
// names and UUIDs, and retain all structural fields before updating these tests.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadKubeBurnerMetricsMapsPodLatencyQuantiles(t *testing.T) {
	runDir := copyKubeBurnerFixtures(t, "podLatencyQuantilesMeasurement.json")
	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 5 {
		t.Fatalf("files/rows = %d/%d", files, len(rows))
	}
	wantValues := map[string]string{
		"pod_latency_p50": "2897",
		"pod_latency_p95": "3510",
		"pod_latency_p99": "3774",
		"pod_latency_max": "3774",
		"pod_latency_avg": "2876",
	}
	for _, row := range rows {
		if row.Source != "raw/metrics/podLatencyQuantilesMeasurement.json" || row.Unit != "milliseconds" || row.Dimensions["jobName"] != "startup-smoke" || row.Dimensions["quantileName"] != "Ready" {
			t.Fatalf("row = %#v", row)
		}
		if row.Value.Text != wantValues[row.Metric] {
			t.Fatalf("row = %#v", row)
		}
		delete(wantValues, row.Metric)
	}
	if len(wantValues) != 0 {
		t.Fatalf("missing metrics = %#v", wantValues)
	}
}

func TestReadKubeBurnerMetricsPreservesRangeSamples(t *testing.T) {
	runDir := copyKubeBurnerFixtures(t, "podCPUUsage.json")
	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, []string{"podCPUUsage"}, map[string]string{"podCPUUsage": "cores"})
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 2 || rows[0].Dimensions["kubeBurner.timestamp"] == rows[1].Dimensions["kubeBurner.timestamp"] {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
	if rows[0].Metric != "podCPUUsage" || rows[0].Unit != "cores" || rows[0].Source != "raw/metrics/podCPUUsage.json" {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Dimensions["label.pod"] != "demo-0" || rows[0].Dimensions["label.namespace"] != "demo" || rows[0].Dimensions["kubeBurner.jobName"] != "startup-smoke" {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Value.Text != "0.3300880234732172" || rows[1].Value.Text != "0.31978102677038506" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestReadKubeBurnerMetricsIgnoresUnsupportedDocuments(t *testing.T) {
	runDir := copyKubeBurnerFixtures(t, "ignored-podLatencyMeasurement.json", "jobSummary.json")
	writeKubeBurnerMetric(t, runDir, "unknown.json", `[{"metricName":"notDeclared","value":1,"timestamp":"2026-07-11T00:00:00Z","labels":{}}]`)
	writeKubeBurnerMetric(t, runDir, "unit-only.json", `[{"metricName":"unitOnly","value":1,"timestamp":"2026-07-11T00:00:00Z","labels":{}}]`)

	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, map[string]string{"unitOnly": "count"})
	if err != nil {
		t.Fatal(err)
	}
	if files != 4 || len(rows) != 0 {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
}

func TestReadKubeBurnerMetricsRejectsMalformedAcceptedDocuments(t *testing.T) {
	tests := []struct {
		name     string
		document string
		field    string
	}{
		{name: "missing quantile", document: `[{"metricName":"podLatencyQuantilesMeasurement","P50":1,"P95":2,"max":4,"avg":3,"jobName":"job","quantileName":"Ready"}]`, field: "P99"},
		{name: "malformed value", document: `[{"metricName":"declared","value":"1","timestamp":"2026-07-11T00:00:00Z","labels":{}}]`, field: "value"},
		{name: "malformed labels", document: `[{"metricName":"declared","value":1,"timestamp":"2026-07-11T00:00:00Z","labels":{"pod":1}}]`, field: "labels"},
		{name: "malformed timestamp", document: `[{"metricName":"declared","value":1,"timestamp":"not-a-time","labels":{}}]`, field: "timestamp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runDir := t.TempDir()
			writeKubeBurnerMetric(t, runDir, "invalid.json", tt.document)
			_, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, []string{"declared"}, map[string]string{"declared": "count"})
			if err == nil || !strings.Contains(err.Error(), "raw/metrics/invalid.json") || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("ReadKubeBurnerMetrics() error = %v, want source and %q", err, tt.field)
			}
		})
	}
}

func TestReadKubeBurnerMetricsRequiresStringMetricName(t *testing.T) {
	for _, document := range []string{`[{"value":1}]`, `[{"metricName":1}]`} {
		runDir := t.TempDir()
		writeKubeBurnerMetric(t, runDir, "invalid.json", document)
		_, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "metricName") {
			t.Fatalf("ReadKubeBurnerMetrics() error = %v", err)
		}
	}
}

func TestReadKubeBurnerMetricsMissingDirectory(t *testing.T) {
	runDir := t.TempDir()
	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files != 0 || !reflect.DeepEqual(rows, []Row(nil)) {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
}

func copyKubeBurnerFixtures(t *testing.T, names ...string) string {
	t.Helper()
	runDir := t.TempDir()
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join("testdata", "kube-burner-2.7.3", name))
		if err != nil {
			t.Fatal(err)
		}
		writeKubeBurnerMetric(t, runDir, name, string(data))
	}
	return runDir
}

func writeKubeBurnerMetric(t *testing.T, runDir, name, document string) {
	t.Helper()
	metricsDir := filepath.Join(runDir, "raw", "metrics")
	if err := os.MkdirAll(metricsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metricsDir, name), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
}
