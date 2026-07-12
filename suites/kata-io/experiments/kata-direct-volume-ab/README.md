# Kata direct-volume A/B experiment

## Status and objective

This is a fail-closed harness for comparing two Kata paths on the same dedicated AKS node and controlled 4 GiB loop-backed ext4 device:

- Path A: a static local PV/PVC with `volumeMode: Block`, `ReadWriteOncePod`, and guest `volumeDevices` at `/dev/testdisk`.
- Path B: Kata direct-volume metadata registered for `/run/kata-direct-volume-ab/<run-id>/workspace` and mounted in the container at `/workspace`.

No result is claimed here. Results remain pending until an approved run produces evidence. The harness emits a result matrix without interpreting failures as product findings.

## Run matrix

| Sequence | Device | First path | Second path | Purpose |
|---|---|---|---|---|
| A -> B | `same-ab` | raw-block filesystem | direct-volume filesystem | Required same-device comparison |
| B -> A | `fresh-ba` | direct-volume filesystem | raw-block filesystem | Required fresh-device reversal |
| isolated raw | `isolated-raw` | reserved ext4-owned raw blocks | n/a | Diagnostic variation; bytes restored before cleanup |
| isolated direct | `isolated-direct` | direct-volume filesystem | n/a | Fresh direct-only variation |

Each filesystem case runs the same immutable probe image, node, CPU/memory resources, runtime class, security context, and ext4 operation matrix: small file, 4 KiB close, 4 KiB fsync, 64 MiB file, 10,000 files, rename/delete, FIO sync queue-depth 1, and tar extraction. A mount or operation failure stops that case's workload immediately, collects available evidence, performs finally-style detach and cleanup, and then continues to later discriminator cases. Safety failures still stop the run and preserve state.

## Safety model

- Use only a dedicated cluster, unique primary resource group, uniquely derived AKS node resource group, and a managed-OS Kata node tainted `dedicated=kata-direct-volume-ab:NoSchedule`.
- Require `RUN_ID`, an opaque UUID `DEPLOYMENT_ID`, an explicit kubeconfig, an immutable probe/host image digest, and an exact `/dev/nvmeXnY` namespace.
- Derive a compact ten-hex-character suffix from the UUID and include it in both resource groups, the cluster, ACR, namespace, node label, and ownership tags.
- Reject existing resource groups, kubeconfigs, result directories, device state, backing files, direct metadata workspaces, or ownership held by another run.
- Reject OS, root, kubelet, or containerd devices; partitions; mounts other than the owned NVMe mount; holders, slaves, swap, users, stale Kata metadata, and pre-existing process FDs.
- Create only a fully preallocated 4 GiB file on the dedicated local NVMe filesystem and verify the loop backing path, size, and major/minor before every host action.
- Format ext4 exactly once per fresh loop device. Raw diagnostics use blocks allocated to a reserved ext4 file, preserve their original bytes after formatting, and restore them before switching paths or cleanup. They never overwrite ext4 superblocks or other unreserved offsets.
- Never host-mount or read the loop device while Kata might own it. Host verification, metadata removal, raw restoration, loop detachment, and configuration restoration require a proven absence of target FDs, sandbox metadata, mounts, holders, slaves, and swap.
- If detach cannot be proven, cleanup preserves PV/PVC, direct metadata, loop, backing file, Kata configuration, and owner state for inspection.
- Trace collection starts only after pod readiness and discovery of exactly one stable Cloud Hypervisor process with an FD matching the run-owned loop major/minor. Syscall tracing is PID/FD scoped when the node's installed `strace` supports `trace-fds`; block request issue, completion, and error tracepoints are filtered to the owned loop major/minor when tracefs permits it. Missing tools, filters, events, attach failures, or empty evidence are unsupported/insufficient, never successful.
- Normal experiment cleanup never deletes the unique namespace or Azure resources. The namespace is preserved for exact dedicated-cluster destruction, and only `destroy-cluster.sh` deletes the exact, tag-validated resource group.

## Prerequisites

The controller needs `az`, `kubectl`, GNU `base64`, and `sed`. The immutable probe image supplies guest tools, including FIO; its `strace` is not claimed to be host-visible. Host actions enter the node root and mount/PID namespaces after `chroot /host`, so `device-manager.sh` explicitly validates node-installed tools such as `jq`, `debugfs`, `filefrag`, `losetup`, and ext4 utilities. Host syscall evidence is unsupported when the node lacks a sufficiently capable `strace`; the harness does not install one.

