# Per-User Azure Resource Names Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Derive default Azure resource names from the signed-in user's normalized UPN alias so different users can operate the same suite without sharing Azure resources.

**Architecture:** Add an Azure identity lookup and alias normalizer to `internal/infra`, then centralize lifecycle default-name resolution in `perf-runner`. Explicit resource-group and cluster-name values bypass default derivation, while Makefile invocations omit implicit names and let the CLI apply the same behavior as direct usage.

**Tech Stack:** Go, Azure CLI, GNU Make, Bicep, Go table-driven tests

## Tasks

1. Add test-driven Azure UPN lookup and alias normalization in `internal/infra`.
2. Add test-driven centralized resource-group and cluster-name resolution.
3. Wire provision, managed run-suite, and destroy before Azure side effects.
4. Remove the shared Makefile default and document the new behavior.
5. Run targeted/full verification, review, commit, merge locally, and verify again.

## Verification

```bash
go test ./internal/infra -count=1
go test ./cmd/perf-runner -count=1
go test ./... -count=1
go build ./cmd/perf-runner
make -n provision TEST_SUITE=kata-perf
make -n run-suite TEST_SUITE=kata-perf
make -n destroy TEST_SUITE=kata-perf
```
