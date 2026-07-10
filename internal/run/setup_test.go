package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/kubetarget"
	"github.com/Azure/aks-burner/internal/suite"
)

var setupTestTarget = kubetarget.Target{Context: "preview"}

func TestResolveSetupPathRejectsUnsafePaths(t *testing.T) {
	suiteDir := t.TempDir()
	unsafe := []string{
		"/tmp/runtimeclass.yml",
		"../runtimeclass.yml",
		"setup/../../runtimeclass.yml",
		`C:\setup\runtimeclass.yml`,
		`C:/setup/runtimeclass.yml`,
		`setup\..\runtimeclass.yml`,
		"setup/../runtimeclass.yml",
	}
	for _, path := range unsafe {
		_, err := ResolveSetupPath(suiteDir, suite.SetupResource{Name: "bad", Path: path})
		if err == nil || !strings.Contains(err.Error(), "invalid setup path") {
			t.Fatalf("ResolveSetupPath(%q) error = %v, want invalid setup path", path, err)
		}
	}
}

func TestResolveSetupPathAcceptsSuiteRelativePath(t *testing.T) {
	suiteDir := t.TempDir()
	got, err := ResolveSetupPath(suiteDir, suite.SetupResource{Name: "runtime", Path: "setup/runtimeclass.yml"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	if got != want {
		t.Fatalf("ResolveSetupPath() = %q, want %q", got, want)
	}
}

func TestWaitRuleArgs(t *testing.T) {
	cases := []struct {
		name string
		rule suite.WaitRule
		want []string
	}{
		{
			name: "exists cluster scoped",
			rule: suite.WaitRule{Kind: "exists", Resource: "runtimeclass/custom-kata"},
			want: []string{"get", "runtimeclass/custom-kata"},
		},
		{
			name: "exists namespaced",
			rule: suite.WaitRule{Kind: "exists", Resource: "configmap/node-prep", Namespace: "kube-system"},
			want: []string{"get", "configmap/node-prep", "--namespace", "kube-system"},
		},
		{
			name: "exists with timeout",
			rule: suite.WaitRule{Kind: "exists", Resource: "runtimeclass/custom-kata", Timeout: "1m"},
			want: []string{"wait", "runtimeclass/custom-kata", "--for=create", "--timeout", "1m"},
		},
		{
			name: "rollout",
			rule: suite.WaitRule{Kind: "rollout", Resource: "daemonset/node-prep", Namespace: "kube-system", Timeout: "10m"},
			want: []string{"rollout", "status", "daemonset/node-prep", "--timeout", "10m", "--namespace", "kube-system"},
		},
		{
			name: "condition",
			rule: suite.WaitRule{Kind: "condition", Resource: "pod/node-prep-check", Namespace: "default", Condition: "Ready", Timeout: "5m"},
			want: []string{"wait", "pod/node-prep-check", "--for=condition=Ready", "--timeout", "5m", "--namespace", "default"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := WaitRuleArgs(tc.rule)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("WaitRuleArgs() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestWaitRuleArgsRejectsInvalidRules(t *testing.T) {
	cases := []suite.WaitRule{
		{Kind: "sleep", Resource: "daemonset/node-prep"},
		{Kind: "exists"},
		{Kind: "condition", Resource: "pod/node-prep-check"},
	}
	for _, rule := range cases {
		if _, err := WaitRuleArgs(rule); err == nil {
			t.Fatalf("WaitRuleArgs(%#v) returned nil error", rule)
		}
	}
}

func TestApplySetupAppliesResourcesAndWaitsInOrder(t *testing.T) {
	suiteDir := t.TempDir()
	manifestPath := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	if err := ensureFile(manifestPath); err != nil {
		t.Fatal(err)
	}
	setup := suite.Setup{Resources: []suite.SetupResource{{
		Name: "kata-runtimeclass",
		Path: "setup/runtimeclass.yml",
		Wait: []suite.WaitRule{{Kind: "exists", Resource: "runtimeclass/custom-kata"}},
	}}}
	var calls [][]string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte("ok"), nil
	}

	if err := applySetup(context.Background(), setupTestTarget, suiteDir, setup, runner); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"kubectl", "--context", "preview", "apply", "-f", manifestPath},
		{"kubectl", "--context", "preview", "get", "runtimeclass/custom-kata"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("kubectl calls = %#v, want %#v", calls, want)
	}
}

func TestApplySetupWithEmptyTargetPreservesKubectlCommand(t *testing.T) {
	suiteDir := t.TempDir()
	manifestPath := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	if err := ensureFile(manifestPath); err != nil {
		t.Fatal(err)
	}
	setup := suite.Setup{Resources: []suite.SetupResource{{Name: "kata-runtimeclass", Path: "setup/runtimeclass.yml"}}}
	var command []string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		command = append([]string(nil), args...)
		return []byte("ok"), nil
	}

	if err := applySetup(context.Background(), kubetarget.Target{}, suiteDir, setup, runner); err != nil {
		t.Fatal(err)
	}
	want := []string{"kubectl", "apply", "-f", manifestPath}
	if !reflect.DeepEqual(command, want) {
		t.Fatalf("kubectl command = %#v, want %#v", command, want)
	}
}

func TestApplySetupFailsBeforeWaitWhenApplyFails(t *testing.T) {
	suiteDir := t.TempDir()
	manifestPath := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	if err := ensureFile(manifestPath); err != nil {
		t.Fatal(err)
	}
	setup := suite.Setup{Resources: []suite.SetupResource{{
		Name: "kata-runtimeclass",
		Path: "setup/runtimeclass.yml",
		Wait: []suite.WaitRule{{Kind: "exists", Resource: "runtimeclass/custom-kata"}},
	}}}
	var calls [][]string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return nil, errors.New("apply failed")
	}

	err := applySetup(context.Background(), setupTestTarget, suiteDir, setup, runner)
	if err == nil || !strings.Contains(err.Error(), "apply setup resource kata-runtimeclass") {
		t.Fatalf("ApplySetup() error = %v, want apply context", err)
	}
	want := [][]string{{"kubectl", "--context", "preview", "apply", "-f", manifestPath}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("kubectl calls = %#v, want %#v", calls, want)
	}
}

func TestApplySetupFailsWhenManifestMissing(t *testing.T) {
	suiteDir := t.TempDir()
	setup := suite.Setup{Resources: []suite.SetupResource{{Name: "missing", Path: "setup/missing.yml"}}}
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		t.Fatalf("runner should not be called for missing manifest: %#v", args)
		return nil, nil
	}

	err := applySetup(context.Background(), setupTestTarget, suiteDir, setup, runner)
	if err == nil || !strings.Contains(err.Error(), "setup manifest") {
		t.Fatalf("ApplySetup() error = %v, want missing manifest error", err)
	}
}

func TestApplySetupRejectsSymlinkManifestOutsideSuiteDir(t *testing.T) {
	suiteDir := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "runtimeclass.yml")
	if err := ensureFile(outsidePath); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, manifestPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	setup := suite.Setup{Resources: []suite.SetupResource{{Name: "linked", Path: "setup/runtimeclass.yml"}}}
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		t.Fatalf("runner should not be called for symlink outside suiteDir: %#v", args)
		return nil, nil
	}

	err := applySetup(context.Background(), setupTestTarget, suiteDir, setup, runner)
	if err == nil || !strings.Contains(err.Error(), "invalid setup path") {
		t.Fatalf("ApplySetup() error = %v, want invalid setup path", err)
	}
}

