# Kata I/O Suite Design

## Goal

Add a new exploratory `kata-io` kube-burner suite that measures AKS Kata Pod Sandboxing I/O and Git clone performance against the standard AKS runtime. The suite should provision a Kata-capable AKS environment, run focused synthetic and real-world workloads across `emptyDir`, Azure Disk, and Azure Files, and copy raw per-job artifacts into the local run results.

## Context

The repository already has a `kata-perf` startup smoke suite and a `perf-runner` workflow for provisioning AKS, building suite images in ACR, installing Prometheus, rendering kube-burner configs, and storing local run output under `results/`. The new suite should not replace `kata-perf`; it should live independently under `suites/kata-io` and follow the same lifecycle commands:

```bash
TEST_SUITE=kata-io make provision
TEST_SUITE=kata-io TEST_MODE=smoke make run-suite
TEST_SUITE=kata-io TEST_MODE=full make run-suite
TEST_SUITE=kata-io make destroy
```

The first version is an exploratory benchmark, not a CI regression gate. It should favor diagnostic breadth over strict reproducibility, while keeping the implementation small enough to validate quickly.

## Chosen Approach

Use an explicit suite plus small runner extensions.

- Add `suites/kata-io` with kube-burner workload YAML, Kubernetes templates, mode files, benchmark image sources, and fio profiles.
- Add suite-specific AKS provisioning support for Kata Pod Sandboxing.
- Add kube-state-metrics installation/scraping support to the runner observability path.
- Add a small artifact-copy step that copies the contents of a results PVC into the local run directory after kube-burner completes.
- Do not add a general matrix engine in v1.

This keeps `perf-runner` close to the current architecture while satisfying the required end-to-end provisioning and raw artifact collection behavior.

## Suite Layout

The suite should add these files:

```text
suites/kata-io/
  suite.yml
  requirements.yml
  infra.bicepparam
  workload.yml
  metrics.yml
  templates/
    namespace.yml
    fio-emptydir-standard-job.yml
    fio-emptydir-kata-job.yml
    fio-pvc-standard-job.yml
    fio-pvc-kata-job.yml
    git-emptydir-standard-job.yml
    git-emptydir-kata-job.yml
    git-pvc-standard-job.yml
    git-pvc-kata-job.yml
    work-pvc.yml
    results-pvc.yml
  vars/
    smoke.yml
    full.yml
  images/
    benchmark/
      Dockerfile
      scripts/
        run-fio.sh
        run-git-clone.sh
      fio-profiles/
        randread-4k.fio
        randwrite-4k.fio
        seqread.fio
        seqwrite.fio
        fsync-heavy.fio
```

`kata-perf` remains unchanged and continues to serve as the lightweight startup/framework validation suite.

## Modes And Matrix

`smoke` validates the harness cheaply:

- Runtimes: standard and Kata.
- Storage: `emptyDir`.
- Workloads: one fio read profile and Git blobless clone.
- Concurrency: 1.

`full` is the exploratory benchmark:

- Runtimes: standard and Kata.
- Storage: `emptyDir`, Azure Disk PVC, Azure Files PVC.
- fio workloads: `randread-4k`, `randwrite-4k`, `seqread`, `seqwrite`, `fsync-heavy`.
- Git workloads: full clone and blobless clone.
- Git source: `https://github.com/kubernetes/kubernetes` by default.
- Concurrency: 1 and 10.

The suite should represent these as explicit kube-burner jobs/templates and mode variables, not by adding a generic matrix expander.

## Infrastructure

Provisioning should make `kata-io` runnable end-to-end.

The AKS infrastructure should include a Kata-capable workload node pool:

- Azure Linux OS SKU.
- A Generation 2 VM size that supports nested virtualization.
- AKS Pod Sandboxing workload runtime enabled for the workload pool, equivalent to `az aks nodepool add --workload-runtime KataVmIsolation`.
- Existing workload label pattern: `perf.azure.com/node-role: workload`.

The default Kata RuntimeClass is `kata-vm-isolation`, matching AKS Pod Sandboxing defaults. The suite should keep the runtime class name configurable through mode/template variables so users can override it for other clusters or runtime classes.

