# Requirements-Driven Bicep Parameters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace checked-in suite Bicep parameter files with validated, typed infrastructure requirements that generate an ephemeral ARM JSON parameter document during provisioning.

**Architecture:** A new `internal/requirements` package loads the complete suite requirements once and enforces suite identity. The existing `internal/infra` package validates AKS pool semantics, derives safe cluster names, serializes ARM parameters, and owns temporary-file deployment. `perf-runner` consumes those APIs for provisioning, dry-run, existing-cluster execution, and suite generation.

**Tech Stack:** Go, YAML, JSON Schema draft 2020-12, Bicep, Azure CLI, Go testing

## Global Constraints

- `requirements.yml` is the only checked-in source of suite infrastructure intent.
- `provider: aks` selects `infra/aks/main.bicep`; suites do not specify template or parameter paths.
- Node pools support only name, mode, count, VM size, OS type, OS SKU, workload runtime, labels, and taints.
- `requires.kubernetes.minVersion` is the requested provisioning version.
- Cluster names default to `aks` plus the suite name without hyphens and use deterministic truncation plus an eight-character SHA-256 suffix above 54 characters.
- Explicit cluster names must match `^[a-z0-9](?:[a-z0-9-]{0,52}[a-z0-9])?$`.
- `kubernetes.azure.com/os-sku` is provider-managed and validates against a pool's `osSKU`; it is not emitted as a custom node label.
- Presence of `requires.images` controls ACR deployment; custom registry names and registry parameter indirection are removed.
- Generated parameter files use ARM JSON, mode `0600`, and are removed on every exit path.
- No compatibility reader for `infra.bicepparam` is added.

---

### Task 1: Typed Requirements And AKS Parameter Generation

**Files:**
- Create: `internal/requirements/requirements.go`
- Create: `internal/requirements/requirements_test.go`
- Create: `internal/infra/parameters.go`
- Create: `internal/infra/parameters_test.go`
- Modify: `internal/run/run.go`
- Modify: `internal/run/run_test.go`
- Modify: `internal/suite/suite.go:42-54`
- Modify: `internal/suite/suite_test.go`

**Interfaces:**
- Produces: `requirements.Document`, `requirements.Load(root, suiteName) (Document, error)`
- Produces: `infra.NodePool`, `infra.ClusterName(suiteName, override string) (string, error)`
- Produces: `infra.ValidateNodePools(suiteName string, pools []NodePool, selectors []run.NodeSelectorRequirement) error`
- Produces: `infra.ParametersJSON(clusterName, kubernetesVersion string, pools []NodePool, deployRegistry bool) ([]byte, error)`

- [ ] **Step 1: Write failing tests for centralized requirements loading**

Create table-driven tests that build temporary suite directories and assert that one load returns infrastructure, Kubernetes, selectors, images, artifacts, and observability. Include identity mismatches:

```go
func TestLoadRejectsRequirementsSuiteMismatch(t *testing.T) {
    root := writeRequirementsFixture(t, "demo", "other")
    _, err := Load(root, "demo")
    if err == nil || !strings.Contains(err.Error(), `requirements suite "other" does not match "demo"`) {
        t.Fatalf("Load() error = %v", err)
    }
}
```

Extend `internal/suite/suite_test.go` with a fixture where `suites/demo/suite.yml` declares `name: other` and assert `suite.Load(root, "demo")` rejects it. Add the following field to `run.NodeSelectorRequirement` and a YAML-loading test proving `pool: userpool` is retained:

```go
Pool string `yaml:"pool"`
```

- [ ] **Step 2: Run loader tests and verify they fail**

Run: `go test ./internal/requirements ./internal/suite -run 'TestLoad|Test.*Mismatch'`

Expected: FAIL because `internal/requirements` and identity comparison do not exist.

- [ ] **Step 3: Implement the requirements document and identity checks**

Define the complete document without anonymous runner structs:

