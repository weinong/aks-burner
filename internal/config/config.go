package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func LoadYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, out)
}

func LoadTemplateYAML(path string, data any, out any) error {
	source, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	parsed, err := template.New(filepath.Base(path)).Option("missingkey=error").Funcs(sprig.TxtFuncMap()).Parse(string(source))
	if err != nil {
		return fmt.Errorf("parse template %s: %w", path, err)
	}
	var rendered bytes.Buffer
	if err := parsed.Execute(&rendered, data); err != nil {
		return fmt.Errorf("render template %s: %w", path, err)
	}
	if err := yaml.Unmarshal(rendered.Bytes(), out); err != nil {
		return fmt.Errorf("decode rendered template %s: %w", path, err)
	}
	return nil
}

func WriteYAML(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func ValidateYAML(schemaPath string, yamlPath string) error {
	var value any
	if err := LoadYAML(yamlPath, &value); err != nil {
		return err
	}
	return ValidateValue(schemaPath, value)
}

func ValidateValue(schemaPath string, value any) error {
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile(schemaPath)
	if err != nil {
		return err
	}
	return schema.Validate(toJSONValue(value))
}

func LoadMergedYAML(schemaPath string, defaults map[string]any, overridesPath string, out any) error {
	var overrides map[string]any
	if err := LoadYAML(overridesPath, &overrides); err != nil {
		return err
	}
	merged := mergeMaps(defaults, overrides)
	if err := ValidateValue(schemaPath, merged); err != nil {
		return err
	}
	data, err := yaml.Marshal(merged)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, out)
}

func mergeMaps(defaults, overrides map[string]any) map[string]any {
	merged := make(map[string]any, len(defaults)+len(overrides))
	for key, value := range defaults {
		if nested, ok := value.(map[string]any); ok {
			merged[key] = mergeMaps(nested, nil)
		} else {
			merged[key] = value
		}
	}
	for key, value := range overrides {
		nestedOverride, overrideIsMap := value.(map[string]any)
		nestedDefault, defaultIsMap := merged[key].(map[string]any)
		if overrideIsMap && defaultIsMap {
			merged[key] = mergeMaps(nestedDefault, nestedOverride)
			continue
		}
		merged[key] = value
	}
	return merged
}

type ImageCatalog struct {
	Images map[string]string `yaml:"images"`
}

func LoadImages(path string) (map[string]string, error) {
	var catalog ImageCatalog
	if err := LoadYAML(path, &catalog); err != nil {
		return nil, err
	}
	return catalog.Images, nil
}

func ResolveImage(images map[string]string, key string) (string, error) {
	image, ok := images[key]
	if !ok || image == "" {
		return "", fmt.Errorf("image key %q not found", key)
	}
	return image, nil
}

func toJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		converted := map[string]any{}
		for key, item := range typed {
			converted[key] = toJSONValue(item)
		}
		return converted
	case []any:
		converted := make([]any, len(typed))
		for i, item := range typed {
			converted[i] = toJSONValue(item)
		}
		return converted
	default:
		return typed
	}
}
