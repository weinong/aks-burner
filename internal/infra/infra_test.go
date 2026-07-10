package infra

import "testing"

func TestProvisionCommands(t *testing.T) {
	opts := ProvisionOptions{
		ResourceGroup:           "rg-aks-burner-test",
		Location:                "westus2",
		ParametersFile:          "suites/kata-perf/infra.bicepparam",
		ClusterName:             "akstest",
		DeployContainerRegistry: true,
	}
	commands := ProvisionCommands(opts)
	if commands[0][0] != "az" || commands[0][1] != "group" || commands[0][2] != "create" {
		t.Fatalf("unexpected group command: %#v", commands[0])
	}
	if commands[1][0] != "az" || commands[1][1] != "deployment" || commands[1][2] != "group" || commands[1][3] != "create" {
		t.Fatalf("unexpected deployment command: %#v", commands[1])
	}
	if !containsArgSequence(commands[1], "--name", DeploymentName) {
		t.Fatalf("deployment command missing explicit name %q: %#v", DeploymentName, commands[1])
	}
	if got := commands[1][len(commands[1])-1]; got != "deployContainerRegistry=true" {
		t.Fatalf("deployment command last argument = %q, want deployContainerRegistry=true", got)
	}
	for _, arg := range commands[1] {
		if arg == "--template-file" {
			t.Fatalf("deployment command must use .bicepparam directly without --template-file: %#v", commands[1])
		}
	}
	if commands[2][0] != "az" || commands[2][1] != "aks" || commands[2][2] != "get-credentials" {
		t.Fatalf("unexpected credentials command: %#v", commands[2])
	}
}

func TestProvisionCommandsDisablesContainerRegistryAfterParameterFile(t *testing.T) {
	commands := ProvisionCommands(ProvisionOptions{
		ResourceGroup:  "rg-aks-burner-test",
		Location:       "westus2",
		ParametersFile: "suites/generated/infra.bicepparam",
		ClusterName:    "akstest",
	})
	deployment := commands[1]
	parametersIndex := -1
	for i, arg := range deployment {
		if arg == "--parameters" {
			parametersIndex = i
			break
		}
	}
	if parametersIndex < 0 || parametersIndex+1 >= len(deployment) {
		t.Fatalf("deployment command missing parameter file: %#v", deployment)
	}
	if got := deployment[len(deployment)-1]; got != "deployContainerRegistry=false" {
		t.Fatalf("deployment command last argument = %q, want deployContainerRegistry=false", got)
	}
	if len(deployment)-1 <= parametersIndex+1 {
		t.Fatalf("derived parameter must follow parameter file: %#v", deployment)
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
