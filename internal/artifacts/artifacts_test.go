package artifacts

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/kubetarget"
)

func TestCopyPodManifestMountsConfiguredPVC(t *testing.T) {
	cfg := Config{Namespace: "kata-io", PVCName: "kata-io-results", MountPath: "/results", CopyImage: "busybox:1.36.1"}
	manifest := CopyPodManifest(cfg, "kata-io-artifact-copy")
	for _, want := range []string{
		"name: kata-io-artifact-copy",
		"namespace: kata-io",
		"image: busybox:1.36.1",
		"command: [/bin/sh, -c, sleep 3600]",
		"claimName: kata-io-results",
		"mountPath: /results",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("manifest missing %q:\n%s", want, manifest)
		}
	}
}

func TestCopyWithRunnerRunsApplyWaitCopyDelete(t *testing.T) {
	cfg := Config{Enabled: true, Namespace: "kata-io", PVCName: "kata-io-results", MountPath: "/results", CopyImage: "busybox:1.36.1"}
	calls := []string{}
	runner := func(ctx context.Context, stdin string, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	if err := CopyWithRunner(context.Background(), cfg, "/tmp/out", runner); err != nil {
		t.Fatal(err)
	}
	podName := copyPodNameFromCalls(t, calls)
	if podName == "kata-io-artifact-copy" {
		t.Fatalf("CopyWithRunner used fixed pod name %q", podName)
	}
	want := []string{
		"apply -f -",
		"wait --for=condition=Ready pod/" + podName + " -n kata-io --timeout=2m",
		"cp kata-io/" + podName + ":/results/. /tmp/out",
		"delete pod " + podName + " -n kata-io --ignore-not-found=true",
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls[%d] = %q, want %q", i, calls[i], want[i])
		}
	}
}

func TestCopyWithTargetRunnerTargetsApplyWaitCopyAndDelete(t *testing.T) {
	cfg := Config{Enabled: true, Namespace: "kata-io", PVCName: "results", MountPath: "/results", CopyImage: "busybox:test"}
	var calls [][]string
	runner := func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		if args[len(args)-1] == "--timeout=2m" {
			return errors.New("wait failed")
		}
		return nil
	}

	err := copyWithTargetRunnerAndPodName(context.Background(), kubetarget.Target{Context: "preview"}, cfg, t.TempDir(), runner, "copy-pod")
	if err == nil || !strings.Contains(err.Error(), "wait failed") {
		t.Fatalf("copy error = %v, want wait failure", err)
	}
	want := [][]string{
		{"kubectl", "--context", "preview", "apply", "-f", "-"},
		{"kubectl", "--context", "preview", "wait", "--for=condition=Ready", "pod/copy-pod", "-n", "kata-io", "--timeout=2m"},
		{"kubectl", "--context", "preview", "delete", "pod", "copy-pod", "-n", "kata-io", "--ignore-not-found=true"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("commands = %#v, want %#v", calls, want)
	}
}

func TestCopyWithTargetRunnerTargetsSuccessfulLifecycle(t *testing.T) {
	cfg := Config{Enabled: true, Namespace: "kata-io", PVCName: "results", MountPath: "/results", CopyImage: "busybox:test"}
	var calls [][]string
	runner := func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	if err := copyWithTargetRunnerAndPodName(context.Background(), kubetarget.Target{Context: "preview"}, cfg, t.TempDir(), runner, "copy-pod"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %#v, want apply, wait, copy, delete", calls)
	}
	wantPrefix := []string{"kubectl", "--context", "preview"}
	for _, call := range calls {
		if len(call) < len(wantPrefix) || !reflect.DeepEqual(call[:len(wantPrefix)], wantPrefix) {
			t.Fatalf("command = %#v, want prefix %#v", call, wantPrefix)
		}
	}
}

