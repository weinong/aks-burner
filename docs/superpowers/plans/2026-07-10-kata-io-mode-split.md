# Kata IO Mode Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add practical `kata-io` fio/git mode split plus fast end-to-end verification modes.

**Architecture:** Reuse the existing explicit kube-burner workload-file model. Add new mode YAML files under `suites/kata-io/vars/` and new explicit workload YAML files under `suites/kata-io/`, all sharing setup, cleanup, per-run job names, metrics, and artifact copy behavior.

**Tech Stack:** Go 1.25, YAML, kube-burner, Kubernetes Jobs/PVCs, AKS, Prometheus.

## Global Constraints

- Do not delete the cluster.
- Keep `full` for compatibility.
- Do not introduce a generic workload matrix engine.
- Do not change benchmark scripts or fio profiles.
- `fio-fast` and `git-fast` are quick end-to-end verification modes.
- `fio` and `git` are practical domain benchmark modes.

---

### Task 1: Add Mode Contract Tests

**Files:**
- Modify: `internal/examples/examples_test.go`

**Interfaces:**
- Consumes: kata-io mode files and workload files.
- Produces: contract tests that validate mode files, workload selection, and scenario domains.

- [ ] **Step 1: Add failing tests**

Add tests requiring `fio-fast`, `git-fast`, `fio`, and `git` mode files, and validating each mode points to the intended workload file.

- [ ] **Step 2: Run tests red**

Run: `go test ./internal/examples -run 'TestKataIOModesIncludeSplitModes|TestKataIOModeWorkloadFilesExist'`

Expected: FAIL because the new modes do not exist.

---

### Task 2: Add Mode And Workload Files

**Files:**
- Create: `suites/kata-io/vars/fio-fast.yml`
- Create: `suites/kata-io/vars/git-fast.yml`
- Create: `suites/kata-io/vars/fio.yml`
- Create: `suites/kata-io/vars/git.yml`
- Create: `suites/kata-io/workload-fio-fast.yml`
- Create: `suites/kata-io/workload-git-fast.yml`
- Create: `suites/kata-io/workload-fio.yml`
- Create: `suites/kata-io/workload-git.yml`

**Interfaces:**
- Consumes: existing kata-io templates and mode schema.
- Produces: four new runnable modes.

- [ ] **Step 1: Implement files**

Copy the existing mode structure and set `workloadFile` to the new workload files. Create explicit workload YAML by selecting scenarios from `workload-full.yml`.

- [ ] **Step 2: Run tests green**

Run: `go test ./internal/examples -run 'TestKataIOModesIncludeSplitModes|TestKataIOModeWorkloadFilesExist'`

Expected: PASS.

---

### Task 3: Validate Scenario Scope

**Files:**
- Modify: `internal/examples/examples_test.go`

**Interfaces:**
- Consumes: new workload files.
- Produces: tests that prevent mode drift.

- [ ] **Step 1: Add domain tests**

Add tests that assert:
- `fio-fast` has only fio, emptyDir, standard/Kata, concurrency 1.
- `git-fast` has only git, emptyDir, blobless, standard/Kata, concurrency 1.
- `fio` has only fio scenarios.
- `git` has only git scenarios.

- [ ] **Step 2: Verify tests**

Run: `go test ./internal/examples -run 'TestKataIOFastModesAreSmall|TestKataIODomainModesContainOnlyTheirDomain'`

Expected: PASS.

---

### Task 4: Verify Locally And With Fast E2E

**Files:**
- Verify all touched files.

**Interfaces:**
- Consumes: completed new modes.
- Produces: local and cluster verification evidence.

- [ ] **Step 1: Run local tests**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 2: Run fast modes**

Run: `TEST_SUITE=kata-io TEST_MODE=fio-fast RESOURCE_GROUP=rg-aks-burner-kata-io make run-suite`

Run: `TEST_SUITE=kata-io TEST_MODE=git-fast RESOURCE_GROUP=rg-aks-burner-kata-io make run-suite`

Expected: both PASS and produce `raw/metrics` plus artifacts.
