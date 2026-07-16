# Azure Files CIFS Unmount Node-Hang Reproducer

This standalone reproducer exercises the CIFS unmount path that caused an AKS Azure Linux node to hang. It uses Azure CLI, `kubectl`, Bash, and public container images; it does not use `aks-burner` or kube-burner.

> [!WARNING]
> A successful reproduction can leave the workload VM, kubelet, and Azure VM agent unresponsive. Use the dedicated disposable cluster created below, not a shared cluster.

## Prerequisites

- Azure CLI with AKS support for `KataMshvVmIsolation`
- `kubectl`
- Bash
- An Azure subscription with quota for one `Standard_D4s_v5` VM and one `Standard_D8s_v5` VM

The workload runs with containerd's default `runc` runtime. `KataMshvVmIsolation` is used only to match the original host image and kernel, where the CIFS unmount occurs; the Pods do not select the Kata RuntimeClass.

The host configuration may require the `aks-preview` extension. Verify support before continuing:

```bash
az extension add --name aks-preview --upgrade
az aks nodepool add --help | grep KataMshvVmIsolation
```

## Reproduce

Run all commands from the repository root.

### 1. Deploy The Cluster

Sign in, select a subscription, and run the deployment script:

```bash
az login
az account set --subscription <subscription-id-or-name>

SUBSCRIPTION=<subscription-id-or-name> \
RESOURCE_GROUP=rg-cifs-unmount-repro \
CLUSTER_NAME=aks-cifs-unmount-repro \
LOCATION=southcentralus \
./reproducers/azure-files-cifs-unmount/deploy.sh
```

The defaults create:

- Kubernetes 1.36 in `southcentralus`
- one Ubuntu `Standard_D4s_v5` system node
- one Azure Linux `Standard_D8s_v5` Kata-capable workload node running the Jobs with `runc`
- Azure CNI Overlay with Cilium
- the managed Azure Files CSI driver
- a kubeconfig context named `aks-cifs-unmount-repro`
- no automatic node-image upgrades during the test

AKS uses the currently available node image for this Kubernetes and host-runtime combination; the CLI cannot pin an ordinary node pool to the historical image. Record the image and kernel in the next step.

### 2. Verify The Target Node

Confirm that the disposable workload node is ready and record its versions:

```bash
KUBE_CONTEXT=aks-cifs-unmount-repro

kubectl --context "$KUBE_CONTEXT" get nodes \
  -l repro.azure.com/target=cifs-unmount \
  -o custom-columns='NAME:.metadata.name,READY:.status.conditions[?(@.type=="Ready")].status,OS:.status.nodeInfo.osImage,KERNEL:.status.nodeInfo.kernelVersion,RUNTIME:.status.nodeInfo.containerRuntimeVersion'
```

Exactly one node should be listed, with Microsoft Azure Linux 3.0 and `READY=True`.

### 3. Run The Workload

```bash
KUBE_CONTEXT=aks-cifs-unmount-repro \
./reproducers/azure-files-cifs-unmount/run.sh
```

The script validates the node and StorageClass, then runs up to 10 attempts. Each attempt:

- creates two Azure Files-backed FIO Jobs on the workload node
- starts both Jobs together
- runs four 4 KiB random-read workers per Job for 60 seconds
- watches the node and `KernelBug` events during execution and CIFS teardown
- captures workload, node, event, lease, FIO version, and FIO result data

Evidence is written to `cifs-unmount-evidence-<timestamp>/`.

Exit status meanings:

- `0`: all 10 attempts completed without detecting the trigger
- `1`: setup or FIO failed, or an attempt timed out
- `2`: a new `KernelBug` event appeared or the target node stopped being `Ready`

The namespace is preserved after the final healthy attempt and immediately after a trigger. Delete it before starting another invocation:

```bash
kubectl --context aks-cifs-unmount-repro delete namespace cifs-unmount-repro
```

## Confirm The Trigger

The expected signal is four `KernelBug` events naming `randread-4k.0.0` through `randread-4k.3.0`, followed by a stale node lease and `NodeStatusUnknown` conditions. Preserve external evidence before recovering the VM:

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

## Recover The Node

AKS may automatically repair a node that remains unhealthy. Collect evidence promptly, then identify the VMSS instance:

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

az vmss get-instance-view \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --name "$VMSS_NAME" \
  --instance-id "$INSTANCE_ID"
```

First try a normal restart:

```bash
az vmss restart \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --name "$VMSS_NAME" \
  --instance-ids "$INSTANCE_ID"
```

If that fails, force a host-level power cycle while preserving the OS disk:

```bash
az vmss deallocate \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --name "$VMSS_NAME" \
  --instance-ids "$INSTANCE_ID"

az vmss start \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --name "$VMSS_NAME" \
  --instance-ids "$INSTANCE_ID"

kubectl --context "$KUBE_CONTEXT" wait \
  --for=condition=Ready node \
  --selector=repro.azure.com/target=cifs-unmount \
  --timeout=15m
```

After recovery, collect the previous boot's journal if persistent journaling retained it:

```bash
az vmss run-command invoke \
  --resource-group "$NODE_RESOURCE_GROUP" \
  --name "$VMSS_NAME" \
  --instance-id "$INSTANCE_ID" \
  --command-id RunShellScript \
  --scripts 'journalctl --list-boots --no-pager; journalctl -b -1 --no-pager -o short-iso-precise | grep -Ei "cifs|dentry|unmount|randread|BUG:"'
```

## Clean Up

Delete the AKS cluster after collecting evidence:

```bash
az aks delete \
  --resource-group rg-cifs-unmount-repro \
  --name aks-cifs-unmount-repro \
  --yes
```

The resource group is retained intentionally. Delete it separately only after confirming it contains no unrelated resources.

## Background

The original failure occurred on 2026-07-15 in `runtime-standard-storage-azure-files-fio-randread-4k-concurrency-10`. Two Pods ran on the affected node. After one FIO container exited successfully, node-problem-detector emitted four dentry-in-use events during CIFS unmount. The kubelet lease stopped renewing, Kubernetes marked the node `NotReady`, and Azure reported the VM agent as unresponsive while the VM remained powered on.

The affected host used image `AKSAzureLinux-V3katagen2-202606.19.0` and kernel `6.6.137.mshv2-1.azl3`. Although the node pool was Kata-capable, the Jobs had no `runtimeClassName` and used containerd's default `runc` runtime. The Kata-capable pool preserves the observed host environment; it does not change the workload runtime from `runc`.

An earlier `randwrite-4k` incident had the same four-dentry signature, suggesting that teardown of the CIFS share containing four FIO job files is the common fault boundary rather than the I/O direction.

## Appendix: Workload Specifications

The complete resources are in [`workload.yaml`](workload.yaml). It defines one shared results PVC, two separate work PVCs, and two FIO Jobs synchronized through the results share.

### PVC Specification

Each Azure Files PVC uses this specification:

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

### Pod Specification

The essential Job Pod configuration is:

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

The Ubuntu image is digest-pinned. FIO is installed from the current Ubuntu 24.04 archive at runtime, and each Job records `fio --version` for comparison.
