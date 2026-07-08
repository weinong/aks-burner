# Kube Burner Test Suite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish a general kube-burner performance test framework that can provision AKS test infrastructure with Bicep, list and run suites through Make targets, optionally install in-cluster Prometheus, and collect metrics through port-forwarding.

**Architecture:** The repository is suite-driven: each suite owns `suite.yml`, `requirements.yml`, kube-burner workload files, modes, and optional Bicep parameters. A Go CLI runner backs Make targets for suite discovery, provisioning, preflight, Prometheus setup, rendering, execution, and teardown. Bicep creates AKS infrastructure, while requirements declare what a suite needs and whether Prometheus must be installed for metrics collection.

**Tech Stack:** Go 1.25, Bicep, Azure CLI, AKS, kube-burner CLI, Kubernetes YAML manifests, `kubectl port-forward`, `gopkg.in/yaml.v3`, `github.com/santhosh-tekuri/jsonschema/v6`, `go test`, Make.

## Global Constraints

- Use repo-root directories: `bin/`, `cmd/`, `internal/`, `infra/`, `observability/`, `schemas/`, `suites/`, and `results/`.
- Implement the runner in Go, not Python.
- Make targets are the primary user interface.
- Suite commands use `TEST_SUITE=<suite-name>` and optional `TEST_MODE=<mode>`.
- Bicep-based AKS creation is in scope.
- Separate Make targets must exist for listing suites, creating infra, running a suite, and destroying infra.
- Default `TEST_MODE` is `smoke`.
- RuntimeClass is not a framework prerequisite; runtime-specific settings belong to suite-owned workload templates, parameters, or documentation.
- Requirements can request in-cluster Prometheus installation.
- Container images are globally defined in `config/images.yml` and referenced from suites by image key.
- Global image key `prometheus` resolves to `mcr.microsoft.com/oss/v2/prometheus/prometheus:v3.11.3`.
- Global image key `pause` resolves to `mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2`.
- If a suite requires Prometheus, `make run-suite` must start a local port-forward and pass the local Prometheus URL to kube-burner.
- Result metadata must not store kubeconfig contents, bearer tokens, Azure access tokens, Prometheus tokens, or authorization headers.

---

## File Structure

- `Makefile`: primary commands: `list-suites`, `provision`, `run-suite`, `destroy`, `test`, `build`, `clean-results`.
- `config/images.yml`: global image catalog used by all suites and observability manifests.
- `go.mod`: Go module and dependencies.
- `cmd/perf-runner/main.go`: CLI dispatcher used by Make targets.
- `internal/repo/`: repository root discovery.
- `internal/config/`: YAML loading, JSON Schema validation, test and suite path resolution, mode loading, workload rendering.
- `internal/suite/`: suite registry, suite listing, suite config loading.
- `internal/infra/`: Azure CLI and Bicep orchestration.
- `internal/prometheus/`: Prometheus manifest rendering, install/verify, port-forward lifecycle, readiness check.
- `internal/run/`: suite execution, result directory creation, metadata capture, kube-burner invocation.
- `schemas/`: JSON Schema contracts for suite, requirements, mode, environment, and Bicep binding files.
- `infra/aks/main.bicep`: reusable AKS Bicep template.
- `observability/prometheus/prometheus.yaml`: self-hosted Prometheus Kubernetes manifests.
- `suites/<suite>/`: suite-owned registry, requirements, infrastructure parameters, workload, templates, and mode files.
- `results/`: generated run output.

---

### Task 1: Go Module, Schemas, And Repository Skeleton

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `README.md`
- Create: `Makefile`
- Create: `config/images.yml`
- Create: `schemas/suite.schema.json`
- Create: `schemas/requirements.schema.json`
- Create: `schemas/mode.schema.json`
- Create: `schemas/environment.schema.json`
- Create: `results/.gitkeep`
- Create: `internal/repo/repo.go`
- Create: `internal/repo/repo_test.go`

**Interfaces:**
- Produces: `repo.Root(start string) (string, error)`.
- Produces: initial Make targets that call future runner commands.
- Produces: global image catalog consumed by render and Prometheus installation tasks.
- Produces: machine-validatable schema contracts consumed by later tasks.
- Consumes: no earlier task outputs.

- [ ] **Step 1: Write the failing repository root test**

Create `internal/repo/repo_test.go` with:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/repo`

Expected: FAIL because `go.mod` or `Root` does not exist.

- [ ] **Step 3: Add module and repository root implementation**

Create `go.mod` with:

```go
module github.com/Azure/aks-burner

go 1.25

require (
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.1
	gopkg.in/yaml.v3 v3.0.1
)
```

Create `internal/repo/repo.go` with:

```go
package repo

import (
	"fmt"
	"os"
	"path/filepath"
)

