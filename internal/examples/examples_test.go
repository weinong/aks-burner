package examples

import (
	"path/filepath"
	"testing"

	"github.com/Azure/aks-burner/internal/config"
)

func TestKataDiskPerfContractsValidate(t *testing.T) {
	root := filepath.Join("..", "..")
	cases := []struct {
		schema string
		file   string
	}{
		{"schemas/suite.schema.json", "suites/kata-disk-perf/suite.yml"},
		{"schemas/requirements.schema.json", "suites/kata-disk-perf/requirements.yml"},
		{"schemas/mode.schema.json", "suites/kata-disk-perf/vars/smoke.yml"},
		{"schemas/mode.schema.json", "suites/kata-disk-perf/vars/full.yml"},
	}
	for _, tc := range cases {
		if err := config.ValidateYAML(filepath.Join(root, tc.schema), filepath.Join(root, tc.file)); err != nil {
			t.Fatalf("%s failed validation against %s: %v", tc.file, tc.schema, err)
		}
	}
}
