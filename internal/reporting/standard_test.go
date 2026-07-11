package reporting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadStandardSummariesExpandsMetrics(t *testing.T) {
	runDir := t.TempDir()
	sampleDir := filepath.Join(runDir, "artifacts", "scenario", "sample")
	if err := os.MkdirAll(sampleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/standard/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sampleDir, "summary.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	rows, files, err := ReadStandardSummaries(filepath.Join(runDir, "artifacts"), runDir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 2 {
		t.Fatalf("files/rows = %d/%d", files, len(rows))
	}
	if rows[0].Source != "artifacts/scenario/sample/summary.json" {
		t.Fatalf("source = %q", rows[0].Source)
	}
	if rows[0].Value.Text != "0" || rows[1].Value.Text != "104113.481442" {
		t.Fatalf("rows = %#v", rows)
	}
	if got := rows[1].Dimensions; !reflect.DeepEqual(got, map[string]string{"runtime": "kata", "storage": "emptydir", "workload": "fio"}) {
		t.Fatalf("dimensions = %#v", got)
	}
}

func TestReadStandardSummariesMissingArtifactsDirectory(t *testing.T) {
	runDir := t.TempDir()
	rows, files, err := ReadStandardSummaries(filepath.Join(runDir, "artifacts"), runDir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 0 || len(rows) != 0 {
		t.Fatalf("files/rows = %d/%d", files, len(rows))
	}
}

func TestReadStandardSummariesRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name     string
		document string
		field    string
	}{
		{name: "unsupported schema", document: `{"schemaVersion":2,"dimensions":{},"metrics":[{"name":"metric","value":1,"unit":"count"}]}`, field: "schemaVersion"},
		{name: "empty metrics", document: `{"schemaVersion":1,"dimensions":{},"metrics":[]}`, field: "metrics"},
		{name: "reserved dimension", document: fixture(t, "invalid-reserved-dimension.json"), field: "dimensions.metric"},
		{name: "empty dimension key", document: `{"schemaVersion":1,"dimensions":{"":"value"},"metrics":[{"name":"metric","value":1,"unit":"count"}]}`, field: "dimensions"},
		{name: "empty metric name", document: `{"schemaVersion":1,"dimensions":{},"metrics":[{"name":"","value":1,"unit":"count"}]}`, field: "metrics[0].name"},
		{name: "empty unit", document: `{"schemaVersion":1,"dimensions":{},"metrics":[{"name":"metric","value":1,"unit":""}]}`, field: "metrics[0].unit"},
		{name: "missing dimensions", document: `{"schemaVersion":1,"metrics":[{"name":"metric","value":1,"unit":"count"}]}`, field: "dimensions"},
		{name: "missing value", document: `{"schemaVersion":1,"dimensions":{},"metrics":[{"name":"metric","unit":"count"}]}`, field: "metrics[0].value"},
		{name: "non-number value", document: `{"schemaVersion":1,"dimensions":{},"metrics":[{"name":"metric","value":"1","unit":"count"}]}`, field: "metrics[0].value"},
		{name: "non-finite binary64", document: fixture(t, "invalid-number.json"), field: "metrics[0].value"},
		{name: "too many significant digits", document: `{"schemaVersion":1,"dimensions":{},"metrics":[{"name":"metric","value":123456789012345678,"unit":"count"}]}`, field: "metrics[0].value"},
		{name: "non-string dimension", document: `{"schemaVersion":1,"dimensions":{"workload":1},"metrics":[{"name":"metric","value":1,"unit":"count"}]}`, field: "dimensions"},
		{name: "unknown field", document: `{"schemaVersion":1,"dimensions":{},"metrics":[{"name":"metric","value":1,"unit":"count"}],"extra":true}`, field: "extra"},
		{name: "unknown metric field", document: `{"schemaVersion":1,"dimensions":{},"metrics":[{"name":"metric","value":1,"unit":"count","extra":true}]}`, field: "extra"},
		{name: "malformed JSON", document: `{"schemaVersion":1`, field: "JSON"},
		{name: "trailing JSON value", document: `{"schemaVersion":1,"dimensions":{},"metrics":[{"name":"metric","value":1,"unit":"count"}]} {}`, field: "JSON"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runDir := t.TempDir()
			source := filepath.Join(runDir, "artifacts", "case", "summary.json")
			if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte(tt.document), 0o644); err != nil {
				t.Fatal(err)
			}

			_, _, err := ReadStandardSummaries(filepath.Join(runDir, "artifacts"), runDir)
			if err == nil || !strings.Contains(err.Error(), "artifacts/case/summary.json") || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("ReadStandardSummaries() error = %v, want source and field %q", err, tt.field)
			}
		})
	}
}

