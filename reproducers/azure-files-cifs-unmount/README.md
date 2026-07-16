# Azure Files CIFS Unmount Node-Hang Reproducer

This is a standalone reproducer for an AKS Azure Linux node hang in the CIFS unmount path. It does not use `aks-burner`, kube-burner, a private registry, or any code from the benchmark suite.

The only required tools are:

- Azure CLI with an AKS command surface that supports `KataMshvVmIsolation`
- `kubectl`
- Bash
- An Azure subscription with quota for one `Standard_D4s_v5` VM and one `Standard_D8s_v5` VM

The MSHV workload runtime might require the `aks-preview` Azure CLI extension for your CLI version. Verify support before deployment:

```bash
az extension add --name aks-preview --upgrade
az aks nodepool add --help | grep KataMshvVmIsolation
```

Warning: a successful reproduction can make the workload VM, kubelet, and Azure VM agent unresponsive. Deploy the dedicated cluster below rather than targeting a shared cluster.

## What Triggered The Failure

The 2026-07-15 run triggered the failure in this scenario:

```text
runtime-standard-storage-azure-files-fio-randread-4k-concurrency-10
```

Two scenario Pods landed on the affected node. One share unmounted cleanly. The second Pod's FIO container exited successfully, and two seconds later node-problem-detector emitted four events:

```text
BUG: Dentry ... n=randread-4k.0.0 still in use (1) [unmount of cifs cifs]
BUG: Dentry ... n=randread-4k.1.0 still in use (1) [unmount of cifs cifs]
BUG: Dentry ... n=randread-4k.2.0 still in use (1) [unmount of cifs cifs]
BUG: Dentry ... n=randread-4k.3.0 still in use (1) [unmount of cifs cifs]
```

The kubelet lease then stopped renewing, Kubernetes marked the node `NotReady`, and Azure reported that the VM agent was unresponsive while the VM remained powered on.

An earlier `randwrite-4k` incident from the same date has the same four-dentry signature. The common fault boundary is therefore teardown of the CIFS share containing four FIO job files, not only the read/write direction.

## Runtime Used

The triggering Jobs had no `spec.template.spec.runtimeClassName`. The affected node's containerd configuration reported:

```text
default_runtime_name = 'runc'
runtimes.runc.runtime_type = 'io.containerd.runc.v2'
```

The effective runtime was containerd's default `runc`, not `kata-vm-isolation`. The affected host nevertheless came from a Kata-capable AKS node pool and used the `AKSAzureLinux-V3katagen2-202606.19.0` node image with kernel `6.6.137.mshv2-1.azl3`. The cluster creation commands preserve the Kata-capable host configuration by setting the workload pool's `--workload-runtime` to `KataMshvVmIsolation`; the Kubernetes Jobs deliberately omit `runtimeClassName`, so their containers still run with `runc`.

## 1. Create The Cluster

Defaults reproduce the observed cluster shape in `southcentralus`:

- Kubernetes `1.36`
- one Ubuntu `Standard_D4s_v5` system node
- one Azure Linux `Standard_D8s_v5` Kata-capable workload node
- Azure CNI Overlay with Cilium
- managed Azure Files CSI driver
- automatic node-image upgrades disabled so the image cannot change during a test

Sign in and choose the subscription:

```bash
az login
az account set --subscription <subscription-id-or-name>
```

Deploy the resource group and cluster:

```bash
SUBSCRIPTION=<subscription-id-or-name> \
RESOURCE_GROUP=rg-cifs-unmount-repro \
CLUSTER_NAME=aks-cifs-unmount-repro \
LOCATION=southcentralus \
./reproducers/azure-files-cifs-unmount/deploy.sh
```

`deploy.sh` uses only Azure CLI commands and obtains credentials under the context `aks-cifs-unmount-repro`. To deploy manually instead:

```bash
if [[ "$(az group exists --name rg-cifs-unmount-repro --output tsv)" == "true" ]]; then
  echo 'error: resource group already exists; choose a new name' >&2
  exit 1
fi

az group create \
  --name rg-cifs-unmount-repro \
  --location southcentralus

az aks create \
  --resource-group rg-cifs-unmount-repro \
  --name aks-cifs-unmount-repro \
  --location southcentralus \
  --kubernetes-version 1.36 \
  --nodepool-name systempool \
  --node-count 1 \
  --node-vm-size Standard_D4s_v5 \
  --node-osdisk-type Managed \
  --os-sku Ubuntu \
  --vm-set-type VirtualMachineScaleSets \
  --network-plugin azure \
  --network-plugin-mode overlay \
  --network-dataplane cilium \
  --network-policy cilium \
  --load-balancer-sku standard \
  --outbound-type loadBalancer \
  --enable-managed-identity \
  --node-os-upgrade-channel None \
  --generate-ssh-keys \
  --tier free \
  --yes

az aks update \
  --resource-group rg-cifs-unmount-repro \
  --name aks-cifs-unmount-repro \
  --enable-file-driver

az aks nodepool add \
  --resource-group rg-cifs-unmount-repro \
  --cluster-name aks-cifs-unmount-repro \
  --name repropool \
  --mode User \
  --node-count 1 \
  --node-vm-size Standard_D8s_v5 \
  --node-osdisk-type Managed \
  --node-osdisk-size 256 \
  --max-pods 250 \
  --os-type Linux \
  --os-sku AzureLinux \
  --workload-runtime KataMshvVmIsolation \
  --labels repro.azure.com/target=cifs-unmount

az aks get-credentials \
  --resource-group rg-cifs-unmount-repro \
  --name aks-cifs-unmount-repro \
  --context aks-cifs-unmount-repro \
  --overwrite-existing
```

AKS CLI creation does not pin an ordinary node pool to the exact historical node-image build. A newly created cluster therefore receives the currently available image for this Kubernetes/runtime combination. Record the resulting image and kernel before testing; reproducing only on the older image is useful version-bisect evidence.

## 2. Verify The Target

Confirm that exactly one disposable workload node exists and record its software versions:

```bash
KUBE_CONTEXT=aks-cifs-unmount-repro

kubectl --context "$KUBE_CONTEXT" get nodes \
  -l repro.azure.com/target=cifs-unmount \
  -o custom-columns='NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,OS:.status.nodeInfo.osImage,KERNEL:.status.nodeInfo.kernelVersion,RUNTIME:.status.nodeInfo.containerRuntimeVersion'

kubectl --context "$KUBE_CONTEXT" get storageclass azurefile-csi -o yaml
kubectl --context "$KUBE_CONTEXT" get runtimeclass -o yaml
```

The target node should report Microsoft Azure Linux 3.0. The `kata-vm-isolation` RuntimeClass may exist because the host is Kata-capable, but the workload below does not select it.

## 3. Review The PVC And Pod Specs

[`workload.yaml`](workload.yaml) contains plain Kubernetes resources:

- one shared Azure Files results PVC
- two separate Azure Files work PVCs
- two FIO Jobs pinned to the one disposable node
- a start barrier on the shared results PVC so both FIO processes run concurrently
- no `runtimeClassName`, which selects containerd's default `runc`
- the observed FIO settings: `libaio`, direct I/O, 4 KiB random reads, queue depth 32, four jobs, 1 GiB per job, and a 60-second runtime

The essential PVC specification is:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: work-0
  namespace: cifs-unmount-repro
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: azurefile-csi
  resources:
    requests:
      storage: 128Gi
```

The essential Pod configuration is:

```yaml
spec:
  restartPolicy: Never
  nodeSelector:
    repro.azure.com/target: cifs-unmount
  # runtimeClassName is intentionally absent: use containerd's default runc.
  containers:
    - name: fio
      image: ubuntu:24.04@sha256:4fbb8e6a8395de5a7550b33509421a2bafbc0aab6c06ba2cef9ebffbc7092d90
      command:
        - /bin/bash
        - -c
        - |
          set -euo pipefail
          apt-get update
          DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends fio
          mkdir -p /work/run
          exec fio \
            --name=randread-4k \
            --directory=/work/run \
            --ioengine=libaio \
            --direct=1 \
            --time_based=1 \
            --runtime=60 \
            --size=1G \
            --bs=4k \
            --iodepth=32 \
            --numjobs=4 \
            --group_reporting=1 \
            --rw=randread
