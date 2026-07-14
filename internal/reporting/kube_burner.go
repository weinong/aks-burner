package reporting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	launchLatencies := map[string][]float64{}
	launchJobs := map[string]bool{}
	sandboxCounters := map[string]map[string]float64{}
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
		launchSamplesBeforeFile := launchSampleCount(launchLatencies)
		fileHasSandboxCounters := false
		for index, document := range documents {
			metricName, err := requiredString(document, "metricName")
			if err != nil {
				return nil, 0, fmt.Errorf("%s: documents[%d].%w", source, index, err)
			}

			switch metricName {
			case "podLatencyQuantilesMeasurement":
				rows, err = appendPodLatencyQuantileRows(rows, source, document)
			case "podLatencyMeasurement":
				jobName, jobErr := requiredString(document, "jobName")
				if jobErr != nil {
					err = jobErr
					break
				}
				launchJobs[jobName] = true
				started, startErr := requiredNumber(document, "containersStartedLatency")
				if startErr != nil {
					err = startErr
					break
				}
				ready, readyErr := requiredNumber(document, "readyToStartContainersLatency")
				if readyErr != nil {
					err = readyErr
					break
				}
				startedValue, _ := strconv.ParseFloat(started.Text, 64)
				readyValue, _ := strconv.ParseFloat(ready.Text, 64)
				if startedValue <= 0 {
					err = fmt.Errorf("containersStartedLatency must be greater than zero")
					break
				}
				if readyValue == 0 {
					continue
				}
				latency := startedValue - readyValue
				if latency < 0 {
					err = fmt.Errorf("post-sandbox container launch latency must not be negative")
					break
				}
				launchLatencies[jobName] = append(launchLatencies[jobName], latency)
			case "jobSummary":
				continue
			default:
				if metricName == "runPodSandboxCount" || metricName == "runPodSandboxCount-start" || metricName == "runPodSandboxSum" || metricName == "runPodSandboxSum-start" {
					jobName, value, handler, sandboxErr := sandboxCounterDocument(document)
					if sandboxErr != nil {
						err = sandboxErr
						break
					}
					key := jobName + "\x00" + handler
					if sandboxCounters[key] == nil {
						sandboxCounters[key] = map[string]float64{}
					}
					if _, duplicate := sandboxCounters[key][metricName]; duplicate {
						err = fmt.Errorf("duplicate %s for job %s handler %s", metricName, jobName, handler)
						break
					}
					sandboxCounters[key][metricName] = value
					fileHasSandboxCounters = true
					continue
				}
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
		if len(rows) > rowsBeforeFile || launchSampleCount(launchLatencies) > launchSamplesBeforeFile || fileHasSandboxCounters {
			contributingFiles++
		}
	}
	for jobName := range launchJobs {
		values := launchLatencies[jobName]
		if len(values) == 0 {
			rows = append(rows, Row{Source: "derived/post-sandbox-container-launch", Dimensions: map[string]string{"jobName": jobName}, Metric: "post_sandbox_container_launch_latency_sample_count", Value: Number{Text: "0"}, Unit: "samples"})
			continue
		}
		rows = appendPostSandboxLaunchRows(rows, "derived/post-sandbox-container-launch", jobName, values)
	}
	rows, err = appendRunPodSandboxRows(rows, sandboxCounters)
	if err != nil {
		return nil, 0, err
	}
	if err := ValidateRows(rows); err != nil {
		return nil, 0, err
	}
	return rows, contributingFiles, nil
}

func sandboxCounterDocument(document map[string]json.RawMessage) (string, float64, string, error) {
	jobName, err := requiredString(document, "jobName")
	if err != nil {
		return "", 0, "", err
	}
	value, err := requiredNumber(document, "value")
	if err != nil {
		return "", 0, "", err
	}
	parsed, _ := strconv.ParseFloat(value.Text, 64)
	handler := "default"
	if encoded, ok := document["labels"]; ok {
		labels := map[string]string{}
		if err := json.Unmarshal(encoded, &labels); err != nil {
			return "", 0, "", fmt.Errorf("labels must be an object containing only string values: %w", err)
		}
		if labels["runtime_handler"] != "" {
			handler = labels["runtime_handler"]
		}
	}
	return jobName, parsed, handler, nil
}