func Root(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("could not find go.mod from %s", start)
		}
		current = parent
	}
}
```

- [ ] **Step 4: Add Makefile, README, ignore rules, and schemas**

Create `.gitignore` with:

```gitignore
results/*
!results/.gitkeep
.rendered/
bin/perf-runner
*.test
```

Create `Makefile` with:

```makefile
TEST_MODE ?= smoke
AZURE_LOCATION ?= westus2
RESOURCE_GROUP ?= rg-aks-burner-$(TEST_SUITE)

.PHONY: test build list-suites provision run-suite destroy clean-results

test:
	go test ./...

build:
	go build -o bin/perf-runner ./cmd/perf-runner

list-suites:
	go run ./cmd/perf-runner list-suites

provision:
	@test -n "$(TEST_SUITE)" || (echo "TEST_SUITE is required" && exit 1)
	go run ./cmd/perf-runner provision --suite "$(TEST_SUITE)" --resource-group "$(RESOURCE_GROUP)" --location "$(AZURE_LOCATION)"

run-suite:
	@test -n "$(TEST_SUITE)" || (echo "TEST_SUITE is required" && exit 1)
	go run ./cmd/perf-runner run-suite --suite "$(TEST_SUITE)" --mode "$(TEST_MODE)" --resource-group "$(RESOURCE_GROUP)"

destroy:
	@test -n "$(TEST_SUITE)" || (echo "TEST_SUITE is required" && exit 1)
	go run ./cmd/perf-runner destroy --suite "$(TEST_SUITE)" --resource-group "$(RESOURCE_GROUP)"

clean-results:
	rm -rf results/*
	touch results/.gitkeep
```

Create `config/images.yml` with:

```yaml
images:
  pause: mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2
  prometheus: mcr.microsoft.com/oss/v2/prometheus/prometheus:v3.11.3
```

Create `README.md` with:

```markdown
# aks-burner

This repository organizes kube-burner performance test suites and can provision AKS infrastructure for those suites with Bicep.

## Common Commands

```bash
make list-suites
TEST_SUITE=kata-disk-perf make provision
TEST_SUITE=kata-disk-perf TEST_MODE=smoke make run-suite
TEST_SUITE=kata-disk-perf make destroy
```

`TEST_MODE` defaults to `smoke`. `RESOURCE_GROUP` defaults to `rg-aks-burner-$(TEST_SUITE)`. `AZURE_LOCATION` defaults to `westus2`.
```

Create `schemas/suite.schema.json` with:

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
    }
  }
}
```

Create `schemas/requirements.schema.json` with:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["suite", "requires"],
  "properties": {
    "suite": { "type": "string", "minLength": 1 },
    "requires": {
      "type": "object",
      "additionalProperties": false,
      "required": ["infrastructure", "kubernetes", "observability"],
      "properties": {
        "infrastructure": {
          "type": "object",
          "additionalProperties": false,
          "required": ["provider", "bicep"],
          "properties": {
            "provider": { "type": "string", "enum": ["aks"] },
            "bicep": {
              "type": "object",
              "additionalProperties": false,
              "required": ["template", "parameters"],
              "properties": {
                "template": { "type": "string", "minLength": 1 },
                "parameters": { "type": "string", "minLength": 1 }
              }
            }
          }
        },
        "kubernetes": {
          "type": "object",
          "additionalProperties": false,
          "required": ["minVersion"],
          "properties": {
            "minVersion": { "type": "string", "pattern": "^v?[0-9]+\\.[0-9]+(\\.[0-9]+)?$" }
          }
        },
        "nodeSelectors": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["name", "required", "minNodes", "labels"],
            "properties": {
              "name": { "type": "string", "minLength": 1 },
              "required": { "type": "boolean" },
              "minNodes": { "type": "integer", "minimum": 0 },
              "labels": { "type": "object", "additionalProperties": { "type": "string" } }
            }
          }
        },
        "observability": {
          "type": "object",
          "additionalProperties": false,
          "required": ["prometheus"],
          "properties": {
            "prometheus": {
              "type": "object",
              "additionalProperties": false,
              "required": ["required", "install", "namespace", "imageKey", "serviceName", "servicePort", "localPort"],
              "properties": {
                "required": { "type": "boolean" },
                "install": { "type": "boolean" },
                "namespace": { "type": "string", "minLength": 1 },
                "imageKey": { "type": "string", "minLength": 1 },
                "serviceName": { "type": "string", "minLength": 1 },
                "servicePort": { "type": "integer", "minimum": 1, "maximum": 65535 },
                "localPort": { "type": "integer", "minimum": 1, "maximum": 65535 },
                "requiredMetrics": { "type": "array", "items": { "type": "string", "minLength": 1 } }
              }
            }
          }
        }
      }
    }
  }
}
```

Create `schemas/mode.schema.json` with:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["iterations", "iterationsPerNamespace", "qps", "burst", "cleanup", "waitWhenFinished", "preLoadImages", "templateVars", "imageVars"],
  "properties": {
    "iterations": { "type": "integer", "minimum": 1 },
    "iterationsPerNamespace": { "type": "integer", "minimum": 1 },
    "qps": { "type": "integer", "minimum": 1 },
    "burst": { "type": "integer", "minimum": 1 },
    "cleanup": { "type": "boolean" },
    "waitWhenFinished": { "type": "boolean" },
    "preLoadImages": { "type": "boolean" },
    "templateVars": { "type": "object", "additionalProperties": true },
    "imageVars": {
      "type": "object",
      "additionalProperties": { "type": "string", "minLength": 1 }
    }
  }
}
```

Create `schemas/environment.schema.json` with:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["environment", "provider", "resourceGroup", "clusterName"],
  "properties": {
    "environment": { "type": "string", "minLength": 1 },
    "provider": { "type": "string", "enum": ["aks"] },
    "resourceGroup": { "type": "string", "minLength": 1 },
    "clusterName": { "type": "string", "minLength": 1 }
  }
}
```

Create `results/.gitkeep` as an empty file.

- [ ] **Step 5: Run tests**

Run: `go test ./internal/repo`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod .gitignore README.md Makefile config/images.yml schemas internal/repo results/.gitkeep
git commit -m "chore: add suite framework skeleton"
```

---

### Task 2: Config, Schema Validation, And Suite Discovery

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `internal/suite/suite.go`
- Create: `internal/suite/suite_test.go`
- Create: `cmd/perf-runner/main.go`

**Interfaces:**
- Consumes: `repo.Root(start string) (string, error)` from Task 1.
- Produces: `config.LoadYAML(path string, out any) error`.
- Produces: `config.WriteYAML(path string, value any) error`.
- Produces: `config.ValidateYAML(schemaPath string, yamlPath string) error`.
- Produces: `suite.Load(root string, name string) (suite.Config, error)`.
- Produces: `suite.List(root string) ([]suite.Config, error)`.
- Produces: CLI command `perf-runner list-suites`.

- [ ] **Step 1: Write failing config tests**

Create `internal/config/config_test.go` with:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateYAMLUsesJSONSchema(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "schema.json")
	yamlPath := filepath.Join(dir, "value.yml")
	if err := os.WriteFile(schemaPath, []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["name"],
  "additionalProperties": false,
  "properties": {"name": {"type": "string"}}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yamlPath, []byte("name: example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateYAML(schemaPath, yamlPath); err != nil {
		t.Fatalf("ValidateYAML returned error: %v", err)
	}
	if err := os.WriteFile(yamlPath, []byte("name: example\nextra: nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateYAML(schemaPath, yamlPath); err == nil {
		t.Fatal("ValidateYAML accepted extra property")
	}
}
```

- [ ] **Step 2: Write failing suite discovery tests**

Create `internal/suite/suite_test.go` with:

```go
package suite

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListSuites(t *testing.T) {
	root := t.TempDir()
	suiteDir := filepath.Join(root, "suites", "kata-disk-perf")
	if err := os.MkdirAll(suiteDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("name: kata-disk-perf\ndescription: Disk perf suite\ntests:\n  - write-iops\n")
	if err := os.WriteFile(filepath.Join(suiteDir, "suite.yml"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	suites, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(suites) != 1 || suites[0].Name != "kata-disk-perf" || suites[0].Tests[0] != "write-iops" {
		t.Fatalf("unexpected suites: %#v", suites)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/config ./internal/suite`

Expected: FAIL because the packages do not exist.

- [ ] **Step 4: Implement config package**

Create `internal/config/config.go` with:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

func LoadYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, out)
}

func WriteYAML(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func ValidateYAML(schemaPath string, yamlPath string) error {
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile(schemaPath)
	if err != nil {
		return err
	}
	var value any
	if err := LoadYAML(yamlPath, &value); err != nil {
		return err
	}
	return schema.Validate(toJSONValue(value))
}

type ImageCatalog struct {
	Images map[string]string `yaml:"images"`
}

func LoadImages(path string) (map[string]string, error) {
	var catalog ImageCatalog
	if err := LoadYAML(path, &catalog); err != nil {
		return nil, err
	}
	return catalog.Images, nil
}

func ResolveImage(images map[string]string, key string) (string, error) {
	image, ok := images[key]
	if !ok || image == "" {
		return "", fmt.Errorf("image key %q not found", key)
	}
	return image, nil
}

func toJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		converted := map[string]any{}
		for key, item := range typed {
			converted[key] = toJSONValue(item)
		}
		return converted
	case []any:
		converted := make([]any, len(typed))
		for i, item := range typed {
			converted[i] = toJSONValue(item)
		}
		return converted
	default:
		return typed
	}
}
```

- [ ] **Step 5: Implement suite package and CLI**

Create `internal/suite/suite.go` with:

```go
package suite

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/Azure/aks-burner/internal/config"
)

type Config struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tests       []string `yaml:"tests"`
}

func Load(root string, name string) (Config, error) {
	var cfg Config
	path := filepath.Join(root, "suites", name, "suite.yml")
	if err := config.LoadYAML(path, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

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
		suites = append(suites, cfg)
	}
	sort.Slice(suites, func(i, j int) bool { return suites[i].Name < suites[j].Name })
	return suites, nil
}
```

Create `cmd/perf-runner/main.go` with:

```go
package main

import (
	"fmt"
	"os"

	"github.com/Azure/aks-burner/internal/repo"
	"github.com/Azure/aks-burner/internal/suite"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: perf-runner <list-suites|provision|run-suite|destroy> ...")
	}
	switch args[0] {
	case "list-suites":
		return listSuites()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func listSuites() error {
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	suites, err := suite.List(root)
	if err != nil {
		return err
	}
	for _, cfg := range suites {
		fmt.Printf("%s\t%s\n", cfg.Name, cfg.Description)
	}
	return nil
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/config ./internal/suite ./cmd/perf-runner`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/config internal/suite cmd/perf-runner go.mod go.sum
git commit -m "feat: add suite discovery and schema validation"
```

---

### Task 3: AKS Bicep Infrastructure And Provision Command

**Files:**
- Create: `infra/aks/main.bicep`
- Create: `internal/infra/infra.go`
- Create: `internal/infra/infra_test.go`
- Modify: `cmd/perf-runner/main.go`

**Interfaces:**
- Consumes: suite requirements from `suites/<suite>/requirements.yml`.
- Produces: `infra.Provision(ctx context.Context, opts infra.ProvisionOptions) error`.
- Produces: CLI command `perf-runner provision --suite SUITE --resource-group RG --location LOCATION`.

- [ ] **Step 1: Write failing infra command construction tests**

Create `internal/infra/infra_test.go` with:

```go
package infra

import "testing"

func TestProvisionCommands(t *testing.T) {
	opts := ProvisionOptions{
		ResourceGroup:  "rg-aks-burner-test",
		Location:       "westus2",
		ParametersFile: "suites/kata-disk-perf/infra.bicepparam",
		ClusterName:    "akstest",
	}
	commands := ProvisionCommands(opts)
	if commands[0][0] != "az" || commands[0][1] != "group" || commands[0][2] != "create" {
		t.Fatalf("unexpected group command: %#v", commands[0])
	}
	if commands[1][0] != "az" || commands[1][1] != "deployment" || commands[1][2] != "group" || commands[1][3] != "create" {
		t.Fatalf("unexpected deployment command: %#v", commands[1])
	}
	for _, arg := range commands[1] {
		if arg == "--template-file" {
			t.Fatalf("deployment command must use .bicepparam directly without --template-file: %#v", commands[1])
		}
	}
	if commands[2][0] != "az" || commands[2][1] != "aks" || commands[2][2] != "get-credentials" {
		t.Fatalf("unexpected credentials command: %#v", commands[2])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/infra`

Expected: FAIL because `internal/infra` does not exist.

- [ ] **Step 3: Add AKS Bicep template**

Create `infra/aks/main.bicep` with:

```bicep
targetScope = 'resourceGroup'

param clusterName string
param location string = resourceGroup().location
param kubernetesVersion string = ''
param systemNodeCount int = 1
param systemNodeVmSize string = 'Standard_D4s_v5'
param userNodeCount int = 3
param userNodeVmSize string = 'Standard_D8s_v5'
param userNodeLabels object = {
  'perf.azure.com/node-role': 'workload'
}

resource aks 'Microsoft.ContainerService/managedClusters@2025-05-01' = {
  name: clusterName
  location: location
  identity: {
    type: 'SystemAssigned'
  }
  properties: {
    dnsPrefix: clusterName
    kubernetesVersion: empty(kubernetesVersion) ? null : kubernetesVersion
    agentPoolProfiles: [
      {
        name: 'systempool'
        mode: 'System'
        count: systemNodeCount
        vmSize: systemNodeVmSize
        osType: 'Linux'
        type: 'VirtualMachineScaleSets'
      }
      {
        name: 'userpool'
        mode: 'User'
        count: userNodeCount
        vmSize: userNodeVmSize
        osType: 'Linux'
        type: 'VirtualMachineScaleSets'
        nodeLabels: userNodeLabels
      }
    ]
    networkProfile: {
      networkPlugin: 'azure'
      networkPluginMode: 'overlay'
      networkDataplane: 'cilium'
      networkPolicy: 'cilium'
    }
  }
}

output clusterName string = aks.name
```

- [ ] **Step 4: Implement infra package**

Create `internal/infra/infra.go` with:

```go
package infra

import (
	"context"
	"os"
	"os/exec"
)

type ProvisionOptions struct {
	ResourceGroup  string
	Location       string
	ParametersFile string
	ClusterName    string
}

func ProvisionCommands(opts ProvisionOptions) [][]string {
	return [][]string{
		{"az", "group", "create", "--name", opts.ResourceGroup, "--location", opts.Location},
		{"az", "deployment", "group", "create", "--resource-group", opts.ResourceGroup, "--parameters", opts.ParametersFile, "location=" + opts.Location},
		{"az", "aks", "get-credentials", "--resource-group", opts.ResourceGroup, "--name", opts.ClusterName, "--overwrite-existing"},
	}
}

func Provision(ctx context.Context, opts ProvisionOptions) error {
	for _, args := range ProvisionCommands(opts) {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Add provision CLI dispatch**

Modify `cmd/perf-runner/main.go` to include:

```go
// Add imports:
// "context"
// "flag"
// "os"
// "path/filepath"
// "regexp"
// "github.com/Azure/aks-burner/internal/config"
// "github.com/Azure/aks-burner/internal/infra"

case "provision":
	return provision(args[1:])

func provision(args []string) error {
	fs := flag.NewFlagSet("provision", flag.ContinueOnError)
	suiteName := fs.String("suite", "", "suite name")
	resourceGroup := fs.String("resource-group", "", "Azure resource group")
	location := fs.String("location", "", "Azure location")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *suiteName == "" || *resourceGroup == "" || *location == "" {
		return fmt.Errorf("usage: perf-runner provision --suite SUITE --resource-group RG --location LOCATION")
	}
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	var req struct {
		Requires struct {
			Infrastructure struct {
				Bicep struct {
					Template   string `yaml:"template"`
					Parameters string `yaml:"parameters"`
				} `yaml:"bicep"`
			} `yaml:"infrastructure"`
		} `yaml:"requires"`
	}
	reqPath := filepath.Join(root, "suites", *suiteName, "requirements.yml")
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), reqPath); err != nil {
		return err
	}
	if err := config.LoadYAML(reqPath, &req); err != nil {
		return err
	}
	parametersPath := filepath.Join(root, req.Requires.Infrastructure.Bicep.Parameters)
	clusterName, err := readBicepParamString(parametersPath, "clusterName")
	if err != nil {
		return err
	}
	return infra.Provision(context.Background(), infra.ProvisionOptions{
		ResourceGroup:  *resourceGroup,
		Location:       *location,
		ParametersFile: parametersPath,
		ClusterName:    clusterName,
	})
}

func readBicepParamString(path string, name string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	pattern := regexp.MustCompile(`(?m)^param\s+` + regexp.QuoteMeta(name) + `\s*=\s*'([^']+)'\s*$`)
	matches := pattern.FindStringSubmatch(string(data))
	if len(matches) != 2 {
		return "", fmt.Errorf("parameter %s not found in %s", name, path)
	}
	return matches[1], nil
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/infra ./cmd/perf-runner`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add infra/aks internal/infra cmd/perf-runner/main.go
git commit -m "feat: add AKS Bicep provisioning"
```

---

### Task 4: Prometheus Install And Port-Forward Support

**Files:**
- Create: `observability/prometheus/prometheus.yaml`
- Create: `internal/prometheus/prometheus.go`
- Create: `internal/prometheus/prometheus_test.go`

**Interfaces:**
- Produces: `prometheus.Config` matching `requirements.yml` observability fields.
- Produces: `prometheus.Install(ctx context.Context, manifestPath string, image string) error`.
- Produces: `prometheus.PortForward(ctx context.Context, cfg prometheus.Config) (*exec.Cmd, string, error)`.
- Produces: `prometheus.WaitReady(ctx context.Context, url string) error`.

- [ ] **Step 1: Write failing Prometheus tests**

Create `internal/prometheus/prometheus_test.go` with:

```go
package prometheus

import "testing"

func TestEndpointURL(t *testing.T) {
	cfg := Config{LocalPort: 9090}
	if got := EndpointURL(cfg); got != "http://127.0.0.1:9090" {
		t.Fatalf("EndpointURL() = %q", got)
	}
}

func TestPortForwardArgs(t *testing.T) {
	cfg := Config{Namespace: "perf-monitoring", ServiceName: "prometheus", ServicePort: 9090, LocalPort: 19090}
	args := PortForwardArgs(cfg)
	want := []string{"kubectl", "-n", "perf-monitoring", "port-forward", "service/prometheus", "19090:9090"}
	if len(args) != len(want) {
		t.Fatalf("args length = %d, want %d: %#v", len(args), len(want), args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestRenderManifestReplacesPrometheusImage(t *testing.T) {
	manifest := "image: {{PROMETHEUS_IMAGE}}\n"
	rendered := RenderManifest(manifest, "mcr.microsoft.com/oss/v2/prometheus/prometheus:v3.11.3")
	if rendered != "image: mcr.microsoft.com/oss/v2/prometheus/prometheus:v3.11.3\n" {
		t.Fatalf("unexpected manifest: %q", rendered)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/prometheus`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Add Prometheus manifest**

Create `observability/prometheus/prometheus.yaml` with:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: perf-monitoring
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: prometheus-config
  namespace: perf-monitoring
data:
  prometheus.yml: |
    global:
      scrape_interval: 15s
    scrape_configs:
      - job_name: kubernetes-nodes-cadvisor
        scheme: https
        kubernetes_sd_configs:
          - role: node
        bearer_token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
        tls_config:
          insecure_skip_verify: true
        relabel_configs:
          - action: labelmap
            regex: __meta_kubernetes_node_label_(.+)
          - target_label: __address__
            replacement: kubernetes.default.svc:443
          - source_labels: [__meta_kubernetes_node_name]
            regex: (.+)
            target_label: __metrics_path__
            replacement: /api/v1/nodes/${1}/proxy/metrics/cadvisor
      - job_name: kubernetes-nodes
        scheme: https
        kubernetes_sd_configs:
          - role: node
        bearer_token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
        tls_config:
          insecure_skip_verify: true
        relabel_configs:
          - action: labelmap
            regex: __meta_kubernetes_node_label_(.+)
          - target_label: __address__
            replacement: kubernetes.default.svc:443
          - source_labels: [__meta_kubernetes_node_name]
            regex: (.+)
            target_label: __metrics_path__
            replacement: /api/v1/nodes/${1}/proxy/metrics
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: prometheus
  namespace: perf-monitoring
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: perf-prometheus
rules:
  - apiGroups: [""]
    resources: [nodes, nodes/proxy, services, endpoints, pods]
    verbs: [get, list, watch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: perf-prometheus
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: perf-prometheus
subjects:
  - kind: ServiceAccount
    name: prometheus
    namespace: perf-monitoring
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: prometheus
  namespace: perf-monitoring
spec:
  replicas: 1
  selector:
    matchLabels:
      app: prometheus
  template:
    metadata:
      labels:
        app: prometheus
    spec:
      serviceAccountName: prometheus
      containers:
        - name: prometheus
          image: {{PROMETHEUS_IMAGE}}
          args:
            - --config.file=/etc/prometheus/prometheus.yml
            - --storage.tsdb.path=/prometheus
          ports:
            - containerPort: 9090
          volumeMounts:
            - name: config
              mountPath: /etc/prometheus
      volumes:
        - name: config
          configMap:
            name: prometheus-config
---
apiVersion: v1
kind: Service
metadata:
  name: prometheus
  namespace: perf-monitoring
spec:
  selector:
    app: prometheus
  ports:
    - name: http
      port: 9090
      targetPort: 9090
```

- [ ] **Step 4: Implement Prometheus package**

Create `internal/prometheus/prometheus.go` with:

```go
package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Config struct {
	Required    bool     `yaml:"required"`
	Install     bool     `yaml:"install"`
	Namespace   string   `yaml:"namespace"`
	ImageKey    string   `yaml:"imageKey"`
	ServiceName string   `yaml:"serviceName"`
	ServicePort int      `yaml:"servicePort"`
	LocalPort   int      `yaml:"localPort"`
	Metrics     []string `yaml:"requiredMetrics"`
}

func EndpointURL(cfg Config) string {
	return fmt.Sprintf("http://127.0.0.1:%d", cfg.LocalPort)
}

func PortForwardArgs(cfg Config) []string {
	return []string{"kubectl", "-n", cfg.Namespace, "port-forward", "service/" + cfg.ServiceName, fmt.Sprintf("%d:%d", cfg.LocalPort, cfg.ServicePort)}
}

func RenderManifest(manifest string, image string) string {
	return strings.ReplaceAll(manifest, "{{PROMETHEUS_IMAGE}}", image)
}

func Install(ctx context.Context, manifestPath string, image string) error {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	rendered := RenderManifest(string(manifest), image)
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(rendered)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func PortForward(ctx context.Context, cfg Config) (*exec.Cmd, string, error) {
	args := PortForwardArgs(cfg)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	return cmd, EndpointURL(cfg), nil
}

func WaitReady(ctx context.Context, endpoint string) error {
	client := http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			resp, err := client.Get(endpoint + "/-/ready")
			if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				_ = resp.Body.Close()
				return nil
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
		}
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/prometheus`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add observability/prometheus internal/prometheus
git commit -m "feat: add in-cluster Prometheus support"
```

---

### Task 5: Suite Example With Infra Requirements And Modes

**Files:**
- Create: `suites/kata-disk-perf/suite.yml`
- Create: `suites/kata-disk-perf/requirements.yml`
- Create: `suites/kata-disk-perf/infra.bicepparam`
- Create: `suites/kata-disk-perf/workload.yml`
- Create: `suites/kata-disk-perf/metrics.yml`
- Create: `suites/kata-disk-perf/templates/pod.yml`
- Create: `suites/kata-disk-perf/vars/smoke.yml`
- Create: `suites/kata-disk-perf/vars/full.yml`
- Create: `internal/examples/examples_test.go`

**Interfaces:**
- Consumes: schema contracts from Task 1.
- Produces: a concrete suite that can be listed with `make list-suites`, provisioned with `TEST_SUITE=kata-disk-perf make provision`, and run with `TEST_SUITE=kata-disk-perf make run-suite`.

- [ ] **Step 1: Write failing example validation test**

Create `internal/examples/examples_test.go` with:

```go
package examples

import (
	"path/filepath"
	"testing"

	"github.com/Azure/aks-burner/internal/config"
)

func TestKataDiskPerfContractsValidate(t *testing.T) {
	root := filepath.Join("..", "..")
	cases := []struct {
		schema string
		file   string
	}{
		{"schemas/suite.schema.json", "suites/kata-disk-perf/suite.yml"},
		{"schemas/requirements.schema.json", "suites/kata-disk-perf/requirements.yml"},
		{"schemas/mode.schema.json", "suites/kata-disk-perf/vars/smoke.yml"},
		{"schemas/mode.schema.json", "suites/kata-disk-perf/vars/full.yml"},
	}
	for _, tc := range cases {
		if err := config.ValidateYAML(filepath.Join(root, tc.schema), filepath.Join(root, tc.file)); err != nil {
			t.Fatalf("%s failed validation against %s: %v", tc.file, tc.schema, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/examples`

Expected: FAIL because `suites/kata-disk-perf` does not exist.

- [ ] **Step 3: Add suite files**

Create `suites/kata-disk-perf/suite.yml` with:

```yaml
name: kata-disk-perf
description: Example suite for validating kube-burner framework provisioning, Prometheus setup, and suite execution.
tests:
  - startup-smoke
```

Create `suites/kata-disk-perf/requirements.yml` with:

```yaml
suite: kata-disk-perf
requires:
  infrastructure:
    provider: aks
    bicep:
      template: infra/aks/main.bicep
      parameters: suites/kata-disk-perf/infra.bicepparam
  kubernetes:
    minVersion: "1.30"
  nodeSelectors:
    - name: workload
      required: true
      minNodes: 3
      labels:
        perf.azure.com/node-role: workload
  observability:
    prometheus:
      required: true
      install: true
      namespace: perf-monitoring
      imageKey: prometheus
      serviceName: prometheus
      servicePort: 9090
      localPort: 9090
      requiredMetrics:
        - container_cpu_usage_seconds_total
        - container_memory_working_set_bytes
```

Create `suites/kata-disk-perf/infra.bicepparam` with:

```bicep
using '../../infra/aks/main.bicep'

param clusterName = 'akskdisktest'
param userNodeCount = 3
param userNodeVmSize = 'Standard_D8s_v5'
param userNodeLabels = {
  'perf.azure.com/node-role': 'workload'
}
```

Create `suites/kata-disk-perf/workload.yml` with:

```yaml
global:
  measurements:
    - name: podLatency
jobs:
  - name: startup-smoke
    jobType: create
    namespace: kata-disk-perf
    namespacedIterations: true
    objects:
      - objectTemplate: templates/pod.yml
        replicas: 1
        inputVars: {}
```

Create `suites/kata-disk-perf/metrics.yml` with:

```yaml
- query: sum(rate(container_cpu_usage_seconds_total[2m])) by (pod, namespace)
  metricName: podCPUUsage
- query: sum(container_memory_working_set_bytes) by (pod, namespace)
  metricName: podMemoryWorkingSet
```

Create `suites/kata-disk-perf/templates/pod.yml` with:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: kata-disk-perf-{{.Iteration}}-{{.Replica}}
  labels:
    app: kata-disk-perf
spec:
  nodeSelector:
    perf.azure.com/node-role: workload
  restartPolicy: Never
  containers:
    - name: pause
      image: {{.image}}
      imagePullPolicy: IfNotPresent
```

Create `suites/kata-disk-perf/vars/smoke.yml` with:

```yaml
iterations: 20
iterationsPerNamespace: 20
qps: 20
burst: 20
cleanup: true
waitWhenFinished: true
preLoadImages: true
templateVars:
  app: kata-disk-perf
imageVars:
  image: pause
```

Create `suites/kata-disk-perf/vars/full.yml` with:

```yaml
iterations: 500
iterationsPerNamespace: 50
qps: 50
burst: 50
cleanup: true
waitWhenFinished: true
preLoadImages: true
templateVars:
  app: kata-disk-perf
imageVars:
  image: pause
```

- [ ] **Step 4: Run test and list suite**

Run: `go test ./internal/examples ./internal/suite`

Expected: PASS.

Run: `make list-suites`

Expected output contains `kata-disk-perf`.

- [ ] **Step 5: Commit**

```bash
git add suites/kata-disk-perf internal/examples
git commit -m "feat: add kata disk performance example suite"
```

---

### Task 6: Render And Run Suite With Prometheus Port-Forward

**Files:**
- Create: `internal/run/run.go`
- Create: `internal/run/run_test.go`
- Modify: `cmd/perf-runner/main.go`

**Interfaces:**
- Consumes: suite config, requirements, mode files, and Prometheus support.
- Produces: `run.RenderWorkload(workload map[string]any, mode Mode, images map[string]string, prometheusEndpoint string) (map[string]any, error)`.
- Produces: `run.CreateRunDir(suiteName string, mode string) (string, error)`.
- Produces: `run.CopyRenderAssets(suiteDir string, runDir string) error`.
- Produces: CLI command `perf-runner run-suite --suite SUITE --mode MODE --resource-group RG`.

- [ ] **Step 1: Write failing run rendering test**

Create `internal/run/run_test.go` with:

```go
package run

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenderWorkloadInjectsPrometheusEndpoint(t *testing.T) {
	workload := map[string]any{"global": map[string]any{}, "jobs": []any{map[string]any{"objects": []any{map[string]any{"inputVars": map[string]any{}}}}}}
	mode := Mode{Iterations: 20, IterationsPerNamespace: 20, QPS: 20, Burst: 20, Cleanup: true, WaitWhenFinished: true, PreLoadImages: true, TemplateVars: map[string]any{"app": "test"}, ImageVars: map[string]string{"image": "pause"}}
	rendered, err := RenderWorkload(workload, mode, map[string]string{"pause": "mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2"}, "http://127.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	endpoints := rendered["metricsEndpoints"].([]any)
	endpoint := endpoints[0].(map[string]any)
	if endpoint["endpoint"] != "http://127.0.0.1:9090" {
		t.Fatalf("endpoint not injected: %#v", endpoint)
	}
	objects := rendered["jobs"].([]any)[0].(map[string]any)["objects"].([]any)
	inputVars := objects[0].(map[string]any)["inputVars"].(map[string]any)
	if inputVars["image"] != "mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2" {
		t.Fatalf("image key was not resolved: %#v", inputVars)
	}
}

func TestCopyRenderAssetsCopiesTemplatesAndMetrics(t *testing.T) {
	suiteDir := t.TempDir()
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(suiteDir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "templates", "pod.yml"), []byte("kind: Pod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "metrics.yml"), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CopyRenderAssets(suiteDir, runDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "rendered", "templates", "pod.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "rendered", "metrics.yml")); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/run`

Expected: FAIL because `internal/run` does not exist.

- [ ] **Step 3: Implement run rendering**

Create `internal/run/run.go` with:

```go
package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Mode struct {
	Iterations             int            `yaml:"iterations"`
	IterationsPerNamespace int            `yaml:"iterationsPerNamespace"`
	QPS                    int            `yaml:"qps"`
	Burst                  int            `yaml:"burst"`
	Cleanup                bool           `yaml:"cleanup"`
	WaitWhenFinished       bool           `yaml:"waitWhenFinished"`
	PreLoadImages          bool           `yaml:"preLoadImages"`
	TemplateVars           map[string]any `yaml:"templateVars"`
	ImageVars              map[string]string `yaml:"imageVars"`
}

func RenderWorkload(workload map[string]any, mode Mode, images map[string]string, prometheusEndpoint string) (map[string]any, error) {
	rendered := cloneMap(workload)
	global := ensureMap(rendered, "global")
	global["gc"] = mode.Cleanup
	global["waitWhenFinished"] = mode.WaitWhenFinished
	rendered["metricsEndpoints"] = []any{map[string]any{
		"endpoint": prometheusEndpoint,
		"metrics":       []any{"metrics.yml"},
		"indexer": map[string]any{
			"type":             "local",
			"metricsDirectory": "raw/metrics",
		},
	}}
	jobs, _ := rendered["jobs"].([]any)
	for _, item := range jobs {
		job, ok := item.(map[string]any)
		if !ok {
			continue
		}
		job["jobIterations"] = mode.Iterations
		job["iterationsPerNamespace"] = mode.IterationsPerNamespace
		job["qps"] = mode.QPS
		job["burst"] = mode.Burst
		job["cleanup"] = mode.Cleanup
		job["waitWhenFinished"] = mode.WaitWhenFinished
		job["preLoadImages"] = mode.PreLoadImages
		objects, _ := job["objects"].([]any)
		for _, objectItem := range objects {
			object, ok := objectItem.(map[string]any)
			if !ok {
				continue
			}
			inputVars := ensureMap(object, "inputVars")
			for key, value := range mode.TemplateVars {
				inputVars[key] = value
			}
			for key, imageKey := range mode.ImageVars {
				image, ok := images[imageKey]
				if !ok || image == "" {
					return nil, fmt.Errorf("image key %q not found", imageKey)
				}
				inputVars[key] = image
			}
		}
	}
	return rendered, nil
}

func CreateRunDir(suiteName string, mode string) (string, error) {
	safeSuite := strings.ReplaceAll(suiteName, "/", "_")
	dir := filepath.Join("results", time.Now().UTC().Format("2006-01-02T15-04-05Z")+"_"+safeSuite+"_"+mode)
	for _, child := range []string{"metadata", "rendered", "logs", "raw", "summary"} {
		if err := os.MkdirAll(filepath.Join(dir, child), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func ExecuteKubeBurner(workloadPath string, logPath string) error {
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command("kube-burner", "init", "-c", filepath.Base(workloadPath))
	cmd.Dir = filepath.Dir(workloadPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
}

func CopyRenderAssets(suiteDir string, runDir string) error {
	if err := copyDir(filepath.Join(suiteDir, "templates"), filepath.Join(runDir, "rendered", "templates")); err != nil {
		return err
	}
	return copyFile(filepath.Join(suiteDir, "metrics.yml"), filepath.Join(runDir, "rendered", "metrics.yml"))
}

func copyDir(src string, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src string, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	parent[key] = created
	return created
}

func cloneMap(input map[string]any) map[string]any {
	data, err := yaml.Marshal(input)
	if err != nil {
		panic(fmt.Sprintf("marshal clone: %v", err))
	}
	var output map[string]any
	if err := yaml.Unmarshal(data, &output); err != nil {
		panic(fmt.Sprintf("unmarshal clone: %v", err))
	}
	return output
}
```

- [ ] **Step 4: Add run-suite CLI dispatch**

Modify `cmd/perf-runner/main.go` to include `run-suite`. The command must load `requirements.yml`, install Prometheus when `required && install`, start port-forward when `required`, wait for readiness, render workload with the port-forward URL, run kube-burner, then stop port-forward by canceling the context.

Use this function body:

```go
func runSuite(args []string) error {
	fs := flag.NewFlagSet("run-suite", flag.ContinueOnError)
	suiteName := fs.String("suite", "", "suite name")
	modeName := fs.String("mode", "smoke", "mode")
	resourceGroup := fs.String("resource-group", "", "Azure resource group")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *suiteName == "" || *resourceGroup == "" {
		return fmt.Errorf("usage: perf-runner run-suite --suite SUITE --mode MODE --resource-group RG")
	}
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	_ = resourceGroup
	reqPath := filepath.Join(root, "suites", *suiteName, "requirements.yml")
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), reqPath); err != nil {
		return err
	}
	var req struct {
		Requires struct {
			Observability struct {
				Prometheus prometheus.Config `yaml:"prometheus"`
			} `yaml:"observability"`
		} `yaml:"requires"`
	}
	if err := config.LoadYAML(reqPath, &req); err != nil {
		return err
	}
	images, err := config.LoadImages(filepath.Join(root, "config", "images.yml"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if req.Requires.Observability.Prometheus.Required && req.Requires.Observability.Prometheus.Install {
		prometheusImage, err := config.ResolveImage(images, req.Requires.Observability.Prometheus.ImageKey)
		if err != nil {
			return err
		}
		if err := prometheus.Install(ctx, filepath.Join(root, "observability", "prometheus", "prometheus.yaml"), prometheusImage); err != nil {
			return err
		}
	}
	prometheusURL := ""
	if req.Requires.Observability.Prometheus.Required {
		cmd, endpoint, err := prometheus.PortForward(ctx, req.Requires.Observability.Prometheus)
		if err != nil {
			return err
		}
		defer func() { _ = cmd.Process.Kill() }()
		if err := prometheus.WaitReady(ctx, endpoint); err != nil {
			return err
		}
		prometheusURL = endpoint
	}
	var workload map[string]any
	if err := config.LoadYAML(filepath.Join(root, "suites", *suiteName, "workload.yml"), &workload); err != nil {
		return err
	}
	var mode runpkg.Mode
	modePath := filepath.Join(root, "suites", *suiteName, "vars", *modeName+".yml")
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "mode.schema.json"), modePath); err != nil {
		return err
	}
	if err := config.LoadYAML(modePath, &mode); err != nil {
		return err
	}
	runDir, err := runpkg.CreateRunDir(*suiteName, *modeName)
	if err != nil {
		return err
	}
	suiteDir := filepath.Join(root, "suites", *suiteName)
	if err := runpkg.CopyRenderAssets(suiteDir, runDir); err != nil {
		return err
	}
	rendered, err := runpkg.RenderWorkload(workload, mode, images, prometheusURL)
	if err != nil {
		return err
	}
	workloadPath := filepath.Join(runDir, "rendered", "workload.yml")
	if err := config.WriteYAML(workloadPath, rendered); err != nil {
		return err
	}
	return runpkg.ExecuteKubeBurner(workloadPath, filepath.Join(runDir, "logs", "kube-burner.log"))
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/run ./cmd/perf-runner`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/run cmd/perf-runner/main.go
git commit -m "feat: run suites with port-forwarded Prometheus"
```

---

### Task 7: Destroy Command And Final Verification

**Files:**
- Create: `internal/infra/destroy_test.go`
- Modify: `internal/infra/infra.go`
- Modify: `cmd/perf-runner/main.go`
- Create: `.github/workflows/ci.yml`
- Modify: `README.md`

**Interfaces:**
- Consumes: Make target `destroy` from Task 1.
- Produces: `infra.Destroy(ctx context.Context, resourceGroup string) error`.
- Produces: CLI command `perf-runner destroy --suite SUITE --resource-group RG`.

- [ ] **Step 1: Write failing destroy command test**

Create `internal/infra/destroy_test.go` with:

```go
package infra

import "testing"

func TestDestroyCommand(t *testing.T) {
	cmd := DestroyCommand("rg-aks-burner-test")
	want := []string{"az", "group", "delete", "--name", "rg-aks-burner-test", "--yes", "--no-wait"}
	if len(cmd) != len(want) {
		t.Fatalf("len = %d, want %d", len(cmd), len(want))
	}
	for i := range want {
		if cmd[i] != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, cmd[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/infra`

Expected: FAIL because `DestroyCommand` does not exist.

- [ ] **Step 3: Implement destroy support**

Add to `internal/infra/infra.go`:

```go
func DestroyCommand(resourceGroup string) []string {
	return []string{"az", "group", "delete", "--name", resourceGroup, "--yes", "--no-wait"}
}

func Destroy(ctx context.Context, resourceGroup string) error {
	args := DestroyCommand(resourceGroup)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
```

Modify `cmd/perf-runner/main.go` to dispatch `destroy`:

```go
case "destroy":
	return destroy(args[1:])

func destroy(args []string) error {
	fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
	suiteName := fs.String("suite", "", "suite name")
	resourceGroup := fs.String("resource-group", "", "Azure resource group")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *suiteName == "" || *resourceGroup == "" {
		return fmt.Errorf("usage: perf-runner destroy --suite SUITE --resource-group RG")
	}
	return infra.Destroy(context.Background(), *resourceGroup)
}
```

- [ ] **Step 4: Add CI workflow and README updates**

Create `.github/workflows/ci.yml` with:

```yaml
name: CI

on:
  pull_request:
  push:
    branches:
      - main

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.25"
      - name: Run tests
        run: make test
      - name: Build runner
        run: make build
      - name: List suites
        run: make list-suites
```

Append to `README.md`:

```markdown
## Suite Lifecycle

```bash
make list-suites
TEST_SUITE=kata-disk-perf make provision
TEST_SUITE=kata-disk-perf TEST_MODE=smoke make run-suite
TEST_SUITE=kata-disk-perf make destroy
```

`provision` creates the Azure resource group and AKS cluster declared by the suite requirements. `run-suite` installs Prometheus when requested, starts a local `kubectl port-forward`, renders kube-burner with the local Prometheus URL, and stores results under `results/`. `destroy` deletes the suite resource group asynchronously.
```

- [ ] **Step 5: Run full verification**

Run: `go test ./...`

Expected: PASS.

Run: `make build`

Expected: `bin/perf-runner` exists.

Run: `make list-suites`

Expected: output includes `kata-disk-perf`.

- [ ] **Step 6: Commit**

```bash
git add internal/infra cmd/perf-runner/main.go .github/workflows/ci.yml README.md
git commit -m "feat: add suite teardown and CI validation"
```

---

## Self-Review

**Spec coverage:** The revised plan includes Bicep AKS provisioning, Make-based suite listing, suite provisioning, suite execution, suite destruction, requirements-driven Prometheus installation, and Prometheus port-forwarding during `run-suite`.

**User command coverage:** The supported lifecycle is `make list-suites`, `TEST_SUITE=kata-disk-perf make provision`, `TEST_SUITE=kata-disk-perf TEST_MODE=smoke make run-suite`, and `TEST_SUITE=kata-disk-perf make destroy`.

**Contract coverage:** `requirements.yml` now includes `requires.infrastructure` and `requires.observability.prometheus`. The schema requires a Prometheus `imageKey`, namespace, service name, service port, and local port when Prometheus config is present. Concrete image values are centralized in `config/images.yml`.

**Deferred scope:** Multiple cloud providers, managed Prometheus, dashboard publishing, Elasticsearch/OpenSearch indexing, and scheduled benchmark publishing are deferred. The first implementation provisions AKS with Bicep and uses self-hosted in-cluster Prometheus.

**Placeholder scan:** This plan avoids open-ended instructions such as `TBD`, `TODO`, or `implement later`. Each task includes concrete files, code snippets, commands, and expected outcomes.

**Type consistency:** The key functions are stable across tasks: `repo.Root`, `config.LoadYAML`, `config.WriteYAML`, `config.ValidateYAML`, `suite.Load`, `suite.List`, `infra.ProvisionCommands`, `infra.Provision`, `infra.DestroyCommand`, `infra.Destroy`, `prometheus.Install`, `prometheus.PortForward`, `prometheus.WaitReady`, `run.RenderWorkload`, `run.CreateRunDir`, and `run.ExecuteKubeBurner`.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-08-kube-burner-test-suite.md`. Two execution options:

**1. Subagent-Driven (recommended)** - Dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
