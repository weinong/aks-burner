# Add Suite Generator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add fast and guided workflows for generating a complete dummy kube-burner suite by name.

**Architecture:** Extend the existing `perf-runner` command dispatcher with `add-suite`, keeping generation logic in Go alongside the current CLI path validation and YAML helpers. The generator writes the same suite file shape as `suites/kata-perf`, while Make exposes one fast target and one guided target.

**Tech Stack:** Go 1.25, standard `flag` package, standard file I/O, `gopkg.in/yaml.v3` through existing `config.WriteYAML`, Make, `go test`.

## Global Constraints

- Do not reset local `main`; this work is based on the current local branch because it is intentionally ahead of `origin/main`.
- Work only in `.worktrees/add-suite-generator` until merge time.
- Use existing suite name validation from `internal/suite.ValidName`.
- Refuse to overwrite an existing `suites/<suite>` directory.
- Keep generated files under `suites/<suite>`.
- Do not add `--force`.
- Preserve existing suite lifecycle behavior.

---

## File Structure

- `cmd/perf-runner/main.go`: add command dispatch, flag parsing, guided prompt support, suite generation, and small helper functions.
- `cmd/perf-runner/main_test.go`: add tests for fast generation, guided defaults, invalid names, and overwrite refusal.
- `Makefile`: add `add-suite` and `add-suite-guided` targets and help entries.
- `README.md`: document the new suite creation workflows.
- `docs/superpowers/specs/2026-07-09-add-suite-generator-design.md`: approved design.
- `docs/superpowers/plans/2026-07-09-add-suite-generator.md`: this implementation plan.

---

### Task 1: Generator CLI Behavior

**Files:**
- Modify: `cmd/perf-runner/main.go`
- Modify: `cmd/perf-runner/main_test.go`

**Interfaces:**
- Consumes: `repo.Root(start string) (string, error)`, `suite.ValidName(name string) bool`, `config.WriteYAML(path string, value any) error`.
- Produces: `addSuite(args []string) error`, `addSuiteWithIO(args []string, in io.Reader, out io.Writer) error`, `suiteDefaults(name string) addSuiteOptions`, and `writeSuite(root string, opts addSuiteOptions) error`.

- [ ] **Step 1: Write failing fast-generation tests**

Add tests that create a temporary repo, run `addSuiteWithIO([]string{"--suite", "demo-suite"}, strings.NewReader(""), io.Discard)`, and assert generated files exist with expected suite-specific content.

- [ ] **Step 2: Run focused test to verify failure**

Run: `go test ./cmd/perf-runner -run 'TestAddSuite'`

Expected: FAIL because `addSuiteWithIO` is not defined.

- [ ] **Step 3: Implement fast generation**

Add `add-suite` dispatch and implement default generation for all suite files. Use existing YAML writer for YAML files and `os.WriteFile` for `infra.bicepparam`.

- [ ] **Step 4: Run focused fast-generation tests**

Run: `go test ./cmd/perf-runner -run 'TestAddSuite'`

Expected: PASS for fast generation and existing tests.

- [ ] **Step 5: Write failing guided and safety tests**

Add tests for invalid suite names, existing suite directory refusal, and guided defaults from newline-separated prompt input.

- [ ] **Step 6: Implement guided prompts and safety checks**

Prompt only when `--guided` is set. Use defaults when input is blank. Validate numeric values and boolean Prometheus input.

- [ ] **Step 7: Run CLI tests**

Run: `go test ./cmd/perf-runner`

Expected: PASS.

---

### Task 2: Make And Documentation

**Files:**
- Modify: `Makefile`
- Modify: `README.md`

**Interfaces:**
- Consumes: `perf-runner add-suite --suite SUITE` and `perf-runner add-suite --guided` from Task 1.
- Produces: `make add-suite` and `make add-suite-guided`.

- [ ] **Step 1: Update Make targets**

Add `add-suite` and `add-suite-guided` to `.PHONY`, `help`, and target definitions. `add-suite` must require `TEST_SUITE`; `add-suite-guided` must not.

- [ ] **Step 2: Update README**

Document fast and guided suite creation commands in Common Commands and Suite Lifecycle.

- [ ] **Step 3: Verify Make help**

Run: `make help`

Expected: output lists `add-suite` and `add-suite-guided`.

- [ ] **Step 4: Verify full project**

Run: `go test ./...`

Expected: PASS.

Run: `make build`

Expected: PASS.

Run: `make list-suites`

Expected: PASS and includes `kata-perf`.

---

## Self-Review

Spec coverage: The plan includes fast and guided CLI workflows, Make targets, generated suite files, safety checks, documentation, and tests.

Placeholder scan: No placeholders remain.

Type consistency: The helper names and produced Make targets are consistent across tasks.
