package suite

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoadRejectsUnsafeSuiteName(t *testing.T) {
	_, err := Load(t.TempDir(), "../outside")
	if err == nil || !strings.Contains(err.Error(), "invalid suite name") {
		t.Fatalf("Load() error = %v, want invalid suite name", err)
	}
}

func TestLoadRejectsUnsafeDeclaredSuiteName(t *testing.T) {
	root := t.TempDir()
	suiteDir := filepath.Join(root, "suites", "kata-disk-perf")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("name: ../outside\ndescription: Disk perf suite\ntests:\n  - write-iops\n")
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.yml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root, "kata-disk-perf")
	if err == nil || !strings.Contains(err.Error(), "invalid suite name") {
		t.Fatalf("Load() error = %v, want invalid declared suite name", err)
	}
}

func TestValidName(t *testing.T) {
	valid := []string{"kata-disk-perf", "a", "a1-b2"}
	for _, name := range valid {
		if !ValidName(name) {
			t.Fatalf("ValidName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "Kata", "-bad", "bad-", "bad_name", "../bad", "bad/path"}
	for _, name := range invalid {
		if ValidName(name) {
			t.Fatalf("ValidName(%q) = true, want false", name)
		}
	}
}
