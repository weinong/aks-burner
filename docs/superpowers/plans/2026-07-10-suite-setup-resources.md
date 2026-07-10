# Suite Setup Resources Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add suite-level persistent Kubernetes setup resources with explicit readiness waits before kube-burner benchmark execution.

**Architecture:** Suite setup is declared in `suite.yml`, parsed through `internal/suite`, executed through a focused `internal/run` setup helper, and wired into `cmd/perf-runner run-suite` before Prometheus and workload rendering. The helper validates suite-relative manifest paths, applies manifests with `kubectl apply -f`, runs explicit wait rules, and records configured setup in run metadata.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3`, JSON Schema draft 2020-12, `github.com/santhosh-tekuri/jsonschema/v6`, Kubernetes `kubectl`, kube-burner, Make, `go test`.

## Global Constraints

- Setup resources are suite-owned, applied by the runner with `kubectl apply`, verified with explicit wait rules, and intentionally kept after the run finishes.
- No automatic deletion of setup resources after `run-suite`.
- No automatic inference of readiness based on Kubernetes kind.
- No general hook system for arbitrary shell commands.
- No mode-level setup overrides in v1.
- No changes to resource-group teardown behavior in `destroy`.
- Paths are resolved relative to `suites/<suite>/`; reject absolute paths and paths that escape the suite directory through `..` traversal.
- Supported wait kinds are exactly `exists`, `rollout`, and `condition`.
- Failed setup apply or failed setup wait must stop `run-suite` before Prometheus installation, workload rendering, or kube-burner execution.
- Existing suites without `setup` must continue to run unchanged.

---

## File Structure

- Modify `schemas/suite.schema.json`: add optional `setup.resources` and wait rule validation.
- Modify `internal/suite/suite.go`: add `Setup`, `SetupResource`, and `WaitRule` model types to `Config`.
- Modify `internal/suite/suite_test.go`: verify `suite.Load` parses setup resources.
- Modify `internal/examples/examples_test.go`: add schema validation coverage for positive and negative setup examples without changing existing suites.
- Create `internal/run/setup.go`: implement setup path validation, `kubectl apply`, wait command construction, and setup orchestration.
- Create `internal/run/setup_test.go`: unit-test setup path validation, command arguments, failure ordering, and no-delete behavior by using a fake `KubectlRunner`.
- Modify `internal/run/run.go`: add setup metadata to `Metadata`.
- Modify `cmd/perf-runner/main.go`: validate `suite.yml`, load suite config, call setup before Prometheus, and include setup in metadata.
- Modify `cmd/perf-runner/main_test.go`: test that `run-suite` validates `suite.yml` setup schema before external Azure or Kubernetes operations.
- Modify `README.md`: document `setup.resources`, wait rules, persistence, and explicit cleanup responsibility.

---

### Task 1: Schema And Suite Model

**Files:**
- Modify: `schemas/suite.schema.json`
- Modify: `internal/suite/suite.go`
- Modify: `internal/suite/suite_test.go`
- Modify: `internal/examples/examples_test.go`

**Interfaces:**
- Produces: `suite.Config.Setup suite.Setup`
- Produces: `suite.Setup{Resources []suite.SetupResource}`
- Produces: `suite.SetupResource{Name string, Path string, Wait []suite.WaitRule}`
- Produces: `suite.WaitRule{Kind string, Resource string, Namespace string, Condition string, Timeout string}`

- [ ] **Step 1: Add failing suite load test for setup parsing**

Add this test to `internal/suite/suite_test.go` after `TestListSuites`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/suite -run TestLoadParsesSetupResources`

Expected: FAIL with compile errors like `cfg.Setup undefined` because the suite model does not yet include setup fields.

- [ ] **Step 3: Add setup model types**

Update `internal/suite/suite.go` so the config and new types are:

```go
type Config struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Tests       []string `yaml:"tests"`
	Setup       Setup  `yaml:"setup"`
}

type Setup struct {
	Resources []SetupResource `yaml:"resources"`
}

type SetupResource struct {
	Name string     `yaml:"name"`
	Path string     `yaml:"path"`
	Wait []WaitRule `yaml:"wait"`
}

type WaitRule struct {
	Kind      string `yaml:"kind"`
	Resource  string `yaml:"resource"`
	Namespace string `yaml:"namespace"`
	Condition string `yaml:"condition"`
	Timeout   string `yaml:"timeout"`
}
```

- [ ] **Step 4: Run suite parsing test to verify it passes**

Run: `go test ./internal/suite -run TestLoadParsesSetupResources`

Expected: PASS.

- [ ] **Step 5: Add failing schema tests for setup validation**

Add these tests to `internal/examples/examples_test.go` after `TestKataPerfContractsValidate`:

```go
func TestSuiteSchemaAcceptsSetupResources(t *testing.T) {
	root := filepath.Join("..", "..")
	dir := t.TempDir()
	path := filepath.Join(dir, "suite.yml")
	data := []byte(`name: setup-suite
description: Suite with setup resources
tests:
  - startup
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
        - kind: condition
          resource: pod/node-prep-check
          namespace: default
          condition: Ready
          timeout: 5m
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas/suite.schema.json"), path); err != nil {
		t.Fatalf("suite schema rejected setup resources: %v", err)
	}
}

func TestSuiteSchemaRejectsInvalidSetupWait(t *testing.T) {
	root := filepath.Join("..", "..")
	dir := t.TempDir()
	path := filepath.Join(dir, "suite.yml")
	data := []byte(`name: setup-suite
description: Suite with invalid setup wait
tests:
  - startup
setup:
  resources:
    - name: node-prep
      path: setup/node-prep-daemonset.yml
      wait:
        - kind: sleep
          resource: daemonset/node-prep
          timeout: 10m
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas/suite.schema.json"), path); err == nil {
		t.Fatal("suite schema accepted invalid setup wait kind")
	}
}

func TestSuiteSchemaRequiresConditionForConditionWait(t *testing.T) {
	root := filepath.Join("..", "..")
	dir := t.TempDir()
	path := filepath.Join(dir, "suite.yml")
	data := []byte(`name: setup-suite
description: Suite with invalid condition wait
tests:
  - startup
setup:
  resources:
    - name: node-prep-check
      path: setup/node-prep-check.yml
      wait:
        - kind: condition
          resource: pod/node-prep-check
          namespace: default
          timeout: 5m
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas/suite.schema.json"), path); err == nil {
		t.Fatal("suite schema accepted condition wait without condition")
	}
}
```

- [ ] **Step 6: Run schema tests to verify they fail**

Run: `go test ./internal/examples -run 'TestSuiteSchemaAcceptsSetupResources|TestSuiteSchemaRejectsInvalidSetupWait|TestSuiteSchemaRequiresConditionForConditionWait'`

Expected: FAIL because `schemas/suite.schema.json` currently rejects the `setup` property.

- [ ] **Step 7: Extend suite schema**

Replace `schemas/suite.schema.json` with:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["name", "description", "tests"],
  "properties": {
    "name": { "type": "string", "minLength": 1 },
    "description": { "type": "string", "minLength": 1 },
    "tests": {
      "type": "array",
      "minItems": 1,
      "items": { "type": "string", "minLength": 1 }
    },
    "setup": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "resources": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["name", "path"],
            "properties": {
              "name": { "type": "string", "minLength": 1 },
              "path": { "type": "string", "minLength": 1 },
              "wait": {
                "type": "array",
                "items": {
                  "type": "object",
                  "additionalProperties": false,
                  "required": ["kind", "resource"],
                  "properties": {
                    "kind": { "type": "string", "enum": ["exists", "rollout", "condition"] },
                    "resource": { "type": "string", "minLength": 1 },
                    "namespace": { "type": "string", "minLength": 1 },
                    "condition": { "type": "string", "minLength": 1 },
                    "timeout": { "type": "string", "minLength": 1 }
                  },
                  "allOf": [
                    {
                      "if": { "properties": { "kind": { "const": "condition" } }, "required": ["kind"] },
                      "then": { "required": ["condition"] }
                    }
                  ]
                }
              }
            }
          }
        }
      }
    }
  }
}
```

