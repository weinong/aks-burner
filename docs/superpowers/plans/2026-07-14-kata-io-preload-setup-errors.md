# Kata I/O Preload and Setup Errors Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the kata-io benchmark image compatible with kube-burner v2 image preloading and expose actionable kubectl output when setup commands fail.

**Architecture:** Keep kube-burner's existing warm-image flow by adding one strict compatibility executable to the benchmark image. Improve setup diagnostics at the shared command-runner boundary so apply and wait callers receive the Kubernetes response without duplicating formatting logic.

**Tech Stack:** Go 1.25, POSIX shell, Docker, Kubernetes/kubectl, kube-burner v2.7.3

## Global Constraints

- Preserve `preLoadImages: true` and existing benchmark cache semantics.
- The compatibility executable accepts exactly one argument with value `command`; all other invocations fail.
- Setup errors preserve the underlying `*exec.ExitError` for `errors.As`.
- Do not change benchmark scenarios, PVC lifecycle, or unrelated command runners.

---

### Task 1: Kube-burner Preload Compatibility

**Files:**
- Create: `suites/kata-io/images/benchmark/scripts/override`
- Modify: `suites/kata-io/images/benchmark/Dockerfile:15-19`
- Modify: `internal/examples/examples_test.go:806-827`

**Interfaces:**
- Consumes: kube-burner preload invocation `override command` resolved through the image `PATH`.
- Produces: executable `/usr/local/bin/override` that exits 0 only for the exact invocation `override command`.

- [ ] **Step 1: Write the failing contract tests**

Add `scripts/override` to `TestKataIOBenchmarkImageFilesExist`. Add a test that copies the script to a temporary `PATH` as `override`, executes `override command`, and verifies missing or extra arguments fail with an `unsupported kube-burner preload command` message. Add a Dockerfile assertion for `COPY scripts/override /usr/local/bin/override`.

```go
func TestKataIOBenchmarkImageSupportsKubeBurnerPreloadCommand(t *testing.T) {
	root := filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark")
	script := filepath.Join(root, "scripts", "override")
	binDir := t.TempDir()
	target := filepath.Join(binDir, "override")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := exec.Command("override", "command")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("override command failed: %v\n%s", err, output)
	}

	for _, args := range [][]string{nil, {"command", "extra"}, {"other"}} {
		cmd := exec.Command("override", args...)
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "unsupported kube-burner preload command") {
			t.Fatalf("override %v error = %v, output = %q", args, err, output)
		}
	}

	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "COPY scripts/override /usr/local/bin/override") {
		t.Fatal("benchmark Dockerfile does not install the kube-burner override command")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/examples -run 'TestKataIOBenchmarkImage(FilesExist|SupportsKubeBurnerPreloadCommand)' -count=1`

Expected: FAIL because `scripts/override` does not exist and the Dockerfile does not install it.

- [ ] **Step 3: Implement the strict compatibility executable**

Create the script:

```sh
#!/usr/bin/env sh
set -eu

if [ "$#" -eq 1 ] && [ "$1" = "command" ]; then
    exit 0
fi

printf '%s\n' 'unsupported kube-burner preload command' >&2
exit 64
```

Update the Dockerfile:

```dockerfile
COPY scripts/override /usr/local/bin/override
COPY scripts/run-fio.sh /usr/local/bin/run-fio.sh
COPY scripts/run-git-clone.sh /usr/local/bin/run-git-clone.sh
COPY fio-profiles /profiles

RUN chmod +x /usr/local/bin/override /usr/local/bin/run-fio.sh /usr/local/bin/run-git-clone.sh
```

- [ ] **Step 4: Run focused tests**

