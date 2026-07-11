# Optional Kubernetes Context Design

## Goal

Allow `run-suite` to target an existing Kubernetes context explicitly without
changing the current context, while preserving the existing provisioned-cluster
workflow when no context is supplied.

This ports only the reusable context-selection behavior from the
`experiment/kata-preview-latency` branch. It does not port kubeconfig selection,
isolated provisioning, ownership-bound cleanup, image overrides, experiment
metadata, or latency-reporting features.

## Command-Line Behavior

`perf-runner run-suite` accepts both `--cluster-name NAME` and the optional
`--kube-context CONTEXT` flag. `--cluster-name` overrides the AKS name otherwise
derived from the suite; `--kube-context` selects the Kubernetes target. The
Makefile exposes these flags through `CLUSTER_NAME` and `KUBE_CONTEXT`. The
existing `RESOURCE_GROUP` default remains unchanged for all targets. Within the
`run-suite` recipe only, the default is not forwarded when `KUBE_CONTEXT` is
set; an explicit-context invocation forwards a resource group only when the
caller explicitly supplies one. `provision` and `destroy` continue receiving
the existing default even if `KUBE_CONTEXT` happens to be present in the
environment.

When `--kube-context` is set:

- `run-suite` skips `az aks get-credentials` and uses the named existing
  context directly.
- Every Kubernetes child process explicitly selects that context.
- `--resource-group` is required only when the suite declares image builds,
  because those builds depend on Azure deployment and registry data.
- The selected context is recorded as `kubeContext` in `metadata/run.yml`.
- `clusterName` is omitted from metadata because the context is the Kubernetes
  target identity, even though a derived or overridden cluster name may still
  be used to validate image-build deployment ownership.

When `--kube-context` is omitted:

- `--resource-group` remains required.
- The AKS cluster name is derived from the suite or overridden by
  `--cluster-name`.
- Image-build suites read the managed deployment's cluster and registry outputs
  and verify that the requested cluster owns the registry's `AcrPull` setup.
- `run-suite` refreshes credentials with `az aks get-credentials`.
- Kubernetes child processes use the resulting current context implicitly.
- Metadata records the resolved `clusterName`.
- `kubeContext` is omitted from run metadata.

The port does not add `--kubeconfig`. All modes use the normal kubeconfig
resolution used by kubectl and kube-burner.

Before mode-specific Azure work, every run loads the typed requirements with
`requirements.Load`, rejects unsupported infrastructure providers, derives or
overrides the cluster name, and validates `infrastructure.nodePools` against
the declared node-selector requirements. Kubernetes version and selector
checks then run against the selected target.

`provision` remains separate from context selection: it loads the same typed
requirements, derives or overrides the cluster name, validates node pools and
selectors, and generates ARM deployment parameters from those values.
`destroy` retains its existing resource-group safety behavior. Neither command
accepts or acts on `--kube-context`.

## Target Abstraction

Add a small context-only Kubernetes target value shared by packages that launch
Kubernetes commands. The target stores an optional context name and provides
command construction for both clients:

- the kubectl command builder returns a complete command beginning with
  `kubectl`, followed by optional `--context <name>`, then the operation
  arguments;
- kubectl execution helpers consume that complete command rather than adding a
  second executable;
- the kube-burner argument builder returns arguments only, appending
  `--kube-context <name>` when the context is non-empty because executable
  selection remains the responsibility of the existing run package;
- an empty context produces the existing command arguments unchanged.

The empty context is valid because it represents the supported legacy path.
The abstraction centralizes the two clients' different flag spellings and
prevents individual packages from accidentally omitting explicit targeting.

## Propagation

The target is created once by `run-suite` and passed through every operation
that communicates with Kubernetes:

- Kubernetes version and node-selector requirement validation;
- suite setup resource apply and wait commands;
- kube-state-metrics installation and rollout wait;
- Prometheus installation, rollout wait, and port-forward;
- kube-burner execution;
- artifact job completion waits;
- artifact helper pod apply, wait, copy, and delete commands.

Existing dependency-injection helpers remain where they make focused unit tests
possible. Production entry points become target-aware so no explicit-context
run can partially fall back to the process-wide current context.

