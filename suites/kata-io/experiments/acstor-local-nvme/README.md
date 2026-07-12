# Kata with Azure Container Storage Local NVMe

This POC tests whether an Azure Container Storage 2.x local-NVMe raw block volume can enter an AKS Pod Sandboxing guest as a block device without using `virtiofs`.

## Isolation and Cost

The POC uses resources that are deliberately separate from other `kata-io` work:

| Setting | Value |
|---|---|
| Resource group | `rg-aks-burner-kata-acstor-<run-id>` |
| AKS cluster | `aksacsnvme<run-id>` |
| Region | `westus2` by default |
| System pool | One `Standard_D4s_v5` node |
| Kata/NVMe pool | One `Standard_L8s_v3` Azure Linux node with a managed OS disk |

Local NVMe data is ephemeral. It is lost when the node is deleted, stopped, deallocated, or replaced. This is a POC topology, not a production topology.

## Why Raw Block First

The local CSI driver supports Kubernetes raw block volumes. Kata supports raw block PVCs through `volumeDevices`, allowing the runtime to attach a block device to the guest instead of sharing a host-mounted filesystem. A filesystem-mode local-CSI PVC is formatted and mounted on the host and can still reach Kata through `virtiofs`; this experiment intentionally does not use that path.

Success means `/dev/acstor-nvme` is a real block device inside the Kata container, can be formatted and mounted by the guest, and the resulting `/mnt/acstor` mount is ext4 rather than `virtiofs`. Guest sysfs evidence should identify the block transport when AKS exposes enough detail.

## Prerequisites

- Azure CLI 2.83.0 or later.
- `kubectl`.
- Permission to create AKS resources and enable Azure Container Storage.
- Quota for one `Standard_D4s_v5` and one `Standard_L8s_v3` VM in the selected region.
- The selected region must support Azure Container Storage 2.x and `Standard_L8s_v3`.

Confirm context and quota before creating resources:

```bash
az account show --query '{name:name,id:id,user:user.name}' --output table
az vm list-usage --location westus2 --output table
az vm list-skus --location westus2 --size Standard_L8s_v3 --all --output table
```

