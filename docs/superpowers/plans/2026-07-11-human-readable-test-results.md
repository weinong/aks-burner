# Human-Readable Test Results Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every successful `perf-runner run-suite` execution print a concise result preview and write all declared measurements to `summary/results.csv`.

**Architecture:** Add a focused `internal/reporting` package with two input adapters: versioned artifact `summary.json` documents and explicitly mapped kube-burner local-indexer documents. Suite requirements declare enabled sources and Prometheus metric units; run orchestration validates that contract and kube-burner `2.7.3` before side effects, then invokes reporting after execution and artifact copy. Producers retain responsibility for result semantics, so perf-runner collates rows but performs no statistical aggregation or comparison.

**Tech Stack:** Go 1.25 standard library (`encoding/csv`, `encoding/json`, `text/tabwriter`, `os/exec`), JSON Schema draft 2020-12, YAML, Bash, jq, kube-burner 2.7.3.

## Global Constraints

- Support every checked-in suite and mode through the same reporting pipeline.
- Require at least one viable declared result source before Azure, Kubernetes, image-build, or result-directory side effects.
- Require kube-burner version `2.7.3` before external side effects.
- Keep raw FIO, Git, Prometheus, kube-burner, log, and diagnostic artifacts.
- Do not calculate count, min, median, max, mean, sum, baseline ratios, regressions, or higher-is-better semantics in perf-runner.
- Print at most 10 deterministically sorted rows and the workspace-relative complete CSV path.
- Fail a successful workload that yields zero valid measurement rows.
- Preserve JSON number tokens in CSV after validating finite IEEE-754 binary64 values with at most 17 significant decimal digits.
- Namespace Prometheus labels as `label.<name>` and reserve `source`, `metric`, `value`, and `unit` as non-dimension names.
- Spreadsheet-neutralize headers and all producer-controlled text cells before CSV encoding.
- Write `summary/results.csv` through a temporary file and atomic rename.

---

### Task 1: Add The Suite Reporting Contract

**Files:**
- Create: `internal/reporting/config.go`
- Create: `internal/reporting/config_test.go`
- Modify: `internal/requirements/requirements.go:17-50`
- Modify: `internal/requirements/requirements_test.go:11-128`
- Modify: `schemas/requirements.schema.json:8-141`
- Modify: `cmd/perf-runner/main.go:284-310`
- Modify: `cmd/perf-runner/main_test.go:1-330`
- Modify: `suites/kata-perf/requirements.yml`
- Modify: `suites/kata-io/requirements.yml`
- Modify: `internal/examples/examples_test.go:18-401`

**Interfaces:**
- Produces: `reporting.Config`, `reporting.Sources`, `reporting.PrometheusMetricNames(path string) ([]string, error)`, and `reporting.ValidateConfig(cfg *Config, artifactsEnabled, prometheusEnabled bool, workload map[string]any, prometheusMetricNames []string) error`.
- Consumes: `requirements.Document.Requires.Artifacts`, `requirements.Document.Requires.Observability.Prometheus.Required`, selected workload YAML, and suite `metrics.yml`.

- [ ] **Step 1: Write failing config and requirements tests**

Create `internal/reporting/config_test.go` with these table-driven cases:

```go
package reporting

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestValidateConfigAcceptsStandardSummaryWithArtifacts(t *testing.T) {
    cfg := Config{Sources: Sources{StandardSummary: true}}
    if err := ValidateConfig(&cfg, true, false, map[string]any{"jobs": []any{}}, nil); err != nil {
        t.Fatal(err)
    }
}

func TestValidateConfigAcceptsSupportedKubeBurnerMeasurement(t *testing.T) {
    workload := map[string]any{"global": map[string]any{"measurements": []any{map[string]any{"name": "podLatency"}}}}
    cfg := Config{Sources: Sources{KubeBurner: true}}
    if err := ValidateConfig(&cfg, false, false, workload, nil); err != nil {
        t.Fatal(err)
    }
}

func TestValidateConfigRejectsNoViableSource(t *testing.T) {
    cfg := Config{}
    err := ValidateConfig(&cfg, false, false, map[string]any{"jobs": []any{}}, nil)
    if err == nil || !strings.Contains(err.Error(), "viable reporting source") {
        t.Fatalf("ValidateConfig() error = %v", err)
    }
}

func TestValidateConfigRejectsStandardSummaryWithoutArtifacts(t *testing.T) {
    cfg := Config{Sources: Sources{StandardSummary: true}}
    err := ValidateConfig(&cfg, false, false, map[string]any{"jobs": []any{}}, nil)
    if err == nil || !strings.Contains(err.Error(), "artifact") {
        t.Fatalf("ValidateConfig() error = %v", err)
    }
}

func TestValidateConfigRejectsPrometheusMetricWithoutUnit(t *testing.T) {
    cfg := Config{Sources: Sources{KubeBurner: true}}
    err := ValidateConfig(&cfg, false, true, map[string]any{"jobs": []any{}}, []string{"podCPUUsage"})
    if err == nil || !strings.Contains(err.Error(), "podCPUUsage") {
        t.Fatalf("ValidateConfig() error = %v", err)
    }
}

func TestValidateConfigRejectsUnitForUndeclaredPrometheusMetric(t *testing.T) {
    cfg := Config{Sources: Sources{KubeBurner: true}, PrometheusMetricUnits: map[string]string{"other": "cores"}}
    err := ValidateConfig(&cfg, false, true, map[string]any{"jobs": []any{}}, []string{"podCPUUsage"})
    if err == nil || !strings.Contains(err.Error(), "other") {
        t.Fatalf("ValidateConfig() error = %v", err)
    }
}

func TestPrometheusMetricNames(t *testing.T) {
    path := filepath.Join(t.TempDir(), "metrics.yml")
    if err := os.WriteFile(path, []byte("- query: up\n  metricName: availability\n"), 0o644); err != nil {
        t.Fatal(err)
    }
    got, err := PrometheusMetricNames(path)
    if err != nil {
        t.Fatal(err)
    }
    if len(got) != 1 || got[0] != "availability" {
        t.Fatalf("PrometheusMetricNames() = %#v", got)
    }
}
```

Extend `internal/requirements/requirements_test.go` to assert `doc.Requires.Reporting.Sources.StandardSummary`, `KubeBurner`, and `PrometheusMetricUnits` load from the fixture. Add reporting YAML to `writeRequirementsFixture`:

```yaml
  reporting:
    sources:
      standardSummary: true
      kubeBurner: true
    prometheusMetricUnits:
      podCPUUsage: cores
      podMemoryWorkingSet: bytes
```

Add `cmd/perf-runner/main_test.go` coverage that `suiteRequirements(addSuiteOptions{Prometheus: true})` contains a `reporting` block with `kubeBurner: true` and units for `podCPUUsage` and `podMemoryWorkingSet`. Add schema rejection tests for a missing reporting block and an unknown source key.

Update every synthetic `requirements.yml` string in `cmd/perf-runner/main_test.go`, `internal/examples/examples_test.go`, and `internal/requirements/requirements_test.go` with a viable reporting block. Fixtures with `podLatency` use `kubeBurner: true`; artifact fixtures use `standardSummary: true`. This keeps unrelated tests valid after the schema starts requiring reporting.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/reporting ./internal/requirements ./cmd/perf-runner -run 'TestValidateConfig|TestPrometheusMetricNames|TestLoad|TestSuiteRequirements'
```

Expected: FAIL because `internal/reporting` and the reporting requirement fields do not exist.

- [ ] **Step 3: Implement config types, schema, loading, and generator output**

Create `internal/reporting/config.go`:

```go
package reporting

import (
    "fmt"
    "sort"

    "github.com/Azure/aks-burner/internal/config"
)

type Config struct {
    Sources               Sources                     `yaml:"sources"`
    PrometheusMetricUnits map[string]string           `yaml:"prometheusMetricUnits"`
    PrometheusMetricNames []string                    `yaml:"-"`
}

type Sources struct {
    StandardSummary bool `yaml:"standardSummary"`
    KubeBurner      bool `yaml:"kubeBurner"`
}

type metricProfileEntry struct {
    MetricName string `yaml:"metricName"`
}

func PrometheusMetricNames(path string) ([]string, error) {
    var entries []metricProfileEntry
    if err := config.LoadYAML(path, &entries); err != nil {
        return nil, err
    }
    names := make([]string, 0, len(entries))
    for _, entry := range entries {
        if entry.MetricName == "" {
            return nil, fmt.Errorf("%s contains an empty metricName", path)
        }
        names = append(names, entry.MetricName)
    }
    sort.Strings(names)
    return names, nil
}

func ValidateConfig(cfg *Config, artifactsEnabled, prometheusEnabled bool, workload map[string]any, prometheusMetricNames []string) error {
    if !cfg.Sources.StandardSummary && !cfg.Sources.KubeBurner {
        return fmt.Errorf("at least one viable reporting source is required")
    }
    if cfg.Sources.StandardSummary && !artifactsEnabled {
        return fmt.Errorf("standardSummary reporting requires enabled artifact collection")
    }
    if !cfg.Sources.KubeBurner {
        return nil
    }
    supportedMeasurement := hasMeasurement(workload, "podLatency")
    if prometheusEnabled {
        declared := map[string]bool{}
        for _, name := range prometheusMetricNames {
            declared[name] = true
            if cfg.PrometheusMetricUnits[name] == "" {
                return fmt.Errorf("Prometheus reporting metric %q requires a unit", name)
            }
        }
        for name := range cfg.PrometheusMetricUnits {
            if !declared[name] {
                return fmt.Errorf("Prometheus metric unit %q has no matching metric in metrics.yml", name)
            }
        }
    }
    if !supportedMeasurement && (!prometheusEnabled || len(prometheusMetricNames) == 0) {
        return fmt.Errorf("kubeBurner reporting requires podLatency or declared Prometheus metrics")
    }
    cfg.PrometheusMetricNames = append([]string(nil), prometheusMetricNames...)
    return nil
}

func hasMeasurement(workload map[string]any, wanted string) bool {
    global, _ := workload["global"].(map[string]any)
    measurements, _ := global["measurements"].([]any)
    for _, item := range measurements {
        measurement, _ := item.(map[string]any)
        if measurement["name"] == wanted {
            return true
        }
    }
    jobs, _ := workload["jobs"].([]any)
    for _, item := range jobs {
        job, _ := item.(map[string]any)
        measurements, _ := job["measurements"].([]any)
        for _, measurementItem := range measurements {
            measurement, _ := measurementItem.(map[string]any)
            if measurement["name"] == wanted {
                return true
            }
        }
    }
    return false
}
```

Add `Reporting reporting.Config` to `requirements.Document.Requires`. Update `schemas/requirements.schema.json` so `reporting` is required and has exactly:

```json
"reporting": {
  "type": "object",
  "additionalProperties": false,
  "required": ["sources", "prometheusMetricUnits"],
  "properties": {
    "sources": {
      "type": "object",
      "additionalProperties": false,
      "required": ["standardSummary", "kubeBurner"],
      "properties": {
        "standardSummary": { "type": "boolean" },
        "kubeBurner": { "type": "boolean" }
      }
    },
    "prometheusMetricUnits": {
      "type": "object",
      "propertyNames": { "minLength": 1 },
      "additionalProperties": { "type": "string", "minLength": 1 }
    }
  }
}
```

Update generated and checked-in requirements:

```yaml
# kata-perf
reporting:
  sources:
    standardSummary: false
    kubeBurner: true
  prometheusMetricUnits:
    podCPUUsage: cores
    podMemoryWorkingSet: bytes

# kata-io
reporting:
  sources:
    standardSummary: true
    kubeBurner: true
  prometheusMetricUnits:
    podCPUUsage: cores
    podMemoryWorkingSet: bytes
    containerFsReadBytes: bytes
    containerFsWriteBytes: bytes
    containerFsIoTime: seconds
    containerNetworkReceiveBytes: bytes
    containerNetworkTransmitBytes: bytes
    kubePodCreated: unix_seconds
    kubePodScheduledTime: unix_seconds
    kubePodInitializedTime: unix_seconds
    kubePodContainerStarted: unix_seconds
    kubePodReadyTime: unix_seconds
    kubePodRuntimeClass: info
    runPodSandboxP95: seconds
    runPodSandboxP99: seconds
    podStartTotalP95: seconds
