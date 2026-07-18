package reporting

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGenerateUsesConfiguredKubeBurnerScheme(t *testing.T) {
	workspace := t.TempDir()
	runDir := filepath.Join(workspace, "results", "run-1")
	writeKubeBurnerMetrics(t, runDir)
	var out bytes.Buffer
	cfg := Config{
		Scheme:                SchemeKubeBurner,
		PrometheusMetricNames: []string{"podCPUUsage"},
		PrometheusMetricUnits: map[string]string{"podCPUUsage": "cores"},
	}

	result, err := Generate(runDir, cfg, RunInfo{Suite: "demo", Mode: "smoke", Timestamp: "2026-07-11T00:00:00Z", WorkspaceRoot: workspace}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceFiles != 1 || result.Rows != 1 {
		t.Fatalf("result = %#v, want 1 source file and 1 row", result)
	}
	if result.CSVPath != filepath.Join(runDir, "summary", "results.csv") {
		t.Fatalf("CSVPath = %q", result.CSVPath)
	}

	records := readCSV(t, result.CSVPath)
	if len(records) != 2 {
		t.Fatalf("CSV has %d data rows, want 1: %#v", len(records)-1, records)
	}
	joined := fmt.Sprint(records)
	for _, want := range []string{"raw/metrics/result.json", "1.2000e-03", "podCPUUsage"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("CSV missing %q: %#v", want, records)
		}
	}
	text := out.String()
	for _, want := range []string{
		"Test results: demo / smoke / 2026-07-11T00:00:00Z",
		"Sources: 1", "Measurements: 1",
		"Results CSV: results/run-1/summary/results.csv",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview missing %q:\n%s", want, text)
		}
	}
}

