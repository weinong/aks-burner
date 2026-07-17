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
