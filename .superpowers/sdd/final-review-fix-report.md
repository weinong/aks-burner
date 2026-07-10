# Final Review Fix Report

## Finding 1: `run-suite` could run against the wrong Kubernetes cluster

Fix: `run-suite` now reads the suite AKS cluster name from the suite Bicep parameter file and runs `az aks get-credentials --resource-group <rg> --name <cluster> --overwrite-existing` through `infra.GetCredentials` before Kubernetes operations. The command construction is covered by `TestGetCredentialsCommand`.

## Finding 2: `destroy` did not validate suite ownership or intent

Fix: `destroy` now loads the suite first, rejects invalid suite names through `suite.Load`, and refuses to delete resource groups that do not match `rg-aks-burner-<suite>` unless the documented `--allow-non-default-resource-group` flag is passed. The README documents the override.

## Finding 3: Requirements were declared but not enforced

Fix: Added minimal Kubernetes preflight validation in `run.ValidateRequirements`. It checks `kubectl version -o json` against `minVersion` and verifies each required node selector has at least `minNodes` matching nodes using `kubectl get nodes -l <labels> -o name` before kube-burner runs.

## Finding 4: User/config-controlled paths were not confined

Fix: Added strict suite and mode name validation with `^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`. Suite-controlled file paths are resolved inside the suite directory, and repo-level template paths are confined to the repository.

## Finding 5: Prometheus port-forward startup was race-prone

Fix: Added `prometheus.WaitRollout`, which runs `kubectl rollout status deployment/prometheus -n <namespace> --timeout=2m` before starting the service port-forward.

## Finding 6: Result metadata capture was missing

Fix: Added `run.WriteMetadata` and call it from `run-suite` to write `metadata/run.yml` under the run directory with suite, mode, timestamp, resolved image catalog, resource group, and cluster name. The metadata helper only writes those explicit fields and does not capture kubeconfig, bearer tokens, Azure tokens, Prometheus tokens, authorization headers, or other auth material.

## Verification

- `go test ./internal/infra ./internal/prometheus ./internal/suite ./internal/run ./cmd/perf-runner`: PASS
- `go test ./...`: PASS
- `make build`: PASS
- `make list-suites`: PASS, output included `kata-disk-perf`

## Requirements-Driven Bicep Parameters Final Review Fixes

Date: 2026-07-10

### Finding 1: AKS API contract did not support both declared Kata runtimes

Verification: The official version-specific `Microsoft.ContainerService/managedClusters@2025-09-02-preview` Bicep reference lists `KataMshvVmIsolation`, `KataVmIsolation`, `OCIContainer`, and `WasmWasi` for `workloadRuntime`: https://learn.microsoft.com/azure/templates/microsoft.containerservice/2025-09-02-preview/managedclusters#property-values

Fix: Updated `infra/aks/main.bicep` from `2025-05-01` to `2025-09-02-preview` and expanded the source-contract test to require the API version and both declared Kata runtime values.

RED:

```text
$ go test -count=1 ./cmd/perf-runner -run '^TestInfraBicepSupportsKataWorkloadRuntimeParameters$'
--- FAIL: TestInfraBicepSupportsKataWorkloadRuntimeParameters (0.00s)
    main_test.go:38: main.bicep missing "Microsoft.ContainerService/managedClusters@2025-09-02-preview"
FAIL
FAIL github.com/Azure/aks-burner/cmd/perf-runner 0.016s
FAIL
```

GREEN:

```text
$ go test -count=1 ./cmd/perf-runner -run '^TestInfraBicepSupportsKataWorkloadRuntimeParameters$'
ok github.com/Azure/aks-burner/cmd/perf-runner 0.025s

$ az bicep build --file infra/aks/main.bicep --stdout
PASS (exit 0); compiled ARM contained:
"apiVersion": "2025-09-02-preview"
"allowedValues": ["KataMshvVmIsolation", "KataVmIsolation", "OCIContainer"]
```

### Finding 2: Image builds could use registry outputs for a different cluster

Fix: `prepareRunSuiteCluster` now reads deployment output `clusterName` first, compares it with the requested or derived cluster, and returns managed-deployment/AcrPull guidance before registry output reads or credential access on mismatch. README now documents that only the deployment cluster's kubelet identity receives `AcrPull`.

RED:

```text
$ go test -count=1 ./cmd/perf-runner -run '^TestRunSuite(RegistryOutputsPrecedeCredentials|RejectsImageBuildClusterMismatchBeforeRegistryAndCredentials)$'
--- FAIL: TestRunSuiteRegistryOutputsPrecedeCredentials (0.00s)
    main_test.go:539: order = "output:containerRegistryName,output:containerRegistryLoginServer,credentials", want "output:clusterName,output:containerRegistryName,output:containerRegistryLoginServer,credentials"
--- FAIL: TestRunSuiteRejectsImageBuildClusterMismatchBeforeRegistryAndCredentials (0.00s)
    main_test.go:551: credentials called after cluster mismatch
FAIL
FAIL github.com/Azure/aks-burner/cmd/perf-runner 0.017s
FAIL
```

GREEN:

```text
$ go test -count=1 ./cmd/perf-runner -run '^TestRunSuite(RegistryOutputsPrecedeCredentials|RejectsImageBuildClusterMismatchBeforeRegistryAndCredentials|RegistryOutputFailureExplainsManagedDeployment)$'
ok github.com/Azure/aks-burner/cmd/perf-runner 0.019s
```

### Finding 3: Accepted v-prefixed Kubernetes versions reached ARM unchanged

Fix: `ParametersJSON` removes one accepted leading `v` at the ARM serialization boundary. This covers direct requirements and suites generated through `add-suite` while preserving the accepted input form in suite requirements.