```

Make `suiteRequirements` emit the two generated metric units regardless of whether Prometheus installation is selected; `podLatency` still makes the kube-burner source viable when Prometheus is disabled.

- [ ] **Step 4: Run targeted and schema tests**

Run:

```bash
go test ./internal/reporting ./internal/requirements ./internal/examples ./cmd/perf-runner -run 'TestValidateConfig|TestPrometheusMetricNames|TestLoad|ContractsValidate|SuiteRequirements|RequirementsSchema'
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/reporting/config.go internal/reporting/config_test.go internal/requirements/requirements.go internal/requirements/requirements_test.go schemas/requirements.schema.json cmd/perf-runner/main.go cmd/perf-runner/main_test.go suites/kata-perf/requirements.yml suites/kata-io/requirements.yml internal/examples/examples_test.go
git commit -m "feat: declare suite reporting sources"
```

---

### Task 2: Pin Kube-Burner And Always Configure Its Local Indexer

**Files:**
- Create: `internal/run/kube_burner.go`
- Create: `internal/run/kube_burner_test.go`
- Modify: `internal/run/run.go:74-92,183-205`
- Modify: `internal/run/run_test.go:31-78,286-394`
- Modify: `cmd/perf-runner/main.go:430-590`
- Modify: `cmd/perf-runner/main_test.go:349-499,1204-1283`

**Interfaces:**
- Produces: `run.RequiredKubeBurnerVersion`, `run.ValidateKubeBurnerVersion(root string) error`, and `run.KubeBurnerExecutable(root string) string`.
- Changes: `run.RenderWorkload(workload map[string]any, mode Mode, images map[string]string, prometheusEndpoint string, kubeBurnerReporting bool) (map[string]any, error)`.
- Consumes: `requirements.Document.Requires.Reporting.Sources.KubeBurner` from Task 1.

- [ ] **Step 1: Write failing version and rendering tests**

Create `internal/run/kube_burner_test.go`:

```go
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
```

Replace `TestRenderWorkloadSkipsPrometheusEndpointWhenEmpty` with two tests:

```go
func TestRenderWorkloadAddsLocalIndexerWithoutPrometheus(t *testing.T) {
    workload := map[string]any{"jobs": []any{}}
    rendered, err := RenderWorkload(workload, Mode{}, nil, "", true)
    if err != nil { t.Fatal(err) }
    endpoint := rendered["metricsEndpoints"].([]any)[0].(map[string]any)
    if _, exists := endpoint["endpoint"]; exists { t.Fatalf("unexpected endpoint: %#v", endpoint) }
    indexer := endpoint["indexer"].(map[string]any)
    if indexer["type"] != "local" || indexer["metricsDirectory"] != "../raw/metrics" {
        t.Fatalf("indexer = %#v", indexer)
    }
}

func TestRenderWorkloadOmitsMetricsEndpointsWhenReportingDisabled(t *testing.T) {
    rendered, err := RenderWorkload(map[string]any{"jobs": []any{}}, Mode{}, nil, "", false)
    if err != nil { t.Fatal(err) }
    if _, exists := rendered["metricsEndpoints"]; exists { t.Fatalf("unexpected endpoint: %#v", rendered) }
}
```

In `cmd/perf-runner/main_test.go`, add a test whose fake kube-burner reports `2.7.2`; assert `az`, `kubectl`, and `results/` remain untouched. Update successful fake kube-burner scripts to print `Version: 2.7.3` for the `version` subcommand.

Move mode YAML loading, selected workload loading, `metrics.yml` name loading, `reporting.ValidateConfig`, and mode image-var validation ahead of `prepareRunSuiteCluster` in this task. Add a missing-workload test that asserts `az`, `kubectl`, kube-burner, and `results/` remain untouched. Update `provisionTestRepo` so its run-suite use creates `vars/smoke.yml`, `workload.yml`, `metrics.yml`, `config/images.yml`, and a viable reporting declaration before `TestRunSuiteClusterOverrideUsesOverrideForCredentials` reaches its credential sentinel. That test must also create a repo-local `bin/kube-burner` using `writeVersionedKubeBurner(..., "2.7.3")`; it must not depend on the developer machine's PATH.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/run ./cmd/perf-runner -run 'KubeBurnerVersion|RenderWorkload.*LocalIndexer|RunSuite.*KubeBurnerVersion'
```

Expected: FAIL because version validation and the always-local indexer do not exist.

- [ ] **Step 3: Implement executable resolution and version validation**

Move kube-burner executable lookup from `run.go` into `internal/run/kube_burner.go` and expose:

```go
package run

import (
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "strings"
)

const RequiredKubeBurnerVersion = "2.7.3"

var kubeBurnerVersionPattern = regexp.MustCompile(`(?m)^Version:\s*v?([^\s]+)\s*$`)

func KubeBurnerExecutable(root string) string {
    candidate := filepath.Join(root, "bin", "kube-burner")
    if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
        return candidate
    }
    return "kube-burner"
}

func ValidateKubeBurnerVersion(root string) error {
    executable := KubeBurnerExecutable(root)
    output, err := exec.Command(executable, "version").CombinedOutput()
    if err != nil {
        return fmt.Errorf("read kube-burner version from %s: %w: %s", executable, err, strings.TrimSpace(string(output)))
    }
    match := kubeBurnerVersionPattern.FindSubmatch(output)
    if match == nil {
        return fmt.Errorf("parse kube-burner version from %s: %q", executable, strings.TrimSpace(string(output)))
    }
    actual := string(match[1])
    if actual != RequiredKubeBurnerVersion {
        return fmt.Errorf("kube-burner version %s is unsupported; install %s", actual, RequiredKubeBurnerVersion)
    }
    return nil
}
```

Use `KubeBurnerExecutable(root)` in `ExecuteKubeBurner` after resolving the repository root. Call `ValidateKubeBurnerVersion(root)` after the reordered local preflight but before `prepareRunSuiteCluster`, `kubectl`, image builds, or `CreateRunDir`.

- [ ] **Step 4: Implement local-indexer rendering**

Change `RenderWorkload` to append one local-indexer endpoint when kube-burner reporting is enabled:

```go
if kubeBurnerReporting {
    endpoint := map[string]any{
        "indexer": map[string]any{
            "type":             "local",
            "metricsDirectory": "../raw/metrics",
        },
    }
    if prometheusEndpoint != "" {
        endpoint["endpoint"] = prometheusEndpoint
        endpoint["metrics"] = []any{"metrics.yml"}
    }
    rendered["metricsEndpoints"] = []any{endpoint}
}
```

Update every call site and test to pass the explicit boolean.

- [ ] **Step 5: Run targeted tests**

Run:

```bash
go test ./internal/run ./cmd/perf-runner -run 'KubeBurner|RenderWorkload|RunSuite'
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/run/kube_burner.go internal/run/kube_burner_test.go internal/run/run.go internal/run/run_test.go cmd/perf-runner/main.go cmd/perf-runner/main_test.go
git commit -m "feat: pin kube-burner reporting output"
```

---

### Task 3: Parse Versioned Standard Summary Documents

**Files:**
- Create: `internal/reporting/row.go`
- Create: `internal/reporting/standard.go`
- Create: `internal/reporting/standard_test.go`
- Create: `internal/reporting/testdata/standard/valid.json`
- Create: `internal/reporting/testdata/standard/invalid-reserved-dimension.json`
- Create: `internal/reporting/testdata/standard/invalid-number.json`

**Interfaces:**
- Produces: `reporting.Row`, `reporting.Number`, `reporting.ReadStandardSummaries(artifactsDir, runDir string) ([]Row, int, error)`, `reporting.SortRows(rows []Row)`, and `reporting.ValidateRows(rows []Row) error`.
- Consumes: `reporting.Config.Sources.StandardSummary` from Task 1.

- [ ] **Step 1: Add fixtures and failing parser tests**

Create `internal/reporting/testdata/standard/valid.json`:

```json
{
  "schemaVersion": 1,
  "dimensions": {
    "runtime": "kata",
    "storage": "emptydir",
    "workload": "fio"
  },
  "metrics": [
    {"name": "read_iops", "value": 104113.481442, "unit": "operations/second"},
    {"name": "exit_code", "value": 0, "unit": "code"}
  ]
}
```

Create `standard_test.go` with tests for valid expansion, unsupported `schemaVersion`, empty metrics, reserved dimension names, empty keys/names/units, non-number values, `1e309`, more than 17 significant digits, duplicate rows in one document, deterministic sorting, and a missing artifacts directory returning zero rows without error. Key assertions:

```go
func TestReadStandardSummariesExpandsMetrics(t *testing.T) {
    runDir := t.TempDir()
    sampleDir := filepath.Join(runDir, "artifacts", "scenario", "sample")
    if err := os.MkdirAll(sampleDir, 0o755); err != nil { t.Fatal(err) }
    data, err := os.ReadFile("testdata/standard/valid.json")
    if err != nil { t.Fatal(err) }
    if err := os.WriteFile(filepath.Join(sampleDir, "summary.json"), data, 0o644); err != nil { t.Fatal(err) }

    rows, files, err := ReadStandardSummaries(filepath.Join(runDir, "artifacts"), runDir)
    if err != nil { t.Fatal(err) }
    if files != 1 || len(rows) != 2 { t.Fatalf("files/rows = %d/%d", files, len(rows)) }
    if rows[0].Source != "artifacts/scenario/sample/summary.json" { t.Fatalf("source = %q", rows[0].Source) }
    if rows[0].Value.Text != "0" || rows[1].Value.Text != "104113.481442" { t.Fatalf("rows = %#v", rows) }
}
```

- [ ] **Step 2: Run parser tests to verify they fail**

Run:

```bash
go test ./internal/reporting -run 'Standard|SortRows|ValidateRows'
```

Expected: FAIL because the row model and parser do not exist.

- [ ] **Step 3: Implement the row and number model**

Create `row.go` with:

```go
package reporting

import (
    "encoding/json"
    "fmt"
    "math"
    "sort"
    "strconv"
    "strings"
    "unicode"
)

var reservedColumns = map[string]bool{"source": true, "metric": true, "value": true, "unit": true}

type Number struct {
    Text string
}

func ParseNumber(value json.Number) (Number, error) {
    text := value.String()
    parsed, err := strconv.ParseFloat(text, 64)
    if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
        return Number{}, fmt.Errorf("value %q is not a finite binary64 number", text)
    }
    mantissa := text
    if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
        mantissa = mantissa[:index]
    }
    digits := 0
    significantStarted := false
    for _, r := range mantissa {
        if !unicode.IsDigit(r) { continue }
        if r != '0' { significantStarted = true }
        if significantStarted { digits++ }
    }
    if digits == 0 { digits = 1 }
    if digits > 17 {
        return Number{}, fmt.Errorf("value %q exceeds 17 significant decimal digits", text)
    }
    return Number{Text: text}, nil
}

type Row struct {
    Source     string
    Dimensions map[string]string
    Metric     string
    Value      Number
    Unit       string
}

func SortRows(rows []Row) {
    dimensionKeys := DimensionColumns(rows)
    sort.SliceStable(rows, func(i, j int) bool {
        return CompareRows(rows[i], rows[j], dimensionKeys) < 0
    })
}

func CompareRows(left, right Row, dimensionKeys []string) int {
    for _, key := range dimensionKeys {
        if result := strings.Compare(left.Dimensions[key], right.Dimensions[key]); result != 0 { return result }
    }
    if result := strings.Compare(left.Metric, right.Metric); result != 0 { return result }
    if result := strings.Compare(left.Unit, right.Unit); result != 0 { return result }
    return strings.Compare(left.Source, right.Source)
}
```

Implement `DimensionColumns` as the sorted union of dimension keys. Implement `ValidateRows` without delimiter-concatenated keys: after `SortRows`, adjacent rows are duplicates when `CompareRows(previous, current, dimensionKeys) == 0`. Preserve `Number.Text`; never serialize through `float64`.

- [ ] **Step 4: Implement strict standard-summary discovery and parsing**

Create `standard.go` with typed JSON structs using `json.Number`, `decoder.UseNumber()`, `decoder.DisallowUnknownFields()`, exact `schemaVersion == 1`, exactly one JSON value per file, and `filepath.WalkDir` discovery of basename `summary.json`. Validate dimensions before expanding metrics. Errors must include the run-relative source path and field name.

Use this document shape:

```go
type standardDocument struct {
    SchemaVersion int               `json:"schemaVersion"`
    Dimensions    map[string]string `json:"dimensions"`
    Metrics       []standardMetric  `json:"metrics"`
}

type standardMetric struct {
    Name  string      `json:"name"`
    Value json.Number `json:"value"`
    Unit  string      `json:"unit"`
}
```

- [ ] **Step 5: Run reporting tests**

Run:

```bash
go test ./internal/reporting -run 'Standard|Number|SortRows|ValidateRows'
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/reporting/row.go internal/reporting/standard.go internal/reporting/standard_test.go internal/reporting/testdata/standard
git commit -m "feat: parse standard benchmark summaries"
```

