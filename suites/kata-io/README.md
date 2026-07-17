# Kata I/O Suite

`kata-io` compares fio and Git workloads across the default container runtime,
Kata Pod Sandboxing, and an experimental patched Kata raw-block path. Azure
Files benchmark scenarios are disabled because they can hang Azure Linux nodes
during CIFS unmount. The shared Azure Files results PVC remains enabled only to
collect benchmark artifacts. This document covers the fio workloads, the full
`fio` result from 2026-07-17, and the archived `fio-fast` result from
2026-07-16.

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
materially longer while outstanding I/O completes under severe queueing.

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

## Full Fio Result: 2026-07-17T17:38:11Z

Source:
`results/2026-07-17T17-38-11.687404676Z_kata-io_fio/summary/results.csv`

The run completed all 50 requested scenario/concurrency groups: five profiles
on five runtime/storage configurations at Kubernetes concurrency 1 and 10. It
produced all 275 expected fio samples, one per configuration/profile at
concurrency 1 and ten at concurrency 10; all 275 fio processes exited
successfully.

The unified table treats each runtime/storage path, including patched Kata with
an Azure Disk raw-block PVC, as a tested capability configuration. It reports
the median and observed minimum-maximum range across the ten concurrency-10
pods; bandwidth uses decimal MB/s or GB/s. The configurations are not
necessarily like-for-like, so use the table to compare observed end-to-end
capability, not to isolate runtime, patch, or storage overhead. Each range shows
pod-to-pod behavior within one shared-load job, not variation across independent
benchmark runs.

| Profile | Runtime and storage | IOPS median (range) | Bandwidth median | Read/write clat p99 median (range) |
| --- | --- | ---: | ---: | ---: |
| `randread-4k` | Default runtime, `emptyDir` | 13,123 (12,862-19,424) read | 53.75 MB/s read | 47.45 (30.28-49.55) ms read |
| `randread-4k` | Kata, `emptyDir` | 87,244 (67,184-104,576) read | 357.35 MB/s read | 27.53 (23.72-28.97) ms read |
| `randread-4k` | Default runtime, Azure Disk filesystem | 18,598 (14,570-25,380) read | 76.18 MB/s read | 56.89 (54.26-58.46) ms read |
| `randread-4k` | Kata, Azure Disk filesystem | 15,165 (8,103-109,893) read | 62.12 MB/s read | 23.59 (15.01-98.04) ms read |
| `randread-4k` | Patched Kata, Azure Disk raw block | 249,978 (228,237-263,616) read | 1.02 GB/s read | 2.02 (1.78-2.24) ms read |
| `randwrite-4k` | Default runtime, `emptyDir` | 488 (416-816) write | 2.00 MB/s write | 1,275.07 (792.72-1,451.23) ms write |
| `randwrite-4k` | Kata, `emptyDir` | 5,876 (2,974-7,961) write | 24.07 MB/s write | 316.67 (51.64-574.62) ms write |
| `randwrite-4k` | Default runtime, Azure Disk filesystem | 573 (561-593) write | 2.35 MB/s write | 406.85 (396.36-417.33) ms write |
| `randwrite-4k` | Kata, Azure Disk filesystem | 5,386 (4,645-7,326) write | 22.06 MB/s write | 552.60 (362.81-1,027.60) ms write |
| `randwrite-4k` | Patched Kata, Azure Disk raw block | 742 (709-7,766) write | 3.04 MB/s write | 50.33 (45.88-346.03) ms write |
| `seqread` | Default runtime, `emptyDir` | 263 (261-400) read | 276.06 MB/s read | 158.33 (110.62-160.43) ms read |
| `seqread` | Kata, `emptyDir` | 4,499 (3,394-5,216) read | 4.72 GB/s read | 24.51 (20.84-52.69) ms read |
| `seqread` | Default runtime, Azure Disk filesystem | 344 (272-396) read | 360.81 MB/s read | 158.33 (111.67-160.43) ms read |
| `seqread` | Kata, Azure Disk filesystem | 3,944 (3,632-4,545) read | 4.14 GB/s read | 29.36 (20.32-53.22) ms read |
| `seqread` | Patched Kata, Azure Disk raw block | 8,135 (5,996-10,440) read | 8.53 GB/s read | 20.45 (17.69-21.63) ms read |
| `seqwrite` | Default runtime, `emptyDir` | 45 (44-71) write | 47.56 MB/s write | 1,451.23 (910.16-1,501.56) ms write |
| `seqwrite` | Kata, `emptyDir` | 75 (44-130) write | 78.20 MB/s write | 2,432.70 (1,115.68-3,036.68) ms write |
| `seqwrite` | Default runtime, Azure Disk filesystem | 100 (98-109) write | 104.87 MB/s write | 574.62 (450.89-583.01) ms write |
| `seqwrite` | Kata, Azure Disk filesystem | 118 (99-127) write | 124.17 MB/s write | 5,972.69 (817.89-8,355.05) ms write |
| `seqwrite` | Patched Kata, Azure Disk raw block | 109 (85-130) write | 114.32 MB/s write | 1,306.53 (666.89-2,197.82) ms write |
| `fsync-heavy` | Default runtime, `emptyDir` | 432 (382-658) write | 1.77 MB/s write | 2.82 (1.94-4.36) ms write |
| `fsync-heavy` | Kata, `emptyDir` | 176 (163-193) write | 0.72 MB/s write | 22.94 (20.32-25.82) ms write |
| `fsync-heavy` | Default runtime, Azure Disk filesystem | 195 (179-247) write | 0.80 MB/s write | 32.90 (29.75-34.87) ms write |
| `fsync-heavy` | Kata, Azure Disk filesystem | 144 (114-179) write | 0.59 MB/s write | 38.54 (30.02-43.25) ms write |
| `fsync-heavy` | Patched Kata, Azure Disk raw block | 295 (294-300) write | 1.21 MB/s write | 0.40 (0.38-0.46) ms write |

