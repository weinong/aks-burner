package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