```go
package requirements

type Document struct {
    Suite    string `yaml:"suite"`
    Requires struct {
        Infrastructure infra.Requirements              `yaml:"infrastructure"`
        Kubernetes     run.KubernetesRequirements      `yaml:"kubernetes"`
        NodeSelectors  []run.NodeSelectorRequirement   `yaml:"nodeSelectors"`
        Images         *acr.Requirements               `yaml:"images"`
        Artifacts      artifacts.Config                `yaml:"artifacts"`
        Observability  ObservabilityRequirements       `yaml:"observability"`
    } `yaml:"requires"`
}

type ObservabilityRequirements struct {
    Prometheus       prometheus.Config       `yaml:"prometheus"`
    KubeStateMetrics kubestatemetrics.Config `yaml:"kubeStateMetrics"`
}

func Load(root, suiteName string) (Document, error) {
    if _, err := suite.Load(root, suiteName); err != nil {
        return Document{}, err
    }
    path := filepath.Join(root, "suites", suiteName, "requirements.yml")
    if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), path); err != nil {
        return Document{}, err
    }
    var doc Document
    if err := config.LoadYAML(path, &doc); err != nil {
        return Document{}, err
    }
    if doc.Suite != suiteName {
        return Document{}, fmt.Errorf("requirements suite %q does not match %q", doc.Suite, suiteName)
    }
    return doc, nil
}
```

Keep the existing `acr.Requirements.Registry` field until the atomic migration in Task 4 so current runner consumers continue to compile. In `suite.Load`, return an error when `cfg.Name != name`.

- [ ] **Step 4: Write failing tests for cluster names, pool validation, and JSON generation**

Cover exact normal names, the 54-character boundary, deterministic long-name hashing, malformed overrides, duplicate pools, no system pool, missing selector pool, insufficient count, custom-label mismatch, provider OS-label success and mismatch, and exact generated JSON.

Use this representative success case:

```go
func TestParametersJSON(t *testing.T) {
    pools := []NodePool{
        {Name: "systempool", Mode: "System", Count: 1, VMSize: "Standard_D4s_v5", OSType: "Linux", OSSKU: "Ubuntu", WorkloadRuntime: "OCIContainer", Labels: map[string]string{}, Taints: []string{}},
        {Name: "userpool", Mode: "User", Count: 4, VMSize: "Standard_D8s_v5", OSType: "Linux", OSSKU: "AzureLinux", WorkloadRuntime: "KataMshvVmIsolation", Labels: map[string]string{"perf.azure.com/node-role": "workload"}, Taints: []string{}},
    }
    got, err := ParametersJSON("akskataio", "1.36", pools, true)
    if err != nil {
        t.Fatal(err)
    }
    var doc map[string]any
    if err := json.Unmarshal(got, &doc); err != nil {
        t.Fatal(err)
    }
    parameters := doc["parameters"].(map[string]any)
    if parameters["clusterName"].(map[string]any)["value"] != "akskataio" {
        t.Fatalf("unexpected parameters: %#v", parameters)
    }
}
```

- [ ] **Step 5: Run parameter tests and verify they fail**

Run: `go test ./internal/infra -run 'TestClusterName|TestValidateNodePools|TestParametersJSON'`

Expected: FAIL because the parameter APIs do not exist.

- [ ] **Step 6: Implement typed pools, validation, name derivation, and ARM JSON**

Add:

```go
type Requirements struct {
    Provider  string     `yaml:"provider"`
    NodePools []NodePool `yaml:"nodePools"`
}

type NodePool struct {
    Name            string            `yaml:"name" json:"name"`
    Mode            string            `yaml:"mode" json:"mode"`
    Count           int               `yaml:"count" json:"count"`
    VMSize          string            `yaml:"vmSize" json:"vmSize"`
    OSType          string            `yaml:"osType" json:"osType"`
    OSSKU           string            `yaml:"osSKU" json:"osSKU"`
    WorkloadRuntime string            `yaml:"workloadRuntime" json:"workloadRuntime"`
    Labels          map[string]string `yaml:"labels" json:"labels"`
    Taints          []string          `yaml:"taints" json:"taints"`
}
```