- [ ] **Step 8: Run task tests**

Run: `go test ./internal/suite ./internal/examples -run 'TestLoadParsesSetupResources|TestSuiteSchemaAcceptsSetupResources|TestSuiteSchemaRejectsInvalidSetupWait|TestSuiteSchemaRequiresConditionForConditionWait|TestKataPerfContractsValidate'`

Expected: PASS.

- [ ] **Step 9: Request code review for Task 1**

Run: `git diff HEAD -- schemas/suite.schema.json internal/suite/suite.go internal/suite/suite_test.go internal/examples/examples_test.go`

Invoke the `code-reviewer` sub-agent with that diff and this task's requirements. Fix verified Critical or High findings before committing.

- [ ] **Step 10: Commit Task 1**

Run:

```bash
git add schemas/suite.schema.json internal/suite/suite.go internal/suite/suite_test.go internal/examples/examples_test.go
git commit -m "feat: model suite setup resources"
```

Expected: commit succeeds.

---

### Task 2: Setup Apply, Wait, And Path Validation Helper

**Files:**
- Create: `internal/run/setup.go`
- Create: `internal/run/setup_test.go`

**Interfaces:**
- Consumes: `suite.Setup`, `suite.SetupResource`, `suite.WaitRule` from Task 1.
- Produces: `func ApplySetup(ctx context.Context, suiteDir string, setup suite.Setup, runner KubectlRunner) error`
- Produces: `func ResolveSetupPath(suiteDir string, resource suite.SetupResource) (string, error)`
- Produces: `func WaitRuleArgs(rule suite.WaitRule) ([]string, error)`

- [ ] **Step 1: Add failing tests for setup path validation**

Create `internal/run/setup_test.go` with:

```go
package run

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/suite"
)

func TestResolveSetupPathRejectsUnsafePaths(t *testing.T) {
	suiteDir := t.TempDir()
	unsafe := []string{"/tmp/runtimeclass.yml", "../runtimeclass.yml", "setup/../../runtimeclass.yml"}
	for _, path := range unsafe {
		_, err := ResolveSetupPath(suiteDir, suite.SetupResource{Name: "bad", Path: path})
		if err == nil || !strings.Contains(err.Error(), "invalid setup path") {
			t.Fatalf("ResolveSetupPath(%q) error = %v, want invalid setup path", path, err)
		}
	}
}

func TestResolveSetupPathAcceptsSuiteRelativePath(t *testing.T) {
	suiteDir := t.TempDir()
	got, err := ResolveSetupPath(suiteDir, suite.SetupResource{Name: "runtime", Path: "setup/runtimeclass.yml"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	if got != want {
		t.Fatalf("ResolveSetupPath() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run path tests to verify they fail**

Run: `go test ./internal/run -run 'TestResolveSetupPathRejectsUnsafePaths|TestResolveSetupPathAcceptsSuiteRelativePath'`

Expected: FAIL with `undefined: ResolveSetupPath`.

- [ ] **Step 3: Implement setup path validation**

Create `internal/run/setup.go` with this initial implementation:

```go
package run

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Azure/aks-burner/internal/suite"
)

