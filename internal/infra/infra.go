package infra

import (
	"context"
	"os"
	"os/exec"
)

type ProvisionOptions struct {
	ResourceGroup  string
	Location       string
	ParametersFile string
	ClusterName    string
}

func ProvisionCommands(opts ProvisionOptions) [][]string {
	return [][]string{
		{"az", "group", "create", "--name", opts.ResourceGroup, "--location", opts.Location},
		{"az", "deployment", "group", "create", "--resource-group", opts.ResourceGroup, "--parameters", opts.ParametersFile, "location=" + opts.Location},
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
	return []string{"az", "group", "delete", "--name", resourceGroup, "--yes", "--no-wait"}
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
