package reporting

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"
)

type RunInfo struct {
	Suite         string
	Mode          string
	Timestamp     string
	WorkspaceRoot string
	Partial       bool
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
		kubeRows, files, err := readKubeBurnerMetrics(
			filepath.Join(runDir, "raw", "metrics"),
			runDir,
			PrometheusMetricNamesFromConfig(cfg),
			cfg.PrometheusMetricUnits,
			cfg.ReportPodReadyMetrics,
			cfg.ReportStorageStartupMetrics,
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
	if info.Partial {
		for index := range rows {
			rows[index].Dimensions["runStatus"] = "partial"
		}
	}
	SortRows(rows)
	if err := ValidateRows(rows); err != nil {
		return Result{}, err
	}

	csvPath := filepath.Join(runDir, "summary", "results.csv")
	if err := writeCSV(csvPath, rows); err != nil {
		return Result{}, err
	}
	result := Result{SourceFiles: sourceFiles, Rows: len(rows), CSVPath: csvPath}
	if err := printPreview(out, info, rows, sourceFiles, csvPath); err != nil {
		return result, fmt.Errorf("write report preview: %w", err)
	}
	return result, nil
}

func PrometheusMetricNamesFromConfig(cfg Config) []string {
	names := append([]string(nil), cfg.PrometheusMetricNames...)
	sort.Strings(names)
	return names
}

func printPreview(out io.Writer, info RunInfo, rows []Row, sourceFiles int, csvPath string) error {
	if _, err := fmt.Fprintf(out, "Test results: %s / %s / %s\n", terminalSafe(info.Suite), terminalSafe(info.Mode), terminalSafe(info.Timestamp)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Sources: %d  Measurements: %d\n", sourceFiles, len(rows)); err != nil {
		return err
	}

	dimensions := DimensionColumns(rows)
	columns := make([]string, 0, len(dimensions)+4)
	columns = append(columns, "source")
	for _, dimension := range dimensions {
		columns = append(columns, terminalSafe(dimension))
	}
	columns = append(columns, "metric", "value", "unit")
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if err := writePreviewRecord(table, columns); err != nil {
		return err
	}
	limit := min(len(rows), 10)
	for _, row := range rows[:limit] {
		record := make([]string, 0, len(columns))
		record = append(record, terminalSafe(row.Source))
		for _, dimension := range dimensions {
			record = append(record, terminalSafe(row.Dimensions[dimension]))
		}
		value := row.Value.Text
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			value = strconv.FormatFloat(parsed, 'g', 6, 64)
		}
		record = append(record, terminalSafe(row.Metric), terminalSafe(value), terminalSafe(row.Unit))
		if err := writePreviewRecord(table, record); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if omitted := len(rows) - limit; omitted > 0 {
		noun := "row"
		if omitted != 1 {
			noun = "rows"
		}
		if _, err := fmt.Fprintf(out, "%d additional %s omitted\n", omitted, noun); err != nil {
			return err
		}
	}

	relativePath := csvPath
	if relative, err := filepath.Rel(info.WorkspaceRoot, csvPath); err == nil {
		relativePath = relative
	}
	_, err := fmt.Fprintf(out, "Results CSV: %s\n", terminalSafe(filepath.ToSlash(relativePath)))
	return err
}

func terminalSafe(value string) string {
	var escaped strings.Builder
	for _, r := range value {
		if !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) && !unicode.Is(unicode.Zl, r) && !unicode.Is(unicode.Zp, r) {
			escaped.WriteRune(r)
			continue
		}
		switch r {
		case '\t':
			escaped.WriteString(`\t`)
		case '\n':
			escaped.WriteString(`\n`)
		case '\r':
			escaped.WriteString(`\r`)
		case '\b':
			escaped.WriteString(`\b`)
		case '\f':
			escaped.WriteString(`\f`)
		default:
			if r <= 0xff {
				fmt.Fprintf(&escaped, `\x%02x`, r)
			} else if r <= 0xffff {
				fmt.Fprintf(&escaped, `\u%04x`, r)
			} else {
				fmt.Fprintf(&escaped, `\U%08x`, r)
			}
		}
	}
	return escaped.String()
}

func writePreviewRecord(out io.Writer, values []string) error {
	for index, value := range values {
		if index > 0 {
			if _, err := fmt.Fprint(out, "\t"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprint(out, value); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out)
	return err
}