func ResolveSetupPath(suiteDir string, resource suite.SetupResource) (string, error) {
	if resource.Path == "" || filepath.IsAbs(resource.Path) {
		return "", fmt.Errorf("invalid setup path for %q: %q", resource.Name, resource.Path)
	}
	clean := filepath.Clean(resource.Path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid setup path for %q: %q", resource.Name, resource.Path)
	}
	return filepath.Join(suiteDir, clean), nil
}
```

- [ ] **Step 4: Run path tests to verify they pass**

Run: `go test ./internal/run -run 'TestResolveSetupPathRejectsUnsafePaths|TestResolveSetupPathAcceptsSuiteRelativePath'`

Expected: PASS.

- [ ] **Step 5: Add failing tests for wait command arguments**

Append these tests to `internal/run/setup_test.go`:

Also add `reflect` to the imports in `internal/run/setup_test.go`.

```go
func TestWaitRuleArgs(t *testing.T) {
	cases := []struct {
		name string
		rule suite.WaitRule
		want []string
	}{
		{
			name: "exists cluster scoped",
			rule: suite.WaitRule{Kind: "exists", Resource: "runtimeclass/custom-kata"},
			want: []string{"get", "runtimeclass/custom-kata"},
		},
		{
			name: "exists namespaced",
			rule: suite.WaitRule{Kind: "exists", Resource: "configmap/node-prep", Namespace: "kube-system"},
			want: []string{"get", "configmap/node-prep", "--namespace", "kube-system"},
		},
		{
			name: "exists with timeout",
			rule: suite.WaitRule{Kind: "exists", Resource: "runtimeclass/custom-kata", Timeout: "1m"},
			want: []string{"wait", "runtimeclass/custom-kata", "--for=create", "--timeout", "1m"},
		},
		{
			name: "rollout",
			rule: suite.WaitRule{Kind: "rollout", Resource: "daemonset/node-prep", Namespace: "kube-system", Timeout: "10m"},
			want: []string{"rollout", "status", "daemonset/node-prep", "--timeout", "10m", "--namespace", "kube-system"},
		},
		{
			name: "condition",
			rule: suite.WaitRule{Kind: "condition", Resource: "pod/node-prep-check", Namespace: "default", Condition: "Ready", Timeout: "5m"},
			want: []string{"wait", "pod/node-prep-check", "--for=condition=Ready", "--timeout", "5m", "--namespace", "default"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WaitRuleArgs(tc.rule)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("WaitRuleArgs() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestWaitRuleArgsRejectsInvalidRules(t *testing.T) {
	cases := []suite.WaitRule{
		{Kind: "sleep", Resource: "daemonset/node-prep"},
		{Kind: "exists"},
		{Kind: "condition", Resource: "pod/node-prep-check"},
	}
	for _, rule := range cases {
		if _, err := WaitRuleArgs(rule); err == nil {
			t.Fatalf("WaitRuleArgs(%#v) returned nil error", rule)
		}
	}
}
```

- [ ] **Step 6: Run wait tests to verify they fail**

Run: `go test ./internal/run -run 'TestWaitRuleArgs|TestWaitRuleArgsRejectsInvalidRules'`

Expected: FAIL with `undefined: WaitRuleArgs`.

- [ ] **Step 7: Implement wait argument builder**

Append this code to `internal/run/setup.go`:

```go
func WaitRuleArgs(rule suite.WaitRule) ([]string, error) {
	if rule.Resource == "" {
		return nil, fmt.Errorf("wait rule %q requires resource", rule.Kind)
	}
	var args []string
	switch rule.Kind {
	case "exists":
		if rule.Timeout == "" {
			args = []string{"get", rule.Resource}
		} else {
			args = []string{"wait", rule.Resource, "--for=create", "--timeout", rule.Timeout}
		}
	case "rollout":
		args = []string{"rollout", "status", rule.Resource}
		if rule.Timeout != "" {
			args = append(args, "--timeout", rule.Timeout)
		}
	case "condition":
		if rule.Condition == "" {
			return nil, fmt.Errorf("condition wait for %q requires condition", rule.Resource)
		}
		args = []string{"wait", rule.Resource, "--for=condition=" + rule.Condition}
		if rule.Timeout != "" {
			args = append(args, "--timeout", rule.Timeout)
		}
	default:
		return nil, fmt.Errorf("unsupported setup wait kind %q", rule.Kind)
	}
	if rule.Namespace != "" {
		args = append(args, "--namespace", rule.Namespace)
	}
	return args, nil
}
```

- [ ] **Step 8: Run wait tests to verify they pass**

Run: `go test ./internal/run -run 'TestWaitRuleArgs|TestWaitRuleArgsRejectsInvalidRules'`

Expected: PASS.

- [ ] **Step 9: Add failing tests for apply and wait orchestration**

Append these tests to `internal/run/setup_test.go`:

Update the imports in `internal/run/setup_test.go` to exactly:

```go
import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/suite"
)
```

```go
func TestApplySetupAppliesResourcesAndWaitsInOrder(t *testing.T) {
	suiteDir := t.TempDir()
	manifestPath := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	if err := ensureFile(manifestPath); err != nil {
		t.Fatal(err)
	}
	setup := suite.Setup{Resources: []suite.SetupResource{{
		Name: "kata-runtimeclass",
		Path: "setup/runtimeclass.yml",
		Wait: []suite.WaitRule{{Kind: "exists", Resource: "runtimeclass/custom-kata"}},
	}}}
	var calls [][]string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte("ok"), nil
	}

	if err := ApplySetup(context.Background(), suiteDir, setup, runner); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"apply", "-f", manifestPath},
		{"get", "runtimeclass/custom-kata"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("kubectl calls = %#v, want %#v", calls, want)
	}
}

func TestApplySetupFailsBeforeWaitWhenApplyFails(t *testing.T) {
	suiteDir := t.TempDir()
	manifestPath := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	if err := ensureFile(manifestPath); err != nil {
		t.Fatal(err)
	}
	setup := suite.Setup{Resources: []suite.SetupResource{{
		Name: "kata-runtimeclass",
		Path: "setup/runtimeclass.yml",
		Wait: []suite.WaitRule{{Kind: "exists", Resource: "runtimeclass/custom-kata"}},
	}}}
	var calls [][]string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, errors.New("apply failed")
	}

	err := ApplySetup(context.Background(), suiteDir, setup, runner)
	if err == nil || !strings.Contains(err.Error(), "apply setup resource kata-runtimeclass") {
		t.Fatalf("ApplySetup() error = %v, want apply context", err)
	}
	want := [][]string{{"apply", "-f", manifestPath}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("kubectl calls = %#v, want %#v", calls, want)
	}
}

