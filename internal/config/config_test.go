package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTemplateYAMLExpandsSprigLoops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workload.yml")
	content := `jobs:
{{- range $profile := list "seqread" "fsync-heavy" }}
- name: fio-{{ $profile }}
  profile: {{ $profile }}
{{- end }}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var workload struct {
		Jobs []struct {
			Name    string `yaml:"name"`
			Profile string `yaml:"profile"`
		} `yaml:"jobs"`
	}
	if err := LoadTemplateYAML(path, nil, &workload); err != nil {
		t.Fatal(err)
	}
	if len(workload.Jobs) != 2 || workload.Jobs[0].Name != "fio-seqread" || workload.Jobs[1].Profile != "fsync-heavy" {
		t.Fatalf("jobs = %#v, want expanded profile loop", workload.Jobs)
	}
}

func TestLoadTemplateYAMLUsesTemplateData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workload.yml")
	if err := os.WriteFile(path, []byte("value: '{{ .runID }}'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var output map[string]string
	if err := LoadTemplateYAML(path, map[string]any{"runID": "run-{{.runTimestamp}}"}, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output["value"], "run-{{.runTimestamp}}"; got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
}

func TestLoadTemplateYAMLRejectsInvalidTemplates(t *testing.T) {
	tests := []struct {
		name    string
		content string
		data    any
	}{
		{name: "syntax", content: "jobs: {{ range }}\n"},
		{name: "missing data", content: "value: '{{ .missing }}'\n", data: map[string]any{}},
		{name: "rendered YAML", content: "jobs: [\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workload.yml")
			if err := os.WriteFile(path, []byte(test.content), 0o644); err != nil {
				t.Fatal(err)
			}
			var output any
			if err := LoadTemplateYAML(path, test.data, &output); err == nil {
				t.Fatal("LoadTemplateYAML accepted invalid workload template")
			}
		})
	}
}

func TestValidateYAMLUsesJSONSchema(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	yamlPath := filepath.Join(dir, "value.yml")
	if err := os.WriteFile(schemaPath, []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["name"],
  "additionalProperties": false,
  "properties": {"name": {"type": "string"}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yamlPath, []byte("name: example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateYAML(schemaPath, yamlPath); err != nil {
		t.Fatalf("ValidateYAML returned error: %v", err)
	}
	if err := os.WriteFile(yamlPath, []byte("name: example\nextra: nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateYAML(schemaPath, yamlPath); err == nil {
		t.Fatal("ValidateYAML accepted extra property")
	}
}

func TestLoadMergedYAMLAppliesNestedOverrides(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	overridesPath := filepath.Join(dir, "overrides.yml")
	if err := os.WriteFile(schemaPath, []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["iterations", "cleanup", "vars"],
  "properties": {
    "iterations": {"type": "integer", "minimum": 1},
    "cleanup": {"type": "boolean"},
    "vars": {"type": "object", "additionalProperties": true}
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overridesPath, []byte("iterations: 20\ncleanup: false\nvars:\n  mode: full\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	defaults := map[string]any{
		"iterations": 5,
		"cleanup":    true,
		"vars":       map[string]any{"app": "example", "mode": "smoke"},
	}
	var output struct {
		Iterations int            `yaml:"iterations"`
		Cleanup    bool           `yaml:"cleanup"`
		Vars       map[string]any `yaml:"vars"`
	}
	if err := LoadMergedYAML(schemaPath, defaults, overridesPath, &output); err != nil {
		t.Fatal(err)
	}
	if output.Iterations != 20 || output.Cleanup {
		t.Fatalf("merged scalar fields = %#v, want iterations 20 and cleanup false", output)
	}
	if output.Vars["app"] != "example" || output.Vars["mode"] != "full" {
		t.Fatalf("merged vars = %#v, want inherited app and overridden mode", output.Vars)
	}
}

func TestLoadMergedYAMLValidatesMergedDocument(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	overridesPath := filepath.Join(dir, "overrides.yml")
	if err := os.WriteFile(schemaPath, []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["iterations"],
  "properties": {"iterations": {"type": "integer", "minimum": 1}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overridesPath, []byte("unexpected: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var output any
	if err := LoadMergedYAML(schemaPath, map[string]any{"iterations": 5}, overridesPath, &output); err == nil {
		t.Fatal("LoadMergedYAML accepted an unknown override field")
	}
}