## Resource Group Handling

CLI validation must account for suite requirements before deciding whether a
resource group is required:

| Mode | Suite image builds | Resource group |
| --- | --- | --- |
| Legacy, no explicit context | Any | Required |
| Explicit context | None | Optional |
| Explicit context | One or more | Required |

For an explicit-context suite without image builds, the typed requirements and
node-pool/selector relationships are still validated. The runner performs no
Azure calls: it neither reads deployment outputs nor refreshes credentials.
An omitted resource group is serialized as an empty metadata value under the
existing `resourceGroup` field.

For an explicit-context suite with image builds, the runner requires a resource
group, derives or overrides the requested cluster name, and reads the managed
deployment's `clusterName`, `containerRegistryName`, and
`containerRegistryLoginServer` outputs. It rejects a cluster-name mismatch
before reading registry outputs or building images because the deployment owns
the kubelet identity's `AcrPull` relationship. It does not refresh credentials;
the explicit context remains the Kubernetes target.

Legacy mode always requires a resource group. It derives or overrides the
cluster name, performs the same deployment-output and ownership checks when
image builds are declared, and then refreshes AKS credentials for that cluster.

## Metadata

Add `KubeContext` to run metadata with YAML key `kubeContext` and `omitempty`.
Explicit runs therefore preserve the target identity without storing
credentials. Legacy run metadata remains byte-for-byte compatible with respect
to this new field because it is omitted when empty.

Change `clusterName` to use `omitempty`. Legacy runs continue to populate and
serialize it. Explicit runs leave it empty because the named context is the
Kubernetes target identity, including when a resolved cluster name is used for
image-build deployment ownership checks.

## Error Handling

The runner fails before creating a run directory or launching external work
when an invalid CLI combination is detected. In particular, an explicit-context
run for a suite with image builds fails with a clear resource-group requirement
if `--resource-group` is absent.

Errors from target-aware command construction and child processes continue to
flow through the existing package-specific context, including setup resource,
observability, benchmark, and artifact-copy errors.

## Testing

Implementation follows test-driven development. Tests cover:

- optional target argument construction for kubectl and kube-burner;
- unchanged argument lists for an empty context;
- `--context` propagation to requirement validation, setup, observability,
  port-forwarding, artifact waits, and artifact copying;
- `--kube-context` propagation to kube-burner;
- credential refresh in legacy mode and its omission in explicit mode;
- typed requirements loading, provider validation, and node-pool/selector
  relationship validation in explicit and legacy modes;
- absence of all Azure calls in explicit no-build mode;
- derived and overridden cluster names for credential refresh and image-build
  deployment validation;
- generated deployment output reads, cluster ownership mismatch rejection, and
  registry preparation for image builds;
- resource-group requirements for all three combinations in the table above;
- no run-directory creation or external command execution when a resource-group
  combination is invalid;
- Makefile forwarding of `CLUSTER_NAME` and `KUBE_CONTEXT`, resource-group
  defaults for legacy and explicit `run-suite` invocations, and unchanged
  `provision` and `destroy` forwarding when `KUBE_CONTEXT` is set;
- `kubeContext` presence for explicit metadata and omission for legacy
  metadata;
- `clusterName` presence for legacy metadata and omission for explicit
  metadata;
- explicit context on artifact helper apply, wait, copy, and failure-path delete
  operations;
- existing error precedence when benchmark and artifact operations fail.

Repository verification runs:

```bash
go test ./...
go vet ./...
go build ./cmd/perf-runner
```

## Compatibility And Scope

The change is backward compatible for current Makefile and CLI callers that do
not set `KUBE_CONTEXT` or `--kube-context`. Existing runs continue to refresh
credentials and use the current context.

This design intentionally excludes:

- `--kubeconfig` or `KUBECONFIG_FILE` support;
- context-driven changes to `provision` or `destroy`;
- isolated credential files or named-context creation;
- experiment IDs, variants, repetitions, or run IDs;
- image overrides and ACR build skipping;
- experiment ownership or safety policy;
- latency capture and reporting.
