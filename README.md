# aks-burner

This repository organizes kube-burner performance test suites and can provision AKS infrastructure for those suites with Bicep.

## Common Commands

```bash
make list-suites
TEST_SUITE=my-suite make add-suite
make add-suite-guided
TEST_SUITE=kata-perf make provision
TEST_SUITE=kata-perf TEST_MODE=smoke make run-suite
TEST_SUITE=kata-perf make destroy
```

`TEST_MODE` defaults to `smoke`. `RESOURCE_GROUP` defaults to `rg-aks-burner-$(TEST_SUITE)`. `AZURE_LOCATION` defaults to `westus2`.

`TEST_SUITE=my-suite make add-suite` creates a complete dummy suite under `suites/my-suite/` using defaults. `make add-suite-guided` prompts for the suite name, description, cluster name, Kubernetes version, node settings, Prometheus, and smoke/full sizes.

## Suite Lifecycle

```bash
make list-suites
TEST_SUITE=my-suite make add-suite
make add-suite-guided
TEST_SUITE=kata-perf make provision
TEST_SUITE=kata-perf TEST_MODE=smoke make run-suite
TEST_SUITE=kata-perf make destroy
```

`provision` creates the Azure resource group, AKS cluster, and suite ACR declared by the suite requirements. `run-suite` builds any suite-declared images with `az acr build`, publishes immutable run-tagged images to the suite ACR, installs Prometheus when requested, starts a local `kubectl port-forward`, renders kube-burner with the local Prometheus URL, and stores results under `results/`. `destroy` deletes the suite resource group and waits for deletion to complete.

`destroy` only deletes the default resource group name `rg-aks-burner-<suite>`. To delete a deliberately overridden suite resource group, call `perf-runner destroy` directly with `--allow-non-default-resource-group`.

## Suite Images

Suites can declare images to build under `requires.images` in `suites/<suite>/requirements.yml`. By default, the AKS Bicep template generates a suite ACR name from the resource group and cluster name, and `run-suite` reads the deployed registry name and login server from the `aks-burner` deployment outputs. Suites can still set the named `.bicepparam` parameter explicitly when they need a fixed public-cloud registry name. Each build context is suite-relative.

```yaml
requires:
  images:
    registry:
      nameParameter: containerRegistryName
    builds:
      - key: kata-pause
        repository: kata-perf/pause
        context: images/pause
        dockerfile: Dockerfile
        platform: linux/amd64
        timeoutSeconds: 1800
```

`run-suite` tags built images with an immutable tag derived from suite, mode, and run timestamp, then overlays those image refs onto `config/images.yml` before rendering mode `imageVars`. Build logs are written under the run directory as `logs/acr-build-<image-key>.log`, and final image refs are recorded in `metadata/run.yml`.

The AKS Bicep template grants the cluster kubelet identity `AcrPull` on the suite ACR. The user running `provision` must have permission to create role assignments, such as Owner or User Access Administrator on the deployment scope.
