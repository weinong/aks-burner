# Kata virtio-blk write localization

## Objective and result

This experiment localizes a 4 KiB write from an AKS Kata guest through Cloud Hypervisor to a loop-backed file on local NVMe. It was created after an earlier direct-volume experiment on the same nominal AKS node image reported guest `EIO` for physical and loop block backends.

The earlier failure did **not** reproduce on the clean cluster created on 2026-07-12. Five fresh Kata sandboxes read three host-seeded markers and completed five write variants. Cloud Hypervisor held the loop device with `O_RDWR`, and every guest pattern matched the host backing device after detachment. Case D also produced a matching host block request issue and successful completion.

Classification: no write failure occurred in this environment. The path was successful through the guest, Cloud Hypervisor, host block layer, and backing file. The difference from the prior `EIO` result is not yet explained. The two runs used different clusters and different Kubernetes-to-Kata volume construction paths.

## Safety constraints

- Use only the dedicated cluster and tainted `Standard_L8s_v3` Kata node.
- Require an exact `/dev/nvmeXnY`; never select a device automatically.
- Prove the device is a whole NVMe disk separate from root, kubelet, and containerd, with no mounts, children, holders, slaves, swap, or users before formatting.
- Put the test device in a fully allocated 4 GiB file on the NVMe ext4 filesystem and attach only its loop device to the guest.
- Never read the loop device from the host while a Kata sandbox owns it.
- Restore the Kata configuration only when its current hash matches the run-modified hash.
- Stop on device identity, detachment, seed integrity, config restoration, or cleanup uncertainty.

## Environment

| Component | Value |
|---|---|
| Date | 2026-07-12 UTC |
| Region | `westus2` |
| Kubernetes | `1.36.1` |
| Kata node image | `AKSAzureLinux-V3katagen2-202606.19.0` |
| Kata node | One managed-OS `Standard_L8s_v3` node |
| Host OS/kernel | Microsoft Azure Linux 3.0, `6.6.137.mshv2-1.azl3` |
| Kata Containers | `3.19.1` |
| Cloud Hypervisor | `51.1.0` |
| Block driver | `virtio-blk` |
| containerd | `2.2.4` |
| Probe image | `sha256:02b0dc2f1fc24bf91903f42e06a1b8a9a5c60c46bc0cdbd5a76a3d2c94f8c2f6` |
| Host backing | 4 GiB preallocated file on ext4 `/dev/nvme0n1`, attached as `/dev/loop0` |

The complete redacted evidence is retained locally in the ignored run directory:

```text
results/virtio-blk-write-trace/20260712T041317Z/
```

A sanitized, commit-safe evidence subset is retained in `evidence.md`.

## Reproduction

Provision the isolated cluster:

```bash
export SUBSCRIPTION_ID=<approved-subscription-guid>
export RUN_ID=<unique-lowercase-run-id>
export DEPLOYMENT_ID=$(uuidgen)
export KUBECONFIG_PATH=/tmp/aksvbw${RUN_ID}.kubeconfig
export CREATE_AZURE_RESOURCES=yes
./provision.sh
```

Build the immutable probe and diagnostic image in the run-scoped ACR:

```bash
./build-probe-image.sh
```

Run the experiment using the exact Kata node and positively identified NVMe namespace:

```bash
KUBECONFIG_PATH="$KUBECONFIG_PATH" \
SUBSCRIPTION_ID="$SUBSCRIPTION_ID" \
EXPECTED_RESOURCE_GROUP="rg-aks-burner-kata-vbw-${RUN_ID}" \
EXPECTED_CLUSTER_NAME="aksvbw${RUN_ID}" \
TARGET_NODE=<exact-katapool-node> \
EXPECTED_NVME_DEVICE=/dev/nvme0n1 \
FORMAT_NVME=yes \
PROBE_IMAGE=<immutable-image-from-build-script> \
HOST_IMAGE=<same-immutable-image> \
./run-experiment.sh
```

The controller creates a static 4 GiB local raw-block PV/PVC, runs one sandbox per case, waits for three guest marker reads, inspects Cloud Hypervisor, releases one write, deletes the pod, proves detachment, and verifies all marker and write offsets from the host.

## Guest read/write matrix

Every sandbox matched 4 KiB markers at 16 MiB, 128 MiB, and 1 GiB before writing.

| Case | Operation | Offset | Guest result | Host pattern |
|---|---|---:|---|---|
| A | Buffered `dd`, no explicit flush | 256 MiB | exit 0 | match |
| B | Buffered `dd`, `fsync` | 384 MiB | exit 0 | match |
| C | Direct `dd`, no explicit flush | 768 MiB | exit 0 | match |
| D | Direct `dd`, `fsync` | 1.5 GiB | exit 0 | match |
| E | FIO sync, queue depth 1, final fsync | 2 GiB | exit 0 | match |

