# Kata I/O Suite

`kata-io` compares fio and Git workloads across the default container runtime,
Kata Pod Sandboxing, and an experimental patched Kata raw-block path. Azure
Files benchmark scenarios are disabled because they can hang Azure Linux nodes
during CIFS unmount. The shared Azure Files results PVC remains enabled only to
collect benchmark artifacts. This document covers the fio workloads and the
archived `fio-fast` result from 2026-07-16.

## Fio Modes

Run the quick smoke comparison with:

```bash
TEST_SUITE=kata-io TEST_MODE=fio-fast make run-suite
```

The current `workload-fio-fast.yml` runs six concurrency-1 samples: default and
Kata runtimes on Azure Disk for `seqread` and `fsync-heavy`, plus patched Kata
on Azure Disk raw block for those profiles. The archived result discussed below
contains a different eight-sample matrix, so it should not be treated as output
from the current file.

The full `fio` mode covers five profiles on `emptyDir` and Azure Disk for the
default and Kata runtimes, plus Azure Disk raw block for patched Kata. It
requests Kubernetes concurrency levels 1 and 10. `workload-fio.yml` defines the
authoritative matrix with Go-template loops that `run-suite` expands before
passing the rendered YAML to kube-burner.

Here, `concurrency` is the number of Kubernetes Job iterations requested by the
workload. It is separate from fio's `numjobs`, the number of fio workers in one
pod. Concurrency 10 does not guarantee that all ten pods begin their active fio
phases simultaneously.

## Workload Profiles

All profiles are time-based with `runtime=60`; reported active runtime can be
slightly longer while outstanding I/O completes.

| Profile | Operation | Block size | Engine | Queue depth | fio workers | Direct I/O | Per-worker size |
| --- | --- | ---: | --- | ---: | ---: | --- | ---: |
| `seqread` | Sequential read | 1 MiB | `libaio` | 16 | 2 | Yes | 2 GiB |
| `seqwrite` | Sequential write | 1 MiB | `libaio` | 16 | 2 | Yes | 2 GiB |
| `randread-4k` | Random read | 4 KiB | `libaio` | 32 | 4 | Yes | 1 GiB |
| `randwrite-4k` | Random write | 4 KiB | `libaio` | 32 | 4 | Yes | 1 GiB |
| `fsync-heavy` | Write and `fsync` after every block | 4 KiB | `sync` | 1 (synchronous) | 4 | No | 512 MiB |

Read profiles may create or lay out their files before the timed phase. That
work affects wall-clock duration but not the active-phase IOPS, bandwidth, or
latency values.

## Storage Paths

The matrix uses these distinct paths:

- `storage-emptydir` uses pod-local `emptyDir` storage.
- `storage-azure-disk` uses a `managed-csi` filesystem PVC.
- `storage-azure-disk-block` gives patched Kata a raw `managed-csi` PVC. The
  wrapper formats it as ext4, mounts it at `/work`, and syncs it before fio.

Filesystem PVC and raw-block results do not hold the storage stack constant,
so differences cannot be attributed solely to runtime overhead or the patch.

## Measurements

`run-fio.sh` creates a sample-specific work directory, runs the selected
profile with JSON output, and retains fio output, logs, process timing,
filesystem and process-I/O snapshots, and tool versions. It parses `fio.json`
into `summary.json`; the run summary CSV contains one row per parsed metric.

| Metric | Meaning |
| --- | --- |
| `read_iops`, `write_iops` | Read or write operations completed per second during fio's active phase. The wrapper sums values across fio job groups. Higher is better only for the same profile and comparable path; block size changes what one operation means. |
| `read_bandwidth`, `write_bandwidth` | Active-phase bytes transferred per second, summed across fio job groups. Higher is better only for comparable workloads and paths. |
| `read_clat_p99`, `write_clat_p99` | 99th-percentile read or write completion latency (`clat`) in nanoseconds. The wrapper takes the maximum p99 across fio job groups. Roughly 99% of completions are at or below it; lower is better. For `fsync-heavy`, this is write completion latency only; fio's separate sync-latency distribution is retained in `fio.json` but not normalized into the summary. |
| `active_runtime` | The larger of fio's reported read and write runtimes, converted from milliseconds to seconds. |
| `total_duration` | Wall time from before the pre-fio process-I/O and filesystem snapshots until fio exits. It includes fio preparation and teardown, but excludes post-fio snapshots. Raw-block formatting and mounting happen before this timer. |
| `setup_overhead` | `max(total_duration - active_runtime, 0)`. This approximates wrapper and fio work outside the active phase, including pre-fio snapshots, file preparation, and teardown. It excludes pod startup and raw-block setup. |
| `block_setup_duration` | Time to create `/work`, format the raw device as ext4, mount it, and sync it. It is zero for non-block jobs and excluded from `total_duration` and `setup_overhead`. Raw-block unmount and cleanup occur later and are not reported. |
| `exit_code` | Fio's process exit status; zero means success. If required metrics from a successful run are missing or malformed, the wrapper fails rather than writing a partial summary. |

