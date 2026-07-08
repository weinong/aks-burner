# aks-burner

This repository organizes kube-burner performance test suites and can provision AKS infrastructure for those suites with Bicep.

## Common Commands

```bash
make list-suites
TEST_SUITE=kata-disk-perf make provision
TEST_SUITE=kata-disk-perf TEST_MODE=smoke make run-suite
TEST_SUITE=kata-disk-perf make destroy
```

`TEST_MODE` defaults to `smoke`. `RESOURCE_GROUP` defaults to `rg-aks-burner-$(TEST_SUITE)`. `AZURE_LOCATION` defaults to `westus2`.