Run: `go test ./internal/examples -run 'TestKataIOBenchmarkImage(FilesExist|SupportsKubeBurnerPreloadCommand)' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the task**

```bash
git add internal/examples/examples_test.go suites/kata-io/images/benchmark/Dockerfile suites/kata-io/images/benchmark/scripts/override
git commit -m "fix: support kube-burner image preload"
```

### Task 2: Setup Command Diagnostics

**Files:**
- Modify: `internal/run/setup.go:130-132`
- Modify: `internal/run/setup_test.go:291`

**Interfaces:**
- Consumes: command path and arguments passed to `commandOutput(context.Context, ...string)`.
- Produces: `([]byte, error)` containing combined process output; failed commands wrap `*exec.ExitError` and append only nonempty trimmed output.

- [ ] **Step 1: Write failing command-runner tests**

Add `os/exec` to the test imports. Add tests using `/bin/sh` to cover successful combined output, failed combined output and trimming, preservation of `*exec.ExitError`, and an output-free failure without an empty suffix.

```go
func TestCommandOutputReturnsCombinedOutput(t *testing.T) {
	output, err := commandOutput(context.Background(), "sh", "-c", "printf stdout; printf stderr >&2")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(output); got != "stdoutstderr" {
		t.Fatalf("commandOutput() = %q, want combined output", got)
	}
}

func TestCommandOutputIncludesTrimmedOutputInError(t *testing.T) {
	_, err := commandOutput(context.Background(), "sh", "-c", "printf ' stdout '; printf ' stderr ' >&2; exit 1")
	if err == nil || !strings.Contains(err.Error(), "stdout  stderr") {
		t.Fatalf("commandOutput() error = %v, want combined trimmed output", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("commandOutput() error = %T, want wrapped *exec.ExitError", err)
	}
}

func TestCommandOutputOmitsEmptyOutputSuffix(t *testing.T) {
	_, err := commandOutput(context.Background(), "sh", "-c", "exit 1")
	if err == nil || err.Error() != "exit status 1" {
		t.Fatalf("commandOutput() error = %q, want bare exit error", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/run -run TestCommandOutput -count=1`

Expected: success-output test fails because stderr is excluded; failure-output test fails because the error is only `exit status 1`.

- [ ] **Step 3: Implement combined output error wrapping**

Add `strings` if not already imported and replace `commandOutput` with:

```go
func commandOutput(ctx context.Context, command ...string) ([]byte, error) {
	output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput()
	if err == nil {
		return output, nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		return output, err
	}
	return output, fmt.Errorf("%w: %s", err, message)
}
```

- [ ] **Step 4: Run focused and package tests**

Run: `go test ./internal/run -run TestCommandOutput -count=1`

Expected: PASS.

Run: `go test ./internal/run -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the task**

```bash
git add internal/run/setup.go internal/run/setup_test.go
git commit -m "fix: expose setup command errors"
```

### Task 3: Integrated Verification

**Files:**
- Verify only; no planned source changes.

**Interfaces:**
- Consumes: Task 1 benchmark image and Task 2 setup diagnostics.
- Produces: evidence that focused behavior, repository tests, build, and container invocation all pass.

- [ ] **Step 1: Run focused package tests**

Run: `go test ./internal/examples ./internal/run -count=1`

Expected: PASS.

- [ ] **Step 2: Run the full Go suite and build**

Run: `go test ./... -count=1`

Expected: PASS.

Run: `go build -o /tmp/opencode/aks-burner-perf-runner ./cmd/perf-runner`

Expected: PASS.

- [ ] **Step 3: Build and exercise the benchmark image**

Run: `docker build -t aks-burner-kata-io-preload-test suites/kata-io/images/benchmark`

Expected: image builds successfully.

Run: `docker run --rm --entrypoint override aks-burner-kata-io-preload-test command`

Expected: exit status 0 with no output.

Run: `docker run --rm --entrypoint override aks-burner-kata-io-preload-test other`

Expected: exit status 64 and `unsupported kube-burner preload command` on stderr.

- [ ] **Step 4: Review the complete branch diff before final commit/merge**

Run: `git status --short && git diff && git diff --cached && git diff main...HEAD`

Expected: the worktree is clean, unstaged and staged diffs are empty, and the branch diff contains only the approved spec, plan, preload compatibility, and setup diagnostics changes.
