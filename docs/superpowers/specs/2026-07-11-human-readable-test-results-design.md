# Human-Readable Test Results Design

## Goal

Make every successful `perf-runner run-suite` execution produce a concise terminal result and a complete CSV that a human can inspect without reading native benchmark JSON.

The reporting mechanism must work across suites and modes. It must preserve native artifacts for detailed analysis and must not make suite-specific statistical assumptions.

## Current State

Run directories already contain `metadata/`, `rendered/`, `logs/`, `raw/`, and `summary/`. Artifact-enabled suites may also contain `artifacts/`.

The current result sources differ by suite:

- `kata-io` benchmark scripts write raw FIO JSON, Git trace files, diagnostics, and compact Prometheus text summaries below `artifacts/`.
- `kata-perf` relies on kube-burner measurements and Prometheus queries written by kube-burner's local indexer below `raw/metrics/`.
- `summary/` is currently empty, and producing a comparison such as `suites/kata-io/findings.md` requires manual extraction and interpretation.

The runner should provide a common human-facing result without replacing these native artifacts.

## Design Principles

1. Producers decide which measurements are meaningful and at what granularity.
2. Perf-runner validates, collates, sorts, displays, and serializes measurements.
3. Perf-runner does not calculate aggregates, baselines, ratios, regressions, or higher-is-better semantics.
4. Native benchmark and kube-burner output remains available for diagnosis.
5. Reporting is mandatory: a run with no valid measurements is not a successful test run.

## Standard Summary Contract

Artifact-producing workloads write a `summary.json` beside each sample's raw artifacts. The initial schema is:

```json
{
  "schemaVersion": 1,
  "dimensions": {
    "runtime": "kata",
    "storage": "emptydir",
    "workload": "fio",
    "profile": "randread-4k",
    "concurrency": "1"
  },
  "metrics": [
    {
      "name": "read_iops",
      "value": 104113.48,
      "unit": "operations/second"
    }
  ]
}
```

Contract rules:

- `schemaVersion` must be `1`.
- `dimensions` contains producer-defined string keys and values. Keys must be non-empty stable identifiers and must not use the reserved names `source`, `metric`, `value`, or `unit`.
- `metrics` must contain at least one metric.
- Every metric has a non-empty stable `name`, a finite numeric `value`, and a non-empty `unit`.
- Metric names and units are producer-owned. The runner does not maintain a catalog of known metrics.
- A producer may report a raw observation or a statistic such as a percentile. Its metric name must describe that meaning; the runner does not derive it.

`kata-io` will migrate its FIO and Git scripts from `summary.prom` to this contract. Raw `fio.json`, Git traces, logs, and diagnostics remain unchanged.

## Result Sources

Perf-runner uses two adapters that produce one internal measurement-row model.

### Standard Summary Adapter

After artifact copy, the adapter recursively discovers files named `summary.json` below the run's `artifacts/` directory. It parses and validates each document and expands each metric into one row that carries the document's dimensions and source path.

### Kube-Burner Adapter

Perf-runner always configures a local indexer at `raw/metrics/`, even when a suite does not collect Prometheus metrics. Kube-burner measurements such as `podLatency` then have a result destination independent of Prometheus. This guarantees that an artifact-free suite can satisfy the reporting contract if it declares a supported kube-burner measurement.

The adapter reads kube-burner's local-indexer JSON documents below `raw/metrics/`. It uses `metricName` as the exact document discriminator; it does not treat arbitrary JSON containing numbers as a result document. The initial adapter supports the document types emitted by the repository's current suites:

- `podLatencyQuantilesMeasurement`: dimensions are `jobName` and `quantileName`; measurements are `P50`, `P95`, `P99`, `max`, and `avg`; the unit is `milliseconds`.
- Prometheus metric documents whose `metricName` is declared in the suite's `metrics.yml`: dimensions are the document's labels prefixed with `label.`, plus `kubeBurner.jobName` when present and `kubeBurner.timestamp`; the measurement name is `metricName`; the value is the document's `value`; the unit comes from a required suite-owned metric-unit mapping. Namespacing prevents label collisions, and retaining the timestamp preserves distinct samples from range queries.

