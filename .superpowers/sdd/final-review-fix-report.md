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

## Notes

- Preserved unrelated untracked file `kube-burner-test-suite-proposal.md`; it was not modified, staged, or removed.
