package reporting

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWriteCSVUsesSortedDimensionUnionAndExactValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary", "results.csv")
	rows := []Row{
		{
			Source:     "artifacts/a,quoted/summary.json",
			Dimensions: map[string]string{"zeta": "last"},
			Metric:     "requests, total",
			Value:      Number{Text: "1.2300e+04"},
			Unit:       "requests",
		},
		{
			Source:     "raw/metrics/result.json",
			Dimensions: map[string]string{"alpha": "first", "zeta": "second"},
			Metric:     "latency",
			Value:      Number{Text: "0.00000100"},
			Unit:       "seconds",
		},
	}

	if err := writeCSV(path, rows); err != nil {
		t.Fatal(err)
	}

	records := readCSV(t, path)
	want := [][]string{
		{"source", "alpha", "zeta", "metric", "value", "unit"},
		{"artifacts/a,quoted/summary.json", "", "last", "requests, total", "1.2300e+04", "requests"},
		{"raw/metrics/result.json", "first", "second", "latency", "0.00000100", "seconds"},
	}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("CSV records = %#v, want %#v", records, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"artifacts/a,quoted/summary.json"`) || !strings.Contains(string(raw), `"requests, total"`) {
		t.Fatalf("CSV did not use standard quoting:\n%s", raw)
	}
}

func TestWriteCSVSpreadsheetEscapesTextCellsAfterLeadingWhitespaceAndControls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary", "results.csv")
	rows := []Row{{
		Source:     "\t=source",
		Dimensions: map[string]string{"\n+dimension": "\r@value"},
		Metric:     " -metric",
		Value:      Number{Text: "-1.25"},
		Unit:       "@unit",
	}}

	if err := writeCSV(path, rows); err != nil {
		t.Fatal(err)
	}

	records := readCSV(t, path)
	want := [][]string{
		{"source", "'\n+dimension", "metric", "value", "unit"},
		{"'\t=source", "'\r@value", "' -metric", "-1.25", "'@unit"},
	}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("CSV records = %#v, want %#v", records, want)
	}
}

func TestSpreadsheetSafeLeavesWhitespaceOnlyAndOrdinaryValuesUnchanged(t *testing.T) {
	for _, value := range []string{"", " \t\n", " ordinary", "0", "'formula"} {
		if got := spreadsheetSafe(value); got != value {
			t.Errorf("spreadsheetSafe(%q) = %q", value, got)
		}
	}
}

func TestWriteCSVAtomicallyReplacesExistingReport(t *testing.T) {
	dir := t.TempDir()
	summaryDir := filepath.Join(dir, "summary")
	if err := os.MkdirAll(summaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(summaryDir, "results.csv")
	if err := os.WriteFile(path, []byte("old incomplete report"), 0o600); err != nil {
		t.Fatal(err)
	}

	rows := []Row{{Source: "artifacts/summary.json", Metric: "latency", Value: Number{Text: "12.00"}, Unit: "ms"}}
	if err := writeCSV(path, rows); err != nil {
		t.Fatal(err)
	}

	records := readCSV(t, path)
	if got := records[1][2]; got != "12.00" {
		t.Fatalf("value = %q, want 12.00", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 644", got)
	}
	temps, err := filepath.Glob(filepath.Join(summaryDir, "results.csv.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary files remain: %v", temps)
	}
}

func TestWriteCSVRejectsSpreadsheetSafeDimensionHeaderCollision(t *testing.T) {
	dir := t.TempDir()
	summaryDir := filepath.Join(dir, "summary")
	if err := os.MkdirAll(summaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(summaryDir, "results.csv")
	original := []byte("existing report\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	rows := []Row{{
		Source:     "source",
		Dimensions: map[string]string{"=x": "first", "'=x": "second"},
		Metric:     "metric",
		Value:      Number{Text: "1"},
		Unit:       "count",
	}}

	err := writeCSV(path, rows)

	if err == nil || !strings.Contains(err.Error(), `dimension headers "'=x" and "=x"`) || !strings.Contains(err.Error(), `both render as "'=x"`) {
		t.Fatalf("writeCSV() error = %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("existing report changed to %q", got)
	}
	temps, globErr := filepath.Glob(filepath.Join(summaryDir, "results.csv.tmp-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary files remain: %v", temps)
	}
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return records
}