`ClusterName` must validate overrides first. For long derived names, hash the full untruncated value with `sha256.Sum256`, then return `derived[:45] + "-" + hex.EncodeToString(sum[:])[:8]`.

`ValidateNodePools` must index pools by name, require a system pool, compare selector counts, compare custom labels, and map `kubernetes.azure.com/os-sku` to `OSSKU`. Return suite-, selector-, and pool-specific errors.

Serialize the standard envelope:

```go
type parameterValue[T any] struct {
    Value T `json:"value"`
}

type parameterDocument struct {
    Schema         string `json:"$schema"`
    ContentVersion string `json:"contentVersion"`
    Parameters     struct {
        ClusterName             parameterValue[string]     `json:"clusterName"`
        KubernetesVersion       parameterValue[string]     `json:"kubernetesVersion"`
        NodePools               parameterValue[[]NodePool] `json:"nodePools"`
        DeployContainerRegistry parameterValue[bool]       `json:"deployContainerRegistry"`
    } `json:"parameters"`
}
```

Use `json.MarshalIndent(doc, "", "  ")` and append one newline.

- [ ] **Step 7: Run focused and package tests**

Run: `go test ./internal/requirements ./internal/infra ./internal/run ./internal/suite`

Expected: PASS.

- [ ] **Step 8: Commit the typed model**

```bash
git add internal/requirements internal/infra/parameters.go internal/infra/parameters_test.go internal/run/run.go internal/run/run_test.go internal/suite/suite.go internal/suite/suite_test.go
git commit -m "feat: model suite infrastructure requirements"
```

### Task 2: Prepare Schema, Suites, And Arbitrary Node-Pool Bicep

**Files:**
- Modify: `schemas/requirements.schema.json`
- Modify: `suites/kata-perf/requirements.yml`
- Modify: `suites/kata-io/requirements.yml`
- Modify: `infra/aks/main.bicep`
- Modify: `internal/examples/examples_test.go`

**Interfaces:**
- Consumes: `infra.NodePool` JSON fields from Task 1
- Produces: Bicep parameters `clusterName`, `kubernetesVersion`, `nodePools`, and `deployContainerRegistry`

- [ ] **Step 1: Add failing schema and migration contract tests**

Update example tests to require both suites to load through `requirements.Load`, contain a `System` pool and their expected `User` pool, and have no `infra.bicepparam` file. Add source-contract assertions that `main.bicep` contains `param nodePools NodePool[]` and `[for pool in nodePools:` and no longer contains `param userNodeCount` or `param systemNodeCount`.

Add a schema rejection fixture for pool name `UPPER`, count `0`, and malformed taint `dedicated=true:Unknown`.

- [ ] **Step 2: Run migration tests and verify they fail**

Run: `go test ./internal/examples -run 'Test.*Requirements|Test.*Bicep|Test.*NodePool'`

Expected: FAIL because suites and Bicep still use fixed parameter files.

- [ ] **Step 3: Replace the infrastructure schema**

Remove `infrastructure.bicep`. Require `provider` and `nodePools`. Define pool constraints exactly:

```json
"nodePools": {
  "type": "array",
  "minItems": 1,
  "items": {
    "type": "object",
    "additionalProperties": false,
    "required": ["name", "mode", "count", "vmSize", "osType", "osSKU", "workloadRuntime", "labels", "taints"],
    "properties": {
      "name": { "type": "string", "pattern": "^[a-z][a-z0-9]{0,11}$" },
      "mode": { "type": "string", "enum": ["System", "User"] },
      "count": { "type": "integer", "minimum": 1, "maximum": 1000 },
      "vmSize": { "type": "string", "minLength": 1 },
      "osType": { "const": "Linux" },
      "osSKU": { "type": "string", "enum": ["Ubuntu", "AzureLinux"] },
      "workloadRuntime": { "type": "string", "enum": ["OCIContainer", "KataMshvVmIsolation", "KataVmIsolation"] },
      "labels": { "type": "object", "propertyNames": { "minLength": 1 }, "additionalProperties": { "type": "string", "minLength": 1 } },
      "taints": { "type": "array", "items": { "type": "string", "pattern": "^[^:=\\s]+(?:=[^:\\s]+)?:(NoSchedule|PreferNoSchedule|NoExecute)$" } }
    }
  }
}
```

