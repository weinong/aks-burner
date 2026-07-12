# Kata IO Findings

## Executive Result

The earlier direct-volume mount experiment could not use a node-local disk: physical and loop-backed ext4 mounts failed with guest `EIO`, while a raw file-backed direct volume failed with `ENOENT`. The packaged Kata runtime contains the upstream direct-volume CLI and uses the `virtio-blk` block driver, but AKS disables block-device use in the default runtime configuration and does not allow a pod annotation to override it.

Changing that setting on one disposable experiment node proved that Cloud Hypervisor could create guest `/dev/vdb`, but none of the tested backing forms produced usable guest storage:

| Backing | Guest observation | Result |
|---|---|---|
| Local physical NVMe | `/dev/vdb` appeared | ext4 mount failed with `EIO` |
| Host loop device | `/dev/vdb` appeared | ext4 mount failed with `EIO` |
| Raw ext4 disk file on local NVMe | Kata attempted block storage setup | guest-agent setup failed with `ENOENT` |

A clean follow-up raw-block experiment on 2026-07-12 did not reproduce the write failure. A Kubernetes `volumeDevice` backed by a 4 GiB loop file completed five guest write variants, including direct-plus-fsync and FIO sync. This narrows the older negative result to the direct-volume construction or another unrecorded difference; it is no longer defensible to classify all node-local `virtio-blk` paths as broken on the image label.

