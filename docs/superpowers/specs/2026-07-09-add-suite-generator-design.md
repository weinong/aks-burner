# Add Suite Generator Design

## Goal

Add a fast and guided way to create a complete dummy kube-burner suite so developers can start from a working suite skeleton instead of copying an existing suite by hand.

## Context

The repository already exposes suite lifecycle actions through `perf-runner` and Make targets. Suites live under `suites/<suite>/` and include `suite.yml`, `requirements.yml`, `infra.bicepparam`, `workload.yml`, `metrics.yml`, `templates/pod.yml`, and mode files under `vars/`. The new workflow should follow those conventions and avoid adding a separate generator language or shell-heavy Make logic.

## User Interface

Add `perf-runner add-suite` with two modes:

- Fast mode: `perf-runner add-suite --suite my-suite` creates a suite using defaults.
- Guided mode: `perf-runner add-suite --guided` prompts for values and uses the same defaults when the user presses Enter.

Add Make targets:

- `TEST_SUITE=my-suite make add-suite` calls fast mode.
- `make add-suite-guided` calls guided mode.

Supported flags are `--suite`, `--description`, `--cluster-name`, `--kubernetes-version`, `--node-count`, `--node-vm-size`, `--prometheus`, `--smoke-iterations`, `--full-iterations`, and `--guided`.

## Generated Suite

The command creates:

- `suites/<suite>/suite.yml`
- `suites/<suite>/requirements.yml`
- `suites/<suite>/infra.bicepparam`
- `suites/<suite>/workload.yml`
- `suites/<suite>/metrics.yml`
- `suites/<suite>/templates/pod.yml`
- `suites/<suite>/vars/smoke.yml`
- `suites/<suite>/vars/full.yml`

Defaults mirror the existing `kata-perf` structure but replace suite-specific names with the requested suite name. Prometheus is enabled by default so the generated suite exercises the repository's normal observability path.

## Safety

The suite name uses the existing `suite.ValidName` validation. The command refuses to overwrite an existing `suites/<suite>` directory. Generated paths are always rooted under `suites/<suite>`.

No `--force` flag is included initially. Overwrite behavior can be added later if there is a real workflow need.

## Testing

Tests cover fast generation, overwrite refusal, invalid suite names, and guided input defaults. Documentation and Make help should include both workflows.