Require selector `pool`. Remove `images.registry` and require only `builds`.

- [ ] **Step 4: Prepare both suite migrations without deleting parameter files**

Prepare the final YAML content in the worktree: give each suite a `systempool` with count 1 and `Standard_D4s_v5`, plus a `userpool` preserving its checked-in count, VM size, OS SKU, workload runtime, and custom label. Set each selector's `pool: userpool`. Keep `kata-perf`'s provider-managed OS SKU selector. Do not delete `infra.bicepparam` or remove `kata-io`'s registry object until Task 4, because the current `run-suite` command still consumes them.

- [ ] **Step 5: Replace fixed Bicep pool parameters with a typed array**

Define:

```bicep
type NodePool = {
  name: string
  mode: 'System' | 'User'
  count: int
  vmSize: string
  osType: 'Linux'
  osSKU: 'Ubuntu' | 'AzureLinux'
  workloadRuntime: 'OCIContainer' | 'KataMshvVmIsolation' | 'KataVmIsolation'
  labels: object
  taints: string[]
}

param nodePools NodePool[]
```

Project all fields into `agentPoolProfiles`:

```bicep
agentPoolProfiles: [for pool in nodePools: {
  name: pool.name
  mode: pool.mode
  count: pool.count
  vmSize: pool.vmSize
  osType: pool.osType
  osSKU: pool.osSKU
  type: 'VirtualMachineScaleSets'
  workloadRuntime: pool.workloadRuntime
  nodeLabels: pool.labels
  nodeTaints: pool.taints
}]
```

- [ ] **Step 6: Compile Bicep and run focused model tests**

Run: `az bicep build --file infra/aks/main.bicep --stdout`

Expected: generated ARM JSON on stdout and exit 0.

Run: `go test ./internal/requirements ./internal/infra`

Expected: PASS.

- [ ] **Step 7: Keep the prepared migration uncommitted**

Run: `git diff --check`

Expected: exit 0. Do not commit Task 2 independently; schema, suite, and template changes remain in the worktree until Task 4 atomically migrates all consumers and deletes the old files.

### Task 3: Ephemeral Deployment And Provision Dry Run

**Files:**
- Modify: `internal/infra/infra.go`
- Modify: `internal/infra/infra_test.go`
- Modify: `cmd/perf-runner/main.go:680-744`
- Modify: `cmd/perf-runner/main_test.go`

**Interfaces:**
- Consumes: `requirements.Load`, `infra.ClusterName`, `infra.ValidateNodePools`, and `infra.ParametersJSON`
- Produces: `infra.ProvisionOptions{TemplateFile, ParametersJSON, ...}` and `provisionWithIO(args []string, out io.Writer) error`

- [ ] **Step 1: Write failing command and temporary-file lifecycle tests**

Change command tests to expect:

```go
wantDeployment := []string{
    "az", "deployment", "group", "create",
    "--resource-group", "rg-aks-burner-test",
    "--name", DeploymentName,
    "--template-file", "infra/aks/main.bicep",
    "--parameters", "@/tmp/generated.parameters.json",
}
```

Add table tests with an injected command runner failing on command 1, 2, or 3. During each call, assert the `@path` exists with permission `0600`; after `Provision` returns, assert it no longer exists. Add a canceled-context case with the same cleanup assertion.

- [ ] **Step 2: Run infra deployment tests and verify they fail**

Run: `go test ./internal/infra -run 'TestProvision'`

Expected: FAIL because `Provision` still accepts a persistent parameter file.

- [ ] **Step 3: Implement ephemeral deployment options and cleanup**

Use:

```go
type CommandRunner func(context.Context, []string) error

type ProvisionOptions struct {
    ResourceGroup  string
    Location       string
    TemplateFile   string
    ParametersJSON []byte
    ClusterName    string
    TempDir        string
    RunCommand     CommandRunner
}
```