func TestReadStandardSummariesDiscoversOnlySummaryJSONBasenames(t *testing.T) {
	runDir := t.TempDir()
	document := `{"schemaVersion":1,"dimensions":{},"metrics":[{"name":"metric","value":1,"unit":"count"}]}`
	writeStandardSummary(t, runDir, "included/summary.json", document)
	writeStandardSummary(t, runDir, "excluded/not-summary.json", document)

	rows, files, err := ReadStandardSummaries(filepath.Join(runDir, "artifacts"), runDir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 1 || rows[0].Source != "artifacts/included/summary.json" {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
}

func TestReadStandardSummariesRejectsDuplicateRows(t *testing.T) {
	document := `{"schemaVersion":1,"dimensions":{"a":"b"},"metrics":[{"name":"metric","value":1,"unit":"count"},{"name":"metric","value":2,"unit":"count"}]}`
	runDir := t.TempDir()
	writeStandardSummary(t, runDir, "duplicate/summary.json", document)

	_, _, err := ReadStandardSummaries(filepath.Join(runDir, "artifacts"), runDir)
	if err == nil || !strings.Contains(err.Error(), "artifacts/duplicate/summary.json") || !strings.Contains(err.Error(), "duplicate row") {
		t.Fatalf("ReadStandardSummaries() error = %v", err)
	}
}

func TestParseNumberPreservesValidJSONToken(t *testing.T) {
	for _, text := range []string{"0", "-0", "0.0000012300", "1.2345678901234567", "1e+10"} {
		number, err := ParseNumber(json.Number(text))
		if err != nil {
			t.Errorf("ParseNumber(%q): %v", text, err)
			continue
		}
		if number.Text != text {
			t.Errorf("ParseNumber(%q).Text = %q", text, number.Text)
		}
	}
}

func TestParseNumberRejectsInvalidBinary64OrPrecision(t *testing.T) {
	for _, text := range []string{"1e309", "123456789012345678", "1.23456789012345670"} {
		if _, err := ParseNumber(json.Number(text)); err == nil {
			t.Errorf("ParseNumber(%q) returned nil error", text)
		}
	}
}

func TestDimensionColumnsAndSortRowsAreDeterministic(t *testing.T) {
	rows := []Row{
		{Source: "z.json", Dimensions: map[string]string{"runtime": "runc"}, Metric: "latency", Unit: "ms"},
		{Source: "b.json", Dimensions: map[string]string{"storage": "disk", "runtime": "kata"}, Metric: "iops", Unit: "ops"},
		{Source: "a.json", Dimensions: map[string]string{"storage": "disk", "runtime": "kata"}, Metric: "iops", Unit: "count"},
		{Source: "c.json", Dimensions: map[string]string{"storage": "disk", "runtime": "kata"}, Metric: "iops", Unit: "count"},
	}

	if got := DimensionColumns(rows); !reflect.DeepEqual(got, []string{"runtime", "storage"}) {
		t.Fatalf("DimensionColumns() = %#v", got)
	}
	SortRows(rows)
	got := []string{rows[0].Source, rows[1].Source, rows[2].Source, rows[3].Source}
	want := []string{"a.json", "c.json", "b.json", "z.json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sorted sources = %#v, want %#v", got, want)
	}
}

func TestValidateRowsUsesStructuredDuplicateIdentity(t *testing.T) {
	rows := []Row{
		{Source: "same", Dimensions: map[string]string{"a": "x\x00y", "b": "z"}, Metric: "metric", Unit: "unit"},
		{Source: "same", Dimensions: map[string]string{"a": "x", "b": "y\x00z"}, Metric: "metric", Unit: "unit"},
	}
	if err := ValidateRows(rows); err != nil {
		t.Fatalf("ValidateRows() = %v", err)
	}

	rows = append(rows, rows[0])
	if err := ValidateRows(rows); err == nil || !strings.Contains(err.Error(), "duplicate row") {
		t.Fatalf("ValidateRows() = %v", err)
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "standard", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeStandardSummary(t *testing.T, runDir, relativePath, document string) {
	t.Helper()
	path := filepath.Join(runDir, "artifacts", relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
}