```

The public Ubuntu image is digest-pinned. FIO is installed from the current Ubuntu 24.04 archive at run time; each Job writes `fio --version` to the results share for package-version comparison.

## 4. Run The Reproducer

The wrapper validates the node and StorageClass, applies `workload.yaml`, waits for both FIO containers to exit, and watches the teardown interval. It captures node events and resource state if a new `KernelBug` event appears or the node leaves `Ready`.

```bash
KUBE_CONTEXT=aks-cifs-unmount-repro \
./reproducers/azure-files-cifs-unmount/run.sh
```

The exit statuses are:

- `0`: both Jobs completed and no trigger was observed during the default three-minute teardown watch
- `1`: setup or FIO failed, or the run timed out
- `2`: a new `KernelBug` event appeared or the target node stopped being `Ready`

Evidence is written to `cifs-unmount-evidence-<timestamp>/`. This includes node state, node events, the node Lease, workload resources, FIO versions, and FIO JSON copied through the Job logs. The namespace is intentionally preserved after every run. Delete it before retrying:

```bash
kubectl --context aks-cifs-unmount-repro delete namespace cifs-unmount-repro
```

Then run `run.sh` again. Multiple waves can help because the hang is not guaranteed on every unmount.

## 5. Confirm A Trigger

On failure, preserve external evidence before recovering the VM:

```bash
KUBE_CONTEXT=aks-cifs-unmount-repro
NODE_NAME=$(kubectl --context "$KUBE_CONTEXT" get nodes \
  -l repro.azure.com/target=cifs-unmount \
  -o jsonpath='{.items[0].metadata.name}')

kubectl --context "$KUBE_CONTEXT" describe node "$NODE_NAME"
kubectl --context "$KUBE_CONTEXT" get events --all-namespaces \
  --field-selector "involvedObject.kind=Node,involvedObject.name=$NODE_NAME" \
  --sort-by=.metadata.creationTimestamp
kubectl --context "$KUBE_CONTEXT" get node "$NODE_NAME" \
  -o jsonpath='{.spec.providerID}{"\n"}'
```

Expected failure evidence is four `KernelBug` events naming `randread-4k.0.0` through `randread-4k.3.0`, followed by a stale node lease and `NodeStatusUnknown` conditions.

## 6. Recover The Node

Get the node resource group, VMSS name, and instance ID from the target node's provider ID:

```bash
RESOURCE_GROUP=rg-cifs-unmount-repro
CLUSTER_NAME=aks-cifs-unmount-repro
KUBE_CONTEXT=aks-cifs-unmount-repro
NODE_NAME=$(kubectl --context "$KUBE_CONTEXT" get nodes \
  -l repro.azure.com/target=cifs-unmount \
  -o jsonpath='{.items[0].metadata.name}')
PROVIDER_ID=$(kubectl --context "$KUBE_CONTEXT" get node "$NODE_NAME" \
  -o jsonpath='{.spec.providerID}')
NODE_RESOURCE_GROUP=$(az aks show \
  --resource-group "$RESOURCE_GROUP" \
  --name "$CLUSTER_NAME" \
  --query nodeResourceGroup -o tsv)
INSTANCE_ID=${PROVIDER_ID##*/}
VMSS_PATH=${PROVIDER_ID%/virtualMachines/*}
VMSS_NAME=${VMSS_PATH##*/}
```

Inspect the VM before changing it:

```bash
az vmss get-instance-view \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --name "$VMSS_NAME" \
  --instance-id "$INSTANCE_ID"
```

In both observed failures, the VM remained in `PowerState/running` while the guest agent became unresponsive. AKS can begin automatic repair after a node remains unhealthy, so collect external evidence promptly before it reboots or reimages the node. First attempt a normal restart:

```bash
az vmss restart \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --name "$VMSS_NAME" \
  --instance-ids "$INSTANCE_ID"
```

If the restart does not recover it, force a host-level power cycle while preserving the OS disk:

```bash
az vmss deallocate \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --name "$VMSS_NAME" \
  --instance-ids "$INSTANCE_ID"

az vmss start \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --name "$VMSS_NAME" \
  --instance-ids "$INSTANCE_ID"

kubectl --context aks-cifs-unmount-repro wait \
  --for=condition=Ready node \
  --selector=repro.azure.com/target=cifs-unmount \
  --timeout=15m
```

After recovery, use VMSS Run Command to collect the previous boot's journal if persistent journaling retained it:

```bash
az vmss run-command invoke \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --name "$VMSS_NAME" \
  --instance-id "$INSTANCE_ID" \
  --command-id RunShellScript \
  --scripts 'journalctl --list-boots --no-pager; journalctl -b -1 --no-pager -o short-iso-precise | grep -Ei "cifs|dentry|unmount|randread|BUG:"'
```

## 7. Delete The Cluster

Delete only the named AKS cluster after collecting evidence. The resource group is intentionally retained so cleanup cannot remove unrelated resources:

```bash
az aks delete \
  --resource-group rg-cifs-unmount-repro \
  --name aks-cifs-unmount-repro \
  --yes
```

After confirming that the resource group contains no unrelated resources, remove it separately if desired.
