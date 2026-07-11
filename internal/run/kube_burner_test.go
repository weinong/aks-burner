package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateKubeBurnerVersionAcceptsRequiredVersion(t *testing.T) {
	root := t.TempDir()
	writeVersionedKubeBurner(t, filepath.Join(root, "bin", "kube-burner"), "2.7.3")
	if err := ValidateKubeBurnerVersion(root); err != nil {
		t.Fatal(err)
	}
}

func TestValidateKubeBurnerVersionRejectsOtherVersion(t *testing.T) {
	root := t.TempDir()
	writeVersionedKubeBurner(t, filepath.Join(root, "bin", "kube-burner"), "2.7.2")
	err := ValidateKubeBurnerVersion(root)
	if err == nil || !strings.Contains(err.Error(), "2.7.3") || !strings.Contains(err.Error(), "2.7.2") {
		t.Fatalf("ValidateKubeBurnerVersion() error = %v", err)
	}
}

func writeVersionedKubeBurner(t *testing.T, path, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then printf 'Version: " + version + "\\n'; exit 0; fi\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
