# aks-burner

This repository organizes kube-burner performance test suites and can provision AKS infrastructure for those suites with Bicep.

## Common Commands

```bash
make list-suites
TEST_SUITE=kata-perf make provision
TEST_SUITE=kata-perf TEST_MODE=smoke make run-suite
TEST_SUITE=kata-perf make destroy
```

`TEST_MODE` defaults to `smoke`. `RESOURCE_GROUP` defaults to `rg-aks-burner-$(TEST_SUITE)`. `AZURE_LOCATION` defaults to `westus2`.

## Suite Lifecycle

```bash
make list-suites
TEST_SUITE=kata-perf make provision
TEST_SUITE=kata-perf TEST_MODE=smoke make run-suite
TEST_SUITE=kata-perf make destroy
```

`provision` creates the Azure resource group and AKS cluster declared by the suite requirements. `run-suite` installs Prometheus when requested, starts a local `kubectl port-forward`, renders kube-burner with the local Prometheus URL, and stores results under `results/`. `destroy` deletes the suite resource group and waits for deletion to complete.

`destroy` only deletes the default resource group name `rg-aks-burner-<suite>`. To delete a deliberately overridden suite resource group, call `perf-runner destroy` directly with `--allow-non-default-resource-group`.
