# Requirements-Driven Bicep Parameters Design

## Goal

Make `requirements.yml` the only checked-in source of suite infrastructure intent. The runner will translate typed AKS requirements into an ephemeral ARM JSON parameter file when provisioning instead of reading a hand-maintained `infra.bicepparam` file.

## Context

The current suite format splits related values across two files. `requirements.yml` describes the minimum Kubernetes version and required node labels, while `infra.bicepparam` independently specifies the cluster name, Kubernetes version, node count, VM size, OS SKU, workload runtime, and node labels. The duplication allows runtime requirements and provisioned infrastructure to drift.

Provisioning also derives some infrastructure choices outside both files. In particular, the runner passes `deployContainerRegistry` based on the presence of `requires.images`. `run-suite` parses `clusterName` and an optional custom registry name from the Bicep parameter file.

The design consolidates suite-owned infrastructure intent in `requirements.yml`, keeps environment identity derivable or overridable at runtime, and makes parameter generation deterministic and testable.

## Requirements Model

`requires.infrastructure.provider` remains the infrastructure implementation selector. The supported value `aks` maps to the repository-owned `infra/aks/main.bicep`; suites no longer provide a template path or parameter-file path.

The AKS infrastructure requirement contains a typed `nodePools` array. The initial surface supports current suite needs without mirroring the complete AKS API:

- `name`: AKS pool name.
- `mode`: `System` or `User`.
- `count`: fixed node count.
- `vmSize`: Azure VM SKU.
- `osType`: initially `Linux`.
- `osSKU`: supported Linux OS SKU.
- `workloadRuntime`: supported AKS workload runtime.
- `labels`: node labels, defaulting to an empty object.
- `taints`: node taints, defaulting to an empty array.

Example:

```yaml
requires:
  infrastructure:
    provider: aks
    nodePools:
      - name: systempool
        mode: System
        count: 1
        vmSize: Standard_D4s_v5
        osType: Linux
        osSKU: Ubuntu
        workloadRuntime: OCIContainer
        labels: {}
        taints: []
      - name: userpool
        mode: User
        count: 4
        vmSize: Standard_D8s_v5
        osType: Linux
        osSKU: AzureLinux
        workloadRuntime: KataMshvVmIsolation
        labels:
          perf.azure.com/node-role: workload
        taints: []
  kubernetes:
    minVersion: "1.36.1"
  nodeSelectors:
    - name: workload
      pool: userpool
      required: true
      minNodes: 1
      labels:
        perf.azure.com/node-role: workload
```

`requires.kubernetes.minVersion` remains the existing-cluster compatibility floor and is also used as the requested version when provisioning. This intentionally treats the suite's minimum supported version as the version for a newly provisioned cluster.

Each node selector references a provisioned pool by `pool`. The selector remains the workload's runtime capability contract; the node pool is the provisioning specification. Keeping the objects separate avoids putting VM sizing and runtime deployment choices into runtime validation while still allowing consistency checks between them.

The cluster name is not suite infrastructure intent. By default, the runner derives it as `aks` followed by the suite name with hyphens removed, matching the existing suite-generator convention. If that value exceeds the 54-character DNS-prefix limit, the runner keeps the first 45 characters and appends `-` plus the first eight lowercase hexadecimal characters of the SHA-256 hash of the untruncated value. Commands that connect to an existing cluster accept `--cluster-name` to override the derived value. Explicit overrides must contain 1-54 lowercase letters, digits, or hyphens and must start and end with a letter or digit; invalid overrides fail before any Azure command.

The CLI `--suite` value and its `suites/<suite>` directory are the authoritative suite identity. Loading rejects a `suite.yml` `name` or `requirements.yml` top-level `suite` value that differs from it, before deriving resource names or invoking external commands.

## Provider Boundary

Add a focused infrastructure requirements package with typed representations for provider configuration and AKS node pools. It owns three operations:

1. Load infrastructure requirements already validated structurally by JSON Schema.
2. Validate semantic and cross-reference rules.
3. Produce a deterministic ARM deployment parameter document.

The runner selects an adapter by `provider`. The `aks` adapter owns the mapping to `infra/aks/main.bicep` and the parameter names consumed by that template. Suite YAML therefore describes AKS concepts but does not depend on Bicep parameter names or repository paths.

No generic extra-parameters map is included. New AKS features should be added as typed fields when a suite needs them. This keeps schema validation meaningful and prevents an unvalidated escape hatch from becoming a second source of truth.

## Generated Parameters

The AKS adapter produces an ARM JSON parameter document containing:

- `clusterName`, derived from the suite name or supplied by `--cluster-name`.
- `kubernetesVersion`, copied from `requires.kubernetes.minVersion`.
- `nodePools`, mapped from the typed pool array.
- `deployContainerRegistry`, derived from whether `requires.images` is present.

The JSON uses the standard ARM deployment parameters envelope and stable field ordering through typed Go structures. It is an execution artifact, not a checked-in configuration file.

ARM JSON is preferred over generated `.bicepparam` text because Azure CLI can pair it explicitly with the provider-selected template, nested arrays and objects serialize without custom Bicep escaping, and the generated artifact does not require a path-sensitive `using` statement.

## Bicep Template

`infra/aks/main.bicep` replaces the individual system and user pool parameters with a typed `nodePools` array. The managed cluster's `agentPoolProfiles` is projected from this array so suites can request arbitrary system and user pools within the supported field surface.

The template retains `clusterName`, `location`, `kubernetesVersion`, and `deployContainerRegistry`. ACR creation and its role assignment remain conditional. The ACR name is always derived in Bicep, and registry outputs remain the runner's source for the deployed name and login server.

The generated parameter document contains all suite-varying values. Bicep defaults remain appropriate only for direct template development and values that are not suite requirements.

## Command Behavior

Normal `provision` flow is:

1. Validate and load `requirements.yml`.
2. Select the provider adapter.
3. Derive the cluster name or apply `--cluster-name`.
4. Run semantic infrastructure validation.
5. Marshal the ARM JSON parameter document.
6. Create a mode `0600` temporary `.parameters.json` file.
7. Create the Azure resource group.
8. Deploy with `az deployment group create --resource-group <resource-group> --name aks-burner --template-file <template> --parameters @<temporary-file>`.
9. Fetch cluster credentials.
10. Remove the temporary file on both success and failure.

`provision --dry-run` performs loading, validation, derivation, and generation, then prints canonical indented JSON to standard output. It creates no Azure resources and does not fetch credentials. The printed document is exactly the payload normal provisioning writes to its temporary file.

`run-suite` derives the same default cluster name and accepts `--cluster-name` for an existing cluster with a different name. It no longer reads a parameter file. If image builds are declared, it obtains the ACR name and login server from the `aks-burner` deployment outputs before making Kubernetes changes. An existing cluster without those outputs can run suites that use only static images, but an image-building suite fails with an error explaining that the cluster must have been provisioned by this runner and its kubelet identity must retain AcrPull access to the deployment's registry.

`add-suite` writes node-pool infrastructure into `requirements.yml` and no longer creates `infra.bicepparam`. Its cluster-name input is removed because cluster identity is derived at runtime. Existing node count and VM-size inputs populate the generated user pool.

## Validation And Errors

JSON Schema validates structural constraints:

- `nodePools` is present and nonempty.
- Required pool fields are present.
- Pool names match `^[a-z][a-z0-9]{0,11}$`.
- Mode is `System` or `User`, OS type is `Linux`, OS SKU is `Ubuntu` or `AzureLinux`, and workload runtime is `OCIContainer`, `KataMshvVmIsolation`, or `KataVmIsolation`.
- Counts are integers from 1 through 1000 and VM sizes are nonempty.
- Label keys and values are nonempty strings. Taints match `<key>[=<value>]:<effect>`, where `effect` is `NoSchedule`, `PreferNoSchedule`, or `NoExecute`.
- Each node selector contains a nonempty `pool` reference.