RED:

```text
$ go test -count=1 ./internal/infra -run '^TestParametersJSONNormalizesVPrefixedKubernetesVersion$'
--- FAIL: TestParametersJSONNormalizesVPrefixedKubernetesVersion (0.00s)
    parameters_test.go:161: ParametersJSON() kubernetesVersion was not normalized:
        "kubernetesVersion": {
          "value": "v1.36"
        }
FAIL
FAIL github.com/Azure/aks-burner/internal/infra 0.015s
FAIL

$ go test -count=1 ./cmd/perf-runner -run '^TestAddSuiteVPrefixedKubernetesVersionProducesNormalizedARMParameters$'
--- FAIL: TestAddSuiteVPrefixedKubernetesVersionProducesNormalizedARMParameters (0.02s)
    main_test.go:342: dry-run kubernetesVersion was not normalized:
        "kubernetesVersion": {
          "value": "v1.36"
        }
FAIL
FAIL github.com/Azure/aks-burner/cmd/perf-runner 0.037s
FAIL
```

GREEN:

```text
$ go test -count=1 ./internal/infra -run '^TestParametersJSON(NormalizesVPrefixedKubernetesVersion)?$'
ok github.com/Azure/aks-burner/internal/infra 0.019s

$ go test -count=1 ./cmd/perf-runner -run '^TestAddSuiteVPrefixedKubernetesVersionProducesNormalizedARMParameters$'
ok github.com/Azure/aks-burner/cmd/perf-runner 0.035s
```

### Finding 4: Node pool labels could set the AKS-owned OS SKU label

Fix: Semantic node-pool validation rejects `kubernetes.azure.com/os-sku` in `NodePool.Labels` even when its value equals `OSSKU`, and directs authors to `osSKU`.

RED:

```text
$ go test -count=1 ./internal/infra -run '^TestValidateNodePoolsRejectsInvalidRelationships/reserved_OS_label_matches_OS_SKU$'
--- FAIL: TestValidateNodePoolsRejectsInvalidRelationships (0.00s)
    --- FAIL: TestValidateNodePoolsRejectsInvalidRelationships/reserved_OS_label_matches_OS_SKU (0.00s)
        parameters_test.go:99: ValidateNodePools() error = <nil>, want "suite kata-io pool \"userpool\" labels must not set reserved label \"kubernetes.azure.com/os-sku\"; use osSKU instead"
FAIL
FAIL github.com/Azure/aks-burner/internal/infra 0.021s
FAIL
```

GREEN:

```text
$ go test -count=1 ./internal/infra -run '^TestValidateNodePools'
ok github.com/Azure/aks-burner/internal/infra 0.023s
```

### Final Verification

```text
$ go test -count=1 ./cmd/perf-runner ./internal/infra
ok github.com/Azure/aks-burner/cmd/perf-runner 0.178s
ok github.com/Azure/aks-burner/internal/infra 0.024s

$ go test -count=1 ./...
ok github.com/Azure/aks-burner/cmd/perf-runner 0.229s
ok github.com/Azure/aks-burner/internal/acr 0.065s
ok github.com/Azure/aks-burner/internal/artifacts 0.034s
ok github.com/Azure/aks-burner/internal/config 0.028s
ok github.com/Azure/aks-burner/internal/examples 0.201s
ok github.com/Azure/aks-burner/internal/infra 0.031s
ok github.com/Azure/aks-burner/internal/kubestatemetrics 0.006s
ok github.com/Azure/aks-burner/internal/prometheus 0.023s
ok github.com/Azure/aks-burner/internal/repo 0.012s
ok github.com/Azure/aks-burner/internal/requirements 0.040s
ok github.com/Azure/aks-burner/internal/run 0.104s
ok github.com/Azure/aks-burner/internal/suite 0.065s

$ az bicep build --file infra/aks/main.bicep --stdout
PASS (exit 0); compiled resource uses Microsoft.ContainerService/managedClusters apiVersion 2025-09-02-preview and includes both Kata workload runtime values.

$ go run ./cmd/perf-runner provision --suite kata-io --resource-group rg-aks-burner-kata-io --location westus2 --dry-run
PASS (exit 0); generated parameters contained clusterName=akskataio, kubernetesVersion=1.36, two node pools, KataMshvVmIsolation, and deployContainerRegistry=true.

$ git diff --check
PASS (exit 0, no output)
```

Final code review: configured `code-reviewer` reviewed the complete uncommitted diff against all four requirements and reported no findings.

## Notes

- Preserved unrelated untracked file `kube-burner-test-suite-proposal.md`; it was not modified, staged, or removed.

## Final Re-Review Fixes

### Finding 1: `run-suite` waited for rollout when Prometheus was not installed by the runner

Fix: `run-suite` now waits for Prometheus deployment rollout only when Prometheus is both required and installed by this runner (`required && install`). Suites with `install: false` still port-forward to the configured service and rely on `prometheus.WaitReady`.

Tests:

- `go test ./cmd/perf-runner`: PASS
- `go test ./cmd/perf-runner ./internal/run`: PASS
- `go test ./...`: PASS
- `make build`: PASS
- `make list-suites`: PASS, output included `kata-disk-perf`

### Finding 2: Run directory names could collide within the same second

Fix: `internal/run.CreateRunDir` now creates run directories with RFC3339Nano timestamps and atomically creates the run root before child directories, making names collision-resistant while keeping deterministic naming covered by `runDirName` tests.

Tests:

- `go test ./internal/run`: PASS
- `go test ./cmd/perf-runner ./internal/run`: PASS
- `go test ./...`: PASS
- `make build`: PASS
- `make list-suites`: PASS, output included `kata-disk-perf`
