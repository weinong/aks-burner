package reporting

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

func writeCSV(path string, rows []Row) (returnErr error) {
	dimensions := DimensionColumns(rows)
	renderedHeaders := make(map[string]string, len(dimensions))
	for _, dimension := range dimensions {
		rendered := spreadsheetSafe(dimension)
		if previous, exists := renderedHeaders[rendered]; exists {
			return fmt.Errorf("dimension headers %q and %q both render as %q after spreadsheet-safe escaping", previous, dimension, rendered)
		}
		renderedHeaders[rendered] = dimension
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create report directory %s: %w", directory, err)
	}
	temporary, err := os.CreateTemp(directory, "results.csv.tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary report in %s: %w", directory, err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if returnErr != nil {
			temporary.Close()
			os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o644); err != nil {
		return fmt.Errorf("set temporary report mode: %w", err)
	}

	header := make([]string, 0, len(dimensions)+4)
	header = append(header, "source")
	for _, dimension := range dimensions {
		header = append(header, spreadsheetSafe(dimension))
	}
	header = append(header, "metric", "value", "unit")

	writer := csv.NewWriter(temporary)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("write CSV header: %w", err)
	}
	for _, row := range rows {
		record := make([]string, 0, len(header))
		record = append(record, spreadsheetSafe(row.Source))
		for _, dimension := range dimensions {
			record = append(record, spreadsheetSafe(row.Dimensions[dimension]))
		}
		record = append(record, spreadsheetSafe(row.Metric), row.Value.Text, spreadsheetSafe(row.Unit))
		if err := writer.Write(record); err != nil {
			return fmt.Errorf("write CSV row for %s: %w", row.Metric, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush CSV report: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close CSV report: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace CSV report %s: %w", path, err)
	}
	return nil
}

func spreadsheetSafe(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
	if trimmed == "" {
		return value
	}
	switch trimmed[0] {
	case '=', '+', '-', '@':
		return "'" + value
	default:
		return value
	}
}
