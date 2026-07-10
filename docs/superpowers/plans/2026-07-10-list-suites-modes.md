# List Suites Modes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show each suite's supported modes in `make list-suites` output while keeping one row per suite.

**Architecture:** Extend `suite.List` to discover mode files from `suites/<suite>/vars/*.yml` without changing direct `suite.Load` behavior. Keep `list-suites` human-oriented by printing suite name, modes, and description in one tab-separated row per suite.

**Tech Stack:** Go standard library, existing `internal/suite` package, existing `cmd/perf-runner` command tests, Make.

## Global Constraints

- Keep `list-suites` as a human discovery command for choosing `TEST_SUITE` and valid `TEST_MODE` values.
- Preserve one output row per suite.
- Order modes as `smoke`, `full`, then any other modes alphabetically.
- Fail clearly when a suite has no `vars/*.yml` mode files.
- Do not add new third-party dependencies.

---

## File Structure

- `internal/suite/suite.go`: Owns suite discovery. Add mode discovery to `List` so CLI code does not inspect suite directory internals directly and direct `Load` callers keep existing behavior.
- `internal/suite/suite_test.go`: Add focused unit tests for mode discovery, ordering, invalid filenames, and missing mode validation.
- `cmd/perf-runner/main.go`: Update `listSuites` formatting to include a modes column from `suite.Config`.
- `cmd/perf-runner/main_test.go`: Add command-level coverage that `list-suites` prints the modes column.
- `README.md`: Document that `list-suites` shows available modes.

### Task 1: Add Modes To Suite Listing

**Files:**
- Modify: `internal/suite/suite.go`
- Modify: `internal/suite/suite_test.go`
- Modify: `cmd/perf-runner/main.go`
- Modify: `cmd/perf-runner/main_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `suite.List(root string) ([]suite.Config, error)`
- Produces: `suite.Config.Modes []string`, populated by `suite.List`

- [ ] **Step 1: Write failing suite package tests**

Add these tests to `internal/suite/suite_test.go`. If the file already has helpers for temporary suites, reuse them and keep the assertions equivalent.

```go
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
```

Ensure imports include:

```go
import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)
```

If `writeFile` does not already exist in `internal/suite/suite_test.go`, add it:

```go
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
```

- [ ] **Step 2: Run suite tests to verify they fail**

Run: `go test ./internal/suite`

Expected: FAIL because `suite.Config` has no `Modes` field yet, or modes are empty.

- [ ] **Step 3: Implement mode discovery in `internal/suite`**

Update `Config` in `internal/suite/suite.go`:

```go
type Config struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tests       []string `yaml:"tests"`
	Modes       []string `yaml:"-"`
}
```

Update the `List` loop so direct `Load` callers keep their current behavior and only listing requires mode discovery:

```go
		modes, err := listModes(root, entry.Name())
		if err != nil {
			return nil, err
		}
		cfg.Modes = modes
		suites = append(suites, cfg)
	}
	sort.Slice(suites, func(i, j int) bool { return suites[i].Name < suites[j].Name })
	return suites, nil
}
```

The full `List` function should be:

```go
func List(root string) ([]Config, error) {
	entries, err := os.ReadDir(filepath.Join(root, "suites"))
	if err != nil {
		return nil, err
	}
	var suites []Config
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cfg, err := Load(root, entry.Name())
		if err != nil {
			return nil, err
		}
		modes, err := listModes(root, entry.Name())
		if err != nil {
			return nil, err
		}
		cfg.Modes = modes
		suites = append(suites, cfg)
	}
	sort.Slice(suites, func(i, j int) bool { return suites[i].Name < suites[j].Name })
	return suites, nil
}
```

Add the helper functions below `List`:

```go
func listModes(root string, suiteName string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "suites", suiteName, "vars"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no mode files found for suite %q", suiteName)
		}
		return nil, fmt.Errorf("read mode files for suite %q: %w", suiteName, err)
	}

	var modes []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yml" {
			continue
		}
		mode := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if !ValidName(mode) {
			return nil, fmt.Errorf("invalid mode name %q for suite %q", mode, suiteName)
		}
		modes = append(modes, mode)
	}
	if len(modes) == 0 {
		return nil, fmt.Errorf("no mode files found for suite %q", suiteName)
	}
	sortModes(modes)
	return modes, nil
}

