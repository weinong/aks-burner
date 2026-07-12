# Final Review Fix Report

## Status

All four independently verified final-review findings are fixed on `design/human-readable-results`. The changes were developed test-first and are included in one final commit.

## Findings Addressed

### 1. Unlabeled Prometheus Scalars

- Added `internal/reporting/testdata/kube-burner-2.7.3/podStartTotalP95.json`, matching the kube-burner v2.7.3 local-indexer shape for an unlabeled scalar. The empty `labels` map is omitted by kube-burner's `json:"labels,omitempty"` tag.
- `appendPrometheusRow` now treats an absent `labels` member as an empty map.
- A present `labels` member remains strict: `null`, arrays, scalars, object values that are not strings, and other non-object shapes are rejected.
- Added adapter assertions for the metric, value, unit, timestamp, job name, and lack of label dimensions.
- Added `Generate` integration coverage using the unlabeled fixture.

### 2. Strict Successful FIO Summaries

- Added failing script tests proving malformed JSON and incomplete JSON paired with benchmark exit 0 must return nonzero and leave no `summary.json`.
- Successful FIO runs now require a non-empty `jobs` array and numeric values for every field consumed by the summary: read/write IOPS, bandwidth, p99 completion latency, and runtime.
- A parsing or shape failure aborts before summary creation.
- Nonzero benchmark exits retain diagnostic behavior: the script writes a summary containing the original `exit_code` and zero benchmark-derived values, then returns the original benchmark exit code.
- Normal successful output is still parsed into the expected summary values.

### 3. Exact, Duplicate-Safe Standard Summary JSON

- Added a token-level validation pass before typed expansion.
- The validator rejects duplicate members at every object level, including the document, dimensions, metrics, and nested objects.
- Document and metric contract members are matched exactly. Case variants such as `SchemaVersion`, `Dimensions`, `Name`, and `Value` are rejected rather than accepted by `encoding/json`'s case-insensitive field matching.
- Added tests for duplicate `schemaVersion`, duplicate dimension keys, duplicate metric `value`, and wrong-case document and metric fields.
- Existing typed validation remains responsible for required fields, types, schema version, metric values, names, and units.

### 4. Kube-Burner Source File Accounting

- `ReadKubeBurnerMetrics` now increments its file count only when at least one accepted row was appended from that file.
- Ignored-only files contribute zero source files.
- Files containing both ignored and accepted documents contribute exactly one source file.
- Added adapter and `Generate` integration coverage for ignored-only and mixed files.

## TDD Evidence

The new regression tests were run before production changes and failed for the expected reasons:

```text
TestReadKubeBurnerMetricsAcceptsUnlabeledPrometheusScalar:
  labels field is required
TestReadKubeBurnerMetricsIgnoresUnsupportedDocuments:
  files/rows = 4/[]
TestReadKubeBurnerMetricsCountsMixedFileOnce:
  labels field is required
TestGenerateCountsOnlyKubeBurnerFilesContributingRows:
  labels field is required
TestReadStandardSummariesRejectsInvalidDocuments:
  duplicate and wrong-case documents returned nil errors
TestKataIOFioSummary:
  run-fio.sh succeeded with malformed FIO JSON and exit 0
```

After the minimal implementation changes, the same focused tests passed.

## Review

The complete pending diff was reviewed for correctness because a reviewer subagent tool was unavailable in this session. The review specifically checked:

- strict decoder recursion, duplicate tracking, exact field matching, and field-path diagnostics;
- jq and Bash failure behavior under `set -euo pipefail`;
- preservation of diagnostic summaries and original nonzero benchmark exit codes;
- absence of `summary.json` after successful-benchmark parse failures;
- source file accounting for accepted, ignored-only, and mixed kube-burner files;
- v2.7.3 fixture fidelity against kube-burner commit `9091baef070ca04e1116cc4d07e53d08490e6896`.

No Critical or High severity issue remained after review.

## Verification

The following commands completed successfully:

```text
gofmt -w internal/examples/examples_test.go internal/reporting/kube_burner.go internal/reporting/kube_burner_test.go internal/reporting/report_test.go internal/reporting/standard.go internal/reporting/standard_test.go
go test ./internal/reporting ./internal/examples -run 'KubeBurner|Standard|Generate|KataIOFioSummary'
go test ./...
go vet ./...
bash -n suites/kata-io/images/benchmark/scripts/run-fio.sh
git diff --check
```

The full Go test suite passed for all packages.

## Concerns

- No live Azure or Kubernetes benchmark was run; the changes are covered by local adapter, integration, producer-script, full-suite, vet, and syntax checks.
- The strict standard-summary decoder intentionally rejects case variants and duplicate members even when their values agree. This is required by the contract and may expose previously tolerated producer defects.
