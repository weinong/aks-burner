# aks-burner

This repository organizes kube-burner performance test suites and can provision AKS infrastructure for those suites with Bicep.

## Common Commands

```bash
make list-suites
TEST_SUITE=my-suite make add-suite
make add-suite-guided
TEST_SUITE=kata-perf make provision
TEST_SUITE=kata-perf TEST_MODE=smoke make run-suite
TEST_SUITE=kata-perf TEST_MODE=smoke KUBE_CONTEXT=<existing-context> make run-suite
TEST_SUITE=kata-perf make destroy
TEST_SUITE=kata-io make provision
TEST_SUITE=kata-io TEST_MODE=fio-fast make run-suite
TEST_SUITE=kata-io TEST_MODE=git-fast make run-suite
TEST_SUITE=kata-io make destroy
```

`TEST_MODE` defaults to `smoke` and `AZURE_LOCATION` defaults to `westus2`. When `RESOURCE_GROUP` is omitted, `perf-runner` reads the signed-in Azure user with `az account show --query user.name --output tsv`, normalizes the UPN alias, and uses `rg-aks-burner-<suite>-<alias>`. The default cluster name is derived from `<suite>-<alias>`. Set `RESOURCE_GROUP` or `CLUSTER_NAME` only to use explicit overrides; an explicit resource group retains the existing suite-only cluster-name derivation when `CLUSTER_NAME` is omitted.

`make list-suites` prints each suite once with its available modes, so use the suite name for `TEST_SUITE` and one of the listed modes for `TEST_MODE`.

`TEST_SUITE=my-suite make add-suite` creates a complete dummy suite under `suites/my-suite/` using defaults. `make add-suite-guided` prompts for the suite name, description, Kubernetes version, node settings, Prometheus, and smoke/full sizes.

## Suite Lifecycle

```bash
make list-suites
TEST_SUITE=my-suite make add-suite
make add-suite-guided
TEST_SUITE=kata-perf make provision
TEST_SUITE=kata-perf TEST_MODE=smoke make run-suite
TEST_SUITE=kata-perf make destroy
TEST_SUITE=kata-io make provision
TEST_SUITE=kata-io TEST_MODE=fio-fast make run-suite
TEST_SUITE=kata-io TEST_MODE=git-fast make run-suite
TEST_SUITE=kata-io make destroy
```

`provision` loads `suites/<suite>/requirements.yml`, validates it, and generates the ARM deployment parameters that configure the AKS cluster, node pools, and optional suite ACR; resource-group creation is a separate provisioning step. `run-suite` builds any suite-declared images with `az acr build`, publishes immutable run-tagged images to the suite ACR, installs Prometheus when requested, starts a local `kubectl port-forward`, renders kube-burner with the local Prometheus URL, and stores results under `results/`. `destroy` deletes the current user's suite resource group and waits for deletion to complete.

Node pools are declared once in `requirements.yml`; no checked-in Bicep parameter file is needed. Each selector names the pool that must satisfy it:

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
        count: 1
        vmSize: Standard_D16as_v5
        osType: Linux
        osSKU: AzureLinux
        workloadRuntime: KataMshvVmIsolation
        labels:
          perf.azure.com/node-role: workload
        taints: []
  nodeSelectors:
    - name: workload
      pool: userpool
      required: true
      minNodes: 1
      labels:
        perf.azure.com/node-role: workload