Metadata, UUIDs, queries, and individual pod-latency timeseries documents are ignored. New kube-burner document types require an explicit typed mapping of discriminator, dimensions, measurement fields, and units before the adapter accepts them.

This adapter is part of perf-runner rather than a suite script because kube-burner-only suites such as `kata-perf` do not run a benchmark container that could emit the standard summary contract. The adapter preserves statistics already calculated by kube-burner, such as pod-latency quantiles, rather than recomputing them from individual documents.

### Kube-Burner Compatibility

The repository adds a required kube-burner version constant for the currently tested release, `2.7.3`. `run-suite` invokes `kube-burner version` during preflight, before Azure, Kubernetes, image-build, or filesystem side effects, and rejects a different version with an actionable error. The existing repo-local-binary preference and `PATH` fallback remain, but both locations must provide the required version.

The implementation plan must capture representative local-indexer fixtures from that version. Adapter mappings are covered by contract tests against those fixtures.

### Suite Reporting Declaration

`requirements.yml` gains a required `reporting` block. It declares the suite's result sources and, for each Prometheus metric accepted by the adapter, its unit. Semantic validation requires at least one viable source:

```yaml
reporting:
  sources:
    standardSummary: false
    kubeBurner: true
  prometheusMetricUnits:
    podCPUUsage: cores
    podMemoryWorkingSet: bytes
```

- `standardSummary` requires enabled artifact collection because `summary.json` files are copied with artifacts.
- `kubeBurner` requires at least one supported kube-burner measurement or at least one metric-unit mapping matching `metrics.yml`.

Validation runs before infrastructure or Kubernetes side effects. This requires loading and validating the selected mode and workload before `prepareRunSuiteCluster`, rather than after cluster preparation as the runner does today. The suite generator emits a valid `kubeBurner` reporting declaration for its existing pod-latency and Prometheus metrics, including units. Existing checked-in suites are migrated in the same change.

### Internal Row

Both adapters produce rows equivalent to:

```text
source: relative path to the native source file
dimensions: map of string keys to string values
metric: stable measurement name
value: finite number
unit: stable unit string
```

This boundary lets future input formats be added without changing CSV or terminal rendering.

## Run Flow

The reporting stage runs after all result-producing activity:

1. Execute kube-burner.
2. Wait for artifact jobs when artifacts are enabled.
3. Copy artifacts when artifacts are enabled.
4. Read only the result sources declared by the suite.
5. Validate and combine all measurement rows.
6. Sort rows deterministically.
7. Write `summary/results.csv`.
8. Print a concise terminal preview and the CSV path.

Reporting runs for artifact-enabled and artifact-free suites. A suite may produce rows from either or both adapters.

If workload execution or artifact copy fails, perf-runner keeps the existing primary failure behavior and does not claim a completed report. If reporting fails after successful workload execution, `run-suite` returns the reporting error.

## No Runner-Side Aggregation

The runner does not calculate count, minimum, median, maximum, sum, or mean.

This is intentional for the current suites:

- A `kata-io` concurrency-one scenario has one sample, so aggregation adds no information.
- A concurrency-ten scenario may need different treatment by metric: throughput may need a sum, latency may need a percentile, and exit codes need a failure count. Generic min/median/max would encode the wrong semantics for some measurements.
- `kata-perf` already delegates pod-latency distributions and quantiles to kube-burner. Re-aggregating those values in perf-runner would duplicate or distort the producer's result.

Producers therefore emit the rows they intend consumers to use. Explicit aggregation can be designed later if a concrete suite requirement establishes its semantics.

## CSV Output

The sole generated report is:

```text
results/<run>/summary/results.csv
```

It contains one row per reported measurement with columns:

```text
source,<sorted union of dimension keys>,metric,value,unit
```

Rules:

- Dimension columns are the union of dimension keys from the run, sorted lexicographically.
- Missing dimensions are empty cells.
- Rows are sorted by dimension values, metric name, unit, and source path.
- Source paths are relative to the run directory.
- CSV quoting follows the standard library's CSV encoding.
- JSON values are decoded with `json.Number`, must parse as finite IEEE-754 binary64 values, may contain at most 17 significant decimal digits, and are written using their original number token.
- Spreadsheet-safe escaping applies to headers and every producer-controlled text cell. After trimming leading Unicode whitespace and control characters for detection, values whose first remaining character is `=`, `+`, `-`, or `@` are prefixed with a single quote before CSV encoding. The original text, including its leading characters, remains after that prefix.
- Output is deterministic for the same input files.
- Perf-runner writes a temporary file in `summary/`, closes it successfully, and atomically renames it to `results.csv`. A failed report cannot leave a truncated file at the authoritative path.

The CSV is the complete human- and tool-consumable result. Native source files remain authoritative for details not represented by the common row model.

## Terminal Output

On success, perf-runner prints:

- Suite, mode, and run timestamp.
- Number of result source files and measurement rows.
- A compact table containing the first 10 deterministically sorted rows.
- The number of omitted rows when the run contains more than 10.
- The workspace-relative path to `summary/results.csv`.

The terminal may abbreviate numeric values for readability. It must not alter values written to CSV.

The preview is deliberately a deterministic first page rather than a hidden importance ranking. Perf-runner has no generic basis for deciding which suite-defined metrics matter most.

## Validation And Errors

Reporting rejects:

- Malformed JSON.
- Unsupported standard-summary schema versions.
- Empty standard-summary metric lists.
- Empty dimension keys or metric names.
- Empty units.
- Non-numeric or non-finite values.
- Duplicate rows with the same source, dimensions, metric, and unit.
- A local-indexer document with a declared or explicitly supported `metricName` that does not match its typed mapping.
- A declared Prometheus result metric without a unit mapping.
- A dimension key that collides with a reserved CSV column.

A successful workload with zero valid measurement rows fails with an error that identifies the searched result locations. This makes the reporting contract mandatory for every suite and mode.

Errors include the source path and invalid field or document shape. Raw artifacts, local-indexer documents, and logs remain in the run directory for diagnosis.

## Components

The implementation should keep reporting separate from run orchestration:

- A reporting package owns the standard contract, internal row type, source adapters, validation, deterministic sorting, CSV writing, and terminal rendering.
- Run orchestration invokes the package only after execution and artifact handling complete.
- `kata-io` benchmark scripts own their suite-specific translation from FIO and Git output to standard summaries.
- The kube-burner adapter owns only conversion of native local-indexer documents to the common row model.

This separation allows adapters and renderers to be tested independently and avoids adding benchmark-specific parsing to `cmd/perf-runner/main.go`.

## Testing

Unit and contract tests cover:

- Valid and invalid version-one standard summaries.
- Finite numeric validation and required names and units.
- Required kube-burner version validation and representative local-indexer fixtures from that version.
- Exact kube-burner document discrimination, explicit field mappings, ignored document types, and preservation of producer-calculated quantiles.
- Reporting declaration schema and semantic validation, including generator and checked-in suite coverage.
- Combining rows from both adapters.
- Duplicate detection.
- Deterministic dimension-column ordering and row ordering.
- CSV escaping, empty dimensions, range-query sample identity, source paths, and binary64-compatible numeric precision.
- Spreadsheet-safe text cells and atomic CSV replacement.
- Terminal previews with fewer than, exactly, and more than 10 rows.
- Missing-result failure.
- Reporting integration for artifact-enabled and artifact-free run paths.
- `kata-io` FIO and Git scripts emitting valid standard summaries while preserving raw artifacts.
- Existing `kata-perf` smoke and full modes producing reportable kube-burner rows.

An end-to-end validation should run one `kata-io` mode and one `kata-perf` mode, confirm the terminal preview, inspect `summary/results.csv`, and verify that the native artifacts remain present.

## Out Of Scope

- HTML reports or dashboards.
- Cross-run history or trend analysis.
- Baseline comparisons and regression thresholds.
- Pass/fail policy based on measurement values.
- Generic statistical aggregation.
- Automatic interpretation of whether higher or lower values are preferable.
- Replacing raw FIO, Git, Prometheus, or kube-burner output.
