package infra

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProvisionCommands(t *testing.T) {
	opts := ProvisionOptions{
		ResourceGroup: "rg-aks-burner-test",
		Location:      "westus2",
		TemplateFile:  "infra/aks/main.bicep",
		ClusterName:   "akstest",
	}
	commands := ProvisionCommands(opts, "/tmp/generated.parameters.json")
	want := [][]string{
		{"az", "group", "create", "--name", "rg-aks-burner-test", "--location", "westus2"},
		{"az", "deployment", "group", "create", "--resource-group", "rg-aks-burner-test", "--name", DeploymentName, "--template-file", "infra/aks/main.bicep", "--parameters", "@/tmp/generated.parameters.json"},
		{"az", "aks", "get-credentials", "--resource-group", "rg-aks-burner-test", "--name", "akstest", "--overwrite-existing"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("ProvisionCommands() = %#v, want %#v", commands, want)
	}
}

func TestProvisionRemovesPrivateParametersFileOnEveryExit(t *testing.T) {
	tests := []struct {
		name        string
		failCommand int
		cancel      bool
	}{
		{name: "first command fails", failCommand: 1},
		{name: "second command fails", failCommand: 2},
		{name: "third command fails", failCommand: 3},
		{name: "context canceled", cancel: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			calls := 0
			var parametersPath string
			tempDir := t.TempDir()
			runCommand := func(ctx context.Context, args []string) error {
				calls++
				for _, arg := range args {
					if strings.HasPrefix(arg, "@") {
						parametersPath = strings.TrimPrefix(arg, "@")
					}
				}
				if parametersPath == "" {
					matches, err := filepath.Glob(filepath.Join(tempDir, "aks-burner-*.parameters.json"))
					if err != nil || len(matches) != 1 {
						t.Fatalf("temporary parameters file count = %d, error = %v", len(matches), err)
					}
					parametersPath = matches[0]
				}
				{
					info, err := os.Stat(parametersPath)
					if err != nil {
						t.Fatalf("parameters file unavailable during command: %v", err)
					}
					if got := info.Mode().Perm(); got != 0o600 {
						t.Fatalf("parameters mode = %o, want 600", got)
					}
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if calls == test.failCommand {
					return errors.New("injected command failure")
				}
				return nil
			}
			err := Provision(ctx, ProvisionOptions{ResourceGroup: "rg", Location: "westus2", TemplateFile: "main.bicep", ParametersJSON: []byte(`{"parameters":{}}`), ClusterName: "aksdemo", TempDir: tempDir, RunCommand: runCommand})
			if err == nil {
				t.Fatal("Provision() error = nil")
			}
			matches, globErr := filepath.Glob(filepath.Join(tempDir, "aks-burner-*.parameters.json"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("temporary parameters files remain: %#v", matches)
			}
		})
	}
}

func TestGetCredentialsCommand(t *testing.T) {
	cmd := GetCredentialsCommand("rg-aks-burner-test", "akstest")
	want := []string{"az", "aks", "get-credentials", "--resource-group", "rg-aks-burner-test", "--name", "akstest", "--overwrite-existing"}
	if len(cmd) != len(want) {
		t.Fatalf("len = %d, want %d", len(cmd), len(want))
	}
	for i := range want {
		if cmd[i] != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, cmd[i], want[i])
		}
	}
}

func TestDeploymentOutputCommand(t *testing.T) {
	cmd := DeploymentOutputCommand("rg-aks-burner-test", DeploymentName, "containerRegistryName")
	want := []string{"az", "deployment", "group", "show", "--resource-group", "rg-aks-burner-test", "--name", DeploymentName, "--query", "properties.outputs.containerRegistryName.value", "--output", "tsv"}
	if len(cmd) != len(want) {
		t.Fatalf("len = %d, want %d", len(cmd), len(want))
	}
	for i := range want {
		if cmd[i] != want[i] {
			t.Fatalf("cmd[%d] = %q, want %q", i, cmd[i], want[i])
		}
	}
}

func containsArgSequence(args []string, want ...string) bool {
	for i := 0; i <= len(args)-len(want); i++ {
		matched := true
		for j := range want {
			if args[i+j] != want[j] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