---

### Task 4: Adapt Explicit Kube-Burner Result Shapes

**Files:**
- Create: `internal/reporting/kube_burner.go`
- Create: `internal/reporting/kube_burner_test.go`
- Create: `internal/reporting/testdata/kube-burner-2.7.3/podLatencyQuantilesMeasurement.json`
- Create: `internal/reporting/testdata/kube-burner-2.7.3/podCPUUsage.json`
- Create: `internal/reporting/testdata/kube-burner-2.7.3/ignored-podLatencyMeasurement.json`
- Create: `internal/reporting/testdata/kube-burner-2.7.3/jobSummary.json`

**Interfaces:**
- Produces: `reporting.ReadKubeBurnerMetrics(metricsDir, runDir string, prometheusMetricNames []string, metricUnits map[string]string) ([]Row, int, error)`.
- Consumes: `reporting.Row`, `reporting.ParseNumber`, the exact metric-name set returned by Task 1, and its validated unit map.

- [ ] **Step 1: Capture v2.7.3 fixture shapes and write failing tests**

Create fixtures with top-level arrays matching kube-burner local-indexer output. Record this provenance at the top of `kube_burner_test.go`: the fixture shapes come from kube-burner v2.7.3 (`9091baef070ca04e1116cc4d07e53d08490e6896`) measurement and local-indexer output. The committed fixture contents are:

```json
// podLatencyQuantilesMeasurement.json
[
  {
    "quantileName": "Ready",
    "uuid": "23c0b5fd-c17e-4326-a389-b3aebc774c82",
    "P99": 3774,
    "P95": 3510,
    "P50": 2897,
    "min": 2801,
    "max": 3774,
    "avg": 2876,
    "timestamp": "2026-07-11T00:00:00Z",
    "metricName": "podLatencyQuantilesMeasurement",
    "jobName": "startup-smoke"
  }
]
```

```json
// podCPUUsage.json
[
  {
    "timestamp": "2026-07-11T00:00:00Z",
    "labels": {"pod": "demo-0", "namespace": "demo"},
    "value": 0.3300880234732172,
    "uuid": "23c0b5fd-c17e-4326-a389-b3aebc774c82",
    "query": "sum(rate(container_cpu_usage_seconds_total[2m])) by (pod, namespace)",
    "metricName": "podCPUUsage",
    "jobName": "startup-smoke"
  },
  {
    "timestamp": "2026-07-11T00:00:30Z",
    "labels": {"pod": "demo-0", "namespace": "demo"},
    "value": 0.31978102677038506,
    "uuid": "23c0b5fd-c17e-4326-a389-b3aebc774c82",
    "query": "sum(rate(container_cpu_usage_seconds_total[2m])) by (pod, namespace)",
    "metricName": "podCPUUsage",
    "jobName": "startup-smoke"
  }
]
```

`ignored-podLatencyMeasurement.json` contains one documented pod timeseries object, and `jobSummary.json` contains one object with `metricName: "jobSummary"`, `elapsedTime`, `passed`, and `jobConfig`. The adapter ignores `min` for the initial CSV contract but the fixture retains it because v2.7.3 emits it. To refresh fixtures after a future version change, run that exact kube-burner version against the `kata-perf` smoke suite, copy the corresponding arrays from `raw/metrics/`, remove environment-specific names/UUIDs, and retain all structural fields before updating adapter tests.

Add tests:

```go
func TestReadKubeBurnerMetricsMapsPodLatencyQuantiles(t *testing.T) {
    runDir := copyKubeBurnerFixtures(t, "podLatencyQuantilesMeasurement.json")
    rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
    if err != nil { t.Fatal(err) }
    if files != 1 || len(rows) != 5 { t.Fatalf("files/rows = %d/%d", files, len(rows)) }
    for _, row := range rows {
        if row.Unit != "milliseconds" || row.Dimensions["jobName"] != "startup-smoke" || row.Dimensions["quantileName"] != "Ready" {
            t.Fatalf("row = %#v", row)
        }
    }
}

func TestReadKubeBurnerMetricsPreservesRangeSamples(t *testing.T) {
    runDir := copyKubeBurnerFixtures(t, "podCPUUsage.json")
    rows, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, []string{"podCPUUsage"}, map[string]string{"podCPUUsage": "cores"})
    if err != nil { t.Fatal(err) }
    if len(rows) != 2 || rows[0].Dimensions["kubeBurner.timestamp"] == rows[1].Dimensions["kubeBurner.timestamp"] {
        t.Fatalf("rows = %#v", rows)
    }
    if rows[0].Dimensions["label.pod"] == "" { t.Fatalf("rows = %#v", rows) }
}
```

Also test: timeseries and job summaries are ignored; an unknown `metricName` is ignored; a unit-map entry absent from `prometheusMetricNames` is ignored; a declared Prometheus metric with malformed `value`, labels, or timestamp fails; a known quantile shape with missing `P99` fails; and a missing metrics directory returns zero rows without error.

- [ ] **Step 2: Run adapter tests to verify they fail**

Run:

```bash
go test ./internal/reporting -run KubeBurner
```

Expected: FAIL because `ReadKubeBurnerMetrics` does not exist.

- [ ] **Step 3: Implement exact document discrimination and mappings**

Create `kube_burner.go`. Decode each JSON file as `[]map[string]json.RawMessage`; every accepted object must have a string `metricName`.

Dispatch exactly:

```go
switch metricName {
case "podLatencyQuantilesMeasurement":
    rows, err = appendPodLatencyQuantileRows(rows, source, document)
case "podLatencyMeasurement", "jobSummary":
    continue
default:
    unit, declared := declaredPrometheusUnits[metricName]
    if !declared {
        continue
    }
    rows, err = appendPrometheusRow(rows, source, metricName, unit, document)
}
```

Build `declaredPrometheusUnits` by iterating only `prometheusMetricNames` and looking up each already-validated unit. Do not treat extra keys in `metricUnits` as accepted document types.

For quantiles, require and emit `P50`, `P95`, `P99`, `max`, and `avg` as separate rows named `pod_latency_p50`, `pod_latency_p95`, `pod_latency_p99`, `pod_latency_max`, and `pod_latency_avg`. Dimensions are `jobName` and `quantileName`; unit is `milliseconds`.