func TestApplySetupAppliesResolvedSymlinkManifestPath(t *testing.T) {
	suiteDir := t.TempDir()
	targetPath := filepath.Join(suiteDir, "setup", "actual", "runtimeclass.yml")
	if err := ensureFile(targetPath); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	if err := os.Symlink(targetPath, manifestPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resolvedManifestPath, err := filepath.EvalSymlinks(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	setup := suite.Setup{Resources: []suite.SetupResource{{Name: "linked", Path: "setup/runtimeclass.yml"}}}
	var calls [][]string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte("ok"), nil
	}

	if err := applySetup(context.Background(), setupTestTarget, suiteDir, setup, runner); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"kubectl", "--context", "preview", "apply", "-f", resolvedManifestPath}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("kubectl calls = %#v, want %#v", calls, want)
	}
}

func TestApplySetupFailsBeforeNextResourceWhenWaitFails(t *testing.T) {
	suiteDir := t.TempDir()
	firstPath := filepath.Join(suiteDir, "setup", "runtimeclass.yml")
	secondPath := filepath.Join(suiteDir, "setup", "daemonset.yml")
	if err := ensureFile(firstPath); err != nil {
		t.Fatal(err)
	}
	if err := ensureFile(secondPath); err != nil {
		t.Fatal(err)
	}
	setup := suite.Setup{Resources: []suite.SetupResource{
		{Name: "kata-runtimeclass", Path: "setup/runtimeclass.yml", Wait: []suite.WaitRule{{Kind: "exists", Resource: "runtimeclass/custom-kata"}}},
		{Name: "node-prep", Path: "setup/daemonset.yml"},
	}}
	var calls [][]string
	runner := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"kubectl", "--context", "preview", "get", "runtimeclass/custom-kata"}) {
			return nil, errors.New("not found")
		}
		return []byte("ok"), nil
	}

	err := applySetup(context.Background(), setupTestTarget, suiteDir, setup, runner)
	if err == nil || !strings.Contains(err.Error(), "wait for setup resource kata-runtimeclass") {
		t.Fatalf("ApplySetup() error = %v, want wait context", err)
	}
	want := [][]string{
		{"kubectl", "--context", "preview", "apply", "-f", firstPath},
		{"kubectl", "--context", "preview", "get", "runtimeclass/custom-kata"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("kubectl calls = %#v, want %#v", calls, want)
	}
}

func ensureFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("kind: RuntimeClass\n"), 0o644)
}