func TestApplySetupFailsWhenManifestMissing(t *testing.T) {
	suiteDir := t.TempDir()
	setup := suite.Setup{Resources: []suite.SetupResource{{Name: "missing", Path: "setup/missing.yml"}}}
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		t.Fatalf("runner should not be called for missing manifest: %#v", args)
		return nil, nil
	}

	err := ApplySetup(context.Background(), suiteDir, setup, runner)
	if err == nil || !strings.Contains(err.Error(), "setup manifest") {
		t.Fatalf("ApplySetup() error = %v, want missing manifest error", err)
	}
}

func TestApplySetupFailsBeforeNextResourceWhenWaitFails(t *testing.T) {
	suiteDir := t.TempDir()
	firstPath := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	secondPath := filepath.Join(suiteDir, "setup", "daemonset.yml")
	if err := ensureFile(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := ensureFile(secondPath); err != nil {
		t.Fatal(err)
	}
	setup := suite.Setup{Resources: []suite.SetupResource{
		{Name: "kata-runtimeclass", Path: "setup/runtimeclass.yml", Wait: []suite.WaitRule{{Kind: "exists", Resource: "runtimeclass/custom-kata"}}},
		{Name: "node-prep", Path: "setup/daemonset.yml"},
	}}
	var calls [][]string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) >= 2 && args[0] == "get" && args[1] == "runtimeclass/custom-kata" {
			return nil, errors.New("not found")
		}
		return []byte("ok"), nil
	}

	err := ApplySetup(context.Background(), suiteDir, setup, runner)
	if err == nil || !strings.Contains(err.Error(), "wait for setup resource kata-runtimeclass") {
		t.Fatalf("ApplySetup() error = %v, want wait context", err)
	}
	want := [][]string{
		{"apply", "-f", firstPath},
		{"get", "runtimeclass/custom-kata"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("kubectl calls = %#v, want %#v", calls, want)
	}
}

func ensureFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("kind: RuntimeClass\n"), 0o644)
}
```

- [ ] **Step 10: Run orchestration tests to verify they fail**

Run: `go test ./internal/run -run 'TestApplySetup'`

Expected: FAIL with `undefined: ApplySetup`.

- [ ] **Step 11: Implement setup orchestration**

Append this code to `internal/run/setup.go`:

Also add `context` and `os` to the imports in `internal/run/setup.go`.

```go
func ApplySetup(ctx context.Context, suiteDir string, setup suite.Setup, runner KubectlRunner) error {
	for _, resource := range setup.Resources {
		manifestPath, err := ResolveSetupPath(suiteDir, resource)
		if err != nil {
			return err
		}
		if _, err := os.Stat(manifestPath); err != nil {
			return fmt.Errorf("setup manifest for %q not found at %s: %w", resource.Name, manifestPath, err)
		}
		if _, err := runner(ctx, "apply", "-f", manifestPath); err != nil {
			return fmt.Errorf("apply setup resource %s: %w", resource.Name, err)
		}
		for _, wait := range resource.Wait {
			args, err := WaitRuleArgs(wait)
			if err != nil {
				return err
			}
			if _, err := runner(ctx, args...); err != nil {
				return fmt.Errorf("wait for setup resource %s: %w", resource.Name, err)
			}
		}
	}
	return nil
}
```

- [ ] **Step 12: Run all setup helper tests**

Run: `go test ./internal/run -run 'TestResolveSetupPath|TestWaitRuleArgs|TestApplySetup'`

Expected: PASS.

- [ ] **Step 13: Run all run package tests**

Run: `go test ./internal/run`

Expected: PASS.

- [ ] **Step 14: Request code review for Task 2**

Run: `git diff HEAD -- internal/run/setup.go internal/run/setup_test.go`

Invoke the `code-reviewer` sub-agent with that diff and this task's requirements. Fix verified Critical or High findings before committing.

- [ ] **Step 15: Commit Task 2**

Run:

```bash
git add internal/run/setup.go internal/run/setup_test.go
git commit -m "feat: apply suite setup resources"
```

Expected: commit succeeds.

---

### Task 3: Run Metadata And CLI Integration

**Files:**
- Modify: `internal/run/run.go`
- Modify: `cmd/perf-runner/main.go`
- Modify: `cmd/perf-runner/main_test.go`

**Interfaces:**
- Consumes: `run.ApplySetup(ctx, suiteDir, suiteCfg.Setup, runpkg.KubectlOutput)` from Task 2.
- Consumes: `suite.Config.Setup` from Task 1.
- Produces: `run.Metadata.Setup suite.Setup` serialized under `metadata/run.yml`.

- [ ] **Step 1: Add failing metadata serialization test**

Append this test to `internal/run/run_test.go`:

```go
func TestWriteMetadataIncludesSetup(t *testing.T) {
	runDir := t.TempDir()
	metadata := Metadata{
		Suite: "kata-perf",
		Mode:  "smoke",
		Setup: suite.Setup{Resources: []suite.SetupResource{{
			Name: "node-prep",
			Path: "setup/node-prep-daemonset.yml",
			Wait: []suite.WaitRule{{Kind: "rollout", Resource: "daemonset/node-prep", Namespace: "kube-system", Timeout: "10m"}},
		}}},
	}
	if err := WriteMetadata(runDir, metadata); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(runDir, "metadata", "run.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"setup:", "node-prep", "setup/node-prep-daemonset.yml", "daemonset/node-prep", "kube-system"} {
		if !strings.Contains(text, want) {
			t.Fatalf("metadata missing %q:\n%s", want, text)
		}
	}
}
```

Add `github.com/Azure/aks-burner/internal/suite` to `internal/run/run_test.go` imports.

- [ ] **Step 2: Run metadata test to verify it fails**

Run: `go test ./internal/run -run TestWriteMetadataIncludesSetup`

Expected: FAIL with `unknown field Setup in struct literal of type Metadata`.

- [ ] **Step 3: Add setup metadata field**

Modify `internal/run/run.go` imports to include suite if needed:

```go
	"github.com/Azure/aks-burner/internal/suite"
