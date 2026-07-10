package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Azure/aks-burner/internal/suite"
)

func ResolveSetupPath(suiteDir string, resource suite.SetupResource) (string, error) {
	if resource.Path == "" || filepath.IsAbs(resource.Path) {
		return "", fmt.Errorf("invalid setup path for %q: %q", resource.Name, resource.Path)
	}
	clean := filepath.Clean(resource.Path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid setup path for %q: %q", resource.Name, resource.Path)
	}
	return filepath.Join(suiteDir, clean), nil
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
		if _, err := runner(ctx, "apply", "-f", manifestPath); err != nil {
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