Storage should be provisioned through Kubernetes templates where possible:

- Azure Disk PVC for block-backed persistent storage scenarios.
- Azure Files PVC for shared filesystem scenarios.
- Results PVC for raw artifact collection. This should be RWX-capable, so Azure Files is the expected backing storage.

Storage class names, volume sizes, and runtime class names should be suite variables rather than hard-coded throughout templates.

## Workload Execution

Workloads should run as Kubernetes `Job` resources, not bare pods.

Common job behavior:

- `backoffLimit: 0`.
- `restartPolicy: Never`.
- Fixed CPU and memory requests/limits for fair standard-vs-Kata comparisons.
- Node selector for the workload node pool.
- Short bounded labels for runtime, storage type, workload type, fio profile or clone mode, and concurrency.
- Full scenario names in annotations and workload environment variables, not Kubernetes label values, to avoid label length limits.
- Standard-runtime scenarios omit `runtimeClassName`.
- Kata scenarios set `runtimeClassName` to the configured RuntimeClass, defaulting to `kata-vm-isolation`.
- `/work` mounts the tested storage backend, while scripts create a per-sample subdirectory below `/work` using `RUN_ID`, `SCENARIO`, and `SAMPLE_ID`.
- `/results` mounts the shared results PVC.
- Azure Disk and Azure Files scenarios use one work PVC per scenario iteration so concurrent pods do not share a work volume.

`run-fio.sh` inputs:

- `RUN_ID`
- `SCENARIO`
- `FIO_PROFILE`
- `WORK_DIR`
- `RESULTS_DIR`

`run-fio.sh` outputs:

- `fio.json`
- `time.txt`
- `stdout.log`
- `stderr.log`
- `proc-self-io-before.txt`
- `proc-self-io-after.txt`
- `df-before.txt`
- `df-after.txt`
- `summary.prom`

`run-git-clone.sh` inputs:

- `RUN_ID`
- `SCENARIO`
- `REPO_URL`
- `CLONE_MODE`
- `WORK_DIR`
- `RESULTS_DIR`

Supported clone modes in v1:

- `full`
- `blobless`, using `--filter=blob:none`

`run-git-clone.sh` outputs:

- `git-trace2-event.json`
- `git-trace2-perf.log`
- `time.txt`
- `git-stdout.log`
- `git-stderr.log`
- `repo-size-bytes.txt`
- `file-count.txt`
- `proc-self-io-before.txt`
- `proc-self-io-after.txt`
- `df-before.txt`
- `df-after.txt`
- `summary.prom`

Each workload should write artifacts below a deterministic path such as:

```text
/results/<run-id>/<scenario>/<iteration>/
```

## Observability

Prometheus remains required for `kata-io`.

Add kube-state-metrics as a required observability component for this suite. The pinned image is:

```text
mcr.microsoft.com/oss/v2/kubernetes/kube-state-metrics:v2.19.0
```

The suite should reference this image through an image key such as `kube-state-metrics` in `config/images.yml` or in suite image requirements. The default namespace should be `perf-monitoring`, matching the existing Prometheus install path.

The runner should support installing kube-state-metrics when declared by suite requirements, and Prometheus should scrape it before kube-burner starts collecting metrics.

Required kube-state-metrics signals:

- `kube_pod_created`
- `kube_pod_status_scheduled_time`
- `kube_pod_status_initialized_time`
- `kube_pod_container_state_started`
- `kube_pod_status_ready_time`
- `kube_pod_runtimeclass_name_info`

The kube-burner metric profile should include:

- `podLatency` measurement.
- Container CPU and memory.
- Container filesystem read/write bytes and I/O time.
- Container network receive/transmit bytes.
- Node disk read/write bytes and read/write/I/O time.
- Pod lifecycle metrics from kube-state-metrics, including created, scheduled, initialized, container started, ready, and RuntimeClass metrics.
- Kubelet pod sandbox/startup histograms where available.

## Artifact Collection

Raw artifacts are required in v1.

Add a small `perf-runner` post-run artifact step for suites that declare an artifact PVC. After kube-burner completes, the runner should:

1. Create a short-lived copy pod that mounts the results PVC.
2. Copy `/results` from that pod into the local run directory.
3. Store artifacts at:

```text
results/<timestamp>_kata-io_<mode>/artifacts/
```

If artifact copy fails, `run-suite` should return an error because raw artifacts are part of the v1 contract.

The artifact PVC should live in the benchmark namespace, defaulting to `kata-io`, and the copy pod should be created in that same namespace so it can mount the PVC directly.

The copy pod image must include `tar` because `kubectl cp` depends on `tar` inside the container. Use a configured utility image with `tar`; do not use the Kubernetes `pause` image for artifact copy.

## Requirements Schema And Runner Changes

The current requirements schema only models Prometheus under observability. Extend it to support kube-state-metrics and artifact collection.

Expected additions:

- `requires.observability.kubeStateMetrics.required`
- `requires.observability.kubeStateMetrics.install`
- `requires.observability.kubeStateMetrics.namespace`
- `requires.observability.kubeStateMetrics.imageKey`
- `requires.observability.kubeStateMetrics.serviceName`
- `requires.observability.kubeStateMetrics.servicePort`
- `requires.artifacts.enabled`
- `requires.artifacts.namespace`
- `requires.artifacts.pvcName`
- `requires.artifacts.mountPath`
- `requires.artifacts.copyImage`

The runner should keep these changes narrow:

- Parse the new requirements fields.
- Install kube-state-metrics when required and install is true.
- Ensure Prometheus can scrape kube-state-metrics.
- Copy artifacts after kube-burner execution when artifact collection is enabled.

No general-purpose benchmark matrix engine should be added in v1.

## Testing

Repository tests should cover:

- Schema validation for new `kata-io` suite files.
- `run.RenderWorkload` image and template variable replacement for the benchmark image.
- Standard runtime scenarios not setting Kata runtime.
- Kata scenarios rendering `runtimeClassName`.
- Results PVC variables rendering correctly.
- Requirements parsing for kube-state-metrics.
- Artifact copy behavior using fake command execution where practical.

End-to-end validation is required before the implementation is considered complete. The validation run should provision real AKS infrastructure, build and publish the benchmark image, install observability components, run the suite, copy artifacts, and destroy the default resource group after results are verified.

Required validation path:

```bash
make test
make build
make list-suites
TEST_SUITE=kata-io make provision
TEST_SUITE=kata-io TEST_MODE=smoke make run-suite
TEST_SUITE=kata-io make destroy
```

The smoke run must verify the complete end-to-end path:

- AKS provisioning succeeds with a Kata-capable workload node pool.
- The configured Kata RuntimeClass exists and Kata jobs complete.
- The benchmark image builds in ACR and is pulled by workload pods.
- Prometheus and kube-state-metrics are installed and scraped.
- Standard-runtime and Kata-runtime scenarios both complete.
- At least one fio scenario and one Git blobless clone scenario complete successfully.
- kube-burner writes pod latency and metric output under the local run directory.
- The artifact copy step creates `results/<timestamp>_kata-io_smoke/artifacts/`.
- The artifacts directory contains raw fio, Git Trace2, `/usr/bin/time`, and `summary.prom` files for the smoke scenarios.

The full exploratory mode should also be run before declaring the benchmark suite operational, but it is not required for the first implementation acceptance if smoke already proves the end-to-end lifecycle. Run it after smoke when time and cluster cost allow:

```bash
TEST_SUITE=kata-io TEST_MODE=full make run-suite
```

If any required smoke validation step fails, the implementation is incomplete until the failure is fixed or the design is explicitly revised.

## Non-Goals For V1

- No matrix engine.
- No Pushgateway.
- No in-cluster Git mirror.
- No CI regression thresholds.
- No Grafana dashboards.
- No Azure Monitor backend storage metrics.
- No custom RuntimeClass creation beyond AKS default Pod Sandboxing support.
- No shallow or sparse Git clone modes beyond full and blobless.

## Open Follow-Ups

Future iterations can add an in-cluster Git mirror, Pushgateway support, dashboards, Azure storage backend metrics, CI thresholds, additional Git clone modes, and a real matrix expander if explicit jobs become difficult to maintain.