func TestGenerateCountsOnlyKubeBurnerFilesContributingRows(t *testing.T) {
	workspace := t.TempDir()
	runDir := filepath.Join(workspace, "results", "run-1")
	writeKubeBurnerMetric(t, runDir, "ignored.json", `[{"metricName":"notDeclared"}]`)
	data, err := os.ReadFile(filepath.Join("testdata", "kube-burner-2.7.3", "podStartTotalP95.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeKubeBurnerMetric(t, runDir, "mixed.json", `[{"metricName":"jobSummary"},`+strings.TrimPrefix(strings.TrimSpace(string(data)), "["))
	var out bytes.Buffer
	cfg := Config{
		Scheme:                SchemeKubeBurner,
		PrometheusMetricNames: []string{"podStartTotalP95"},
		PrometheusMetricUnits: map[string]string{"podStartTotalP95": "seconds"},
	}

	result, err := Generate(runDir, cfg, RunInfo{WorkspaceRoot: workspace}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceFiles != 1 || result.Rows != 1 {
		t.Fatalf("result = %#v, want 1 contributing source and 1 row", result)
	}
}

func TestGeneratePassesStorageStartupReportingMode(t *testing.T) {
	workspace := t.TempDir()
	runDir := filepath.Join(workspace, "results", "run-1")
	writeStorageJobSummaries(t, runDir, 1)
	writeKubeBurnerMetric(t, runDir, "podLatencyMeasurement.json", `[
  {"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"storage-startup-kata-none","namespace":"kata-perf-storage-kata-none-0","podName":"storage-kata-none-0-1","jobIteration":0,"replica":1,"schedulingLatency":100,"readyToStartContainersLatency":1100,"containersStartedLatency":1200}
]`)
	var out bytes.Buffer
	result, err := Generate(runDir, Config{Scheme: SchemeStorageStartup}, RunInfo{WorkspaceRoot: workspace}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows == 0 {
		t.Fatal("storage report produced no rows")
	}
}

func TestGeneratePreviewRowLimit(t *testing.T) {
	for _, test := range []struct {
		rows        int
		wantMetrics int
		omitted     string
	}{
		{rows: 9, wantMetrics: 9},
		{rows: 10, wantMetrics: 10},
		{rows: 11, wantMetrics: 10, omitted: "1 additional row omitted"},
		{rows: 12, wantMetrics: 10, omitted: "2 additional rows omitted"},
	} {
		t.Run(fmt.Sprint(test.rows), func(t *testing.T) {
			workspace := t.TempDir()
			runDir := filepath.Join(workspace, "results", "run")
			values := make([]string, test.rows)
			for index := range values {
				values[index] = fmt.Sprintf("%d.123456789", index+1)
			}
			writeStandardMetrics(t, runDir, values)
			var out bytes.Buffer

			_, err := Generate(runDir, Config{Scheme: SchemeStandardSummary}, RunInfo{WorkspaceRoot: workspace}, &out)
			if err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if got := strings.Count(text, "metric-"); got != test.wantMetrics {
				t.Fatalf("preview contains %d metrics, want %d:\n%s", got, test.wantMetrics, text)
			}
			if test.omitted != "" && !strings.Contains(text, test.omitted) {
				t.Fatalf("preview missing %q:\n%s", test.omitted, text)
			}
			if test.omitted == "" && strings.Contains(text, "row omitted") {
				t.Fatalf("preview unexpectedly reports omitted rows:\n%s", text)
			}
		})
	}
}

func TestPreviewHandlesZeroRowsAndAbbreviatesOnlyTerminalValues(t *testing.T) {
	var empty bytes.Buffer
	printPreview(&empty, RunInfo{}, nil, 0, "/workspace/results/run/summary/results.csv")
	if strings.Contains(empty.String(), "row omitted") {
		t.Fatalf("empty preview reports omitted rows:\n%s", empty.String())
	}

	rows := []Row{{Source: "source", Metric: "metric", Value: Number{Text: "123456789.123456"}, Unit: "count"}}
	var out bytes.Buffer
	printPreview(&out, RunInfo{WorkspaceRoot: "/workspace"}, rows, 1, "/workspace/results/run/summary/results.csv")
	if !strings.Contains(out.String(), "1.23457e+08") || strings.Contains(out.String(), "123456789.123456") {
		t.Fatalf("terminal value was not abbreviated:\n%s", out.String())
	}
}

func TestPreviewEscapesProducerControlledTerminalText(t *testing.T) {
	rows := []Row{{
		Source:     "source\tforged",
		Dimensions: map[string]string{"header\nforged": "value\rforged"},
		Metric:     "metric\x1b[31mred\u202eflip\u2028line",
		Value:      Number{Text: "1"},
		Unit:       "unit\x00hidden",
	}}
	var out bytes.Buffer
	info := RunInfo{
		Suite:         "suite\tforged",
		Mode:          "mode\nforged",
		Timestamp:     "time\rforged",
		WorkspaceRoot: "/workspace",
	}

	if err := printPreview(&out, info, rows, 1, "/workspace/results/\x1b[2Jrun/summary/results.csv"); err != nil {
		t.Fatal(err)
	}

	text := out.String()
	for _, want := range []string{
		`suite\tforged`, `mode\nforged`, `time\rforged`,
		`header\nforged`, `source\tforged`, `value\rforged`,
		`metric\x1b[31mred\u202eflip\u2028line`, `unit\x00hidden`, `results/\x1b[2Jrun/summary/results.csv`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview missing escaped text %q:\n%s", want, text)
		}
	}
	if strings.ContainsRune(text, '\x1b') || strings.ContainsRune(text, '\x00') || strings.ContainsRune(text, '\u202e') || strings.ContainsRune(text, '\u2028') {
		t.Fatalf("preview contains raw terminal controls: %q", text)
	}
	if got := terminalSafe("ordinary café"); got != "ordinary café" {
		t.Fatalf("terminalSafe() = %q, want ordinary text unchanged", got)
	}
}

func TestGeneratePropagatesPreviewErrorAndRetainsCSV(t *testing.T) {
	workspace := t.TempDir()
	runDir := filepath.Join(workspace, "results", "run")
	writeStandardMetrics(t, runDir, []string{"1.2500"})

	result, err := Generate(
		runDir,
		Config{Scheme: SchemeStandardSummary},
		RunInfo{WorkspaceRoot: workspace},
		failingWriter{err: io.ErrClosedPipe},
	)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Generate() error = %v, want closed pipe", err)
	}

	path := filepath.Join(runDir, "summary", "results.csv")
	if result.CSVPath != path || result.SourceFiles != 1 || result.Rows != 1 {
		t.Fatalf("Generate() result = %#v, want completed CSV result", result)
	}
	records := readCSV(t, path)
	if got := records[1][3]; got != "1.2500" {
		t.Fatalf("retained CSV value = %q, want 1.2500", got)
	}
}

func TestPreviewPropagatesTabwriterFlushError(t *testing.T) {
	rows := []Row{{Source: "source", Metric: "metric", Value: Number{Text: "1"}, Unit: "count"}}
	out := &failAfterWrites{remaining: 2, err: io.ErrUnexpectedEOF}

	err := printPreview(out, RunInfo{WorkspaceRoot: "/workspace"}, rows, 1, "/workspace/results/run/summary/results.csv")

	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("printPreview() error = %v, want unexpected EOF", err)
	}
}

type failingWriter struct {
	err error
}

func (writer failingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

type failAfterWrites struct {
	remaining int
	err       error
}

func (writer *failAfterWrites) Write(data []byte) (int, error) {
	if writer.remaining == 0 {
		return 0, writer.err
	}
	writer.remaining--
	return len(data), nil
}

func TestGenerateSortsRowsDeterministically(t *testing.T) {
	workspace := t.TempDir()
	runDir := filepath.Join(workspace, "run")
	writeStandardMetrics(t, runDir, []string{"3", "2", "1"})
	var first bytes.Buffer
	result, err := Generate(runDir, Config{Scheme: SchemeStandardSummary}, RunInfo{WorkspaceRoot: workspace}, &first)
	if err != nil {
		t.Fatal(err)
	}

	records := readCSV(t, result.CSVPath)
	metrics := []string{records[1][2], records[2][2], records[3][2]}
	if want := []string{"metric-01", "metric-02", "metric-03"}; !reflect.DeepEqual(metrics, want) {
		t.Fatalf("metrics = %v, want %v", metrics, want)
	}
	var second bytes.Buffer
	if _, err := Generate(runDir, Config{Scheme: SchemeStandardSummary}, RunInfo{WorkspaceRoot: workspace}, &second); err != nil {
		t.Fatal(err)
	}
	if second.String() != first.String() {
		t.Fatalf("preview changed between runs:\nfirst:\n%s\nsecond:\n%s", first.String(), second.String())
	}
}

func TestGenerateRejectsZeroRows(t *testing.T) {
	runDir := t.TempDir()
	var out bytes.Buffer
	_, err := Generate(runDir, Config{Scheme: SchemeStandardSummary}, RunInfo{WorkspaceRoot: filepath.Dir(runDir)}, &out)
	if err == nil || !strings.Contains(err.Error(), "no valid measurements") {
		t.Fatalf("Generate() error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("Generate() wrote a preview on failure:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(runDir, "summary", "results.csv")); !os.IsNotExist(statErr) {
		t.Fatalf("results.csv exists after failure: %v", statErr)
	}
}

func TestPrometheusMetricNamesFromConfigReturnsSortedCopy(t *testing.T) {
	cfg := Config{PrometheusMetricNames: []string{"zeta", "alpha"}}
	got := PrometheusMetricNamesFromConfig(cfg)
	if want := []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PrometheusMetricNamesFromConfig() = %v, want %v", got, want)
	}
	got[0] = "changed"
	if cfg.PrometheusMetricNames[0] != "zeta" {
		t.Fatalf("function returned aliased slice: %v", cfg.PrometheusMetricNames)
	}
}

func writeStandardMetrics(t *testing.T, runDir string, values []string) {
	t.Helper()
	dir := filepath.Join(runDir, "artifacts", "bench")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var metrics strings.Builder
	for index := len(values) - 1; index >= 0; index-- {
		if metrics.Len() > 0 {
			metrics.WriteByte(',')
		}
		fmt.Fprintf(&metrics, `{"name":"metric-%02d","value":%s,"unit":"seconds"}`, index+1, values[index])
	}
	document := fmt.Sprintf(`{"schemaVersion":1,"dimensions":{"scenario":"demo"},"metrics":[%s]}`, metrics.String())
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeKubeBurnerMetrics(t *testing.T, runDir string) {
	t.Helper()
	dir := filepath.Join(runDir, "raw", "metrics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	document := `[{"metricName":"podCPUUsage","value":1.2000e-03,"timestamp":"2026-07-11T00:00:00Z","labels":{"namespace":"demo"}}]`
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
}
