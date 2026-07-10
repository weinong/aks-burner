package infra

import (
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/run"
)

func TestClusterName(t *testing.T) {
	tests := []struct {
		name      string
		suiteName string
		override  string
		want      string
	}{
		{name: "derived", suiteName: "kata-io", want: "akskataio"},
		{name: "override", suiteName: "kata-io", override: "existing-cluster", want: "existing-cluster"},
		{name: "54 character boundary", suiteName: strings.Repeat("a", 51), want: "aks" + strings.Repeat("a", 51)},
		{name: "long name is hashed", suiteName: strings.Repeat("a", 52), want: "aks" + strings.Repeat("a", 42) + "-42f95bbf"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ClusterName(test.suiteName, test.override)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ClusterName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClusterNameRejectsMalformedOverrides(t *testing.T) {
	for _, override := range []string{"", "Upper", "-leading", "trailing-", "has_underscore", strings.Repeat("a", 55)} {
		if override == "" {
			continue
		}
		t.Run(override, func(t *testing.T) {
			_, err := ClusterName("demo", override)
			if err == nil || !strings.Contains(err.Error(), "invalid cluster name") {
				t.Fatalf("ClusterName() error = %v, want invalid cluster name", err)
			}
		})
	}
}

func TestValidateNodePools(t *testing.T) {
	pools := validNodePools()
	selectors := []run.NodeSelectorRequirement{{Name: "workload", Pool: "userpool", Required: true, MinNodes: 4, Labels: map[string]string{
		"perf.azure.com/node-role":    "workload",
		"kubernetes.azure.com/os-sku": "AzureLinux",
	}}}
	if err := ValidateNodePools("kata-io", pools, selectors); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNodePoolsRejectsInvalidRelationships(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func([]NodePool, []run.NodeSelectorRequirement) ([]NodePool, []run.NodeSelectorRequirement)
		wantError string
	}{
		{name: "duplicate pool", mutate: func(p []NodePool, s []run.NodeSelectorRequirement) ([]NodePool, []run.NodeSelectorRequirement) {
			return append(p, p[1]), s
		}, wantError: `suite kata-io has duplicate pool "userpool"`},
		{name: "no system pool", mutate: func(p []NodePool, s []run.NodeSelectorRequirement) ([]NodePool, []run.NodeSelectorRequirement) {
			p[0].Mode = "User"
			return p, s
		}, wantError: "suite kata-io requires at least one system pool"},
		{name: "missing selector pool", mutate: func(p []NodePool, s []run.NodeSelectorRequirement) ([]NodePool, []run.NodeSelectorRequirement) {
			s[0].Pool = "missing"
			return p, s
		}, wantError: "suite kata-io selector workload references missing pool missing"},
		{name: "insufficient count", mutate: func(p []NodePool, s []run.NodeSelectorRequirement) ([]NodePool, []run.NodeSelectorRequirement) {
			p[1].Count = 3
			return p, s
		}, wantError: "suite kata-io selector workload requires 4 nodes on pool userpool, which has 3"},
		{name: "custom label mismatch", mutate: func(p []NodePool, s []run.NodeSelectorRequirement) ([]NodePool, []run.NodeSelectorRequirement) {
			p[1].Labels["perf.azure.com/node-role"] = "other"
			return p, s
		}, wantError: "suite kata-io selector workload requires label perf.azure.com/node-role=workload on pool userpool"},
		{name: "OS label mismatch", mutate: func(p []NodePool, s []run.NodeSelectorRequirement) ([]NodePool, []run.NodeSelectorRequirement) {
			p[1].OSSKU = "Ubuntu"
			return p, s
		}, wantError: "suite kata-io selector workload requires label kubernetes.azure.com/os-sku=AzureLinux on pool userpool"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pools, selectors := test.mutate(validNodePools(), validSelectors())
			err := ValidateNodePools("kata-io", pools, selectors)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ValidateNodePools() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestParametersJSON(t *testing.T) {
	got, err := ParametersJSON("akskataio", "1.36", validNodePools(), true)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "$schema": "https://schema.management.azure.com/schemas/2019-04-01/deploymentParameters.json#",
  "contentVersion": "1.0.0.0",
  "parameters": {
    "clusterName": {
      "value": "akskataio"
    },
    "kubernetesVersion": {
      "value": "1.36"
    },
    "nodePools": {
      "value": [
        {
          "name": "systempool",
          "mode": "System",
          "count": 1,
          "vmSize": "Standard_D4s_v5",
          "osType": "Linux",
          "osSKU": "Ubuntu",
          "workloadRuntime": "OCIContainer",
          "labels": {},
          "taints": []
        },
        {
          "name": "userpool",
          "mode": "User",
          "count": 4,
          "vmSize": "Standard_D8s_v5",
          "osType": "Linux",
          "osSKU": "AzureLinux",
          "workloadRuntime": "KataMshvVmIsolation",
          "labels": {
            "perf.azure.com/node-role": "workload"
          },
          "taints": []
        }
      ]
    },
    "deployContainerRegistry": {
      "value": true
    }
  }
}
`
	if string(got) != want {
		t.Fatalf("ParametersJSON() =\n%s\nwant:\n%s", got, want)
	}
}

func validNodePools() []NodePool {
	return []NodePool{
		{Name: "systempool", Mode: "System", Count: 1, VMSize: "Standard_D4s_v5", OSType: "Linux", OSSKU: "Ubuntu", WorkloadRuntime: "OCIContainer", Labels: map[string]string{}, Taints: []string{}},
		{Name: "userpool", Mode: "User", Count: 4, VMSize: "Standard_D8s_v5", OSType: "Linux", OSSKU: "AzureLinux", WorkloadRuntime: "KataMshvVmIsolation", Labels: map[string]string{"perf.azure.com/node-role": "workload"}, Taints: []string{}},
	}
}

func validSelectors() []run.NodeSelectorRequirement {
	return []run.NodeSelectorRequirement{{Name: "workload", Pool: "userpool", Required: true, MinNodes: 4, Labels: map[string]string{
		"perf.azure.com/node-role":    "workload",
		"kubernetes.azure.com/os-sku": "AzureLinux",
	}}}
}
