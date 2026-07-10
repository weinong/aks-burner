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
TEST_SUITE=kata-io TEST_MODE=smoke make run-suite
TEST_SUITE=kata-io TEST_MODE=full make run-suite
TEST_SUITE=kata-io make destroy
```

`TEST_MODE` defaults to `smoke`. `RESOURCE_GROUP` defaults to `rg-aks-burner-$(TEST_SUITE)`. `AZURE_LOCATION` defaults to `westus2`.

`make list-suites` prints each suite once with its available modes, so use the suite name for `TEST_SUITE` and one of the listed modes for `TEST_MODE`.

`TEST_SUITE=my-suite make add-suite` creates a complete dummy suite under `suites/my-suite/` using defaults. `make add-suite-guided` prompts for the suite name, description, cluster name, Kubernetes version, node settings, Prometheus, and smoke/full sizes.

## Suite Lifecycle

```bash
make list-suites
TEST_SUITE=my-suite make add-suite
make add-suite-guided
TEST_SUITE=kata-perf make provision
TEST_SUITE=kata-perf TEST_MODE=smoke make run-suite
TEST_SUITE=kata-perf make destroy
TEST_SUITE=kata-io make provision
TEST_SUITE=kata-io TEST_MODE=smoke make run-suite
TEST_SUITE=kata-io TEST_MODE=full make run-suite
TEST_SUITE=kata-io make destroy
```

`provision` creates the Azure resource group, AKS cluster, and suite ACR declared by the suite requirements. `run-suite` builds any suite-declared images with `az acr build`, publishes immutable run-tagged images to the suite ACR, installs Prometheus when requested, starts a local `kubectl port-forward`, renders kube-burner with the local Prometheus URL, and stores results under `results/`. `destroy` deletes the suite resource group and waits for deletion to complete.

## Existing AKS Cluster

`run-suite` can target an existing AKS cluster without running `provision`. The target cluster name is read from the suite Bicep parameter file, such as `suites/kata-perf/infra.bicepparam`.

```bash
TEST_SUITE=kata-perf TEST_MODE=smoke RESOURCE_GROUP=<existing-resource-group> make run-suite
TEST_SUITE=kata-perf TEST_MODE=smoke KUBE_CONTEXT=<existing-context> make run-suite
```

With `KUBE_CONTEXT` set, `run-suite` skips `az aks get-credentials` and targets that context for every `kubectl` and kube-burner operation. Suites without image builds may omit `RESOURCE_GROUP` in this mode. Suites with image builds still require `RESOURCE_GROUP` for ACR and deployment access. A separate kubeconfig option is not supported.

For `kata-perf`, the expected cluster name is `akskataperf` unless `suites/kata-perf/infra.bicepparam` is updated. Validate the existing cluster before running the suite: check Kubernetes version with `kubectl version -o json`, and check required node selectors with `kubectl get nodes -l <labels> -o name`. `kata-perf` requires Kubernetes `>= 1.36` and at least one node with labels `perf.azure.com/node-role=workload,kubernetes.azure.com/os-sku=AzureLinux`.

When Prometheus is `required` and `install: true`, `run-suite` installs Prometheus before running the workload.

`kata-io` provisions a Kata Pod Sandboxing-capable AKS workload pool, builds a benchmark image, installs Prometheus and kube-state-metrics, runs fio and Git clone workloads, and copies raw artifacts from the results PVC into the local run directory.

`destroy` only deletes the default resource group name `rg-aks-burner-<suite>`. To delete a deliberately overridden suite resource group, call `perf-runner destroy` directly with `--allow-non-default-resource-group`.

## Suite Images

Suites can declare images to build under `requires.images` in `suites/<suite>/requirements.yml`. For suites that declare builds, the AKS Bicep template generates a suite ACR name from the resource group and cluster name, and `run-suite` reads the deployed registry name and login server from the `aks-burner` deployment outputs. Suites without `requires.images`, such as `kata-perf`, use static images from `config/images.yml` and do not provision ACR. Each build context is suite-relative.

```yaml
requires:
  images:
    registry:
      nameParameter: containerRegistryName
    builds:
      - key: benchmark
        repository: kata-io/benchmark
        context: images/benchmark
        dockerfile: Dockerfile
        platform: linux/amd64
        timeoutSeconds: 1800
```

`run-suite` tags built images with an immutable tag derived from suite, mode, and run timestamp, then overlays those image refs onto `config/images.yml` before rendering mode `imageVars`. Build logs are written under the run directory as `logs/acr-build-<image-key>.log`, and final image refs are recorded in `metadata/run.yml`.

When a suite declares image builds, the AKS Bicep template grants the cluster kubelet identity `AcrPull` on the suite ACR. The user running `provision` for such a suite must have permission to create role assignments, such as Owner or User Access Administrator on the deployment scope.

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
