# Kata I/O Preload and Setup Error Design

## Problem

The `kata-io` suite enables kube-burner image preloading. Kube-burner v2.7.3 starts each preload image with `override command`, but the suite's benchmark image does not provide an `override` executable. The image is pulled, but every preload helper container enters `RunContainerError`, making preload noisy and unreliable.

Separately, setup-resource failures report only `exit status 1`. The setup command runner calls `exec.Cmd.Output`, and callers wrap the process error without including command stderr. This hides the Kubernetes API error needed to diagnose failures such as applying `results-pvc`.

## Design

### Preload Compatibility

Add a small executable script at `/usr/local/bin/override` in the `kata-io` benchmark image. It accepts exactly one argument, `command`, and exits successfully. All other invocations print an error to stderr and return a nonzero exit status.

This preserves the existing warm-image benchmark behavior. The actual benchmark Jobs continue to invoke `/usr/local/bin/run-fio.sh` or `/usr/local/bin/run-git-clone.sh`, so the compatibility script does not participate in benchmark execution.

The strict argument check ensures a future kube-burner behavior change fails visibly instead of being silently accepted.

### Setup Diagnostics

Change the setup command runner to capture combined stdout and stderr. When a command fails, return an error that wraps the original process error and appends nonempty trimmed command output. Wrapping preserves `errors.As` access to `*exec.ExitError`, while the rendered message exposes the actionable kubectl response.

Successful commands return their combined output; setup callers do not render successful command output. Failed commands with no output return the original process error without an empty suffix.

## Testing

- Verify the preload compatibility script is executable through `PATH`, accepts exactly `override command`, and rejects other invocations.
- Verify the benchmark Dockerfile installs the script at `/usr/local/bin/override`.
- Verify successful setup commands return combined stdout and stderr. Verify failures include combined output, trim surrounding whitespace, omit an empty suffix when no output exists, and still wrap `*exec.ExitError`.
- Run focused package tests, the full Go test suite, a Go build, and a benchmark-image build/runtime check.

## Scope

The change is limited to the `kata-io` benchmark image and the shared setup command error path. It does not disable preloading, change benchmark scenarios, alter PVC lifecycle, or generalize command execution elsewhere in the repository.