The host manager detects the installed `kata-runtime direct-volume` CLI at runtime, invokes its advertised `add`/`remove` interface, locates the generated `mountInfo.json`, and requires its normalized JSON to equal:

```json
{"device":"/dev/loopN","fstype":"ext4","metadata":{},"options":["rw"],"volume-type":"block"}
```

An unsupported CLI/schema is an explicit failed/unsupported experiment outcome, not a successful registration.

## Provision

Provisioning creates Azure resources and therefore requires an explicit gate. It fails if the unique target resource group, kubeconfig, or timestamped result target already exists.

```bash
export SUBSCRIPTION_ID=<approved-subscription-guid>
export RUN_ID=<unique-lowercase-id>
export DEPLOYMENT_ID=$(uuidgen)
export KUBECONFIG_PATH=/tmp/kdva-${RUN_ID}.kubeconfig
export CREATE_AZURE_RESOURCES=yes
./provision.sh
```

Build the purpose-built image containing the required guest and host diagnostics in the run-scoped ACR, then use the immutable digest printed by the script. Do not install diagnostic packages on the node during the experiment.

```bash
./build-probe-image.sh
```

## Run

Review all scripts before enabling the live gate. This repository task does not run the experiment.

```bash
export RUN_EXPERIMENT=yes
export TARGET_NODE=<exact-katapool-node>
export EXPECTED_NVME_DEVICE=/dev/nvme0n1
export FORMAT_NVME=yes
export PROBE_IMAGE=<registry/repository@sha256:digest>
export HOST_IMAGE="$PROBE_IMAGE"
./run-experiment.sh
```

Evidence is written below `results/<UTC timestamp>/` with mode `0700`; existing targets are never overwritten. Files include environment and RuntimeClass snapshots, rendered manifests, per-case guest/mount errno evidence, host state, raw OCI/device data where available, direct `mountInfo.json`/registration data, decoded Cloud Hypervisor FD modes, per-case trace directories, `normalized-metadata-fd.tsv`, `normalized-comparison.tsv`, `result-matrix.tsv`, and `diagnostic-matrix.tsv`. Missing fields are marked insufficient. Planned matrix rows exist before any case runs and become stopped/skipped rather than disappearing after an early safety stop. Logs are sanitized by controller helpers where commands may include Azure identifiers or credentials. No template contains fabricated measurements.

## Cleanup and destruction

The run trap removes only UID-owned, run-labeled pods and exact UID-owned PV/PVC names after fail-closed ownership checks, followed by direct metadata, loop files, and owned traces after safe detach. It never deletes the unique namespace and logs `preserving unique namespace for cluster destruction`. For every device, it validates exact PV/PVC ownership labels, deletes and verifies PVC absence first, then deletes and verifies PV absence. All devices must pass that barrier before raw restoration, direct metadata removal, loop/backing cleanup, or shared configuration restoration begins. Any cleanup failure sets the run unsafe and preserves the diagnostic pod and namespace. Raw diagnostic blocks are compared byte-for-byte after restoration and checked with `e2fsck -fn`. Kata configuration replacement/restoration is atomic, restart-required state is persisted before mutation, containerd is restarted, and effective runtime configuration is verified before state is cleared. A foreign config hash is never overwritten.

Definition of Done cleanup is completed by `destroy-cluster.sh`, not normal experiment cleanup. The exact dedicated cluster and resource groups, including the preserved namespace, remain until that identity- and ownership-validated destruction succeeds.

After evidence review, delete the dedicated cluster only with exact identity inputs:

```bash
export DELETE_AZURE_RESOURCES=yes
./destroy-cluster.sh
```

Destruction validates the active subscription, exact derived primary group name, and `deployment-id`, `deployment-suffix`, `purpose`, and `run-id` tags. It inventories that group and refuses deletion unless every resource has all four matching ownership tags and the inventory contains only the optional exact managed cluster and optional exact ACR. If AKS exists, its exact tag-validated object must report the derived node resource group. The node group ID and `managedBy` must point to that exact AKS resource, no management locks may exist, and every resource must carry AKS system ownership tags for this exact cluster/resource group and, where applicable, one of the exact `systempool` or `katapool` node pools. Because custom tags are not trusted in the node group, any unproven resource preserves both groups. If AKS never exists after partial provisioning, the node group is deleted only when the exact ID, `managedBy`, lock, and empty/AKS-managed inventory checks still prove ownership; otherwise it is preserved. When the AKS resource and an explicit kubeconfig both exist, the kubeconfig server is also validated against that cluster's FQDN. Destruction performs no prefix or wildcard deletion.