For Prometheus samples, require `value`, `timestamp`, and `labels`. Prefix every label key with `label.`. Add `kubeBurner.timestamp` and optional `kubeBurner.jobName`. Reject any resulting dimension key equal to a reserved column.

- [ ] **Step 4: Run all reporting tests**

Run:

```bash
go test ./internal/reporting
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/reporting/kube_burner.go internal/reporting/kube_burner_test.go internal/reporting/testdata/kube-burner-2.7.3
git commit -m "feat: adapt kube-burner result documents"
```

---

### Task 5: Write Atomic CSV And Concise Terminal Preview

**Files:**
- Create: `internal/reporting/output.go`
- Create: `internal/reporting/output_test.go`
- Create: `internal/reporting/report.go`
- Create: `internal/reporting/report_test.go`

**Interfaces:**
- Produces: `reporting.RunInfo`, `reporting.Result`, `reporting.Generate(runDir string, cfg Config, info RunInfo, out io.Writer) (Result, error)`.
- Consumes: both adapters from Tasks 3-4 and `reporting.SortRows`/`ValidateRows`.

- [ ] **Step 1: Write failing CSV and preview tests**

Create tests covering union/sorted dimension columns, empty cells, source paths, original number text, standard CSV quoting, spreadsheet escaping after leading whitespace/control characters, deterministic rows, atomic replacement, 0/9/10/11-row previews, omitted count, and workspace-relative CSV path.

Use these public types:

```go
type RunInfo struct {
    Suite         string
    Mode          string
    Timestamp     string
    WorkspaceRoot string
}

type Result struct {
    SourceFiles int
    Rows        int
    CSVPath     string
}
```

Representative preview assertion:

```go
func TestGeneratePrintsTenRowsAndCSVPath(t *testing.T) {
    runDir := writeElevenStandardRows(t)
    var out bytes.Buffer
    result, err := Generate(runDir, Config{Sources: Sources{StandardSummary: true}}, RunInfo{Suite: "demo", Mode: "smoke", Timestamp: "2026-07-11T00:00:00Z", WorkspaceRoot: filepath.Dir(runDir)}, &out)
    if err != nil { t.Fatal(err) }
    text := out.String()
    if strings.Count(text, "metric-") != 10 || !strings.Contains(text, "1 additional row omitted") {
        t.Fatalf("output:\n%s", text)
    }
    relative, err := filepath.Rel(filepath.Dir(runDir), result.CSVPath)
    if err != nil { t.Fatal(err) }
    if !strings.Contains(text, filepath.ToSlash(relative)) {
        t.Fatalf("output:\n%s", text)
    }
}
```

- [ ] **Step 2: Run output tests to verify they fail**

Run:

```bash
go test ./internal/reporting -run 'CSV|Preview|Generate|Spreadsheet|Atomic'
```

Expected: FAIL because output generation does not exist.

- [ ] **Step 3: Implement spreadsheet-safe CSV output**

Implement `writeCSV(path string, rows []Row) error` using `encoding/csv`. Build dimension headers from a sorted union. Neutralize each header/text cell with:

```go
func spreadsheetSafe(value string) string {
    trimmed := strings.TrimLeftFunc(value, func(r rune) bool {
        return unicode.IsSpace(r) || unicode.IsControl(r)
    })
    if trimmed == "" { return value }
    switch trimmed[0] {
    case '=', '+', '-', '@':
        return "'" + value
    default:
        return value
    }
}
```

Create the temporary file with `os.CreateTemp(summaryDir, "results.csv.tmp-*")`, set mode `0644`, call `writer.Flush()`, check `writer.Error()`, close successfully, then `os.Rename` to `results.csv`. Remove the temp file on any error.

- [ ] **Step 4: Implement orchestration and preview rendering**

Implement `Generate`:

```go
func Generate(runDir string, cfg Config, info RunInfo, out io.Writer) (Result, error) {
    var rows []Row
    sourceFiles := 0
    if cfg.Sources.StandardSummary {
        standardRows, files, err := ReadStandardSummaries(filepath.Join(runDir, "artifacts"), runDir)
        if err != nil { return Result{}, err }
        rows = append(rows, standardRows...)
        sourceFiles += files
    }
    if cfg.Sources.KubeBurner {
        metricNames := PrometheusMetricNamesFromConfig(cfg)
        kubeRows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, metricNames, cfg.PrometheusMetricUnits)
        if err != nil { return Result{}, err }
        rows = append(rows, kubeRows...)
        sourceFiles += files
    }
    if len(rows) == 0 {
        return Result{}, fmt.Errorf("no valid measurements found under %s/artifacts or %s/raw/metrics", runDir, runDir)
    }
    SortRows(rows)
    if err := ValidateRows(rows); err != nil { return Result{}, err }
    csvPath := filepath.Join(runDir, "summary", "results.csv")
    if err := writeCSV(csvPath, rows); err != nil { return Result{}, err }
    printPreview(out, info, rows, sourceFiles, csvPath)
    return Result{SourceFiles: sourceFiles, Rows: len(rows), CSVPath: csvPath}, nil
}
```

Add `PrometheusMetricNames []string` to `Config` with `yaml:"-"`. `reporting.ValidateConfig` copies the validated `metrics.yml` names into that field, and `PrometheusMetricNamesFromConfig` returns a sorted copy. This preserves the exact declared metric-name set through `Generate`; it must not derive acceptance from unit-map keys.

```go
func PrometheusMetricNamesFromConfig(cfg Config) []string {
    names := append([]string(nil), cfg.PrometheusMetricNames...)
    sort.Strings(names)
    return names
}
```

Use `text/tabwriter` for the preview and `strconv.FormatFloat(parsed, 'g', 6, 64)` only for terminal values. Include suite/mode/timestamp, source/row counts, first 10 rows, omitted count, and `Results CSV: ` followed by `filepath.Rel(workspaceRoot, csvPath)`. Add `WorkspaceRoot string` to `RunInfo`; production passes the repository root and tests pass their temporary repository root.

- [ ] **Step 5: Run reporting tests**

Run:

```bash
go test ./internal/reporting
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/reporting/output.go internal/reporting/output_test.go internal/reporting/report.go internal/reporting/report_test.go
git commit -m "feat: render test result reports"
```

---

### Task 6: Migrate Kata IO Producers To Standard Summaries

