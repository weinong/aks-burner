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
)

type standardDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	Dimensions    map[string]string `json:"dimensions"`
	Metrics       []standardMetric  `json:"metrics"`
}

func (document *standardDocument) UnmarshalJSON(data []byte) error {
	var encoded struct {
		SchemaVersion int               `json:"schemaVersion"`
		Dimensions    map[string]string `json:"dimensions"`
		Metrics       []json.RawMessage `json:"metrics"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil {
		return err
	}

	metrics := make([]standardMetric, 0, len(encoded.Metrics))
	for index, data := range encoded.Metrics {
		var metric standardMetric
		if err := json.Unmarshal(data, &metric); err != nil {
			return fmt.Errorf("metrics[%d].%w", index, err)
		}
		metrics = append(metrics, metric)
	}
	document.SchemaVersion = encoded.SchemaVersion
	document.Dimensions = encoded.Dimensions
	document.Metrics = metrics
	return nil
}

type standardMetric struct {
	Name  string      `json:"name"`
	Value json.Number `json:"value"`
	Unit  string      `json:"unit"`
}

func (metric *standardMetric) UnmarshalJSON(data []byte) error {
	var encoded struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
		Unit  string          `json:"unit"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil {
		return err
	}
	if len(encoded.Value) == 0 {
		return fmt.Errorf("value field is required")
	}
	valueDecoder := json.NewDecoder(bytes.NewReader(encoded.Value))
	valueDecoder.UseNumber()
	var value any
	if err := valueDecoder.Decode(&value); err != nil {
		return fmt.Errorf("value: %w", err)
	}
	number, ok := value.(json.Number)
	if !ok {
		return fmt.Errorf("value must be a JSON number")
	}
	metric.Name = encoded.Name
	metric.Value = number
	metric.Unit = encoded.Unit
	return nil
}

func ReadStandardSummaries(artifactsDir, runDir string) ([]Row, int, error) {
	paths := []string{}
	err := filepath.WalkDir(artifactsDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == "summary.json" {
			paths = append(paths, path)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("discover standard summaries in %s: %w", artifactsDir, err)
	}
	sort.Strings(paths)

	rows := []Row{}
	for _, path := range paths {
		source, err := filepath.Rel(runDir, path)
		if err != nil {
			return nil, 0, fmt.Errorf("standard summary %s source path: %w", path, err)
		}
		source = filepath.ToSlash(source)
		document, err := readStandardDocument(path, source)
		if err != nil {
			return nil, 0, err
		}
		documentRows, err := expandStandardDocument(document, source)
		if err != nil {
			return nil, 0, err
		}
		rows = append(rows, documentRows...)
	}
	if err := ValidateRows(rows); err != nil {
		return nil, 0, err
	}
	return rows, len(paths), nil
}

func readStandardDocument(path, source string) (standardDocument, error) {
	file, err := os.Open(path)
	if err != nil {
		return standardDocument{}, fmt.Errorf("%s: open JSON: %w", source, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var document standardDocument
	if err := decoder.Decode(&document); err != nil {
		return standardDocument{}, fmt.Errorf("%s: invalid JSON field or document shape: %w", source, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return standardDocument{}, fmt.Errorf("%s: invalid trailing JSON: %w", source, err)
	}
	return document, nil
}

func expandStandardDocument(document standardDocument, source string) ([]Row, error) {
	if document.SchemaVersion != 1 {
		return nil, fmt.Errorf("%s: schemaVersion must be 1, got %d", source, document.SchemaVersion)
	}
	if document.Dimensions == nil {
		return nil, fmt.Errorf("%s: dimensions field is required", source)
	}
	for key := range document.Dimensions {
		if key == "" {
			return nil, fmt.Errorf("%s: dimensions contains an empty key", source)
		}
		if reservedColumns[key] {
			return nil, fmt.Errorf("%s: dimensions.%s uses a reserved column name", source, key)
		}
	}
	if len(document.Metrics) == 0 {
		return nil, fmt.Errorf("%s: metrics must contain at least one metric", source)
	}

	rows := make([]Row, 0, len(document.Metrics))
	for index, metric := range document.Metrics {
		if metric.Name == "" {
			return nil, fmt.Errorf("%s: metrics[%d].name must not be empty", source, index)
		}
		if metric.Unit == "" {
			return nil, fmt.Errorf("%s: metrics[%d].unit must not be empty", source, index)
		}
		value, err := ParseNumber(metric.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: metrics[%d].value: %w", source, index, err)
		}
		rows = append(rows, Row{
			Source:     source,
			Dimensions: document.Dimensions,
			Metric:     metric.Name,
			Value:      value,
			Unit:       metric.Unit,
		})
	}
	return rows, nil
}