All three immutable host guard patterns, all three guest read markers, and all non-target write offsets remained unchanged after every case.

## Cloud Hypervisor FD analysis

For all five sandboxes, the Cloud Hypervisor FD that matched loop device major/minor `7:0` had octal flags `02100002`:

```text
access_mode=O_RDWR
O_DIRECT=no
O_NONBLOCK=no
```

The host loop device independently reported `RO=0` and `blockdev --getro=0`. This rules out an effectively read-only guest mapping and an incorrectly read-only Cloud Hypervisor open in this run.

## Host trace timeline

Case D wrote 4 KiB at byte offset `1610612736`, loop sector `3145728`:

```text
2131.074011 block_rq_issue    7,0 WS 4096 sector 3145728 + 8
2131.074066 block_rq_complete 7,0 WS      sector 3145728 + 8 error 0
```

The 55 microsecond issue-to-completion interval and host hash match prove that the host block request completed successfully. No kernel security denial was found in the retained case journals. SELinux and AppArmor status tools were unavailable on the node.

Userspace syscall tracing was unavailable because `strace` was not installed, and no permanent node package was installed. The block-layer issue/completion pair satisfies the experiment's userspace **or** block-layer correlation requirement. The live controller initially marked the trace insufficient because it incorrectly required both sources; the retained raw result records that conservative status, and the automation now accepts complete block evidence independently.

## Decision table

| Observation | Result |
|---|---|
| Guest reads seeded patterns | Working at three non-overlapping offsets in all sandboxes |
| Guest device effectively read-only | Ruled out for this run |
| Cloud Hypervisor FD read-only | Ruled out; actual FD was `O_RDWR` |
| Host security rejection | No denial observed; writes succeeded |
| Cloud Hypervisor receives but does not submit I/O | Ruled out for case D by block issue |
| Host block request returns an error | Ruled out for case D by completion error `0` |
| Host succeeds but guest receives `EIO` | Not observed |
| Host data changes despite guest failure | Not observed; guest and host both reported success |

## Remaining uncertainty

This run cannot localize the earlier write failure because it does not reproduce it. The prior and current tests report the same AKS node image label, Kata `3.19.1`, and Cloud Hypervisor `51.1`, but they were different cluster instances and setup paths. The prior loop test used Kata direct-volume filesystem mounting; this run used a Kubernetes raw-block `volumeDevice`. Either an image rollout beneath the same label, a setup-path difference, or another unrecorded environmental variable may explain the discrepancy.

The next experiment should rerun the prior direct-volume mount probe and this raw-block probe back-to-back on the same node and loop device. If a write failure returns, add a diagnostic image containing `strace` at build time and retain the exact Cloud Hypervisor `io_uring` activity; do not install it on the node.

## Recommended owner and next action

Owner: AKS Pod Sandboxing/node-image team, with Kata/Cloud Hypervisor maintainers if the failure is reproduced.

Next action: compare direct-volume and Kubernetes raw-block construction on the same current node. Do not open a Cloud Hypervisor write-failure issue from this run alone because all controlled writes succeeded.

## Cleanup verification

- Original Kata config SHA-256 restored: `8fe2c4565e453d667ff357ed77c6b47a9e4c3aeca6c15185e38b36ba7e79bf90`.
- containerd active and Kata node `Ready`.
- No experiment pod, namespace, PVC, or PV remains.
- No loop mapping, backing file, NVMe mount, or run owner remains.
- The first automated tracefs cleanup used `rm -rf`, which tracefs rejected. The owned instance was then disabled and removed with `rmdir`; a post-cleanup absence check passed. The script now uses `rmdir`.
- The dedicated cluster is deleted after evidence retention; `destroy-cluster.sh` validates that the isolated kubeconfig targets that cluster, waits for deletion, and removes the kubeconfig.

## Issue-ready summary

**Minimal reproducer:** static local raw-block PV backed by a 4 GiB loop file on dedicated local NVMe; one Kata pod reads three markers and writes one 4 KiB pattern.

**Expected behavior:** guest write succeeds and the exact pattern reaches the host backing device.

**Actual behavior:** expected behavior observed in all five variants. Earlier `EIO` was not reproduced.

**Smallest relevant trace:**

```text
block_rq_issue:    7,0 WS 4096 sector 3145728 + 8
block_rq_complete: 7,0 WS      sector 3145728 + 8 [0]
```

**Environment:** AKS `1.36.1`, image `AKSAzureLinux-V3katagen2-202606.19.0`, Kata `3.19.1`, Cloud Hypervisor `51.1.0`, Azure Linux 3, kernel `6.6.137.mshv2-1.azl3`.

**Classification:** non-reproduction; successful guest-to-host write path. Insufficient evidence to assign a root cause to the historical failure.

**Evidence:** committed subset in `evidence.md`; complete local bundle in `results/virtio-blk-write-trace/20260712T041317Z/`.
