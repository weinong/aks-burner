# Creating Test Suites

This repository provisions AKS test infrastructure, renders and runs
kube-burner workloads, copies benchmark artifacts, and writes normalized CSV
results. Current code and JSON schemas are authoritative. Files under
`docs/superpowers/` describe historical designs and may be stale.

## Start Here

Use a lowercase kebab-case suite name:

```sh
TEST_SUITE=my-suite make add-suite
make list-suites
```

A suite lives under `suites/<name>/` and normally contains:

- `suite.yml`: identity, descriptive test names, shared mode defaults, and persistent setup resources.
- `requirements.yml`: AKS, Kubernetes, images, observability, artifacts, and Prometheus metric units.
- `vars/<mode>.yml`: mode-specific overrides. Each filename is an available mode.
- `workload.yml` or `workload-*.yml`: kube-burner jobs, measurements, and hooks.
- `templates/`: Kubernetes object templates referenced by workloads.
- `metrics.yml`: kube-burner Prometheus queries.

Optional directories are `setup/`, `hooks/`, and `images/`. The `tests` array in
`suite.yml` is descriptive; it does not select jobs or workloads.

## Mode Configuration

Put shared values in `suite.yml` and differences in `vars/<mode>.yml`:

```yaml
modeDefaults:
  iterationsPerNamespace: 1
  qps: 5
  burst: 5
  cleanup: true
  waitWhenFinished: false
  preLoadImages: true
  reporting:
    scheme: kube-burner
  templateVars:
    app: my-suite
  imageVars:
    image: pause
```

```yaml
# vars/smoke.yml
iterations: 5
```

Nested maps are merged recursively. Explicit mode values override defaults. The
complete merged mode must satisfy `schemas/mode.schema.json`.

Mode extension points are:

- `workloadFile`: selects an alternate workload; defaults to `workload.yml`.
- `templateVars`: data for strict Go-template rendering with Sprig and for kube-burner `inputVars`.
- `imageVars`: maps an `inputVars` name to a key from `config/images.yml` or a suite image build.
- `artifactSubpath`: selects the run-specific directory copied from the artifact PVC. Artifact-enabled modes must declare one containing `{{.runTimestamp}}`; it must be a single safe path segment.
- Scheduling fields such as `iterations`, `iterationsPerNamespace`, `qps`, `burst`, cleanup, waits, preload, pauses, and metrics closing.
- `reporting.scheme`: selects exactly one reporting contract.

Timestamp placeholders are `{{.runTimestamp}}` for an RFC3339-like safe value
and `{{.runTimestampDNS}}` for Kubernetes names.

## Workload Extensions

Workload YAML is rendered as a Go template with Sprig before it is decoded.
Use loops for scenario matrices and keep Kubernetes resources in `templates/`.
Every object may receive `inputVars`; mode template and image variables are
injected into them.

kube-burner hooks are supported at its lifecycle points, including `beforeGC`,
`afterGC`, `beforeCleanup`, and `afterCleanup`. Hook paths are copied into the
run's `rendered/hooks/` directory. Hooks are commands, not Go plugins.

Persistent cluster preparation belongs in `suite.yml` under `setup.resources`.
Resources are applied in order and may declare a rollout restart followed by
`exists`, `rollout`, or `condition` waits. Setup resources survive the run;
there is no paired setup teardown hook.

## Infrastructure And Images

Declare AKS node pools once in `requirements.yml`. Every node selector names its
pool. Runtime checks enforce the Kubernetes version and required selector node
counts. Storage reporting may also require declared StorageClasses.

Static image keys live in `config/images.yml`. Suite-owned images use
`requires.images.builds` with a suite-relative context and Dockerfile. The
runner builds immutable run-tagged images in the suite ACR and overlays those
references on the static catalog before rendering.

## Reporting Schemes

Choose one scheme per mode:

- `standard-summary`: recursively reads artifact files named `summary.json`. Requires enabled artifact collection.
- `kube-burner`: reads pod latency and declared Prometheus metrics from the local indexer.
- `pod-ready`: kube-burner reporting plus offered-load Ready throughput and missing-sample derivations. The workload must create exactly one measured pod per iteration.
- `storage-startup`: kube-burner reporting for the repository's fixed six-job storage startup matrix. This is a specialized contract, not a generic storage reporter.

`requirements.yml` supplies `prometheusMetricUnits`. Every metric name in
`metrics.yml` needs a unit when Prometheus reporting is enabled. The parser is
pinned to kube-burner `2.7.3` document shapes.

### Standard Summary

Benchmark producers write:

```json
{
  "schemaVersion": 1,
  "dimensions": {"runtime": "kata", "workload": "fio"},
  "metrics": [
    {"name": "read_iops", "value": 104113.481442, "unit": "operations/second"}
  ]
}
```

The document rejects unknown and duplicate fields. Metric names and units must
be nonempty. Values must be finite binary64 numbers with at most 17 significant
decimal digits. Dimension keys must be nonempty and cannot be `source`,
`metric`, `value`, or `unit`. Each file must contain at least one metric.

### Result Layout

Each run creates `results/<timestamp>_<suite>_<mode>/` with:

- `metadata/run.yml`: target, images, setup, and preflight metadata.
- `rendered/`: the final workload, object templates, hooks, and metrics profile.
- `logs/`: kube-burner and ACR build logs.
- `raw/metrics/`: kube-burner local-indexer JSON.
- `artifacts/`: copied benchmark output.
- `summary/results.csv`: normalized measurements.

CSV columns are `source`, alphabetically sorted dimension columns, `metric`,
`value`, and `unit`. Rows are deterministic and spreadsheet-safe. The terminal
prints at most ten rows. `pod-ready` and `storage-startup` may preserve usable
failed-run data with the dimension `runStatus=partial` while returning the
original workload error.

## Lifecycle

`run-suite` validates configuration and image keys, checks kube-burner, resolves
the target, performs Kubernetes preflights, creates the run directory, builds
images, applies setup, writes metadata, starts observability, renders the
workload, runs kube-burner, copies artifacts, and reports results. Artifact copy
is attempted after workload failure. Setup resources persist. Workload cleanup
is controlled by kube-burner. `destroy` deletes the provisioned resource group.

## Validation

Before submitting a suite change, run:

```sh
make test
make build
make list-suites
```

Add or update tests under `internal/examples/` for checked-in suite contracts,
scenario matrices, benchmark scripts, and expected output. Reporting changes
need focused tests under `internal/reporting/`; rendering and lifecycle changes
need tests under `internal/run/` or `cmd/perf-runner/`.

Use a dry run to inspect generated infrastructure parameters without changing
Azure resources:

```sh
go run ./cmd/perf-runner provision --suite my-suite --resource-group dry-run-unused --location westus2 --dry-run
```

## Boundaries

There is no dynamic Go plugin API, reporter registry, result upload, retention,
database, comparison, threshold, or regression framework. Internal command
runner function types are test seams, not public extensions. Workload and
object-template YAML do not have repository-owned JSON schemas. Do not treat
`schemas/environment.schema.json` or observability `requiredMetrics` as a live
runtime metric-presence contract.
