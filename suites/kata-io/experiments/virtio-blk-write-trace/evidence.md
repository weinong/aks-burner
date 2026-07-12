# Sanitized evidence

This is the commit-safe subset of the redacted local bundle at `results/virtio-blk-write-trace/20260712T041317Z/`. Subscription, tenant, resource IDs, pod UIDs, container IDs, node names, and credentials are omitted.

## Environment

```text
AKS:              1.36.1
Node image:       AKSAzureLinux-V3katagen2-202606.19.0
Node VM:          Standard_L8s_v3, managed OS
Host OS:          Microsoft Azure Linux 3.0
Host kernel:      6.6.137.mshv2-1.azl3
Kata Containers:  3.19.1
Cloud Hypervisor: cloud-hypervisor v51.1.0
Block driver:     virtio-blk
containerd:       2.2.4
Probe digest:     sha256:02b0dc2f1fc24bf91903f42e06a1b8a9a5c60c46bc0cdbd5a76a3d2c94f8c2f6
```

## Device proof

```text
OS/root/kubelet/containerd parent: /dev/sda
Azure resource disk:               /dev/sdb1 mounted at /mnt
Experiment local NVMe:             /dev/nvme0n1, 1,920,383,410,176 bytes
Scratch backing:                   preallocated 4,294,967,296-byte file on NVMe ext4
Scratch loop:                      /dev/loop0, major:minor 7:0, RO=0
```

Before formatting, the NVMe had no mount, child, holder, slave, swap use, or process user. The loop had no filesystem and was never host-read while attached to a sandbox.

## Matrix

All guest reads matched the expected SHA-256 values at 16 MiB, 128 MiB, and 1 GiB in every fresh sandbox.

| Case | Mode | Guest exit | Host result |
|---|---|---:|---|
| A | Buffered, no flush | 0 | exact pattern match |
| B | Buffered plus fsync | 0 | exact pattern match |
| C | Direct, no flush | 0 | exact pattern match |
| D | Direct plus fsync | 0 | exact pattern match |
| E | FIO sync, QD1, final fsync | 0 | exact pattern match |

All guard and non-target offsets retained their prior hashes after every case.

## Cloud Hypervisor FD

Each sandbox had one Cloud Hypervisor FD matching loop major/minor `7:0`. Case D was representative:

```text
fd=160
flags_octal=02100002
access_mode=O_RDWR
O_DIRECT=no
O_NONBLOCK=no
blockdev_getro=0
```

## Block trace

Case D wrote 4 KiB at byte offset `1610612736`, loop sector `3145728`:

```text
iou-wrk [...] 2131.074011: block_rq_issue:    7,0 WS 4096 () 3145728 + 8
<...>   [...] 2131.074066: block_rq_complete: 7,0 WS ()      3145728 + 8 [0]
```

The host hash at the same offset matched the guest pattern. Userspace syscall tracing was unavailable because `strace` was absent; no node package was installed. The live controller conservatively returned `trace_status=insufficient` because it initially required both userspace and block evidence. Post-run review established that the complete block issue/completion pair meets the stated userspace-or-block requirement, and the script now classifies that evidence as sufficient.

## Security evidence

No `denied`, AppArmor, SELinux, audit, seccomp, or Landlock rejection was found in the retained case kernel journals. `getenforce` and `aa-status` were unavailable.

## Configuration and cleanup

```text
Original Kata config SHA-256: 8fe2c4565e453d667ff357ed77c6b47a9e4c3aeca6c15185e38b36ba7e79bf90
Modified Kata config SHA-256: f8f9ba69ce44afac2367e21dafe44faa357163cb02b888c41699e4dddebdb1d1
Restored Kata config SHA-256: 8fe2c4565e453d667ff357ed77c6b47a9e4c3aeca6c15185e38b36ba7e79bf90
containerd after restore: active
node after restore: Ready
experiment pods/PVC/PV: absent
loop mapping/backing file/NVMe mount/owner: absent
```

The initial tracefs removal command was incorrect for tracefs and returned an error. The owned instance was subsequently disabled, its block events were disabled, `rmdir` removed it, and an explicit absence check passed. The automation now uses that sequence.
