# Kata Azure Container Storage Local NVMe POC

## Status

Experiment completed

## Goal

Determine whether an Azure Container Storage 2.x local-NVMe volume can be exposed to an AKS Kata Pod Sandboxing guest as a block device, without using `virtiofs`, and compare the usable path with the existing `kata-io` filesystem baseline.

## Scope

- Mode: modify the existing repository with an isolated experiment.
- Provision a dedicated AKS cluster so this work cannot conflict with other experiments.
- Enable Azure Container Storage 2.x with the `ephemeralDisk` storage type.
- Start with a raw Kubernetes block volume backed by the `local-csi` storage class.
- Run one Kata probe pod that receives the volume through `volumeDevices`.
- Record enough host and guest evidence to determine whether the device entered the guest through a block transport rather than `virtiofs`.
- Add an FIO comparison only after the block-device probe succeeds.

## Azure Context

| Setting | Proposed value |
|---|---|
| Subscription | Explicit `SUBSCRIPTION_ID`, validated against the active Azure CLI context |
| Location | `westus2`; use another supported region if SKU capacity or quota blocks the POC |
| Resource group | `rg-aks-burner-kata-acstor-<run-id>` |
| Cluster | `aksacsnvme<run-id>` |
| Azure Container Storage | Latest 2.x major, `ephemeralDisk` |

The resource group and cluster names are intentionally unrelated to the normal `kata-io` defaults.

## Minimal Topology

| Pool | Count | VM size | OS | Runtime | Purpose |
|---|---:|---|---|---|---|
| `systempool` | 1 | `Standard_D4s_v5` | Ubuntu | OCI | AKS system workloads |
| `nvmepool` | 1 | `Standard_L8s_v3` | Azure Linux | `KataMshvVmIsolation` | Kata workload and local NVMe |

`Standard_L8s_v3` is the smallest documented Lsv3 size and exposes one local NVMe disk. The POC uses managed OS disks so the local NVMe disk remains available to Azure Container Storage.

## Implemented Repository Changes

1. Add `suites/kata-io/experiments/acstor-local-nvme/README.md` with the complete provision, enable, probe, evidence, and cleanup workflow.
2. Add a `local-csi` StorageClass manifest scoped by node affinity to `nvmepool` if the installer does not create an equivalent class.
3. Add a persistent raw-block PVC using:
   - `storageClassName: local-csi`
   - `volumeMode: Block`
   - `accessModes: [ReadWriteOncePod]`
   - `localdisk.csi.acstor.io/accept-ephemeral-storage: "true"`
4. Add a Kata probe pod using `runtimeClassName: kata-vm-isolation` and `volumeDevices`, not `volumeMounts`.
5. Add standard-runtime controls using an independent PVC and then the exact same PVC after the Kata failure.
6. Add commands to collect PVC/PV, CSI, node device, Kata guest device, mount, and shim/hypervisor evidence.
7. Add optional FIO commands that run directly against the block device only after the probe confirms the device path.

## Execution Phases

### Phase 1: Provision isolated AKS

1. Confirm the Azure subscription and region.
2. Provision the dedicated resource group and two-node AKS topology.
3. Verify the Kata runtime class and workload-node labels.
4. Enable Azure Container Storage:

   ```bash
   az aks update \
     --resource-group rg-aks-burner-kata-acstor-<run-id> \
     --name aksacsnvme<run-id> \
     --enable-azure-container-storage ephemeralDisk
   ```

5. Verify `local-csi`, the local CSI pods, and per-node `CSIStorageCapacity`.

### Phase 2: Establish controls

1. Run the existing Kata `emptyDir` probe and confirm it reports `virtiofs`.
2. Run a standard-runtime local-NVMe raw-block pod and confirm the local CSI volume works independently of Kata.

### Phase 3: Test Kata raw block

1. Create a dedicated raw-block PVC for the Kata pod.
2. Attach it using `volumeDevices` at `/dev/acstor-nvme`.
3. Verify the pod reaches `Ready` and the path is a block device.
4. Format the disposable device as ext4, mount it inside the guest, write a marker, unmount it, and remount it.
5. Confirm no `virtiofs` mount backs the test path.

### Phase 4: Identify transport

Collect:

- `lsblk`, `findmnt`, `/sys/class/block`, and device major/minor from the guest.
- PVC, PV, volume attachment, pod events, and CSI driver logs.
- Node-side LVM logical volume and kubelet publish path.
- Kata shim and hypervisor arguments where AKS node access permits it.

Success requires a guest-visible block device that can be formatted and mounted in the guest, with no `virtiofs` source for the test filesystem. The evidence should distinguish `virtio-blk`, `virtio-scsi`, or another block transport when possible.

### Phase 5: Benchmark and conclude

If Phase 3 succeeds, run a short direct-I/O FIO profile against the raw block device and preserve the JSON output. Compare it with the existing `kata-io` results only as an exploratory data point because the storage substrates differ.

## Feasibility Notes

- Azure Container Storage local NVMe is ephemeral. Data is lost if the node is deleted, stopped, deallocated, or the workload moves.
- The current open-source local CSI driver supports raw block volume capabilities and bind-publishes its LVM device for Kubernetes raw-block consumers.
- Upstream Kata supports Kubernetes raw block volumes through `volumeDevices`; this path does not require a filesystem share.
- Filesystem-mode CSI volumes are different: the local CSI driver formats and mounts them on the host. Avoiding `virtiofs` for those volumes requires CSI integration with Kata direct-volume metadata, which the current local CSI implementation does not expose.
- A successful raw-block POC proves a block-device path is possible. It does not prove that ordinary filesystem-mode `volumeMounts` avoid `virtiofs`.
- The POC must use one PVC per pod. Sharing the same writable block device between Kata guests is unsafe.

## Validation

Before any Azure deployment:

```bash
go test ./...
go run ./cmd/perf-runner provision --suite kata-io --resource-group rg-aks-burner-kata-acstor-<run-id> --cluster-name aksacsnvme<run-id> --location westus2 --dry-run
kubectl apply --dry-run=client -f suites/kata-io/experiments/acstor-local-nvme/
```

Live validation was performed after explicit approval of the Azure subscription, region, and resource creation.

## Validation Proof

Validated on 2026-07-11 against the explicitly approved active subscription:

| Check | Result |
|---|---|
| `bash -n suites/kata-io/experiments/acstor-local-nvme/provision.sh` | Passed |
| `go test ./...` | Passed |
| `kubectl apply --dry-run=client` for namespace, StorageClass, and both probe manifests | Passed |
| `Microsoft.ContainerService` provider registration | `Registered` |
| `Standard_L8s_v3` in `westus2` | Available with no SKU restrictions |
| LSv3 and total regional quota in `westus2` | Sufficient for the two-node POC |
| Dedicated resource group preexistence | Does not exist |

## Outcome

The guest received the local CSI raw block PVC as `vdb` through `virtio_blk`, so the volume bypassed `virtiofs`. Synchronized writes failed in the Kata guest. After deleting only the Kata pod and attaching the unchanged PVC/PV to a standard-runtime pod on the same node, the identical synchronized write succeeded. The tested managed Kata/cloud-hypervisor stack therefore exposes the block transport but does not provide a usable writable mapping for this local CSI device-mapper LV. Redacted evidence is preserved in `suites/kata-io/experiments/acstor-local-nvme/evidence.md`.

## Cleanup

Delete the dedicated resource group after preserving experiment evidence:

```bash
SUBSCRIPTION_ID=<approved-subscription-guid> RUN_ID=<run-id> DELETE_AZURE_RESOURCES=yes \
  ./suites/kata-io/experiments/acstor-local-nvme/cleanup.sh
```

Resource-group deletion is destructive and requires explicit confirmation at execution time.

## Sources

- [Install Azure Container Storage with AKS](https://learn.microsoft.com/azure/storage/container-storage/install-container-storage-aks?pivots=azurecli)
- [Use Azure Container Storage with local NVMe](https://learn.microsoft.com/azure/storage/container-storage/use-container-storage-with-local-disk)
- [Azure local CSI driver](https://github.com/Azure/local-csi-driver)
- [Kata direct block device assignment](https://github.com/kata-containers/kata-containers/blob/main/docs/design/direct-blk-device-assignment.md)
- [Kata Kubernetes block-volume test](https://github.com/kata-containers/kata-containers/blob/main/tests/integration/kubernetes/k8s-block-volume.bats)
