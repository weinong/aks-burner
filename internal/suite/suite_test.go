package suite

import (
	"os"
	"path/filepath"
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

	suites, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(suites) != 1 || suites[0].Name != "kata-perf" || suites[0].Tests[0] != "write-iops" {
		t.Fatalf("unexpected suites: %#v", suites)
	}
}

func TestLoadParsesSetupResources(t *testing.T) {
	root := t.TempDir()
	suiteDir := filepath.Join(root, "suites", "kata-perf")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`name: kata-perf
description: Kata perf suite
tests:
  - write-iops
setup:
  resources:
    - name: kata-runtimeclass
      path: setup/runtimeclass.yml
      wait:
        - kind: exists
          resource: runtimeclass/custom-kata
          timeout: 1m
    - name: node-prep
      path: setup/node-prep-daemonset.yml
      wait:
        - kind: rollout
          resource: daemonset/node-prep
          namespace: kube-system
          timeout: 10m
`)
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.yml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(root, "kata-perf")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Setup.Resources) != 2 {
		t.Fatalf("setup resources = %#v, want 2 resources", cfg.Setup.Resources)
	}
	first := cfg.Setup.Resources[0]
	if first.Name != "kata-runtimeclass" || first.Path != "setup/runtimeclass.yml" {
		t.Fatalf("first setup resource = %#v", first)
	}
	if len(first.Wait) != 1 || first.Wait[0].Kind != "exists" || first.Wait[0].Resource != "runtimeclass/custom-kata" || first.Wait[0].Timeout != "1m" {
		t.Fatalf("first wait rule = %#v", first.Wait)
	}
	second := cfg.Setup.Resources[1]
	if len(second.Wait) != 1 || second.Wait[0].Kind != "rollout" || second.Wait[0].Namespace != "kube-system" {
		t.Fatalf("second wait rule = %#v", second.Wait)
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
