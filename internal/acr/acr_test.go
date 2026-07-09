package acr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunTagIsDockerSafeAndImmutable(t *testing.T) {
	timestamp := time.Date(2026, 7, 9, 1, 2, 3, 4, time.UTC)
	got := RunTag("kata-perf", "smoke", timestamp)
	want := "kata-perf-smoke-20260709T010203.000000004Z"
	if got != want {
		t.Fatalf("RunTag() = %q, want %q", got, want)
	}
	for _, forbidden := range []string{":", "/", " "} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("RunTag() contains forbidden %q: %q", forbidden, got)
		}
	}
}

func TestBuildCommandsConstructsDeterministicAcrBuild(t *testing.T) {
	suiteDir := t.TempDir()
	contextDir := filepath.Join(suiteDir, "images", "pause")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := BuildOptions{
		SuiteDir:       suiteDir,
		RegistryName:   "acrakskataperf",
		RegistryServer: "acrakskataperf.azurecr.io",
		ResourceGroup:  "rg-aks-burner-kata-perf",
		Tag:            "kata-perf-smoke-20260709T010203Z",
		Builds: []ImageBuild{{
			Key:            "kata-pause",
			Repository:     "kata-perf/pause",
			Context:        "images/pause",
			Dockerfile:     "Dockerfile",
			Platform:       "linux/amd64",
			TimeoutSeconds: 1800,
			BuildArgs:      map[string]string{"Z_ARG": "last", "A_ARG": "first"},
		}},
	}

	commands, built, err := BuildCommands(opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"az", "acr", "build",
		"--registry", "acrakskataperf",
		"--resource-group", "rg-aks-burner-kata-perf",
		"--image", "kata-perf/pause:kata-perf-smoke-20260709T010203Z",
		"--file", "Dockerfile",
		"--platform", "linux/amd64",
		"--timeout", "1800",
		"--build-arg", "A_ARG=first",
		"--build-arg", "Z_ARG=last",
		contextDir,
	}
	if len(commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(commands))
	}
	assertStringSlice(t, commands[0], want)
	if len(built) != 1 {
		t.Fatalf("len(built) = %d, want 1", len(built))
	}
	if built[0].Key != "kata-pause" || built[0].Image != "acrakskataperf.azurecr.io/kata-perf/pause:kata-perf-smoke-20260709T010203Z" {
		t.Fatalf("built image = %#v", built[0])
	}
}

func TestBuildCommandsRejectsRepositoryWithRegistryOrTag(t *testing.T) {
	cases := []string{"example.azurecr.io/kata/pause", "kata/pause:latest"}
	for _, repository := range cases {
		t.Run(repository, func(t *testing.T) {
			suiteDir := t.TempDir()
			_, _, err := BuildCommands(BuildOptions{
				SuiteDir:       suiteDir,
				RegistryName:   "acrtest",
				RegistryServer: "acrtest.azurecr.io",
				Tag:            "run-1",
				Builds:         []ImageBuild{{Key: "image", Repository: repository, Context: ".", Dockerfile: "Dockerfile"}},
			})
			if err == nil || !strings.Contains(err.Error(), "repository") {
				t.Fatalf("BuildCommands() error = %v, want repository validation error", err)
			}
		})
	}
}

func TestBuildCommandsRejectsContextOutsideSuite(t *testing.T) {
	_, _, err := BuildCommands(BuildOptions{
		SuiteDir:       t.TempDir(),
		RegistryName:   "acrtest",
		RegistryServer: "acrtest.azurecr.io",
		Tag:            "run-1",
		Builds:         []ImageBuild{{Key: "image", Repository: "kata/pause", Context: "../outside", Dockerfile: "Dockerfile"}},
	})
	if err == nil || !strings.Contains(err.Error(), "outside suite directory") {
		t.Fatalf("BuildCommands() error = %v, want outside suite directory", err)
	}
}