**Files:**
- Modify: `suites/kata-io/images/benchmark/scripts/run-fio.sh:4-88`
- Modify: `suites/kata-io/images/benchmark/scripts/run-git-clone.sh:4-76`
- Modify: `suites/kata-io/templates/fio-emptydir-standard-job.yml`
- Modify: `suites/kata-io/templates/fio-emptydir-kata-job.yml`
- Modify: `suites/kata-io/templates/fio-pvc-standard-job.yml`
- Modify: `suites/kata-io/templates/fio-pvc-kata-job.yml`
- Modify: `suites/kata-io/templates/git-emptydir-standard-job.yml`
- Modify: `suites/kata-io/templates/git-emptydir-kata-job.yml`
- Modify: `suites/kata-io/templates/git-pvc-standard-job.yml`
- Modify: `suites/kata-io/templates/git-pvc-kata-job.yml`
- Modify: `internal/examples/examples_test.go:791-834`
- Modify: `.github/workflows/ci.yml:12-20`

**Interfaces:**
- Produces: one schema-version-1 `summary.json` per Kata IO sample.
- Consumes: the standard summary contract from Task 3.

- [ ] **Step 1: Write failing producer contract tests**

Replace the script string-only assertion with tests that execute both scripts against fake `fio`, `git`, and `time` commands in a temporary PATH. Add `TIME_BIN="${TIME_BIN:-/usr/bin/time}"` to the implementation and set it in tests. The test harness uses real `bash`, `jq`, `awk`, `date`, `df`, `du`, `find`, `wc`, and `tr`; check each with `exec.LookPath` at test start and fail with an actionable missing-tool message rather than silently skipping. Add a CI prerequisite step `sudo apt-get update && sudo apt-get install -y jq gawk coreutils findutils` before `make test` so the harness is reproducible on GitHub Actions.

The tests must parse generated `summary.json` through `reporting.ReadStandardSummaries` and assert:

- FIO emits dimensions `runtime`, `storage`, `workload=fio`, `profile`, `concurrency`, and `sample`.
- Git emits dimensions `runtime`, `storage`, `workload=git`, `profile=<clone mode>`, `concurrency`, and `sample`.
- Existing raw files remain.
- `summary.prom` is not created.
- The script returns the benchmark's exit code after writing the summary.

Add template tests requiring these environment variables in all eight templates:

```text
RUNTIME
STORAGE_TYPE
CONCURRENCY
FIO_PROFILE_NAME or CLONE_MODE
```

- [ ] **Step 2: Run producer tests to verify they fail**

Run:

```bash
go test ./internal/examples -run 'KataIO.*Summary|KataIO.*Template.*Dimensions'
```

Expected: FAIL because scripts still write `summary.prom` and templates do not pass all dimensions.

- [ ] **Step 3: Update all Kata IO templates**

Add the producer dimension environment variables to every FIO/Git template, using existing input variables. For example:

```yaml
- name: RUNTIME
  value: {{.runtime}}
- name: STORAGE_TYPE
  value: {{.storageType}}
- name: CONCURRENCY
  value: "{{.concurrency}}"
```

- [ ] **Step 4: Replace Prometheus text with standard JSON**

In both scripts define the shared inputs:

```bash
RUNTIME="${RUNTIME:?RUNTIME is required}"
STORAGE_TYPE="${STORAGE_TYPE:?STORAGE_TYPE is required}"
CONCURRENCY="${CONCURRENCY:?CONCURRENCY is required}"
TIME_BIN="${TIME_BIN:-/usr/bin/time}"
```

Only `run-fio.sh` additionally requires `FIO_PROFILE_NAME="${FIO_PROFILE_NAME:?FIO_PROFILE_NAME is required}"`. Only `run-git-clone.sh` requires the existing `CLONE_MODE="${CLONE_MODE:-full}"`; do not add `FIO_PROFILE_NAME` to the Git script.

Use jq to guarantee valid escaping and numeric JSON. FIO summary shape:

```bash
jq -n \
  --arg runtime "$RUNTIME" \
  --arg storage "$STORAGE_TYPE" \
  --arg profile "$FIO_PROFILE_NAME" \
  --arg concurrency "$CONCURRENCY" \
  --arg sample "$SAMPLE_ID" \
  --argjson totalDuration "$duration_seconds" \
  --argjson activeRuntime "$active_runtime_seconds" \
  --argjson setupOverhead "$setup_overhead_seconds" \
  --argjson exitCode "$exit_code" \
  --argjson readIOPS "$read_iops" \
  --argjson writeIOPS "$write_iops" \
  --argjson readBW "$read_bw_bytes" \
  --argjson writeBW "$write_bw_bytes" \
  --argjson readP99 "$read_clat_p99_ns" \
  --argjson writeP99 "$write_clat_p99_ns" \
  '{schemaVersion:1,dimensions:{runtime:$runtime,storage:$storage,workload:"fio",profile:$profile,concurrency:$concurrency,sample:$sample},metrics:[
    {name:"total_duration",value:$totalDuration,unit:"seconds"},
    {name:"active_runtime",value:$activeRuntime,unit:"seconds"},
    {name:"setup_overhead",value:$setupOverhead,unit:"seconds"},
    {name:"exit_code",value:$exitCode,unit:"code"},
    {name:"read_iops",value:$readIOPS,unit:"operations/second"},
    {name:"write_iops",value:$writeIOPS,unit:"operations/second"},
    {name:"read_bandwidth",value:$readBW,unit:"bytes/second"},
    {name:"write_bandwidth",value:$writeBW,unit:"bytes/second"},
    {name:"read_clat_p99",value:$readP99,unit:"nanoseconds"},
    {name:"write_clat_p99",value:$writeP99,unit:"nanoseconds"}
  ]}' > "$OUT_DIR/summary.json"
```

Git emits `clone_duration` (`seconds`), `exit_code` (`code`), `repository_size` (`bytes`), and `file_count` (`files`). Do not report start/end timestamps as benchmark measurements; those remain available in logs and raw files.

- [ ] **Step 5: Run producer and examples tests**

Run:

```bash
go test ./internal/examples ./internal/reporting
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add suites/kata-io/images/benchmark/scripts/run-fio.sh suites/kata-io/images/benchmark/scripts/run-git-clone.sh suites/kata-io/templates internal/examples/examples_test.go .github/workflows/ci.yml
git commit -m "feat: emit kata io result summaries"
```

---

### Task 7: Integrate Reporting Into Run-Suite

