package infra

import "testing"

func TestProvisionCommands(t *testing.T) {
	opts := ProvisionOptions{
		ResourceGroup:  "rg-aks-burner-test",
		Location:       "westus2",
		ParametersFile: "suites/kata-disk-perf/infra.bicepparam",
		ClusterName:    "akstest",
	}
	commands := ProvisionCommands(opts)
	if commands[0][0] != "az" || commands[0][1] != "group" || commands[0][2] != "create" {
		t.Fatalf("unexpected group command: %#v", commands[0])
	}
	if commands[1][0] != "az" || commands[1][1] != "deployment" || commands[1][2] != "group" || commands[1][3] != "create" {
		t.Fatalf("unexpected deployment command: %#v", commands[1])
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
