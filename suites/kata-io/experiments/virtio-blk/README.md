# Kata local NVMe experiment

This experiment compared two supported paths backed by the same local NVMe filesystem:

- A standard pod mounting a host directory from local ext4.
- A Kata pod mounting a separate host directory from that ext4 filesystem through `virtiofs`.

The raw per-sample metrics are in `benchmark-samples.csv`. They were produced sequentially, standard samples first, with this image and profile:

```text
benchmark image digest: sha256:3e5019582a39ea981bd998c69288bd062eff1a083f61020ffa26f3c154c29b80
fio version: fio-3.36
profile: suites/kata-io/images/benchmark/fio-profiles/randread-4k.fio
```

For each sample:

1. Pin the pod to the same local-NVMe node.
2. Remove and recreate an empty runtime-specific host directory on the NVMe filesystem.
3. Mount that directory at `/work` with `hostPath`.
4. Use the standard runtime or `runtimeClassName: kata-vm-isolation`.
5. Run `/usr/local/bin/run-fio.sh` with `WORK_DIR=/work`, `FIO_PROFILE=/profiles/randread-4k.fio`, and unique `RUN_ID`, `SCENARIO`, and `SAMPLE_ID` values.
6. Record `summary.prom`, `fio.json`, `df`, and `/proc/mounts`, then delete the pod and benchmark directory before the next sample.

Expected mount evidence:

```text
standard: /dev/nvme0n1 /work ext4
kata:     none         /work virtiofs
```

The direct-volume probe requires host preparation outside Kubernetes:

1. Create the test namespace if the suite has not already created it: `kubectl create namespace kata-io`.
2. Use a disposable node and positively identify an unmounted, unclaimed data device. Never use the OS, root, kubelet, containerd, or a host-mounted filesystem.
3. Format the device ext4 and register `/var/lib/kata-direct-volume/slot0` with `kata-runtime direct-volume add`.
4. The tested AKS image also required changing `disable_block_device_use` from `true` to `false`; this is unsupported and only suitable for an isolated POC.
5. Replace `REPLACE_NODE` in `direct-volume-probe.yml`, apply it, and require the manifest's `/dev/vd*` ext4 assertions to pass.
6. Delete the pod before mounting the device on the host to verify `probe.txt`. Never mount the filesystem in host and guest concurrently.

On the tested managed AKS image, physical and loop direct volumes reached guest `/dev/vdb` but failed ext4 mount with `EIO`; a raw file-backed disk failed guest setup with `ENOENT`.