func sortModes(modes []string) {
	priority := map[string]int{"smoke": 0, "full": 1}
	sort.Slice(modes, func(i, j int) bool {
		leftPriority, leftOK := priority[modes[i]]
		rightPriority, rightOK := priority[modes[j]]
		if leftOK || rightOK {
			if !leftOK {
				leftPriority = len(priority)
			}
			if !rightOK {
				rightPriority = len(priority)
			}
			if leftPriority != rightPriority {
				return leftPriority < rightPriority
			}
		}
		return modes[i] < modes[j]
	})
}
```

Add `strings` to the import block:

```go
import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Azure/aks-burner/internal/config"
)
```

- [ ] **Step 4: Run suite tests to verify mode discovery passes**

Before running tests, update the existing `TestListSuites` fixture in `internal/suite/suite_test.go` to create at least one mode file because `List` now requires listed suites to have modes:

```go
	if err := os.MkdirAll(filepath.Join(suiteDir, "vars"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "vars", "smoke.yml"), []byte("iterations: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
```

Run: `go test ./internal/suite`

Expected: PASS.

- [ ] **Step 5: Write failing CLI test for `list-suites` output**

Add this test to `cmd/perf-runner/main_test.go`. If the file already has repo setup helpers, reuse them and keep the assertions equivalent.

```go
func TestListSuitesPrintsModes(t *testing.T) {
	root := testRepoRoot(t)
	suiteDir := filepath.Join(root, "suites", "demo")
	if err := os.MkdirAll(filepath.Join(suiteDir, "vars"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.yml"), []byte("name: demo\ndescription: Demo suite\ntests:\n  - startup-smoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "vars", "smoke.yml"), []byte("iterations: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "vars", "full.yml"), []byte("iterations: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withWorkingDir(t, root)

	var out bytes.Buffer
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	os.Stdout = w
	err = run([]string{"list-suites"})
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	os.Stdout = oldStdout
	if _, copyErr := io.Copy(&out, r); copyErr != nil {
		t.Fatalf("Copy() error = %v", copyErr)
	}
	if err != nil {
		t.Fatalf("run(list-suites) error = %v", err)
	}

	if got, want := out.String(), "demo\tsmoke, full\tDemo suite\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
```

Ensure imports include `bytes`. `io`, `os`, and `path/filepath` already exist in `cmd/perf-runner/main_test.go`.

- [ ] **Step 6: Run CLI test to verify it fails**

Run: `go test ./cmd/perf-runner -run TestListSuitesPrintsModes -count=1`

Expected: FAIL because `listSuites` still prints only suite name and description.

- [ ] **Step 7: Update `listSuites` output formatting**

Change the loop in `cmd/perf-runner/main.go`:

```go
	for _, cfg := range suites {
		fmt.Printf("%s\t%s\t%s\n", cfg.Name, strings.Join(cfg.Modes, ", "), cfg.Description)
	}
```

`cmd/perf-runner/main.go` already imports `strings`, so no new import is needed.

- [ ] **Step 8: Run CLI test to verify it passes**

Run: `go test ./cmd/perf-runner -run TestListSuitesPrintsModes -count=1`

Expected: PASS.

- [ ] **Step 9: Update README**

In `README.md`, after the `Common Commands` block or the `Suite Lifecycle` block, add this sentence:

```markdown
`make list-suites` prints each suite once with its available modes, so use the suite name for `TEST_SUITE` and one of the listed modes for `TEST_MODE`.
```

- [ ] **Step 10: Run full verification**

Run: `go test ./...`

Expected: PASS.

Run: `make list-suites`

Expected: output includes one row per suite with three tab-separated columns. For the current checked-in suites, rows should include mode text like `smoke, full`.

- [ ] **Step 11: Review pending diff**

Run: `git diff HEAD`

Expected: diff only touches `internal/suite/suite.go`, `internal/suite/suite_test.go`, `cmd/perf-runner/main.go`, `cmd/perf-runner/main_test.go`, and `README.md`.

- [ ] **Step 12: Commit implementation**

Run:

```bash
git add internal/suite/suite.go internal/suite/suite_test.go cmd/perf-runner/main.go cmd/perf-runner/main_test.go README.md
git commit -m "feat: show suite modes in list-suites"
```

Expected: commit succeeds after the required pre-commit code review process.

---

## Self-Review

- Spec coverage: The plan keeps one row per suite, adds a modes column, orders `smoke` and `full` first, fails on missing or invalid mode files during listing, preserves direct `Load` behavior, updates tests, and documents behavior.
- Placeholder scan: No placeholder sections or deferred implementation steps remain.
- Type consistency: `suite.Config.Modes []string` is introduced before CLI formatting uses it, and all references use the same field name and type.
