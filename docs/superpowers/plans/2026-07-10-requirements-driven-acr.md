# Requirements-Driven ACR Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provision ACR only when a suite declares `requires.images`.

**Architecture:** The runner loads `requires.images` as an optional pointer and derives a typed `DeployContainerRegistry` provisioning option. The Azure deployment command appends that value as an inline Bicep parameter after the suite parameter file, while Bicep conditionally creates ACR and its role assignment.

**Tech Stack:** Go, Azure CLI, Bicep, Go testing

## Global Constraints

- Absence of `requires.images` disables ACR provisioning.
- Presence of valid `requires.images` preserves current ACR provisioning.
- Do not rewrite suite parameter files.
- Keep direct Bicep deployments backward-compatible by defaulting the parameter to `true`.
- Do not delete an ACR left by an earlier incremental deployment; disabling controls creation in the current deployment and cleanup remains a separate destructive lifecycle concern.

---

### Task 1: Derive and pass ACR provisioning

**Files:**
- Modify: `cmd/perf-runner/main.go`
- Modify: `internal/infra/infra.go`
- Test: `cmd/perf-runner/main_test.go`
- Test: `internal/infra/infra_test.go`

**Interfaces:**
- Consumes: optional `requires.images` YAML object
- Produces: `infra.ProvisionOptions.DeployContainerRegistry bool`

- [ ] Add tests proving absent images derives `false` and present images derives `true`.
- [ ] Add tests proving the deployment command appends `deployContainerRegistry=false` or `true` after the parameter file.
- [ ] Run targeted tests and confirm they fail for the missing behavior.
- [ ] Add the minimal option, derivation helper, and command argument.
- [ ] Run targeted tests and confirm they pass.

### Task 2: Condition Bicep resources

**Files:**
- Modify: `infra/aks/main.bicep`
- Test: `internal/examples/examples_test.go`

**Interfaces:**
- Consumes: `deployContainerRegistry bool`
- Produces: conditional ACR, role assignment, and empty outputs when disabled

- [ ] Add a source-contract test for the parameter and resource conditions.
- [ ] Run the test and confirm it fails.
- [ ] Add the defaulted Bicep parameter and conditions.
- [ ] Run `az bicep build --file infra/aks/main.bicep --stdout` and all Go tests.

### Task 3: Review and integrate

**Files:**
- Review all changed files.

- [ ] Run `git diff --check`, `go test ./...`, and Bicep compilation.
- [ ] Request code review and resolve verified Critical or High findings.
- [ ] Commit the reviewed feature branch.
- [ ] Merge and verify on local `main`.
- [ ] Merge local `main` into `experiment/kata-preview-latency`, resolve its existing optional-ACR implementation in favor of requirements-derived behavior, and verify.
