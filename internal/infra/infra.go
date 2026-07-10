package infra

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type ProvisionOptions struct {
	ResourceGroup  string
	Location       string
	TemplateFile   string
	ParametersJSON []byte
	ClusterName    string
	TempDir        string
	RunCommand     CommandRunner
}

type CommandRunner func(context.Context, []string) error

const DeploymentName = "aks-burner"

func ProvisionCommands(opts ProvisionOptions, parametersPath string) [][]string {
	return [][]string{
		{"az", "group", "create", "--name", opts.ResourceGroup, "--location", opts.Location},
		{"az", "deployment", "group", "create", "--resource-group", opts.ResourceGroup, "--name", DeploymentName, "--template-file", opts.TemplateFile, "--parameters", "@" + parametersPath},
		GetCredentialsCommand(opts.ResourceGroup, opts.ClusterName),
	}
}

func Provision(ctx context.Context, opts ProvisionOptions) error {
	file, err := os.CreateTemp(opts.TempDir, "aks-burner-*.parameters.json")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(opts.ParametersJSON); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	runCommand := opts.RunCommand
	if runCommand == nil {
		runCommand = func(ctx context.Context, args []string) error {
			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
	}
	for _, args := range ProvisionCommands(opts, path) {
		if err := runCommand(ctx, args); err != nil {
			return err
		}
	}
	return nil
}

func DestroyCommand(resourceGroup string) []string {
	return []string{"az", "group", "delete", "--name", resourceGroup, "--yes"}
}

func Destroy(ctx context.Context, resourceGroup string) error {
	args := DestroyCommand(resourceGroup)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func GetCredentialsCommand(resourceGroup string, clusterName string) []string {
	return []string{"az", "aks", "get-credentials", "--resource-group", resourceGroup, "--name", clusterName, "--overwrite-existing"}
}

func GetCredentials(ctx context.Context, resourceGroup string, clusterName string) error {
	args := GetCredentialsCommand(resourceGroup, clusterName)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func DeploymentOutputCommand(resourceGroup string, deploymentName string, outputName string) []string {
	return []string{"az", "deployment", "group", "show", "--resource-group", resourceGroup, "--name", deploymentName, "--query", "properties.outputs." + outputName + ".value", "--output", "tsv"}
}

func DeploymentOutput(ctx context.Context, resourceGroup string, deploymentName string, outputName string) (string, error) {
	args := DeploymentOutputCommand(resourceGroup, deploymentName, outputName)
	data, err := exec.CommandContext(ctx, args[0], args[1:]...).Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("deployment output %q not found", outputName)
	}
	return value, nil
}
