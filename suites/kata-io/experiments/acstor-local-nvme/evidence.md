# Live Evidence

Collected on 2026-07-11 from the dedicated POC cluster in `westus2`. Subscription and tenant identifiers are intentionally omitted.

## Versions

```text
AKS:                    1.35.5
Azure Container Storage: 2.2.0
local CSI driver:       0.2.17
Kata shim:              1.7.27+unknown
cloud-hypervisor:       51.1.0
```

## Storage Capacity

```text
CLASS                CAPACITY    NODE
local-csi   1831416Mi   aks-nvmepool-...-vmss000000
local-csi   0           aks-systempool-...-vmss000000
```

The local CSI log reported one available device:

```text
NVMe device found (unformatted) path="/dev/nvme0n1" size=1920383410176
Disk discovery complete: found 1 available disk(s)
```

## Kata Transport and Write Failure

The raw block PVC was attached with `volumeDevices` at `/dev/acstor-nvme`:

```text
device=vdb   10G  0 disk virtio
driver=/sys/bus/virtio/drivers/virtio_blk
dd: fsync failed for '/dev/acstor-nvme': Input/output error
```

Guest kernel evidence:

```text
I/O error, dev vdb, sector 0 op 0x1:(WRITE)
Buffer I/O error on dev vdb, logical block 0, lost async page write
```

Cloud-hypervisor VM information reported the local CSI device-mapper backend as writable:

```json
{
  "path": "/dev/dm-0",
  "readonly": false,
  "direct": false,
  "id": "_disk4",
  "image_type": "Raw"
}
```

Node-side device mapper evidence associated that device with the 10 GiB local CSI LV on the local NVMe disk:

```text
containerstorage-pvc--... (254:0)
 `- (259:0)
0 20971520 linear 259:0 2048
```

## Same-PVC Standard Control

The Kata pod was deleted without deleting its PVC. The same PVC and PV were then attached to `standard-same-pvc.yml` under the standard runtime.

The exact PV value was captured before deleting the Kata pod and after starting the standard pod. Both observations returned the same value:

```text
kata-pv=<same-pv-id>
standard-pv=<same-pv-id>
```

Reproduction sequence:

```bash
PV_BEFORE=$(kubectl get pvc -n kata-acstor-nvme kata-local-nvme-block -o jsonpath='{.spec.volumeName}')
kubectl exec -n kata-acstor-nvme kata-local-nvme-block -- \
  dd if=/dev/zero of=/dev/acstor-nvme bs=4096 count=1 conv=fsync status=none
kubectl delete pod -n kata-acstor-nvme kata-local-nvme-block --wait=true
kubectl apply -f standard-same-pvc.yml
kubectl wait -n kata-acstor-nvme --for=condition=Ready pod/standard-kata-local-nvme-block --timeout=10m
PV_AFTER=$(kubectl get pvc -n kata-acstor-nvme kata-local-nvme-block -o jsonpath='{.spec.volumeName}')
test "$PV_BEFORE" = "$PV_AFTER"
kubectl get pod -n kata-acstor-nvme standard-kata-local-nvme-block -o wide
kubectl exec -n kata-acstor-nvme standard-kata-local-nvme-block -- \
  dd if=/dev/zero of=/dev/acstor-nvme bs=4096 count=1 conv=fsync status=none
```

Kata output:

```text
dd: fsync failed for '/dev/acstor-nvme': Input/output error
```

The identical synchronized write then succeeded:

```text
device=fe:0 block special file
standard-same-pvc-write-ok
```

This controls for the local NVMe disk, volume group, logical volume, PVC, PV, storage class, node, and requested capacity. The differing variable is the container runtime and its VM block path.

## Managed Kata Configuration Tests

The managed node initially contained:

```text
disable_block_device_use = true
block_device_driver = "virtio-blk"
```

On the disposable POC node, the following isolated tests were made:

1. Set `disable_block_device_use = false`; the warning `Block device not supported` disappeared, but synchronized writes still failed.
2. Enabled `block_device_cache_set = true` and `block_device_cache_direct = true`; cloud-hypervisor reported `direct:true`, but synchronized writes still failed.

The original managed Kata configuration was restored, its temporary backup removed, containerd restarted, and the node verified `Ready` afterward.

## Conclusion

Azure Container Storage local NVMe can be surfaced inside this AKS Kata guest as `virtio-blk` without using `virtiofs` for the volume. On the tested managed Kata/cloud-hypervisor versions, however, that device is not writable. The same PVC is writable through the standard runtime, isolating the failure to the Kata VM block path rather than the Azure Container Storage LV.