Create the temporary file with `os.CreateTemp(opts.TempDir, "aks-burner-*.parameters.json")`, explicitly `Chmod(0o600)`, write and close it, and `defer os.Remove(path)` before executing any command. Default `RunCommand` to the current `exec.CommandContext` behavior. Make `ProvisionCommands(opts, path)` return group creation, named template deployment with `@` plus the path, and credentials commands.

- [ ] **Step 4: Write failing provision dry-run tests**

Add tests that invoke `provisionWithIO` against a temporary repository fixture. Assert `--dry-run` prints parseable ARM JSON with the derived cluster name and no command execution. Assert `--cluster-name custom-aks` appears in output, and invalid overrides fail. Assert semantic pool errors occur before provisioning.

- [ ] **Step 5: Run runner provision tests and verify they fail**

Run: `go test ./cmd/perf-runner -run 'TestProvision|TestClusterName'`

Expected: FAIL because dry-run and generated parameters are not wired.

- [ ] **Step 6: Rework provision around the typed document**

Implement `provisionWithIO`; keep `provision(args)` as `return provisionWithIO(args, os.Stdout)`. Add flags `--cluster-name` and `--dry-run`. Load with `requirements.Load`, require provider `aks`, derive the name, validate pools/selectors, generate JSON, and either print it or call:

```go
return infra.Provision(context.Background(), infra.ProvisionOptions{
    ResourceGroup:  *resourceGroup,
    Location:       *location,
    TemplateFile:   filepath.Join(root, "infra", "aks", "main.bicep"),
    ParametersJSON: parameterJSON,
    ClusterName:    clusterName,
})
```

Remove parameter-path resolution, `readBicepParamString`, and inline `deployContainerRegistry` command arguments.

- [ ] **Step 7: Run provision and infra tests**

Run: `go test ./internal/infra ./cmd/perf-runner -run 'TestProvision|TestClusterName|TestValidateNodePools|TestParametersJSON'`

Expected: PASS.

- [ ] **Step 8: Keep provisioning changes with the prepared migration**

Run: `git diff --check`

Expected: exit 0. Do not commit Task 3 independently because `run-suite` still requires the old parameter-file schema; Task 4 completes and commits the atomic migration.

### Task 4: Existing-Cluster Execution And Suite Generation

**Files:**
- Modify: `cmd/perf-runner/main.go:55-367,400-540`
- Modify: `cmd/perf-runner/main_test.go`
- Modify: `internal/acr/acr.go:20-27`
- Modify: `internal/examples/examples_test.go`
- Delete: `suites/kata-perf/infra.bicepparam`
- Delete: `suites/kata-io/infra.bicepparam`

**Interfaces:**
- Consumes: `requirements.Load` and `infra.ClusterName`
- Produces: `run-suite --cluster-name` and generated suites without `infra.bicepparam`

- [ ] **Step 1: Write failing run-suite identity and registry preflight tests**

Add a `--cluster-name existing-aks` test that verifies credentials use the override. Add a command-order seam around deployment-output lookup and assert image-building suites fetch both registry outputs before `GetCredentials` or any Kubernetes command. Make missing outputs return an error containing `requires an aks-burner deployment with container registry outputs`.

- [ ] **Step 2: Run run-suite tests and verify they fail**

Run: `go test ./cmd/perf-runner -run 'TestRunSuite.*Cluster|TestRunSuite.*Registry|TestRequirementsIdentity'`

Expected: FAIL because run-suite still reads `infra.bicepparam` and discovers ACR after credentials.

- [ ] **Step 3: Migrate run-suite to centralized requirements**

Add `--cluster-name`, load once through `requirements.Load`, derive/validate the target name, and remove all Bicep path parsing. For image builds, retrieve `containerRegistryName` and `containerRegistryLoginServer` before calling `infra.GetCredentials`; wrap missing output errors with the explicit managed-cluster requirement. Use deployment outputs exclusively and delete `registryNameFromRequirements`.

