package suite

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestListSuites(t *testing.T) {
	root := t.TempDir()
	suiteDir := filepath.Join(root, "suites", "kata-perf")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("name: kata-perf\ndescription: Kata perf suite\ntests:\n  - write-iops\n")
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.yml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(suiteDir, "vars"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "vars", "smoke.yml"), []byte("iterations: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	suites, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(suites) != 1 || suites[0].Name != "kata-perf" || suites[0].Tests[0] != "write-iops" {
		t.Fatalf("unexpected suites: %#v", suites)
	}
}

func TestListIncludesModesInPreferredOrder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "suites", "demo", "suite.yml"), "name: demo\ndescription: Demo suite\ntests:\n  - startup-smoke\n")
	writeFile(t, filepath.Join(root, "suites", "demo", "vars", "zeta.yml"), "iterations: 1\n")
	writeFile(t, filepath.Join(root, "suites", "demo", "vars", "full.yml"), "iterations: 1\n")
	writeFile(t, filepath.Join(root, "suites", "demo", "vars", "smoke.yml"), "iterations: 1\n")

	suites, err := List(root)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(suites) != 1 {
		t.Fatalf("List() returned %d suites, want 1", len(suites))
	}
	if got, want := suites[0].Modes, []string{"smoke", "full", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Modes = %v, want %v", got, want)
	}
}

func TestListFailsWhenSuiteHasNoModes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "suites", "demo", "suite.yml"), "name: demo\ndescription: Demo suite\ntests:\n  - startup-smoke\n")

	_, err := List(root)
	if err == nil || !strings.Contains(err.Error(), "no mode files found") {
		t.Fatalf("List() error = %v, want no mode files found", err)
	}
}

func TestListRejectsInvalidModeName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "suites", "demo", "suite.yml"), "name: demo\ndescription: Demo suite\ntests:\n  - startup-smoke\n")
	writeFile(t, filepath.Join(root, "suites", "demo", "vars", "bad_mode.yml"), "iterations: 1\n")

	_, err := List(root)
	if err == nil || !strings.Contains(err.Error(), "invalid mode name") {
		t.Fatalf("List() error = %v, want invalid mode name", err)
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
	suiteDir := filepath.Join(root, "suites", "kata-perf")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("name: ../outside\ndescription: Kata perf suite\ntests:\n  - write-iops\n")
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.yml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root, "kata-perf")
	if err == nil || !strings.Contains(err.Error(), "invalid suite name") {
		t.Fatalf("Load() error = %v, want invalid declared suite name", err)
	}
}

func TestValidName(t *testing.T) {
	valid := []string{"kata-perf", "a", "a1-b2"}
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

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
