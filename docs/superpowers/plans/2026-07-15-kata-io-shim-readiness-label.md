# Kata I/O Shim Readiness Label Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans and superpowers:test-driven-development. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Gate runner-owned patched kata-io suite pods on a positive, revision-specific shim readiness label.

**Architecture:** On each node, the suite-local patch DaemonSet init container clears that node's readiness before verification and restores it only after that node completes shim verification. The generic runner uses the resource's declared setup wait to require full `kubectl rollout status` before starting suite workloads. Dedicated patched workload templates require the static patchpool label and exact readiness revision; generic runner, shared setup, schemas, infrastructure Bicep, and infrastructure node selectors remain unchanged.

**Tech Stack:** Go contract tests, Kubernetes YAML, POSIX shell, kubectl

## Global Constraints

- Use `perf.azure.com/kata-shim-revision=r1-d78b0c859c25f795`, tied to full SHA `d78b0c859c25f795dee201f8ae1b28c987fd6b0537efd0430b5fd6ad47a93ec1`.
- Keep patchpool static label `perf.azure.com/node-role=patchpool` and `taints: []`.
- Preserve least-privilege node `get`/`patch` RBAC, projected 600-second token, init-only host mount, unprivileged sleeper, and no containerd restart.
- Do not change perf-runner, shared setup logic, schemas, infrastructure Bicep, or infrastructure `nodeSelectors`.
- Existing patchpools with the old desired pending taint require an AKS control-plane migration or pool reprovision/recreation; kubectl-only removal is not durable.
- Do not perform live-cluster validation without separate authorization.

---

### Task 1: RED Contracts

**Files:**
- Modify: `internal/examples/examples_test.go`

- [ ] Assert empty patchpool taints and no patch DaemonSet toleration or legacy taint-removal logic.
- [ ] Assert readiness removal uses `application/merge-patch+json` with `null` before target/file verification and readiness set uses the exact revision after full installed SHA/mode/UID/GID verification.
- [ ] Assert baseline and patched preload templates are distinct, all four workloads use both correctly without `nodeRole`, and patched preload/raw-block templates contain both exact selectors.
- [ ] Add a README contract requiring the existing-pool `az aks nodepool update` migration command and essential flags, and rejecting kubectl-only removal as sufficient.
- [ ] Run the focused command and confirm failures describe the old taint, toleration, dynamic preload, and missing readiness selector behavior.

### Task 2: GREEN Suite Manifests

**Files:**
- Modify: `suites/kata-io/requirements.yml`
- Modify: `suites/kata-io/setup/patch-kata-shim.yml`
- Modify: `suites/kata-io/templates/preload-pod.yml`
- Create: `suites/kata-io/templates/preload-patch-pod.yml`
- Modify: `suites/kata-io/templates/fio-block-kata-job.yml`
- Modify: `suites/kata-io/templates/git-block-kata-job.yml`
- Modify: `suites/kata-io/workload-fio-fast.yml`
- Modify: `suites/kata-io/workload-git-fast.yml`
- Modify: `suites/kata-io/workload-fio.yml`
- Modify: `suites/kata-io/workload-git.yml`

- [ ] Remove the patchpool taint and DaemonSet toleration.
- [ ] Clear readiness first and set it only after all existing patch verification succeeds, using node API merge patches.
- [ ] Make baseline placement static and add exact readiness placement to dedicated patched preload and raw-block templates.
- [ ] Update all four preload jobs and remove obsolete `nodeRole` inputs.
- [ ] Rerun the focused command and confirm PASS.

### Task 3: Documentation and Verification

**Files:**
- Modify: `README.md`
- Create: `docs/superpowers/specs/2026-07-15-kata-io-shim-readiness-label-design.md`
- Create: `docs/superpowers/plans/2026-07-15-kata-io-shim-readiness-label.md`

- [ ] Document the existing-cluster AKS control-plane migration and validate its command/flags, including that kubectl-only removal is not durable and reprovision/recreation is supported.
- [ ] Document successful-clear failure semantics and clear-PATCH failure semantics: a later failure leaves readiness absent, while a clear API failure aborts the DaemonSet and runner barrier but can leave an old label for external concurrent consumers.
- [ ] Document replacement/new-node readiness absence and the in-place reimage scope: every run restarts and verifies the DaemonSet before runner-owned workloads, but this runner-owned suite guarantee is not immediate global fail-closed behavior for in-place reimages or external workloads.
- [ ] Preserve the rolling-restart concurrency limitation: not-yet-updated nodes can retain the old same-revision label and verified nodes regain it before global rollout completion, so independently submitted concurrent workloads are not pool-wide revoked.
- [ ] Run `go test ./internal/examples -count=1` and confirm PASS.
- [ ] Run `kubectl create --dry-run=client --validate=false -f suites/kata-io/setup/patch-kata-shim.yml -o yaml` and confirm successful client-side parsing only.
- [ ] Review the diff and confirm protected generic files are unchanged and no legacy taint/toleration logic remains.
- [ ] For a separately authorized live test, validate empty desired taints on migrated existing pools, per-node label absence after a successful clear and during later init/failure, restoration after verification, absence on replacement/new nodes until patched, and full setup rollout before runner-started workloads. Validate that clear-PATCH failure stops the setup barrier but may leave an old label for external consumers. Do not expect immediate global protection for in-place reimages or pool-wide label absence during the rolling restart, and do not run this step in the current task.
