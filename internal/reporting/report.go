package reporting

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"text/tabwriter"
)

type RunInfo struct {
	Suite         string
	Mode          string
	Timestamp     string
	WorkspaceRoot string
}

type Result struct {
	SourceFiles int
	Rows        int
	CSVPath     string
}

func Generate(runDir string, cfg Config, info RunInfo, out io.Writer) (Result, error) {
	var rows []Row
	sourceFiles := 0
	if cfg.Sources.StandardSummary {
		standardRows, files, err := ReadStandardSummaries(filepath.Join(runDir, "artifacts"), runDir)
		if err != nil {
			return Result{}, err
		}
		rows = append(rows, standardRows...)
		sourceFiles += files
	}
	if cfg.Sources.KubeBurner {
		kubeRows, files, err := ReadKubeBurnerMetrics(
			filepath.Join(runDir, "raw", "metrics"),
			runDir,
			PrometheusMetricNamesFromConfig(cfg),
			cfg.PrometheusMetricUnits,
		)
		if err != nil {
			return Result{}, err
		}
		rows = append(rows, kubeRows...)
		sourceFiles += files
	}
	if len(rows) == 0 {
		return Result{}, fmt.Errorf("no valid measurements found under %s/artifacts or %s/raw/metrics", runDir, runDir)
	}
	SortRows(rows)
	if err := ValidateRows(rows); err != nil {
		return Result{}, err
	}

	csvPath := filepath.Join(runDir, "summary", "results.csv")
	if err := writeCSV(csvPath, rows); err != nil {
		return Result{}, err
	}
	printPreview(out, info, rows, sourceFiles, csvPath)
	return Result{SourceFiles: sourceFiles, Rows: len(rows), CSVPath: csvPath}, nil
}

func PrometheusMetricNamesFromConfig(cfg Config) []string {
	names := append([]string(nil), cfg.PrometheusMetricNames...)
	sort.Strings(names)
	return names
}

func printPreview(out io.Writer, info RunInfo, rows []Row, sourceFiles int, csvPath string) {
	fmt.Fprintf(out, "Test results: %s / %s / %s\n", info.Suite, info.Mode, info.Timestamp)
	fmt.Fprintf(out, "Sources: %d  Measurements: %d\n", sourceFiles, len(rows))

	dimensions := DimensionColumns(rows)
	columns := make([]string, 0, len(dimensions)+4)
	columns = append(columns, "source")
	columns = append(columns, dimensions...)
	columns = append(columns, "metric", "value", "unit")
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	writePreviewRecord(table, columns)
	limit := min(len(rows), 10)
	for _, row := range rows[:limit] {
		record := make([]string, 0, len(columns))
		record = append(record, row.Source)
		for _, dimension := range dimensions {
			record = append(record, row.Dimensions[dimension])
		}
		value := row.Value.Text
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			value = strconv.FormatFloat(parsed, 'g', 6, 64)
		}
		record = append(record, row.Metric, value, row.Unit)
		writePreviewRecord(table, record)
	}
	table.Flush()
	if omitted := len(rows) - limit; omitted > 0 {
		noun := "row"
		if omitted != 1 {
			noun = "rows"
		}
		fmt.Fprintf(out, "%d additional %s omitted\n", omitted, noun)
	}

	relativePath := csvPath
	if relative, err := filepath.Rel(info.WorkspaceRoot, csvPath); err == nil {
		relativePath = relative
	}
	fmt.Fprintf(out, "Results CSV: %s\n", filepath.ToSlash(relativePath))
}

func writePreviewRecord(out io.Writer, values []string) {
	for index, value := range values {
		if index > 0 {
			fmt.Fprint(out, "\t")
		}
		fmt.Fprint(out, value)
	}
	fmt.Fprintln(out)
}