func TestCopySubpathWithRunnerCopiesOnlyRequestedSubpath(t *testing.T) {
	cfg := Config{Enabled: true, Namespace: "kata-io", PVCName: "kata-io-results", MountPath: "/results", CopyImage: "busybox:1.36.1"}
	calls := []string{}
	runner := func(ctx context.Context, stdin string, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	}
	if err := CopySubpathWithRunner(context.Background(), cfg, "/tmp/out", "kata-io-full-20260709T010203.000000004Z", runner); err != nil {
		t.Fatal(err)
	}
	podName := copyPodNameFromCalls(t, calls)
	wantCopy := "cp kata-io/" + podName + ":/results/kata-io-full-20260709T010203.000000004Z/. /tmp/out"
	if len(calls) != 4 {
		t.Fatalf("calls = %#v, want 4 calls", calls)
	}
	if calls[2] != wantCopy {
		t.Fatalf("copy call = %q, want %q", calls[2], wantCopy)
	}
}

func TestCopySubpathWithRunnerRejectsUnsafeSubpath(t *testing.T) {
	cfg := Config{Enabled: true, Namespace: "kata-io", PVCName: "kata-io-results", MountPath: "/results", CopyImage: "busybox:test"}
	unsafeSubpaths := []string{"", ".", "..", "../old-run", "run/child", `run\child`, "run id"}
	for _, subpath := range unsafeSubpaths {
		t.Run(subpath, func(t *testing.T) {
			called := false
			err := CopySubpathWithRunner(context.Background(), cfg, filepath.Join(t.TempDir(), "out"), subpath, func(ctx context.Context, stdin string, args ...string) error {
				called = true
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "invalid artifact subpath") {
				t.Fatalf("CopySubpathWithRunner() error = %v, want invalid artifact subpath", err)
			}
			if called {
				t.Fatalf("runner called for unsafe subpath %q", subpath)
			}
		})
	}
}

func TestCopyWithRunnerReturnsCleanupErrorWhenCopySucceeds(t *testing.T) {
	cfg := Config{Enabled: true, Namespace: "kata-io", PVCName: "kata-io-results", MountPath: "/results", CopyImage: "busybox:test"}
	cleanupErr := errors.New("cleanup failed")
	runner := func(ctx context.Context, stdin string, args ...string) error {
		if len(args) >= 2 && args[0] == "delete" && args[1] == "pod" {
			return cleanupErr
		}
		return nil
	}

	err := CopyWithRunner(context.Background(), cfg, filepath.Join(t.TempDir(), "out"), runner)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("CopyWithRunner() error = %v, want cleanup error", err)
	}
}

func TestCopyWithRunnerIncludesCleanupContextWhenCopyFails(t *testing.T) {
	cfg := Config{Enabled: true, Namespace: "kata-io", PVCName: "kata-io-results", MountPath: "/results", CopyImage: "busybox:test"}
	copyErr := errors.New("copy failed")
	cleanupErr := errors.New("cleanup failed")
	runner := func(ctx context.Context, stdin string, args ...string) error {
		switch {
		case len(args) >= 1 && args[0] == "cp":
			return copyErr
		case len(args) >= 2 && args[0] == "delete" && args[1] == "pod":
			return cleanupErr
		default:
			return nil
		}
	}

	err := CopyWithRunner(context.Background(), cfg, filepath.Join(t.TempDir(), "out"), runner)
	if !errors.Is(err, copyErr) || !strings.Contains(fmt.Sprint(err), "cleanup failed") {
		t.Fatalf("CopyWithRunner() error = %v, want primary copy error with cleanup context", err)
	}
}

func copyPodNameFromCalls(t *testing.T, calls []string) string {
	t.Helper()
	for _, call := range calls {
		if strings.HasPrefix(call, "wait --for=condition=Ready pod/") {
			parts := strings.Split(call, " ")
			if len(parts) >= 3 {
				return strings.TrimPrefix(parts[2], "pod/")
			}
		}
	}
	t.Fatalf("no wait call found: %#v", calls)
	return ""
}