func appendRunPodSandboxRows(rows []Row, counters map[string]map[string]float64) ([]Row, error) {
	activeJobs := map[string]bool{}
	seenJobs := map[string]bool{}
	for key, values := range counters {
		parts := strings.SplitN(key, "\x00", 2)
		jobName, handler := parts[0], parts[1]
		seenJobs[jobName] = true
		countEnd, hasCountEnd := values["runPodSandboxCount"]
		sumEnd, hasSumEnd := values["runPodSandboxSum"]
		countStart, hasCountStart := values["runPodSandboxCount-start"]
		sumStart, hasSumStart := values["runPodSandboxSum-start"]
		if !hasCountEnd || !hasSumEnd || hasCountStart != hasSumStart {
			return nil, fmt.Errorf("incomplete RunPodSandbox counters for job %s handler %s", jobName, handler)
		}
		count := countEnd - countStart
		sum := sumEnd - sumStart
		if count < 0 {
			return nil, fmt.Errorf("RunPodSandbox count delta must be positive for job %s handler %s", jobName, handler)
		}
		if count != math.Trunc(count) {
			return nil, fmt.Errorf("RunPodSandbox count delta must be an integer for job %s handler %s", jobName, handler)
		}
		if sum < 0 {
			return nil, fmt.Errorf("RunPodSandbox sum delta must not be negative for job %s handler %s", jobName, handler)
		}
		if count == 0 {
			if sum != 0 {
				return nil, fmt.Errorf("RunPodSandbox sum delta must be zero when count delta is zero for job %s handler %s", jobName, handler)
			}
			continue
		}
		activeJobs[jobName] = true
		dimensions := map[string]string{"kubeBurner.jobName": jobName, "label.runtime_handler": handler}
		rows = append(rows,
			Row{Source: "derived/run-podsandbox", Dimensions: dimensions, Metric: "runPodSandboxCount", Value: Number{Text: strconv.FormatFloat(count, 'g', -1, 64)}, Unit: "count"},
			Row{Source: "derived/run-podsandbox", Dimensions: dimensions, Metric: "runPodSandboxMean", Value: Number{Text: strconv.FormatFloat(sum/count, 'g', -1, 64)}, Unit: "seconds"},
		)
	}
	for jobName := range seenJobs {
		if !activeJobs[jobName] {
			return nil, fmt.Errorf("RunPodSandbox count delta must be positive for job %s", jobName)
		}
	}
	return rows, nil
}

func launchSampleCount(valuesByJob map[string][]float64) int {
	total := 0
	for _, values := range valuesByJob {
		total += len(values)
	}
	return total
}

func appendPostSandboxLaunchRows(rows []Row, source, jobName string, values []float64) []Row {
	sort.Float64s(values)
	percentile := func(percent float64) float64 {
		if len(values) == 1 {
			return values[0]
		}
		index := percent / 100 * float64(len(values))
		if index == float64(int64(index)) {
			return values[int(index)-1]
		}
		if index > 1 {
			i := int(index)
			return (values[i-1] + values[i]) / 2
		}
		return values[0]
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	dimensions := map[string]string{"jobName": jobName}
	metrics := []struct {
		name  string
		value float64
		unit  string
	}{
		{name: "post_sandbox_container_launch_latency_p50", value: percentile(50), unit: "milliseconds"},
		{name: "post_sandbox_container_launch_latency_p95", value: percentile(95), unit: "milliseconds"},
		{name: "post_sandbox_container_launch_latency_p99", value: percentile(99), unit: "milliseconds"},
		{name: "post_sandbox_container_launch_latency_max", value: values[len(values)-1], unit: "milliseconds"},
		{name: "post_sandbox_container_launch_latency_avg", value: total / float64(len(values)), unit: "milliseconds"},
		{name: "post_sandbox_container_launch_latency_sample_count", value: float64(len(values)), unit: "samples"},
	}
	for _, metric := range metrics {
		rows = append(rows, Row{Source: source, Dimensions: dimensions, Metric: metric.name, Value: Number{Text: strconv.Itoa(int(metric.value))}, Unit: metric.unit})
	}
	return rows
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
	if metricName == "runPodSandboxCount" || metricName == "runPodSandboxMean" {
		if handler, exists := labels["runtime_handler"]; !exists || handler == "" {
			labels["runtime_handler"] = "default"
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
