package reporting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func ReadKubeBurnerMetrics(metricsDir, runDir string, prometheusMetricNames []string, metricUnits map[string]string) ([]Row, int, error) {
	if _, err := os.Stat(metricsDir); errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	} else if err != nil {
		return nil, 0, fmt.Errorf("stat kube-burner metrics root %s: %w", metricsDir, err)
	}

	declaredPrometheusUnits := make(map[string]string, len(prometheusMetricNames))
	for _, name := range prometheusMetricNames {
		declaredPrometheusUnits[name] = metricUnits[name]
	}

	paths := []string{}
	err := filepath.WalkDir(metricsDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("discover kube-burner metrics in %s: %w", metricsDir, err)
	}
	sort.Strings(paths)

	rows := []Row{}
	contributingFiles := 0
	for _, path := range paths {
		source, err := filepath.Rel(runDir, path)
		if err != nil {
			return nil, 0, fmt.Errorf("kube-burner metric %s source path: %w", path, err)
		}
		source = filepath.ToSlash(source)
		documents, err := readKubeBurnerDocuments(path, source)
		if err != nil {
			return nil, 0, err
		}
		rowsBeforeFile := len(rows)
		for index, document := range documents {
			metricName, err := requiredString(document, "metricName")
			if err != nil {
				return nil, 0, fmt.Errorf("%s: documents[%d].%w", source, index, err)
			}

			switch metricName {
			case "podLatencyQuantilesMeasurement":
				rows, err = appendPodLatencyQuantileRows(rows, source, document)
			case "podLatencyMeasurement", "jobSummary":
				continue
			default:
				unit, declared := declaredPrometheusUnits[metricName]
				if !declared {
					continue
				}
				rows, err = appendPrometheusRow(rows, source, metricName, unit, document)
			}
			if err != nil {
				return nil, 0, fmt.Errorf("%s: documents[%d]: %w", source, index, err)
			}
		}
		if len(rows) > rowsBeforeFile {
			contributingFiles++
		}
	}
	if err := ValidateRows(rows); err != nil {
		return nil, 0, err
	}
	return rows, contributingFiles, nil
}

func readKubeBurnerDocuments(path, source string) ([]map[string]json.RawMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: open JSON: %w", source, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var documents []map[string]json.RawMessage
	if err := decoder.Decode(&documents); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON document array: %w", source, err)
	}
	if documents == nil {
		return nil, fmt.Errorf("%s: invalid JSON document array: top-level value must be an array", source)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return nil, fmt.Errorf("%s: invalid trailing JSON: %w", source, err)
	}
	return documents, nil
}

func appendPodLatencyQuantileRows(rows []Row, source string, document map[string]json.RawMessage) ([]Row, error) {
	jobName, err := requiredString(document, "jobName")
	if err != nil {
		return nil, err
	}
	quantileName, err := requiredString(document, "quantileName")
	if err != nil {
		return nil, err
	}
	dimensions := map[string]string{"jobName": jobName, "quantileName": quantileName}
	for _, metric := range []struct {
		field string
		name  string
	}{
		{field: "P50", name: "pod_latency_p50"},
		{field: "P95", name: "pod_latency_p95"},
		{field: "P99", name: "pod_latency_p99"},
		{field: "max", name: "pod_latency_max"},
		{field: "avg", name: "pod_latency_avg"},
	} {
		value, err := requiredNumber(document, metric.field)
		if err != nil {
			return nil, err
		}
		rows = append(rows, Row{Source: source, Dimensions: dimensions, Metric: metric.name, Value: value, Unit: "milliseconds"})
	}
	return rows, nil
}

func appendPrometheusRow(rows []Row, source, metricName, unit string, document map[string]json.RawMessage) ([]Row, error) {
	value, err := requiredNumber(document, "value")
	if err != nil {
		return nil, err
	}
	timestamp, err := requiredString(document, "timestamp")
	if err != nil {
		return nil, err
	}
	if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
		return nil, fmt.Errorf("timestamp must be RFC3339: %w", err)
	}

	labels := map[string]string{}
	if encodedLabels, ok := document["labels"]; ok {
		if err := json.Unmarshal(encodedLabels, &labels); err != nil {
			return nil, fmt.Errorf("labels must be an object containing only string values: %w", err)
		}
		if labels == nil {
			return nil, fmt.Errorf("labels must be an object")
		}
	}
	dimensions := make(map[string]string, len(labels)+2)
	for key, value := range labels {
		dimensions["label."+key] = value
	}
	dimensions["kubeBurner.timestamp"] = timestamp
	if encodedJobName, ok := document["jobName"]; ok {
		var jobName string
		if err := json.Unmarshal(encodedJobName, &jobName); err != nil {
			return nil, fmt.Errorf("jobName must be a string: %w", err)
		}
		dimensions["kubeBurner.jobName"] = jobName
	}
	for key := range dimensions {
		if reservedColumns[key] {
			return nil, fmt.Errorf("dimension %q uses a reserved column name", key)
		}
	}
	return append(rows, Row{Source: source, Dimensions: dimensions, Metric: metricName, Value: value, Unit: unit}), nil
}

func requiredString(document map[string]json.RawMessage, field string) (string, error) {
	encoded, ok := document[field]
	if !ok {
		return "", fmt.Errorf("%s field is required", field)
	}
	var value string
	if err := json.Unmarshal(encoded, &value); err != nil {
		return "", fmt.Errorf("%s must be a string: %w", field, err)
	}
	return value, nil
}

func requiredNumber(document map[string]json.RawMessage, field string) (Number, error) {
	encoded, ok := document[field]
	if !ok {
		return Number{}, fmt.Errorf("%s field is required", field)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return Number{}, fmt.Errorf("%s: %w", field, err)
	}
	number, ok := value.(json.Number)
	if !ok {
		return Number{}, fmt.Errorf("%s must be a JSON number", field)
	}
	parsed, err := ParseNumber(number)
	if err != nil {
		return Number{}, fmt.Errorf("%s: %w", field, err)
	}
	return parsed, nil
}