func TestBuildCommandsRejectsDockerfileOutsideContext(t *testing.T) {
	suiteDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(suiteDir, "images", "pause"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := BuildCommands(BuildOptions{
		SuiteDir:       suiteDir,
		RegistryName:   "acrtest",
		RegistryServer: "acrtest.azurecr.io",
		Tag:            "run-1",
		Builds:         []ImageBuild{{Key: "image", Repository: "kata/pause", Context: "images/pause", Dockerfile: "../Dockerfile"}},
	})
	if err == nil || !strings.Contains(err.Error(), "dockerfile") {
		t.Fatalf("BuildCommands() error = %v, want dockerfile validation error", err)
	}
}

func TestBuildCommandsRejectsSymlinkedDockerfileOutsideContext(t *testing.T) {
	suiteDir := t.TempDir()
	contextDir := filepath.Join(suiteDir, "images", "pause")
	if err := os.MkdirAll(contextDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideDockerfile := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(outsideDockerfile, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDockerfile, filepath.Join(contextDir, "Dockerfile")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, _, err := BuildCommands(BuildOptions{
		SuiteDir:       suiteDir,
		RegistryName:   "acrtest",
		RegistryServer: "acrtest.azurecr.io",
		Tag:            "run-1",
		Builds:         []ImageBuild{{Key: "image", Repository: "kata/pause", Context: "images/pause", Dockerfile: "Dockerfile"}},
	})
	if err == nil || !strings.Contains(err.Error(), "dockerfile") {
		t.Fatalf("BuildCommands() error = %v, want dockerfile symlink validation error", err)
	}
}

func TestBuildCommandsRejectsDuplicateBuildKeys(t *testing.T) {
	suiteDir := t.TempDir()
	_, _, err := BuildCommands(BuildOptions{
		SuiteDir:       suiteDir,
		RegistryName:   "acrtest",
		RegistryServer: "acrtest.azurecr.io",
		Tag:            "run-1",
		Builds: []ImageBuild{
			{Key: "image", Repository: "kata/one", Context: ".", Dockerfile: "Dockerfile"},
			{Key: "image", Repository: "kata/two", Context: ".", Dockerfile: "Dockerfile"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("BuildCommands() error = %v, want duplicate key error", err)
	}
}

func TestBuildCommandsRejectsUnsafeBuildKeys(t *testing.T) {
	suiteDir := t.TempDir()
	_, _, err := BuildCommands(BuildOptions{
		SuiteDir:       suiteDir,
		RegistryName:   "acrtest",
		RegistryServer: "acrtest.azurecr.io",
		Tag:            "run-1",
		Builds:         []ImageBuild{{Key: "a/b", Repository: "kata/one", Context: ".", Dockerfile: "Dockerfile"}},
	})
	if err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("BuildCommands() error = %v, want key validation error", err)
	}
}

func TestBuildCommandsRejectsSymlinkedContextOutsideSuite(t *testing.T) {
	suiteDir := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.Symlink(outsideDir, filepath.Join(suiteDir, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, _, err := BuildCommands(BuildOptions{
		SuiteDir:       suiteDir,
		RegistryName:   "acrtest",
		RegistryServer: "acrtest.azurecr.io",
		Tag:            "run-1",
		Builds:         []ImageBuild{{Key: "image", Repository: "kata/one", Context: "linked", Dockerfile: "Dockerfile"}},
	})
	if err == nil || !strings.Contains(err.Error(), "outside suite directory") {
		t.Fatalf("BuildCommands() error = %v, want symlink escape error", err)
	}
}

func TestBuildCommandsRejectsOverlyLongTag(t *testing.T) {
	suiteDir := t.TempDir()
	_, _, err := BuildCommands(BuildOptions{
		SuiteDir:       suiteDir,
		RegistryName:   "acrtest",
		RegistryServer: "acrtest.azurecr.io",
		Tag:            strings.Repeat("a", 129),
		Builds:         []ImageBuild{{Key: "image", Repository: "kata/one", Context: ".", Dockerfile: "Dockerfile"}},
	})
	if err == nil || !strings.Contains(err.Error(), "tag") {
		t.Fatalf("BuildCommands() error = %v, want tag validation error", err)
	}
}

func assertStringSlice(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d\ngot:  %#v\nwant: %#v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg[%d] = %q, want %q\ngot:  %#v\nwant: %#v", i, got[i], want[i], got, want)
		}
	}
}