### Findings

- Patched Kata with Azure Disk raw block showed the highest concurrency-10
  median IOPS and bandwidth for `randread-4k` and `seqread`. For `fsync-heavy`,
  it led the Azure Disk-backed configurations but not default-runtime
  `emptyDir`.
- Patched raw-block `randwrite-4k` was bimodal under concurrency 10. Four pods
  completed their active phases near 60 seconds at 7,710-7,766 IOPS, while six
  reported 205-240-second active runtimes at 709-749 IOPS. All exited
  successfully. With a time-based workload, fio can report longer than the
  configured 60 seconds while outstanding I/O completes. In each long sample,
  about 0.08% of completions, approximately the 128 I/Os represented by four
  workers at queue depth 32, took at least two seconds; maximum completion
  latency was 200.73-235.83 seconds. This extreme terminal drain stall inflated
  active runtime and depressed average IOPS. The normalized p99 range of
  45.88-50.59 ms for those samples excludes that catastrophic tail, so neither
  the 742-IOPS median nor 50.33-ms median alone describes the instability.
- On both `emptyDir` and Azure Disk filesystem, Kata showed much higher
  sequential-read capability than the default runtime. At concurrency 10,
  Azure Disk filesystem medians were 3,944 versus 344 read IOPS and 29.36
  versus 158.33 ms read clat p99.
- Kata also showed higher random-write IOPS on both filesystem paths, but its
  Azure Disk filesystem write clat p99 was higher than the default runtime:
  552.60 versus 406.85 ms.
- Kata's `fsync-heavy` capability was lower than the default runtime on both
  filesystem paths. Patched Kata raw block instead recorded 295 median write
  IOPS and 0.40 ms write clat p99, but that p99 still excludes fio's separate
  sync-latency distribution.
- Raw-block preparation had a 27.96-second median and 21.62-111.98-second range
  across the 50 concurrency-10 pods. Two `seqwrite` pods took 74.62 and 111.98
  seconds to prepare their block devices.
- Concurrency-1 groups have one sample each and are omitted from the table. The
  concurrency-10 pods share cluster and storage load and are not independent
  reruns. Scheduling overlap, node placement, caching outside the guest, and
  transient storage or cluster conditions can affect the results; repeat the
  suite before treating them as a durable baseline.

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
