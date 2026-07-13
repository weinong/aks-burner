# Sanitized evidence

This summarizes a local timestamped run bundle; Azure destruction claims additionally use its corresponding local destruction bundle. Subscription, tenant, deployment identifiers, resource IDs, mutable Azure and Kubernetes names, UIDs, IP addresses, and credentials are omitted.

## Environment

```text
AKS:              1.36.1
Host OS:          Microsoft Azure Linux 3.0
Host kernel:      6.6.137.mshv2-1.azl3
Cloud Hypervisor: cloud-hypervisor v51.1.0
Block driver:     virtio-blk
containerd:       2.2.4
Probe digest:     sha256:42aba86084599f97afbe08505f7bd0cffc24af6b286b7603144be3fad4dc57f6
```

The effective Kata configuration used `disable_block_device_use = false`. The run used fresh, preallocated 4 GiB loop-backed ext4 devices on dedicated local NVMe.

## Results

| Cases | Observation | Harness classification |
|---|---|---|
| Raw-block filesystem `A` and `A2` | The guest completed a direct raw read, then ext4 mount exited `32` with `can't read superblock on /dev/testdisk` | `failure-with-evidence` |
| Direct-volume `B`, `B2`, and `DIRECT` | Registration recorded ext4, `rw` block metadata; container creation then reported `failed to mount /dev/vdb ... EIO` | `insufficient` because required runtime and trace evidence was incomplete |
| Isolated `RAW` | Seeded reads and two guest direct writes completed; block trace recorded owned-loop requests | `unsupported` because host syscall tracing was unavailable |

The observed outcomes were unchanged by order:

- `A -> B`: raw read succeeded before raw ext4 mount failure; direct mount then failed with `EIO`.
- `B2 -> A2`: direct-first mount failed with `EIO`; the subsequent raw read succeeded before raw ext4 mount failure.
- Isolated raw reproduced successful direct reads and writes.
- Isolated direct reproduced the `/dev/vdb` mount `EIO`.

Prior raw use is therefore not required for the direct-volume `EIO`, and a failed direct attempt did not prevent subsequent raw access. Neither ordered handoff completed a filesystem write or marker, so no filesystem-path winner or persistence comparison is claimed.

## Sanitized excerpts

Both raw filesystem cases emitted the same guest sequence:

```text
GUEST_RAW_READ_OK
READY_FOR_TEST
mount: /tmp/fs: can't read superblock on /dev/testdisk.
```

The isolated raw case emitted distinct before/after hashes for both reserved blocks and ended with:

```text
GUEST_RAW_WRITE_OK
```

All three direct registrations generated the same normalized schema, with only the loop path changing:

```json
{"volume-type":"block","device":"/dev/loopN","fstype":"ext4","metadata":{},"options":["rw"]}
```

All three direct pods then reported during container creation:

```text
failed to mount /dev/vdb to /run/kata-containers/sandbox/storage/..., with error: EIO: I/O error
```

The matrix retained raw-read success for `A` and `A2`, raw-read/write success for `RAW`, and ext4-mount failure for every filesystem case. Both normalized ordered comparisons remained `insufficient`.

## Trace limits

For the raw cases, one owned Cloud Hypervisor process held the target block device `O_RDWR`. Host `strace` with FD filtering was unavailable and no package was installed. Block tracing was sufficient for the isolated raw device and recorded successful guest write activity, but it cannot localize either ext4 mount failure.

The direct cases failed before workload execution and lacked sufficient case-scoped Cloud Hypervisor and syscall evidence. Their retained Kubernetes `EIO` events are reproducible observations, but the root cause remains unproven.

## Cleanup

The run restored and verified the isolated-raw reserved bytes, but final automatic device cleanup later failed while scanning runtime metadata and preserved the remaining isolated-raw state plus the diagnostic pod and namespace. No retained transcript independently establishes the subsequent node-local recovery. A separate destruction bundle records topology validation and successful deletion waits for both exact Azure resource groups; the kubeconfig path is absent.

No performance result or general product-wide conclusion is claimed.
