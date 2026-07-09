# Destroy Wait Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `make destroy` wait until the Azure resource group delete operation completes.

**Architecture:** Keep the existing `Makefile -> perf-runner destroy -> infra.Destroy` flow. Change the `az group delete` command builder to use Azure CLI's default synchronous behavior by removing `--no-wait`, and update documentation so it no longer describes asynchronous deletion.

**Tech Stack:** Go, Azure CLI command construction, Makefile, README documentation.

## Global Constraints

- Do not run `make destroy` because it deletes Azure resources.
- Keep the change minimal; do not add custom polling unless tests show Azure CLI synchronous deletion is insufficient.
- Preserve the existing command-construction test style in `internal/infra`.

---

## File Structure

- Modify `internal/infra/destroy_test.go`: encode the desired destroy command without `--no-wait`.
- Modify `internal/infra/infra.go`: remove `--no-wait` from `DestroyCommand`.
- Modify `README.md`: describe `destroy` as waiting for deletion to complete.

### Task 1: Make Destroy Synchronous

**Files:**
- Modify: `internal/infra/destroy_test.go:5-14`
- Modify: `internal/infra/infra.go:36-38`
- Modify: `README.md:25`

**Interfaces:**
- Consumes: `DestroyCommand(resourceGroup string) []string`
- Produces: `DestroyCommand(resourceGroup string) []string` returning `[]string{"az", "group", "delete", "--name", resourceGroup, "--yes"}` so `Destroy(ctx, resourceGroup)` waits through Azure CLI default behavior.

- [ ] **Step 1: Write the failing test**

Change `internal/infra/destroy_test.go` so the expected command omits `--no-wait`:

```go
func TestDestroyCommand(t *testing.T) {
	cmd := DestroyCommand("rg-aks-burner-test")
	want := []string{"az", "group", "delete", "--name", "rg-aks-burner-test", "--yes"}
	if len(cmd) != len(want) {
		t.Fatalf("len = %d, want %d", len(cmd), len(want))
	}
	for i := range want {
		if cmd[i] != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, cmd[i], want[i])
		}
	}
}
```

- [ ] **Step 2: Run the targeted test to verify it fails**

Run: `go test ./internal/infra`

Expected: FAIL in `TestDestroyCommand` because the implementation still returns `--no-wait`.

- [ ] **Step 3: Write the minimal implementation**

Change `internal/infra/infra.go`:

```go
func DestroyCommand(resourceGroup string) []string {
	return []string{"az", "group", "delete", "--name", resourceGroup, "--yes"}
}
```

- [ ] **Step 4: Update the README behavior statement**

Change the suite lifecycle paragraph in `README.md` so the final sentence says:

```markdown
`destroy` deletes the suite resource group and waits for deletion to complete.
```

- [ ] **Step 5: Run targeted verification**

Run: `go test ./internal/infra`

Expected: PASS.

- [ ] **Step 6: Run broad verification**

Run: `go test ./...`

Expected: all packages PASS.

- [ ] **Step 7: Run Makefile verification**

Run: `make test`

Expected: `go test ./...` runs successfully through the Makefile.

- [ ] **Step 8: Commit**

```bash
git add internal/infra/destroy_test.go internal/infra/infra.go README.md docs/superpowers/plans/2026-07-09-destroy-wait.md
git commit -m "fix: wait for resource group deletion"
```

## Self-Review

- Spec coverage: The plan covers the requested `make destroy` wait behavior through the `perf-runner destroy` command path and updates documentation.
- Placeholder scan: No placeholders remain.
- Type consistency: `DestroyCommand(resourceGroup string) []string` is unchanged, so existing callers keep the same interface.
