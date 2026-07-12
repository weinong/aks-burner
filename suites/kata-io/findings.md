# Kata IO Findings

## Summary

The post-rebase `fio-fast` run confirms that the standard and Kata `emptyDir` variants are not measuring the same storage path, and the split FIO metrics make the timing difference clearer:

- Standard pods see `/work` on host ext4: `/dev/sda3`.
- Kata pods see `/work` through a guest-visible `virtiofs` mount: `none`.
- Both runtimes spend about `60s` in FIO's timed randread phase.
- Kata's total command duration is higher because setup/file layout before the timed read phase is much slower.
- Kata reports higher timed randread IOPS and bandwidth with lower p99 latency, but that should not be read as a general disk I/O win because the storage substrate differs.

## Latest Split FIO Run

Run directory:

```text
results/2026-07-11T02-00-54.099786593Z_kata-io_fio-fast
```

Command:

```bash
TEST_SUITE=kata-io TEST_MODE=fio-fast make run-suite
```

An earlier post-rebase run produced blank `fio_setup_overhead_seconds` values because the script's `awk` expression parsed the ternary expression as an output redirection. After correcting that expression in the benchmark branch, the command was rerun and produced the metrics below.

| Metric | Standard | Kata | Kata / Standard |
|---|---:|---:|---:|
| `fio_exit_code` | `0` | `0` | |
| `fio_total_duration_seconds` | `92.1632` | `256.727` | `2.79x` |
| `fio_active_runtime_seconds` | `60.034` | `60.001` | `1.00x` |
| `fio_setup_overhead_seconds` | `32.1292` | `196.726` | `6.12x` |
| `fio_read_runtime_seconds` | `60.034` | `60.001` | `1.00x` |
| `fio_read_iops` | `38,924.226272` | `104,113.481442` | `2.67x` |
| `fio_read_bw_bytes_per_second` | `159,433,630` | `426,448,819` | `2.67x` |
| `fio_read_clat_p99_nanoseconds` | `52,166,656` | `24,510,464` | `0.47x` |

## Filesystem Evidence

The benchmark used the same `fio-fast` randread profile and the same `emptyDir` workload shape, but the mounted filesystem visible at `/work` differed.

Standard artifact `df-before.txt`:

```text
Filesystem      Size  Used Avail Use% Mounted on
/dev/sda3       252G   29G  213G  12% /work
```

Kata artifact `df-before.txt`:

```text
Filesystem      Size  Used Avail Use% Mounted on
none            252G   29G  213G  12% /work
```

The `df` artifacts show different filesystem sources for the two pods. Separate diagnostic inspection of equivalent pods showed standard `emptyDir` mounted from host ext4 and Kata `emptyDir` exposed through `virtiofs` from inside the guest VM.

## Manual Azure Disk PVC Experiment

A separate manual experiment, independent of the benchmark jobs, verified how a dynamically provisioned Azure Disk PVC reaches a Kata pod. The experiment used a `4Gi` `managed-csi` PVC, a pod with `runtimeClassName: kata-vm-isolation`, and direct inspection of both the AKS node and the Kata guest.

On the AKS node, the Azure Disk CSI driver attached the PVC as a block device and mounted it as ext4:

```text
/var/lib/kubelet/pods/<pod-uid>/volumes/kubernetes.io~csi/<pvc-name>/mount ... - ext4 /dev/sdb rw
/dev/sdb  disk  ext4  4G
```

Inside the Kata guest, the same volume was mounted at `/disk` as `virtiofs`, with no block-device source:

```text
TARGET SOURCE FSTYPE   OPTIONS
/disk  none   virtiofs rw,relatime
```

The guest block-device inventory contained only the approximately `180MiB` `vda` guest root disk. It did not contain the `4GiB` Azure Disk as a guest block device. Node process inspection showed the Kata shim running `virtiofsd` with the sandbox shared directory alongside `cloud-hypervisor`.

Data-path validation succeeded in both directions: a marker written through guest `/disk` was visible in the node-side CSI mount, and a marker written through the node-side mount was visible through guest `/disk`.

This confirms the Azure Disk is attached and mounted on the AKS host by the CSI driver, then exposed into this Kata VM through `virtiofs`; it is not passed through as a block device for the guest to mount directly.

## Interpretation

The split metrics separate two different effects:

1. The timed FIO randread phase is effectively the same duration for both runtimes, as expected from the `runtime=60` profile setting.
2. Kata spends substantially more time before the timed read phase, which is consistent with FIO laying out roughly `4GiB` of test files over the Kata `virtiofs` path before randread starts.
3. The read-phase IOPS, bandwidth, and p99 latency values describe the timed randread phase only. They do not include the setup/file-layout work that dominates Kata's total command duration.
4. The standard and Kata `emptyDir` results should not be treated as an apples-to-apples comparison of the same filesystem implementation.

## Conclusions

1. Use `fio_total_duration_seconds`, `fio_active_runtime_seconds`, and `fio_setup_overhead_seconds` together when comparing standard and Kata FIO results.
2. The latest run shows Kata total duration is `2.79x` standard, while Kata setup overhead is `6.12x` standard and active read runtime is unchanged.
3. Kata's higher read IOPS and lower p99 latency are scoped to the timed randread phase and likely reflect the different guest-visible storage path and caching behavior.
4. Git clone remains a useful metadata-heavy workload from prior runs, but it was not rerun here because the requested split FIO metrics were sufficient to explain the timing gap without additional cluster churn.

## Recommended Follow-Ups

- Keep reporting split FIO metrics in benchmark artifacts.
- Compare `fio_active_runtime_seconds` separately from `fio_setup_overhead_seconds` in summaries and dashboards.
- If the goal is storage-substrate comparison, add a mode that controls the backing filesystem explicitly instead of comparing host ext4 `emptyDir` to Kata `virtiofs`.

## Azure Container Storage Local NVMe Raw Block POC

A separate POC on 2026-07-11 tested Azure Container Storage 2.x local NVMe with a Kubernetes raw block PVC and `volumeDevices`. The standard-runtime control wrote successfully. The Kata guest received the 10 GiB volume as `vdb`, and sysfs identified `/sys/bus/virtio/drivers/virtio_blk`, proving that this path bypasses `virtiofs`.

The device was not usable for writes under the tested AKS managed Kata stack: `mkfs.ext4` and synchronized `dd` writes returned guest `vdb` I/O errors. Cloud-hypervisor opened the correct device-mapper LV as writable. After deleting only the Kata pod, the same PVC and PV were attached unchanged to a standard-runtime pod, where the identical synchronized write succeeded. Testing `disable_block_device_use = false` and direct block cache mode did not resolve the Kata failure; the managed node configuration was restored after the test.

The experiment manifests, commands, versions, and evidence are recorded in [`experiments/acstor-local-nvme/README.md`](experiments/acstor-local-nvme/README.md). The next step is a newer managed Kata runtime or a self-managed current Kata build, not a filesystem-mode fallback through `virtiofs`.