An inactive direction is emitted as zero, such as write metrics for a read-only
profile. Those zeros are not storage-performance measurements.

## Archived Fio-Fast Result

Source:
`results/2026-07-16T16-10-10.731181891Z_kata-io_fio-fast/summary/results.csv`

The archived run predates the Azure Files disablement and has one concurrency-1
sample for each of eight scenarios: the
default and Kata runtimes on Azure Disk filesystem for `seqread` and
`fsync-heavy`, patched Kata on Azure Disk raw block for those profiles, and the
default and Kata runtimes on Azure Files for `randread-4k`. All eight fio
processes exited successfully. The table converts byte rates to decimal MB/s
or GB/s and nanoseconds to milliseconds.

| Profile | Runtime and storage | IOPS | Bandwidth | Read/write clat p99 | Fio setup overhead | Raw-block setup |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| `fsync-heavy` | Kata, Azure Disk filesystem | 165 write | 0.68 MB/s write | 42.21 ms write | 0.50 s | 0 s |
| `fsync-heavy` | Patched Kata, Azure Disk raw block | 300 write | 1.23 MB/s write | 0.51 ms write | 0.43 s | 21.63 s |
| `fsync-heavy` | Default runtime, Azure Disk filesystem | 285 write | 1.17 MB/s write | 28.97 ms write | 0.52 s | 0 s |
| `randread-4k` | Kata, Azure Files | 121 read | 0.49 MB/s read | 7,281.31 ms read | 177.57 s | 0 s |
| `randread-4k` | Default runtime, Azure Files | 3,825 read | 15.67 MB/s read | 210.76 ms read | 30.65 s | 0 s |
| `seqread` | Kata, Azure Disk filesystem | 4,305 read | 4.51 GB/s read | 52.17 ms read | 37.11 s | 0 s |
| `seqread` | Patched Kata, Azure Disk raw block | 7,960 read | 8.35 GB/s read | 19.27 ms read | 33.01 s | 25.99 s |
| `seqread` | Default runtime, Azure Disk filesystem | 671 read | 0.70 GB/s read | 64.23 ms read | 39.70 s | 0 s |

### Findings

- Patched Kata raw block had the highest IOPS and bandwidth in both archived
  Azure Disk profile groups. Its different storage path means the differences
  cannot be credited solely to patched Kata.
- On Azure Files, Kata recorded 121 random-read IOPS versus 3,825 for the
  default runtime, 7.28-second versus 210.76-ms p99 latency, and 177.57 versus
  30.65 seconds of setup overhead.
- Kata's Azure Disk filesystem sample outperformed the default runtime for
  sequential reads, but underperformed it for the fsync-heavy workload.
- Raw-block preparation added 21.63 to 25.99 seconds before fio timing began.
- The fsync-heavy p99 values are write completion latency, not fio's separate
  sync latency, so they do not measure end-to-end durability latency.

## Interpretation Limits

This is a smoke result, not a performance baseline:

- Each scenario has one sample, so there is no variance estimate or protection
  against transient node, network, or storage conditions.
- The comparisons use different storage implementations. Patched Kata also
  uses the raw-block preparation path.
- Concurrency is 1 throughout this archived run; it says nothing about behavior
  under the full mode's requested concurrency of 10.
- The read working sets are 4 GiB per pod: 2 GiB for each of two `seqread`
  workers and 1 GiB for each of four `randread-4k` workers. `direct=1` reduces
  page-cache effects but does not rule out caching elsewhere in the guest,
  host, network, or storage stack. The multi-GB/s sequential-read values should
  therefore not be assumed to represent physical disk throughput.
- Active-phase metrics omit pod startup, file preparation, raw-block setup, and
  raw-block cleanup. Compare the timing metrics separately and inspect raw
  artifacts before treating a difference as a regression or improvement.
