package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDispatchesRunSuite(t *testing.T) {
	err := run([]string{"run-suite"})
	if err == nil || !strings.Contains(err.Error(), "usage: perf-runner run-suite") {
		t.Fatalf("run-suite dispatch error = %v", err)
	}
}

func TestValidateDestroyTargetRequiresDefaultResourceGroup(t *testing.T) {
	err := validateDestroyTarget("kata-disk-perf", "rg-not-owned", false)
	if err == nil || !strings.Contains(err.Error(), "rg-aks-burner-kata-disk-perf") {
		t.Fatalf("validateDestroyTarget() error = %v, want default resource group error", err)
	}
	if err := validateDestroyTarget("kata-disk-perf", "rg-not-owned", true); err != nil {
		t.Fatalf("validateDestroyTarget() with override returned error: %v", err)
	}
}

func TestResolveSuitePathRejectsOutsideSuite(t *testing.T) {
	root := t.TempDir()
	_, err := resolveSuitePath(root, "kata-disk-perf", "../outside.bicepparam")
	if err == nil || !strings.Contains(err.Error(), "outside suite directory") {
		t.Fatalf("resolveSuitePath() error = %v, want outside suite directory", err)
	}
}

func TestResolveSuitePathAcceptsRepoRelativeSuitePath(t *testing.T) {
	root := t.TempDir()
	got, err := resolveSuitePath(root, "kata-disk-perf", "suites/kata-disk-perf/infra.bicepparam")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "suites", "kata-disk-perf", "infra.bicepparam")
	if got != want {
		t.Fatalf("resolveSuitePath() = %q, want %q", got, want)
	}
}

func TestResolveRepoPathRejectsOutsideRepo(t *testing.T) {
	root := t.TempDir()
	_, err := resolveRepoPath(root, "../outside/main.bicep")
	if err == nil || !strings.Contains(err.Error(), "outside repo") {
		t.Fatalf("resolveRepoPath() error = %v, want outside repo", err)
	}
}
