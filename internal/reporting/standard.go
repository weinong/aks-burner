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
	return readStandardSummaries(artifactsDir, runDir, filepath.WalkDir)
}

func readStandardSummaries(artifactsDir, runDir string, walkDir func(string, fs.WalkDirFunc) error) ([]Row, int, error) {
	if _, err := os.Stat(artifactsDir); errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	} else if err != nil {
		return nil, 0, fmt.Errorf("stat standard summary root %s: %w", artifactsDir, err)
	}

	paths := []string{}
	err := walkDir(artifactsDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if !entry.IsDir() && entry.Name() == "summary.json" {
			paths = append(paths, path)
		}
		return nil
	})
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
	data, err := os.ReadFile(path)
	if err != nil {
		return standardDocument{}, fmt.Errorf("%s: open JSON: %w", source, err)
	}
	if err := validateStandardJSON(data); err != nil {
		return standardDocument{}, fmt.Errorf("%s: invalid JSON field or document shape: %w", source, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
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

type standardJSONContext int

const (
	standardJSONGeneric standardJSONContext = iota
	standardJSONDocument
	standardJSONDimensions
	standardJSONMetrics
	standardJSONMetric
)

func validateStandardJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateStandardJSONValue(decoder, "", standardJSONDocument); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("unexpected trailing token %v", token)
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func validateStandardJSONValue(decoder *json.Decoder, path string, context standardJSONContext) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return validateStandardJSONObject(decoder, path, context)
	case '[':
		index := 0
		for decoder.More() {
			childContext := standardJSONGeneric
			childPath := fmt.Sprintf("%s[%d]", path, index)
			if context == standardJSONMetrics {
				childContext = standardJSONMetric
			}
			if err := validateStandardJSONValue(decoder, childPath, childContext); err != nil {
				return err
			}
			index++
		}
		_, err := decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

func validateStandardJSONObject(decoder *json.Decoder, path string, context standardJSONContext) error {
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return fmt.Errorf("%s object member name must be a string", path)
		}
		fieldPath := name
		if path != "" {
			fieldPath = path + "." + name
		}
		if seen[name] {
			return fmt.Errorf("%s is a duplicate object member", fieldPath)
		}
		seen[name] = true

		childContext := standardJSONGeneric
		switch context {
		case standardJSONDocument:
			switch name {
			case "schemaVersion":
			case "dimensions":
				childContext = standardJSONDimensions
			case "metrics":
				childContext = standardJSONMetrics
			default:
				return fmt.Errorf("%s is not an allowed field", fieldPath)
			}
		case standardJSONMetric:
			switch name {
			case "name", "value", "unit":
			default:
				return fmt.Errorf("%s is not an allowed field", fieldPath)
			}
		}
		if err := validateStandardJSONValue(decoder, fieldPath, childContext); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
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