Quota and capacity are separate. If the Lsv3 family or regional vCPU quota is insufficient, request at least 8 available Lsv3 vCPUs in `westus2`. If the SKU has no regional capacity, set `LOCATION` to another [Azure Container Storage region](https://learn.microsoft.com/azure/storage/container-storage/container-storage-introduction#regional-availability), verify quota there, and request that region's quota separately.

## Provision

`provision.sh` creates run-scoped Azure resources, validates the active subscription, tags ownership, disables SSH, and writes credentials to an isolated kubeconfig. Choose a unique 4-12 character lowercase alphanumeric run ID.

```bash
export SUBSCRIPTION_ID=<approved-subscription-guid>
export RUN_ID=<unique-run-id>
export CREATE_AZURE_RESOURCES=yes
./suites/kata-io/experiments/acstor-local-nvme/provision.sh
export KUBECONFIG=/tmp/aksacsnvme${RUN_ID}.kubeconfig
```

To use another supported region:

```bash
LOCATION=eastus2 ./suites/kata-io/experiments/acstor-local-nvme/provision.sh
```

The script creates the AKS cluster, adds the one-node Kata/NVMe pool, and runs:

```bash
az aks update \
  --resource-group rg-aks-burner-kata-acstor-<run-id> \
  --name aksacsnvme<run-id> \
  --enable-azure-container-storage ephemeralDisk \
  --container-storage-version 2
```

It annotates the installer-managed `local-csi` class so the local CSI driver targets only `nvmepool`, then waits for nonzero capacity there.

## Verify the Cluster

```bash
kubectl get nodes -L kubernetes.azure.com/agentpool,perf.azure.com/node-role,node.kubernetes.io/instance-type
kubectl get runtimeclass kata-vm-isolation
kubectl get storageclass local-csi -o yaml
kubectl get pods -n kube-system -o wide
kubectl get csistoragecapacities.storage.k8s.io -n kube-system \
  -o custom-columns=NAME:.metadata.name,STORAGE_CLASS:.storageClassName,CAPACITY:.capacity,NODE:.nodeTopology.matchLabels.'topology\.localdisk\.csi\.acstor\.io/node'
```

Do not continue until `local-csi` has capacity on the `nvmepool` node and the Kata runtime class exists.

## Standard Runtime Control

The control proves raw-block provisioning works before Kata is involved:

```bash
kubectl apply -f suites/kata-io/experiments/acstor-local-nvme/standard-raw-block.yml
kubectl wait --namespace kata-acstor-nvme --for=condition=Ready pod/standard-local-nvme-block --timeout=10m
kubectl get pvc,pv --namespace kata-acstor-nvme -o wide
kubectl exec --namespace kata-acstor-nvme standard-local-nvme-block -- lsblk -o NAME,KNAME,PATH,MAJ:MIN,TYPE,FSTYPE,SIZE,TRAN,MOUNTPOINTS /dev/acstor-nvme
until kubectl exec --namespace kata-acstor-nvme standard-local-nvme-block -- test -b /dev/acstor-nvme; do sleep 2; done
```

Delete the control before testing Kata so the one-node POC remains easy to inspect:

```bash
kubectl delete -f suites/kata-io/experiments/acstor-local-nvme/standard-raw-block.yml
```

## Kata Raw-Block Probe

```bash
kubectl apply -f suites/kata-io/experiments/acstor-local-nvme/kata-raw-block.yml
kubectl wait --namespace kata-acstor-nvme --for=condition=Ready pod/kata-local-nvme-block --timeout=10m
kubectl get pvc,pv --namespace kata-acstor-nvme -o wide
kubectl describe pod --namespace kata-acstor-nvme kata-local-nvme-block
until kubectl exec --namespace kata-acstor-nvme kata-local-nvme-block -- test -b /dev/acstor-nvme; do sleep 2; done
kubectl exec --namespace kata-acstor-nvme kata-local-nvme-block -- lsblk -o NAME,KNAME,PATH,MAJ:MIN,TYPE,FSTYPE,SIZE,TRAN,MOUNTPOINTS /dev/acstor-nvme
```

Capture the guest block transport. A `virtio_blk` driver path is direct evidence for `virtio-blk`:

```bash
kubectl exec --namespace kata-acstor-nvme kata-local-nvme-block -- bash -c '
  set -euo pipefail
  major_minor=$(lsblk -ndo MAJ:MIN /dev/acstor-nvme | tr -d "[:space:]")
  readlink -f "/sys/dev/block/${major_minor}"
  readlink -f "/sys/dev/block/${major_minor}/device/driver" || true
  cat "/sys/dev/block/${major_minor}/device/modalias" || true
'
```

Format and mount the disposable volume inside the Kata guest:

```bash
kubectl exec --namespace kata-acstor-nvme kata-local-nvme-block -- bash -c '
  set -euo pipefail
  test -b /dev/acstor-nvme
  if ! blkid /dev/acstor-nvme; then
    mkfs.ext4 -F /dev/acstor-nvme
  fi
  mkdir -p /mnt/acstor
  mount /dev/acstor-nvme /mnt/acstor
  printf "kata-local-nvme\n" >/mnt/acstor/marker
  sync
  findmnt -T /mnt/acstor -o TARGET,SOURCE,FSTYPE,OPTIONS
  cat /mnt/acstor/marker
  findmnt -t virtiofs || true
'
```

Expected test mount evidence:

```text
TARGET      SOURCE             FSTYPE
/mnt/acstor /dev/acstor-nvme  ext4
```

The presence of unrelated Kata shared-directory `virtiofs` mounts does not fail the POC. `/mnt/acstor` itself must not be backed by `virtiofs`.

## Short FIO Probe

Run FIO only after the filesystem test succeeds. This destroys the filesystem and marker on this disposable PVC:

```bash
kubectl exec --namespace kata-acstor-nvme kata-local-nvme-block -- umount /mnt/acstor
kubectl exec --namespace kata-acstor-nvme kata-local-nvme-block -- \
  fio --name=kata-acstor-raw \
      --filename=/dev/acstor-nvme \
      --direct=1 \
      --rw=randread \
      --bs=4k \
      --iodepth=32 \
      --runtime=30 \
      --time_based \
      --output-format=json
```

Do not compare this raw-device result directly with the existing filesystem-based `kata-io` result as if the storage substrates were identical.

## Node-Side Evidence

Record the PV handle, kubelet publish path, and local CSI logs:

```bash
kubectl get pvc --namespace kata-acstor-nvme kata-local-nvme-block -o yaml
kubectl get pv "$(kubectl get pvc --namespace kata-acstor-nvme kata-local-nvme-block -o jsonpath='{.spec.volumeName}')" -o yaml
kubectl get pods -n kube-system -o wide
kubectl logs -n kube-system -l app.kubernetes.io/name=local-csi-driver --all-containers --tail=500
```

If the managed component uses different labels, identify the local CSI pod from `kubectl get pods -n kube-system` and collect its logs by pod name. Use `kubectl debug node/<nvmepool-node>` only if guest evidence is inconclusive; do not modify the host device or LVM configuration.

## Decision

The experiment succeeds when all of these are true:

1. The standard and Kata pods both receive their own `local-csi` raw block PVC.
2. `/dev/acstor-nvme` passes `test -b` inside Kata.
3. The guest formats and mounts the device as ext4.
4. `findmnt -T /mnt/acstor` reports the block device and ext4, not `none` and `virtiofs`.
5. Sysfs identifies a block transport, preferably `virtio_blk`; if the exact transport is hidden, record that limitation instead of claiming `virtio-blk`.

If the pod fails before receiving the device, preserve pod events and CSI logs. If it receives the device but guest mount operations fail, preserve `lsblk`, sysfs, and mount errors before deleting anything.

## Observed Result: 2026-07-11

The POC was run in `westus2` on AKS `1.35.5`, Azure Container Storage `2.2.0`, local CSI driver `0.2.17`, managed Kata shim `1.7.27`, and cloud-hypervisor `51.1.0`.

The standard-runtime control succeeded:

- `localdisk.csi.acstor.io` created a 10 GiB LVM logical volume on `/dev/nvme0n1`.
- The control container received the raw device and a synchronized 4 KiB write completed.
- After reproducing the Kata failure, the same PVC and PV were attached unchanged to a standard-runtime pod with `standard-same-pvc.yml`; the identical synchronized write completed.

The Kata probe partially succeeded:

- The PVC bound and kubelet reported successful raw-device mapping.
- The guest received `/dev/acstor-nvme` as 10 GiB disk `vdb`.
- Guest sysfs resolved the driver to `/sys/bus/virtio/drivers/virtio_blk`.
- Cloud-hypervisor reported the correct host backend `/dev/dm-0`, `readonly:false`.
- The test volume did not use `virtiofs`; unrelated guest root and Kubernetes metadata paths still used `virtiofs`.

The writable path failed:

```text
I/O error, dev vdb, sector 0 op 0x1:(WRITE)
Buffer I/O error on dev vdb, logical block 0, lost async page write
```

`mkfs.ext4` and a synchronized 4 KiB `dd` both failed. The same host local-NVMe/LVM path remained writable from the standard container.

The managed Kata config had `disable_block_device_use = true`, matching the area addressed by upstream Kata PR [#12863](https://github.com/kata-containers/kata-containers/pull/12863). On this disposable node, changing only that flag to `false` removed the runtime's `Block device not supported` warning but did not make writes work. Enabling Kata's direct block cache mode made cloud-hypervisor report `direct:true`, but writes still failed. The original node configuration was restored afterward.

Conclusion: AKS Kata currently passes this Azure Container Storage raw block PVC into the guest over `virtio-blk`, bypassing `virtiofs`, but the tested managed Kata/cloud-hypervisor stack does not provide a usable writable device for this device-mapper-backed local CSI volume. The next experiment should use a managed runtime build with the direct-volume/block fixes or reproduce outside managed AKS with a current Kata build before changing Azure Container Storage.

The redacted command output and the exact-same-PVC control are preserved in [`evidence.md`](evidence.md).

To reproduce the exact-same-PVC control, follow the command sequence in `evidence.md`: capture the PV name, reproduce the Kata write failure, delete only the Kata pod, apply `standard-same-pvc.yml`, verify the PV name is unchanged and the standard pod is on the same node, then run the identical `dd` command.

## Cleanup

Delete Kubernetes test resources first:

```bash
kubectl delete -f suites/kata-io/experiments/acstor-local-nvme/standard-same-pvc.yml --ignore-not-found
kubectl delete -f suites/kata-io/experiments/acstor-local-nvme/kata-raw-block.yml --ignore-not-found
kubectl delete -f suites/kata-io/experiments/acstor-local-nvme/standard-raw-block.yml --ignore-not-found
```

After preserving evidence, delete the dedicated resource group. This is irreversible:

```bash
export SUBSCRIPTION_ID=<approved-subscription-guid>
export RUN_ID=<exact-provisioned-run-id>
export DELETE_AZURE_RESOURCES=yes
./suites/kata-io/experiments/acstor-local-nvme/cleanup.sh
```

## References

- [Install Azure Container Storage with AKS](https://learn.microsoft.com/azure/storage/container-storage/install-container-storage-aks?pivots=azurecli)
- [Use Azure Container Storage with local NVMe](https://learn.microsoft.com/azure/storage/container-storage/use-container-storage-with-local-disk)
- [Manage local CSI driver placement](https://learn.microsoft.com/azure/storage/container-storage/manage-local-container-storage-interface-driver-placement)
- [Azure local CSI driver](https://github.com/Azure/local-csi-driver)
- [Kata Kubernetes block-volume test](https://github.com/kata-containers/kata-containers/blob/main/tests/integration/kubernetes/k8s-block-volume.bats)