**Files:**
- Modify: `cmd/perf-runner/main.go:430-590,610-643`
- Modify: `cmd/perf-runner/main_test.go:349-499,909-1068,1152-1309`
- Modify: `README.md:27-126`

**Interfaces:**
- Consumes: `reporting.ValidateConfig`, `run.ValidateKubeBurnerVersion`, `reporting.Generate`, and both adapters.
- Produces: completed `run-suite` behavior with terminal preview and `summary/results.csv`.

- [ ] **Step 1: Write failing preflight and integration tests**

Add tests proving:

1. Invalid reporting declarations fail before `az`, `kubectl`, kube-burner, result directory, or image-build calls.
2. Artifact-enabled execution order is `execute`, `wait`, `copy`, `report`.
3. Artifact-free execution order is `execute`, `report`.
4. A successful workload with no result rows returns an error containing both searched locations.
5. Reporting failure is returned after workload success.
6. Kube-burner failure remains primary and no successful report is printed.
7. A standard-summary fixture creates `summary/results.csv`, prints at most 10 rows, and prints the path.
8. A kube-burner fixture creates the same CSV for an artifact-free suite.

Update every existing successful run-suite fixture, including explicit-context, legacy credentials, image-build, and cluster-override tests, so its fake kube-burner handles `version` with `Version: 2.7.3` and writes a minimal declared local-indexer fixture into the rendered `../raw/metrics` directory during `init`. Keep failure-path fake commands unchanged except where they must pass version preflight to reach the intended sentinel.

Refactor the existing helper to make the post-run stage injectable:

```go
type resultReporter func(runDir string, cfg reporting.Config, info reporting.RunInfo, out io.Writer) (reporting.Result, error)

func executeRunCopyAndReport(
    ctx context.Context,
    target kubetarget.Target,
    workloadPath string,
    logPath string,
    artifactCfg artifacts.Config,
    images map[string]string,
    artifactDestination string,
    artifactSubpath string,
    runDir string,
    reportingCfg reporting.Config,
    runInfo reporting.RunInfo,
    out io.Writer,
    execute targetKubeBurnerExecutor,
    waitArtifactJobs targetArtifactJobWaiter,
    copyArtifacts targetArtifactCopier,
    report resultReporter,
) error
```

- [ ] **Step 2: Run integration tests to verify they fail**

Run:

```bash
go test ./cmd/perf-runner -run 'RunSuite.*Reporting|RunSuite.*Workload.*SideEffects|ExecuteRunCopyAndReport'
```

Expected: FAIL because preflight ordering and reporting integration do not exist.

- [ ] **Step 3: Integrate report generation after artifact handling**

Replace `executeRunAndCopyArtifacts` with `executeRunCopyAndReport`. Preserve existing execution/copy error precedence. Invoke reporting only when execution and artifact copy both succeed:

```go
if executeErr != nil {
    if artifactErr != nil {
        return fmt.Errorf("kube-burner failed: %w; artifact copy also failed: %v", executeErr, artifactErr)
    }
    return executeErr
}
if artifactErr != nil {
    return artifactErr
}
_, err := report(runDir, reportingCfg, runInfo, out)
return err
```

Pass `os.Stdout` from production and test buffers from unit tests. Reporting must use `runTimestamp.Format(time.RFC3339Nano)` to match run metadata.

- [ ] **Step 4: Update documentation**

Add a README section showing:

```text
Test results: kata-io / fio-fast / 2026-07-11T00:00:00Z
Sources: 2  Measurements: 20
...
10 additional rows omitted
Results CSV: results/RUN_DIRECTORY/summary/results.csv
```

Document `reporting.sources`, `prometheusMetricUnits`, standard `summary.json`, mandatory results, the 10-row preview, no runner-side aggregation, and kube-burner `2.7.3`.

- [ ] **Step 5: Run targeted integration tests**

Run:

```bash
go test ./cmd/perf-runner ./internal/reporting ./internal/run ./internal/requirements ./internal/examples
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/perf-runner/main.go cmd/perf-runner/main_test.go README.md
git commit -m "feat: report suite results after runs"
```

---

### Task 8: Full Verification And End-To-End Validation

**Files:**
- Verify: all files changed in Tasks 1-7
- Optional test-only fixtures: `internal/reporting/testdata/`

**Interfaces:**
- Consumes: completed reporting pipeline.
- Produces: reviewed branch with local and live evidence.

- [ ] **Step 1: Format and run static checks**

Run:

```bash
gofmt -w internal/reporting/*.go internal/run/kube_burner*.go internal/requirements/*.go cmd/perf-runner/*.go internal/examples/*.go
```

Expected: all commands exit 0 with no diagnostics.

- [ ] **Step 2: Run the complete local test suite**

Run:

```bash
go test ./...
```

Expected: PASS for every package.

- [ ] **Step 3: Review pending changes**

Run:

```bash
git status --short
```

Invoke the `code-reviewer` subagent with the complete diff. Independently verify every Critical or High finding against the code, tests, project conventions, and pinned kube-burner behavior. Fix verified issues and rerun Steps 1-2 before proceeding.

- [ ] **Step 4: Run live Kata IO validation**

Prerequisites: authenticated Azure CLI, provisioned `kata-io` cluster, repo-local or PATH kube-burner `2.7.3`, and sufficient quota.

Run:

```bash
TEST_SUITE=kata-io TEST_MODE=fio-fast make run-suite
```

Expected:

- Command exits 0.
- Terminal prints no more than 10 measurement rows and the CSV path.
- `summary/results.csv` contains standard-summary FIO rows and declared kube-burner Prometheus rows.
- `artifacts/**/summary.json`, `fio.json`, logs, and diagnostics remain present.

- [ ] **Step 5: Run live Kata Perf validation**

Prerequisites: authenticated Azure CLI and a provisioned `kata-perf` cluster.

Run:

```bash
TEST_SUITE=kata-perf TEST_MODE=smoke make run-suite
```

Expected:

- Command exits 0.
- The artifact-free suite prints a concise preview and CSV path.
- `summary/results.csv` contains pod-latency quantile rows and declared Prometheus rows from `raw/metrics/`.
- Native local-indexer JSON remains in `raw/metrics/`.

- [ ] **Step 6: Commit verification fixes if needed**

If review or live validation required changes:

```bash
git add internal/reporting internal/run internal/requirements internal/examples cmd/perf-runner schemas suites README.md
git commit -m "fix: address result reporting validation"
```

If no changes were required, do not create an empty commit.
