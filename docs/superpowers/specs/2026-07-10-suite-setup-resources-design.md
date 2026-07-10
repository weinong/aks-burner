# Suite Setup Resources Design

## Summary

Allow a test suite to install persistent Kubernetes setup resources before benchmark execution. This supports suite-specific cluster preparation such as custom `RuntimeClass` objects or node-mutating `DaemonSet` resources that must be ready before kube-burner starts workload jobs.

Setup resources are suite-owned, applied by the runner with `kubectl apply`, verified with explicit wait rules, and intentionally kept after the run finishes.

## Goals

- Let suites declare Kubernetes setup manifests that run before benchmark workloads.
- Keep setup separate from kube-burner benchmark jobs.
- Support explicit readiness gates so asynchronous setup, especially `DaemonSet`-based node preparation, completes before tests start.
- Make repeated runs idempotent by using `kubectl apply` and not deleting setup resources after `run-suite`.
- Record applied setup resources and waits in run metadata for auditability.

## Non-Goals

- No automatic deletion of setup resources after `run-suite`.
- No automatic inference of readiness based on Kubernetes kind.
- No general hook system for arbitrary shell commands.
- No mode-level setup overrides in v1.
- No changes to resource-group teardown behavior in `destroy`.

## Suite Configuration

Add an optional `setup` section to `suite.yml`:

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

Each resource has:

- `name`: stable human-readable identifier used in logs and metadata.
- `path`: suite-relative path to a Kubernetes manifest file.
- `wait`: optional list of explicit readiness checks.

Paths are resolved relative to `suites/<suite>/`. The runner rejects absolute paths and paths that escape the suite directory through `..` traversal.

## Wait Rules

V1 supports a small set of wait rules that map directly to `kubectl` commands:

```yaml
wait:
  - kind: exists
    resource: runtimeclass/custom-kata
    timeout: 1m
  - kind: rollout
    resource: daemonset/node-prep
    namespace: kube-system
    timeout: 10m
  - kind: condition
    resource: pod/node-prep-check
    namespace: default
    condition: Ready
    timeout: 5m
```

Mappings:

- `exists`: `kubectl get <resource>` with optional `--namespace` when no timeout is set, or `kubectl wait <resource> --for=create --timeout <timeout>` with optional `--namespace` when a timeout is set.
- `rollout`: `kubectl rollout status <resource> --timeout <timeout>` with optional `--namespace`.
- `condition`: `kubectl wait <resource> --for=condition=<condition> --timeout <timeout>` with optional `--namespace`.

If a setup manifest has no wait rules, the runner applies it and continues immediately. This is acceptable for resources that are immediately usable or where the suite author deliberately does not need a readiness gate.

If any wait rule fails, `run-suite` fails before Prometheus installation, workload rendering, or kube-burner execution begins.

## Runner Lifecycle

The `run-suite` lifecycle becomes:

```text
load suite
load mode
validate requirements
build suite images
apply setup resources
wait for setup readiness
install/start Prometheus if required
render kube-burner workload
run kube-burner
write metadata/results
```

Setup resources are applied before Prometheus so a suite can prepare nodes or runtime classes before any auxiliary workload is installed. This keeps suite-defined cluster preparation ahead of all in-cluster run activity.

## Persistence And Idempotency

Setup resources are persistent cluster preparation. `run-suite` does not delete them, regardless of success or failure. This avoids accidentally undoing node-level preparation and allows repeated runs to reuse the same prepared cluster.

Suite authors are responsible for writing idempotent manifests. The runner uses `kubectl apply -f <path>`, so existing resources are updated rather than treated as errors.

If a setup resource must be removed, users should delete it explicitly with `kubectl delete` or destroy the provisioned resource group when using an isolated suite cluster.

## Schema Changes

Extend `schemas/suite.schema.json` with optional `setup.resources`.

Resource validation:

- `name` is required and non-empty.
- `path` is required and non-empty.
- `wait` is optional.

Wait validation:

- `kind` is required and one of `exists`, `rollout`, or `condition`.
- `resource` is required and non-empty.
- `namespace` is optional.
- `timeout` is optional and non-empty when present.
- `condition` is required only for `kind: condition`.

## Go Model Changes

Extend `internal/suite.Config` with setup configuration:

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

The exact formatting can follow repository conventions during implementation.

## Metadata

Record setup activity in `results/<run>/metadata/run.yml`:

```yaml
setup:
  resources:
    - name: node-prep
      path: setup/node-prep-daemonset.yml
      wait:
        - kind: rollout
          resource: daemonset/node-prep
          namespace: kube-system
          timeout: 10m
```

Metadata should describe configured setup resources and wait rules. It does not need to include full manifest content.

## Error Handling

- Missing setup manifest path fails `run-suite` before applying later setup resources.
- Invalid setup path fails before invoking `kubectl`.
- Failed `kubectl apply` fails `run-suite` and skips benchmark execution.
- Failed wait rule fails `run-suite` and skips benchmark execution.
- Already-existing resources are not errors when `kubectl apply` succeeds.

The runner should surface which setup resource is being applied or waited on in returned errors so failures are attributable to suite configuration.

## Tests

Unit tests should cover:

- Suite schema accepts `setup.resources`.
- Suite schema rejects invalid wait kinds and missing required fields.
- Suite schema requires `condition` for `kind: condition`.
- Path validation rejects absolute paths and parent traversal.
- Setup apply runs before workload execution.
- Wait rules map to expected `kubectl` arguments.
- Failed apply stops before waits and kube-burner execution.
- Failed wait stops before kube-burner execution.
- Run metadata includes configured setup resources and waits.

## Acceptance Criteria

- A suite can declare a custom `RuntimeClass` manifest and wait for it to exist before kube-burner starts.
- A suite can declare a node-preparation `DaemonSet` and wait for rollout completion before kube-burner starts.
- `run-suite` keeps setup resources after success or failure.
- Re-running the same suite reapplies setup manifests without treating existing resources as errors.
- Invalid setup paths and invalid wait rules fail with actionable error messages.
- Existing suites without `setup` continue to run unchanged.