```

Update `Metadata` in `internal/run/run.go` to:

```go
type Metadata struct {
	Suite         string            `yaml:"suite"`
	Mode          string            `yaml:"mode"`
	Timestamp     string            `yaml:"timestamp"`
	ResourceGroup string            `yaml:"resourceGroup"`
	ClusterName   string            `yaml:"clusterName"`
	Images        map[string]string `yaml:"images"`
	BuiltImages   []acr.BuiltImage  `yaml:"builtImages,omitempty"`
	Setup         suite.Setup       `yaml:"setup,omitempty"`
}
```

- [ ] **Step 4: Run metadata test to verify it passes**

Run: `go test ./internal/run -run TestWriteMetadataIncludesSetup`

Expected: PASS.

- [ ] **Step 5: Add failing CLI test for suite schema validation with setup**

Add this focused test to `cmd/perf-runner/main_test.go` after `TestRunDispatchesRunSuite` to prove `run-suite` validates `suite.yml` and rejects invalid setup before any external Azure calls:

```go
func TestRunSuiteRejectsInvalidSuiteSetupSchema(t *testing.T) {
	root := testRepoRoot(t)
	suiteDir := filepath.Join(root, "suites", "bad-setup")
	if err := os.MkdirAll(filepath.Join(suiteDir, "vars"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.yml"), []byte(`name: bad-setup
description: Bad setup suite
tests:
  - startup
setup:
  resources:
    - name: node-prep
      path: setup/node-prep.yml
      wait:
        - kind: sleep
          resource: daemonset/node-prep
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "requirements.yml"), []byte(`suite: bad-setup
requires:
  infrastructure:
    provider: aks
    bicep:
      template: infra/aks/main.bicep
      parameters: suites/bad-setup/infra.bicepparam
  kubernetes:
    minVersion: ""
  nodeSelectors: []
  observability:
    prometheus:
      required: false
      install: false
      namespace: perf-monitoring
      imageKey: prometheus
      serviceName: prometheus
      servicePort: 9090
      localPort: 9090
      requiredMetrics: []
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "infra.bicepparam"), []byte("param clusterName = 'aksbadsetup'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "infra", "aks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "infra", "aks", "main.bicep"), []byte("param clusterName string\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "images.yml"), []byte("pause: mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2\nprometheus: prom/prometheus:v2.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "workload.yml"), []byte("jobs: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "vars", "smoke.yml"), []byte(`iterations: 1
iterationsPerNamespace: 1
qps: 1
burst: 1
cleanup: true
waitWhenFinished: true
preLoadImages: false
templateVars: {}
imageVars: {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	withWorkingDir(t, root)

	err = run([]string{"run-suite", "--suite", "bad-setup", "--mode", "smoke", "--resource-group", "rg-aks-burner-bad-setup"})
	if err == nil || !strings.Contains(err.Error(), "sleep") {
		t.Fatalf("run-suite error = %v, want suite setup schema validation error", err)
	}
}
```

- [ ] **Step 6: Run CLI schema test to verify it fails**

Run: `go test ./cmd/perf-runner -run TestRunSuiteRejectsInvalidSuiteSetupSchema`

Expected: FAIL because `runSuite` does not yet validate `suite.yml`; it will proceed to Azure credential retrieval or another later operation instead of failing on suite schema.

- [ ] **Step 7: Validate and load suite config in runSuite**

Modify `cmd/perf-runner/main.go` inside `runSuite`, after validating suite/mode names and before `requirements.yml` validation:

```go
	suitePath, err := resolveSuitePath(root, *suiteName, "suite.yml")
	if err != nil {
		return err
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "suite.schema.json"), suitePath); err != nil {
		return err
	}
	suiteCfg, err := suite.Load(root, *suiteName)
	if err != nil {
		return err
	}
```

Keep the existing `suiteDir := filepath.Join(root, "suites", *suiteName)` later in the function.

- [ ] **Step 8: Run CLI schema test to verify it passes**

Run: `go test ./cmd/perf-runner -run TestRunSuiteRejectsInvalidSuiteSetupSchema`

Expected: PASS.

- [ ] **Step 9: Integrate setup apply before Prometheus**

Modify `cmd/perf-runner/main.go` after `images := mergeImages(staticImages, builtImageMap)` and before `if req.Requires.Observability.Prometheus.Required ...`:

```go
	if err := runpkg.ApplySetup(ctx, suiteDir, suiteCfg.Setup, runpkg.KubectlOutput); err != nil {
		return err
	}
```

Modify the metadata write call to include setup:

```go
	if err := runpkg.WriteMetadata(runDir, runpkg.Metadata{Suite: *suiteName, Mode: *modeName, Timestamp: runTimestamp.Format(time.RFC3339), ResourceGroup: *resourceGroup, ClusterName: clusterName, Images: images, BuiltImages: builtImages, Setup: suiteCfg.Setup}); err != nil {
		return err
	}
```

- [ ] **Step 10: Run integration-adjacent package tests**

Run: `go test ./cmd/perf-runner ./internal/run ./internal/suite`

Expected: PASS.

- [ ] **Step 11: Request code review for Task 3**

Run: `git diff HEAD -- internal/run/run.go internal/run/run_test.go cmd/perf-runner/main.go cmd/perf-runner/main_test.go`

Invoke the `code-reviewer` sub-agent with that diff and this task's requirements. Fix verified Critical or High findings before committing.

- [ ] **Step 12: Commit Task 3**

Run:

```bash
git add internal/run/run.go internal/run/run_test.go cmd/perf-runner/main.go cmd/perf-runner/main_test.go
git commit -m "feat: run suite setup before benchmarks"
```

Expected: commit succeeds.

---

### Task 4: Documentation And Full Verification

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: suite config format from Task 1.
- Consumes: setup lifecycle from Task 3.
- Produces: user-facing docs for `setup.resources`.

- [ ] **Step 1: Add README documentation**

Add this section to `README.md` after the `Suite Images` section:

````markdown
## Suite Setup Resources

Suites can install persistent Kubernetes setup resources before kube-burner starts. Use this for suite-specific cluster preparation such as custom `RuntimeClass` objects or node-preparation `DaemonSet` resources.

Declare setup resources in `suites/<suite>/suite.yml`:

```yaml
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
```

`run-suite` applies each manifest with `kubectl apply -f` and then runs the declared waits before installing Prometheus or rendering the benchmark workload. Supported wait kinds are:

- `exists`: runs `kubectl get <resource>` when no timeout is set, or `kubectl wait <resource> --for=create --timeout <timeout>` when a timeout is set.
- `rollout`: runs `kubectl rollout status <resource>`.
- `condition`: runs `kubectl wait <resource> --for=condition=<condition>`.

Setup resources are intentionally kept after the run finishes. Remove them manually with `kubectl delete` when they are no longer needed, or destroy the suite resource group when using an isolated provisioned cluster.
````

- [ ] **Step 2: Run focused tests**

Run: `go test ./internal/suite ./internal/run ./internal/examples ./cmd/perf-runner`

Expected: PASS.

- [ ] **Step 3: Run full test suite**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 4: Review final diff**

Run: `git diff HEAD`

Expected: diff only includes suite setup resources implementation, tests, docs, the approved design spec, and this plan unless earlier tasks were committed separately.

- [ ] **Step 5: Run required code review before final commit if changes remain unstaged**

Run: `git diff HEAD`

Invoke the `code-reviewer` sub-agent with the complete diff. Fix verified Critical or High findings before committing.

- [ ] **Step 6: Commit Task 4**

Run:

```bash
git add README.md docs/superpowers/specs/2026-07-10-suite-setup-resources-design.md docs/superpowers/plans/2026-07-10-suite-setup-resources.md
git commit -m "docs: describe suite setup resources"
```

Expected: commit succeeds.

---

## Self-Review Notes

- Spec coverage: Task 1 covers suite declaration and schema validation. Task 2 covers apply, wait, path validation, idempotent `kubectl apply`, and failure ordering at helper level. Task 3 covers run lifecycle integration and metadata. Task 4 covers user docs and full verification.
- Red-flag scan: no unfinished markers or unspecified implementation steps remain.
- Type consistency: all tasks use `suite.Setup`, `suite.SetupResource`, `suite.WaitRule`, `run.ApplySetup`, `run.ResolveSetupPath`, and `run.WaitRuleArgs` consistently.