Go semantic validation enforces rules that depend on multiple objects:

- Pool names are unique.
- At least one pool uses `System` mode.
- Every node selector references an existing pool.
- A referenced pool's count is at least the selector's `minNodes`.
- Every custom selector label exists on the referenced pool with the same value.
- Provider-managed selector label `kubernetes.azure.com/os-sku` matches the pool's typed `osSKU` value rather than being copied into `labels`.

Validation runs before resource-group creation. Errors identify the suite and exact pool or selector, for example: `suite kata-io selector workload requires label perf.azure.com/node-role=workload on pool userpool`.

## Registry Simplification

Presence of `requires.images` continues to mean the deployment needs an ACR. The `registry` object is removed entirely because requirements no longer address Bicep parameters and custom registry names are not needed by current suites. The post-migration contract is:

```yaml
images:
  builds:
    - key: benchmark
      repository: kata-io/benchmark
      context: images/benchmark
      dockerfile: Dockerfile
      platform: linux/amd64
      timeoutSeconds: 3600
```

The Bicep template derives the registry name, and `run-suite` reads `containerRegistryName` and `containerRegistryLoginServer` from deployment outputs. Suites without `requires.images` generate `deployContainerRegistry: false` and continue using static image configuration.

## Migration

Both existing suites migrate atomically:

- `kata-perf` declares one system pool and one `Standard_D16as_v5` Azure Linux Kata workload pool with count 1.
- `kata-io` declares one system pool and one `Standard_D8s_v5` Azure Linux Kata workload pool with count 4. Its requested provisioning version intentionally changes from the deleted parameter file's `1.36.1` to its declared minimum `1.36`.
- Existing node selectors reference their workload pool by name.
- Both `infra.bicepparam` files are deleted.
- The complete `requires.images.registry` object is deleted from the schema and `kata-io` requirements.
- README examples and suite-generator documentation describe requirements-driven provisioning.

No compatibility reader for checked-in `infra.bicepparam` files is included. The files are repository-internal, all known suites migrate in the same change, and retaining fallback behavior would preserve the ambiguity this design removes.

## Testing

Tests cover:

- Schema acceptance of both migrated suites and rejection of malformed pool fields.
- Typed loading and deterministic ARM JSON generation.
- Default cluster-name derivation and explicit override behavior.
- Deterministic hashing for long derived names plus boundary and invalid override cases.
- Rejection of mismatched CLI, directory, `suite.yml`, and `requirements.yml` suite identities.
- Duplicate pools, missing system pools, missing selector references, insufficient counts, and mismatched labels.
- Provider-managed `kubernetes.azure.com/os-sku` selector validation against the typed pool OS SKU.
- `provision --dry-run` output and absence of Azure side effects.
- Temporary parameter file permissions and cleanup after resource-group, deployment, credential, and context-cancellation failures.
- Azure CLI command construction with the selected template and generated JSON file.
- Bicep compilation and projection of arbitrary multiple node pools.
- Existing-cluster `run-suite --cluster-name` behavior.
- Early rejection of image-building suites when the `aks-burner` deployment outputs are unavailable.
- Suite generation without `infra.bicepparam`.
- ACR output lookup when image builds are present and no ACR when they are absent.

Final verification runs `git diff --check`, `go test ./...`, and `az bicep build --file infra/aks/main.bicep --stdout`.

## Out Of Scope

- A generic Bicep parameter escape hatch.
- Full mirroring of the AKS `agentPoolProfiles` API.
- Autoscaling, disk, subnet, availability-zone, or spot-pool settings until a suite requires them.
- Persistent generated parameter files.
- Environment mapping files for resource groups and cluster names.
- Automatic migration of external suites not contained in this repository.
