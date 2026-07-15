# Kata I/O Shim Readiness Label Design

## Goal

Keep runner-owned patched kata-io workloads off replacement or unverified nodes without using a node-pool taint. Readiness is represented by the suite-owned label `perf.azure.com/kata-shim-revision=r1-d78b0c859c25f795`, tied to the expected full shim SHA `d78b0c859c25f795dee201f8ae1b28c987fd6b0537efd0430b5fd6ad47a93ec1`.

## Design

The patchpool retains only its static `perf.azure.com/node-role=patchpool` label and has no taints. Existing AKS pools whose desired configuration still contains `perf.azure.com/kata-shim-patch=pending:NoSchedule` require an AKS control-plane migration before the untainted DaemonSet can roll out: run `az aks nodepool update --resource-group <resource-group> --cluster-name <cluster-name> --name patchpool --node-taints ""`, or reprovision/recreate the pool. Removing the taint only through `kubectl` is not durable. Newly provisioned requirements already use empty taints.

The patch DaemonSet selects the static patchpool label and uses its existing least-privilege node `get`/`patch` identity and projected 600-second token. On each node, that node's init container removes that node's readiness label with a Kubernetes node API merge patch before target or file verification. It restores the exact revision with another merge patch only after that node's existing download and installed SHA, size, mode, UID, and GID checks all succeed. If the clear succeeds, a later failure leaves readiness absent on that node. If the clear API PATCH itself fails, `sh -e` aborts the DaemonSet pod and runner setup barrier, but an old label can remain for external concurrent consumers.

The baseline preload template selects the static workload pool. A dedicated patched preload template and both raw-block Job templates select the static patchpool label plus the exact readiness revision. Replacement and new nodes lack the suite-owned label and cannot host these patched suite pods until patched and verified.

On every run, the generic runner restarts the DaemonSet and waits for full `kubectl rollout status` through the resource's declared setup wait before it starts runner-owned workloads. This makes rollout completion a runner-owned per-run suite barrier, including after an in-place reimage. The supported guarantee is runner-owned suite safety: in-place reimage safety depends on the next runner setup barrier and is not a global immediate fail-closed guarantee for external workloads. It is not pool-wide revocation for independently submitted concurrent workloads: while the DaemonSet rolls, nodes whose new init container has not started can retain the old same-revision label, and nodes that complete verification regain that label before the global rollout completes.

The init container alone retains the host mount; the unprivileged sleeper has no host access. No containerd restart is introduced. Perf-runner, shared setup logic, requirements schemas, infrastructure Bicep, and infrastructure `nodeSelectors` remain generic and unchanged because requirements validation occurs before suite setup can create a dynamic label.

## Validation

- Contract tests cover the existing-pool AKS control-plane migration documentation, empty patchpool taints, removal of legacy taint/toleration logic, merge-patch ordering, exact readiness selectors, workload preload template choice, and existing security and patch-integrity guarantees.
- Focused and full `internal/examples` tests must pass.
- Client-side `kubectl create --dry-run=client --validate=false` must parse the setup manifest.
- A future authorized live validation should first confirm old AKS patchpools have empty desired taints, then observe each successfully cleared node's readiness absent during verification and restored after that node succeeds, replacement/new nodes absent until patched, and the runner waiting for full setup rollout before runner-owned suite workloads. It should verify that a clear API PATCH failure stops the setup barrier while allowing for an old label visible to external consumers, and should not expect immediate global in-place-reimage protection or pool-wide label absence during the rolling restart. No live-cluster action is part of this implementation.