```

Preview the generated ARM parameter JSON without creating a resource group or running Azure commands:

```bash
go run ./cmd/perf-runner provision --suite kata-perf --resource-group dry-run-unused --location westus2 --dry-run
```

## Existing AKS Cluster

`run-suite` can target an existing AKS cluster without running `provision`. With an explicit resource group, the cluster name retains its suite-only derivation (for example, `kata-perf` becomes `akskataperf`); pass `--cluster-name` when the existing cluster uses another name. With the default resource group, the cluster name is derived from the suite and signed-in user alias.

```bash
go run ./cmd/perf-runner run-suite --suite kata-perf --mode smoke --resource-group <existing-resource-group> --cluster-name <existing-cluster>
# Or through Make:
TEST_SUITE=kata-perf RESOURCE_GROUP=<existing-resource-group> CLUSTER_NAME=<existing-cluster> make run-suite
TEST_SUITE=kata-perf TEST_MODE=smoke KUBE_CONTEXT=<existing-context> make run-suite
```

Without `KUBE_CONTEXT`, `run-suite` derives the cluster name from `TEST_SUITE`, applies an optional `CLUSTER_NAME` override, and refreshes credentials with `az aks get-credentials`. Metadata records `clusterName` and omits `kubeContext`.

With `KUBE_CONTEXT` set, `run-suite` skips credential refresh and targets that context for every `kubectl` and kube-burner operation. Suites without image builds do not need `RESOURCE_GROUP` and do not query Azure identity. Suites with image builds derive the per-user resource group when it is omitted so `run-suite` can validate the deployment cluster's `AcrPull` relationship and retrieve registry outputs. Explicit-context metadata records `kubeContext` and omits `clusterName`, even if `CLUSTER_NAME` is supplied for image-build deployment validation. A separate kubeconfig option is not supported.

Both modes load and validate `requirements.yml`, including node-pool and selector relationships. For a legacy run, validate the refreshed current context with `kubectl version -o json` and `kubectl get nodes -l <labels> -o name`. For an explicit target, validate the same cluster with `kubectl --context <existing-context> version -o json` and `kubectl --context <existing-context> get nodes -l <labels> -o name`. `kata-perf` requires Kubernetes `>= 1.36` and at least one node with labels `perf.azure.com/node-role=workload,kubernetes.azure.com/os-sku=AzureLinux`.

When Prometheus is `required` and `install: true`, `run-suite` installs Prometheus before running the workload.

`kata-io` provisions a Kata Pod Sandboxing-capable AKS workload pool, builds a benchmark image, installs Prometheus and kube-state-metrics, runs fio and Git clone workloads, and copies raw artifacts from the results PVC into the local run directory.

`destroy` without `--resource-group` derives and deletes only the signed-in user's default `rg-aks-burner-<suite>-<alias>` resource group. An explicit resource group skips identity lookup and requires `--allow-non-default-resource-group`, including for legacy shared resource groups such as `rg-aks-burner-<suite>`.

## Suite Images

Suites can declare images to build under `requires.images` in `suites/<suite>/requirements.yml`. For suites that declare builds, the AKS Bicep template generates a suite ACR name from the resource group and cluster name, and `run-suite` reads the deployed registry name and login server from the `aks-burner` deployment outputs. Suites without `requires.images`, such as `kata-perf`, use static images from `config/images.yml` and do not provision ACR. Each build context is suite-relative.

```yaml
requires:
  images:
    builds:
      - key: benchmark
        repository: kata-io/benchmark
        context: images/benchmark
        dockerfile: Dockerfile
        platform: linux/amd64
        timeoutSeconds: 1800
```

`run-suite` tags built images with an immutable tag derived from suite, mode, and run timestamp, then overlays those image refs onto `config/images.yml` before rendering mode `imageVars`. Build logs are written under the run directory as `logs/acr-build-<image-key>.log`, and final image refs are recorded in `metadata/run.yml`.

When a suite declares image builds, `run-suite` must target the cluster recorded in the managed `aks-burner` deployment outputs. The AKS Bicep template grants that cluster's kubelet identity `AcrPull` on the suite ACR; a different cluster does not receive that role. The user running `provision` for such a suite must have permission to create role assignments, such as Owner or User Access Administrator on the deployment scope.

## Suite Setup Resources

Suites can install persistent Kubernetes setup resources before kube-burner starts. Use this for suite-specific cluster preparation such as custom `RuntimeClass` objects or node-preparation `DaemonSet` resources.

Declare setup resources in `suites/<suite>/suite.yml`:

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

`run-suite` applies each manifest with `kubectl apply -f` and then runs the declared waits before installing Prometheus or rendering the benchmark workload. Supported wait kinds are:

- `exists`: runs `kubectl get <resource>` when no timeout is set, or `kubectl wait <resource> --for=create --timeout <timeout>` when a timeout is set.
- `rollout`: runs `kubectl rollout status <resource>`.
- `condition`: runs `kubectl wait <resource> --for=condition=<condition>`.

Setup resources are intentionally kept after the run finishes. Remove them manually with `kubectl delete` when they are no longer needed, or destroy the suite resource group when using an isolated provisioned cluster.
