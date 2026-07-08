package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootFindsGoMod(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module example.com/test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(tmp, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Root(nested)
	if err != nil {
		t.Fatalf("Root returned error: %v", err)
	}
	if got != tmp {
		t.Fatalf("Root() = %q, want %q", got, tmp)
	}
}

func TestRootErrorsWhenGoModMissing(t *testing.T) {
	_, err := Root(t.TempDir())
	if err == nil {
		t.Fatal("Root returned nil error without go.mod")
	}
}
