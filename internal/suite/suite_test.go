package suite

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListSuites(t *testing.T) {
	root := t.TempDir()
	suiteDir := filepath.Join(root, "suites", "kata-disk-perf")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("name: kata-disk-perf\ndescription: Disk perf suite\ntests:\n  - write-iops\n")
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.yml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	suites, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(suites) != 1 || suites[0].Name != "kata-disk-perf" || suites[0].Tests[0] != "write-iops" {
		t.Fatalf("unexpected suites: %#v", suites)
	}
}