Now change `acr.Requirements` to contain only `Builds []ImageBuild`, remove `kata-io`'s registry object, and delete both suite `infra.bicepparam` files. These removals occur only after all runner consumers have migrated.

- [ ] **Step 4: Write failing generator tests**

Update fast and guided generator assertions to require two node pools in `requirements.yml`, `pool: userpool`, and no `infra.bicepparam`. Assert `--cluster-name` is rejected as an unknown flag and the guided prompts omit cluster name.

- [ ] **Step 5: Run generator tests and verify they fail**

Run: `go test ./cmd/perf-runner -run 'TestAddSuite|TestPromptAddSuite|TestWriteSuite'`

Expected: FAIL because the generator still writes and prompts for cluster parameters.

- [ ] **Step 6: Simplify suite generation**

Remove `ClusterName` from `addSuiteOptions`, its flag, validation, default, and prompt. Stop writing `infra.bicepparam` and delete `infraBicepParam`. Generate `systempool` and `userpool` maps under infrastructure, using `NodeCount` and `NodeVMSize` for the user pool, and add `pool: userpool` to the selector.

- [ ] **Step 7: Run runner and example tests**

Run: `go test ./cmd/perf-runner ./internal/examples`

Expected: PASS.

- [ ] **Step 8: Commit command migration**

```bash
git add cmd/perf-runner internal/acr internal/examples internal/infra infra/aks/main.bicep schemas/requirements.schema.json suites/kata-perf suites/kata-io
git commit -m "feat: provision AKS from suite requirements"
```

### Task 5: Documentation, Full Verification, And Review

**Files:**
- Modify: `README.md`
- Modify: `Makefile`
- Modify: any tests affected by stale `infra.bicepparam`, `nameParameter`, or fixed-pool assumptions

**Interfaces:**
- Consumes: all completed behavior
- Produces: documented dry-run and cluster override workflows

- [ ] **Step 1: Find stale contracts**

Run: `rg -n 'infra\.bicepparam|nameParameter|userNodeCount|userNodeVmSize|cluster-name' --glob '!docs/superpowers/**' .`

Expected: only intentional historical-free references remain; every production, test, README, and Make reference is evaluated.

- [ ] **Step 2: Update README and Make workflows**

Document that requirements generate deployment parameters, show the `nodePools` model, add a dry-run example, and show `run-suite --cluster-name` for existing clusters. Add `CLUSTER_NAME ?=` to Make and conditionally append `--cluster-name "$(CLUSTER_NAME)"` to provision and run-suite commands without changing defaults.

- [ ] **Step 3: Run formatting and focused static checks**

Run: `gofmt -w cmd/perf-runner/main.go cmd/perf-runner/main_test.go internal/acr/acr.go internal/infra/*.go internal/requirements/*.go internal/suite/*.go`

Run: `git diff --check`

Expected: both commands exit 0.

- [ ] **Step 4: Run complete verification**

Run: `go test ./...`

Expected: PASS.

Run: `az bicep build --file infra/aks/main.bicep --stdout`

Expected: generated ARM JSON on stdout and exit 0.

Run: `go run ./cmd/perf-runner provision --suite kata-perf --resource-group dry-run-unused --location westus2 --dry-run`

Expected: canonical ARM parameter JSON on stdout, no Azure command output, and exit 0.

- [ ] **Step 5: Review the full implementation diff**

Run: `git diff HEAD~4 --check && git diff HEAD~4 --stat && git diff HEAD~4`

Invoke the `code-reviewer` subagent on the complete implementation range. Independently verify every Critical or High finding against the code and official AKS/Bicep behavior; fix verified issues and rerun Step 4.

- [ ] **Step 6: Commit documentation and verified cleanup**

```bash
git add README.md Makefile cmd internal schemas suites infra
git commit -m "docs: describe requirements-driven provisioning"
```

- [ ] **Step 7: Confirm clean delivery state**

Run: `git status --short --branch && git log --oneline -6`

Expected: clean feature branch with the implementation commits above the local main base.
