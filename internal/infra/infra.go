package infra

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type ProvisionOptions struct {
	ResourceGroup           string
	Location                string
	ParametersFile          string
	ClusterName             string
	DeployContainerRegistry bool
}

const DeploymentName = "aks-burner"

func ProvisionCommands(opts ProvisionOptions) [][]string {
	return [][]string{
		{"az", "group", "create", "--name", opts.ResourceGroup, "--location", opts.Location},
		{"az", "deployment", "group", "create", "--resource-group", opts.ResourceGroup, "--name", DeploymentName, "--parameters", opts.ParametersFile, "location=" + opts.Location, fmt.Sprintf("deployContainerRegistry=%t", opts.DeployContainerRegistry)},
		GetCredentialsCommand(opts.ResourceGroup, opts.ClusterName),
	}
}

func Provision(ctx context.Context, opts ProvisionOptions) error {
	for _, args := range ProvisionCommands(opts) {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
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