The supported Kata path remains `virtiofs`. On the same `Standard_L8s_v3` local NVMe filesystem, three native standard-container samples delivered a median `379,836` read IOPS and `1.56 GB/s`; three Kata `virtiofs` samples delivered `127,183` read IOPS and `0.52 GB/s. Kata setup overhead was `36.51x` standard and p99 completion latency was `30.14x` standard.

## Environment

| Component | Value |
|---|---|
| Resource group | Dedicated experiment resource group |
| Cluster | Dedicated experiment cluster |
| Region | `westus2` |
| Kubernetes | `1.36.1` |
| Kata Containers | `3.19.1` |
| Cloud Hypervisor | `51.1` |
| Node image | `AKSAzureLinux-V3katagen2-202606.19.0` |
| POC node pool | One `Standard_L8s_v3`, managed OS, `KataVmIsolation` |
| POC node | One node in the isolated workload pool |
| Benchmark image | Experiment ACR `kata-io/benchmark` image |
| Benchmark image digest | `sha256:3e5019582a39ea981bd998c69288bd062eff1a083f61020ffa26f3c154c29b80` |
| FIO | `fio-3.36` |

The final node device topology was:

| Device | Use |
|---|---|
| `/dev/sda` | 256 GiB managed OS disk; root, kubelet, and containerd |
| `/dev/sdb1` | 80 GiB Azure resource disk mounted at `/mnt` |
| `/dev/nvme0n1` | 1.92 TB local NVMe, formatted ext4 for the experiment |

An earlier `Standard_D8ds_v5` pool did not expose a separate local disk because AKS placed its 300 GiB ephemeral OS disk on the SKU's 300 GiB resource disk. The pool was replaced with a managed-OS `Standard_L8s_v3` pool so that the local NVMe remained separate from the OS.

## Direct `virtio-blk` Experiment

### Direct block write localization

A clean follow-up cluster on 2026-07-12 did not reproduce the prior loop-backed `EIO`. The environment again reported AKS `1.36.1`, node image `AKSAzureLinux-V3katagen2-202606.19.0`, Kata `3.19.1`, and Cloud Hypervisor `51.1.0`, but used a Kubernetes raw-block `volumeDevice` backed by a 4 GiB loop file on dedicated local NVMe.

Five fresh Kata sandboxes each matched three host-seeded 4 KiB markers. Buffered, buffered-plus-fsync, direct, direct-plus-fsync, and FIO sync queue-depth-1 writes all exited successfully, and every exact write pattern was present on the host after verified detachment. Cloud Hypervisor's matching backing FD was `O_RDWR` in every case. For the direct-plus-fsync case, tracefs captured a loop-device write request issue followed 55 microseconds later by completion with error `0`.

The new result proves that the current raw-block path can complete end to end on this clean cluster; it does not explain the older failure. Userspace syscall tracing was unavailable because `strace` was not installed, and no node package was installed. The next useful comparison is the prior direct-volume mount construction and this raw-block construction on the same current node and loop device. Full automation, evidence, and cleanup details are retained in `experiments/virtio-blk-write-trace/`.

### Managed Runtime Configuration

The node shipped with:

```text
disable_block_device_use = true
block_device_driver = "virtio-blk"
enable_annotations = ["enable_iommu", "virtio_fs_extra_args", "kernel_params"]
```

`/usr/local/bin/kata-runtime direct-volume` supports `add`, `remove`, `stats`, and `resize`, but `disable_block_device_use` is not in the allowed annotation list. The default negative control logged `Block device not supported` and continued through the normal shared-filesystem path.

For the POC only, the configuration was backed up and changed to `disable_block_device_use = false` on one experiment node. The direct-volume source path was `/var/lib/kata-direct-volume/slot0`.

Physical NVMe metadata:

```json
{"volume-type":"block","device":"/dev/nvme0n1","fstype":"ext4","metadata":{},"options":["rw"]}
```

The container failed to start with:

```text
failed to mount /dev/vdb to /run/kata-containers/sandbox/storage/..., with error: EIO: I/O error
```

The same `EIO` occurred with a formatted loop device. A 16 GiB raw ext4 disk file stored on the local NVMe was then registered with the required file-block option:

```json
{"device":"/mnt/kata-local-nvme/kata-direct.raw","volume-type":"block","fstype":"ext4","metadata":{},"options":["rw","io.katacontainers.fs-opt.block_device=file"]}
```

That path failed during guest-agent storage setup with `ENOENT` before the container command ran.

The physical and loop tests show that hot-plug reached the guest as `/dev/vdb`; the failure occurred when the guest tried to use the device. The file-backed test failed earlier while resolving or mounting the hot-plugged storage. No direct `virtio-blk` FIO benchmark was possible.

Before every direct physical-device attempt, the device was verified as separate from OS, root, kubelet, and containerd, with no mountpoint, holders, or slaves. The local NVMe was unmounted before assignment and was never mounted by host and guest concurrently. It was remounted on the host only after the failed sandbox was deleted.

After the probes failed, the node configuration was restored byte-for-byte to `disable_block_device_use = true`, and direct-volume metadata and the raw disk image were removed.

## Supported Same-NVMe Comparison

The controlled comparison used the same physical local NVMe and identical FIO profile, but intentionally measured the two supported interfaces:

| Runtime | Host backing | Pod `/work` | Guest block inventory |
|---|---|---|---|
| Standard | `/dev/nvme0n1`, ext4 | `/dev/nvme0n1`, ext4 | Native host container |
| Kata | `/dev/nvme0n1`, ext4 | `none`, `virtiofs` | Only the approximately 180 MiB guest `vda` |

FIO profile:

```text
ioengine=libaio
direct=1
time_based=1
runtime=60
size=1G
bs=4k
iodepth=32
numjobs=4
rw=randread
```

The runs were sequential: standard samples 1-3, then Kata samples 1-3. Each sample used a clean benchmark directory on `/dev/nvme0n1` and requested four 1 GiB jobs.

The machine-readable samples and reproduction procedure are retained under `experiments/virtio-blk/`.

| Runtime | Sample | Total seconds | Active read seconds | Setup seconds | Read IOPS | Bandwidth B/s | p99 clat ns |
|---|---:|---:|---:|---:|---:|---:|---:|
| Standard ext4 | 1 | `64.6918` | `60.001` | `4.6908` | `379,835.82` | `1,555,807,516` | `477,184` |
| Standard ext4 | 2 | `64.6859` | `60.001` | `4.6849` | `363,974.43` | `1,490,839,280` | `505,856` |
| Standard ext4 | 3 | `64.6693` | `60.001` | `4.6683` | `383,854.94` | `1,572,269,816` | `493,568` |
| Kata virtiofs | 1 | `231.066` | `60.001` | `171.065` | `127,182.85` | `520,940,941` | `14,876,672` |
| Kata virtiofs | 2 | `204.046` | `60.025` | `144.021` | `123,044.41` | `503,989,923` | `19,267,584` |
| Kata virtiofs | 3 | `231.713` | `60.002` | `171.711` | `132,437.32` | `542,463,257` | `11,993,088` |

### Median Results

| Metric | Standard | Kata | Kata / Standard |
|---|---:|---:|---:|
| Total duration | `64.6859s` | `231.066s` | `3.57x` |
| Active read runtime | `60.001s` | `60.002s` | `1.00x` |
| Setup overhead | `4.6849s` | `171.065s` | `36.51x` |
| Read IOPS | `379,835.82` | `127,182.85` | `0.335x` |
| Read bandwidth | `1,555,807,516 B/s` | `520,940,941 B/s` | `0.335x` |
| Read p99 latency | `493,568ns` | `14,876,672ns` | `30.14x` |

The fixed 60-second active phase explains why active runtime is equal. The total-duration difference is dominated by laying out the 4 GiB test set over `virtiofs`. During the timed read phase, Kata reached about one third of native ext4 IOPS and bandwidth, with about 30 times the p99 completion latency.

These are three samples from one node. They characterize native ext4 versus supported Kata `virtiofs` on the same local NVMe; they are not projected `virtio-blk` numbers and do not include cross-node variance or host cache reset controls.

## Earlier EmptyDir Baseline

The first run on the original `Standard_D8s_v5` workload pool reproduced the earlier filesystem distinction:

```text
Standard: /dev/sda3 ext4    /work
Kata:     none      virtiofs /work
```

That single run reported standard `87.3729s` total, `38,950` IOPS, and `159,540,916 B/s`, versus Kata `187.588s` total, `115,435` IOPS, and `472,820,638 B/s`. It is retained as evidence of the `emptyDir` path but is superseded for performance interpretation by the controlled same-NVMe medians above. The older apparent Kata read advantage reflected different storage and caching behavior, not a general Kata I/O advantage.

## Azure Disk PVC Evidence

A separate manual experiment verified how a dynamically provisioned Azure Disk PVC reaches a Kata pod. The Azure Disk CSI driver attached the PVC and mounted it as ext4 on the AKS node:

```text
/var/lib/kubelet/pods/<pod-uid>/volumes/kubernetes.io~csi/<pvc-name>/mount ... - ext4 /dev/sdb rw
```

Inside the Kata guest, the same volume appeared as:

```text
TARGET SOURCE FSTYPE   OPTIONS
/disk  none   virtiofs rw,relatime
```

The guest block inventory did not contain the Azure Disk. Bidirectional marker writes confirmed that the host-mounted CSI path was exported into the guest through `virtiofs`.

## Conclusion And Next Action

1. The supported host-filesystem path remains `virtiofs`; the tested direct-volume mount path failed.
2. A separate Kubernetes raw-block `volumeDevice` path succeeded end to end after isolated enablement of block-device use.
3. On identical local NVMe backing, Kata `virtiofs` delivered about `33.5%` of native ext4 read IOPS and bandwidth, with `36.51x` setup overhead and `30.14x` p99 latency.
4. The next useful engineering work is a same-node, same-loop comparison of direct-volume mount construction and Kubernetes raw-block construction, with an explicitly verified node-visible tracing tool when available.

## Kata Direct-Volume A/B Follow-Up

A narrowly scoped, fail-closed harness for the proposed same-node comparison is available in `experiments/kata-direct-volume-ab/`. It covers the required A-to-B same-device sequence, a fresh-device B-to-A reversal, and isolated raw and direct variations. The strengthened harness preserves and verifies ext4 metadata, performs the same ext4 operation matrix on both filesystem paths, verifies path-specific handoff markers, and records scoped path/runtime evidence without treating an expected case failure as a reason to omit later cases.

Results remain pending until an approved run produces evidence. Direct pre-mount raw writes are not implemented because the installed runtime has not demonstrated a safe attach-without-mount interface; mount-failure-time guest and Cloud Hypervisor evidence is collected instead. Host syscall tracing also depends on a node-installed `strace` with FD filtering and is explicitly unsupported/insufficient when absent. No success, failure, performance, or root-cause conclusion should be inferred from the harness or its empty result templates.

## Retained Experiment State

- The earlier completed experiment cluster and resource group were deleted after evidence collection; the pending A/B harness has not provisioned resources as part of this repository change.
- Kata runtime configuration is restored to its supported default.
- Direct-volume metadata, loop mapping, backing file, local-NVMe mount, and tracefs instance are removed.
- Temporary probe, diagnostic, and benchmark Kubernetes resources were removed.
