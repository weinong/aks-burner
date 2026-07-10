# Simplify Kata Perf Image Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run `kata-perf` with the existing tagged static pause image without provisioning ACR or building a pass-through image.

**Architecture:** Remove `requires.images` from `kata-perf`, point both modes to the existing `pause` image key, and delete the redundant Dockerfile. Requirements-driven provisioning will then pass `deployContainerRegistry=false` without suite-specific infrastructure parameters.

**Tech Stack:** YAML, Go contract tests, Bicep validation

## Global Constraints

- Retain `mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2` in `config/images.yml`.
- Do not change generic ACR build support or its unit-test fixtures.
- Do not add a manual `deployContainerRegistry` suite parameter.

---

### Task 1: Migrate kata-perf to the static image

**Files:**
- Modify: `internal/examples/examples_test.go`
- Modify: `suites/kata-perf/requirements.yml`
- Modify: `suites/kata-perf/vars/smoke.yml`
- Modify: `suites/kata-perf/vars/full.yml`
- Modify: `suites/kata-perf/infra.bicepparam`
- Delete: `suites/kata-perf/images/pause/Dockerfile`
- Modify: `README.md`

**Interfaces:**
- Consumes: static image key `pause` from `config/images.yml`
- Produces: a `kata-perf` suite with no `requires.images` declaration

- [ ] Add a contract test asserting `requires.images` is absent and both modes use `pause`.
- [ ] Run `go test ./internal/examples` and confirm the contract test fails.
- [ ] Remove the image-build declaration and ACR SKU, switch both modes, and delete the Dockerfile.
- [ ] Update the README image-build example so it no longer presents `kata-perf` as requiring a build.
- [ ] Run targeted and full tests, Bicep compilation, `list-suites`, and `git diff --check`.
- [ ] Request code review, commit, merge to local `main`, and verify the merged branch.
