package run

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Azure/aks-burner/internal/suite"
)

func ResolveSetupPath(suiteDir string, resource suite.SetupResource) (string, error) {
	if resource.Path == "" || strings.Contains(resource.Path, `\`) || strings.HasPrefix(resource.Path, "/") || hasWindowsDrivePrefix(resource.Path) {
		return "", fmt.Errorf("invalid setup path for %q: %q", resource.Name, resource.Path)
	}
	for _, segment := range strings.Split(resource.Path, "/") {
		if segment == ".." {
			return "", fmt.Errorf("invalid setup path for %q: %q", resource.Name, resource.Path)
		}
	}
	clean := path.Clean(resource.Path)
	if clean == "." {
		return "", fmt.Errorf("invalid setup path for %q: %q", resource.Name, resource.Path)
	}
	joined := filepath.Join(suiteDir, filepath.FromSlash(clean))
	absSuiteDir, err := filepath.Abs(suiteDir)
	if err != nil {
		return "", err
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absSuiteDir, absJoined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid setup path for %q: %q", resource.Name, resource.Path)
	}
	return joined, nil
}

func hasWindowsDrivePrefix(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	letter := path[0]
	return (letter >= 'A' && letter <= 'Z') || (letter >= 'a' && letter <= 'z')
}

func WaitRuleArgs(rule suite.WaitRule) ([]string, error) {
	if rule.Resource == "" {
		return nil, fmt.Errorf("wait rule %q requires resource", rule.Kind)
	}
	var args []string
	switch rule.Kind {
	case "exists":
		if rule.Timeout == "" {
			args = []string{"get", rule.Resource}
		} else {
			args = []string{"wait", rule.Resource, "--for=create", "--timeout", rule.Timeout}
		}
	case "rollout":
		args = []string{"rollout", "status", rule.Resource}
		if rule.Timeout != "" {
			args = append(args, "--timeout", rule.Timeout)
		}
	case "condition":
		if rule.Condition == "" {
			return nil, fmt.Errorf("condition wait for %q requires condition", rule.Resource)
		}
		args = []string{"wait", rule.Resource, "--for=condition=" + rule.Condition}
		if rule.Timeout != "" {
			args = append(args, "--timeout", rule.Timeout)
		}
	default:
		return nil, fmt.Errorf("unsupported setup wait kind %q", rule.Kind)
	}
	if rule.Namespace != "" {
		args = append(args, "--namespace", rule.Namespace)
	}
	return args, nil
}

func ApplySetup(ctx context.Context, suiteDir string, setup suite.Setup, runner KubectlRunner) error {
	for _, resource := range setup.Resources {
		manifestPath, err := ResolveSetupPath(suiteDir, resource)
		if err != nil {
			return err
		}
		if _, err := os.Stat(manifestPath); err != nil {
			return fmt.Errorf("setup manifest for %q not found at %s: %w", resource.Name, manifestPath, err)
		}
		resolvedSuiteDir, err := filepath.EvalSymlinks(suiteDir)
		if err != nil {
			return err
		}
		resolvedManifestPath, err := filepath.EvalSymlinks(manifestPath)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(resolvedSuiteDir, resolvedManifestPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("invalid setup path for %q: %q", resource.Name, resource.Path)
		}
		// The suite directory is assumed stable during a run. This check prevents
		// configured paths and symlink targets from escaping the suite at apply time.
		if _, err := runner(ctx, "apply", "-f", resolvedManifestPath); err != nil {
			return fmt.Errorf("apply setup resource %s: %w", resource.Name, err)
		}
		for _, wait := range resource.Wait {
			args, err := WaitRuleArgs(wait)
			if err != nil {
				return err
			}
			if _, err := runner(ctx, args...); err != nil {
				return fmt.Errorf("wait for setup resource %s: %w", resource.Name, err)
			}
		}
	}
	return nil
}
