package examples

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/config"
	"github.com/Azure/aks-burner/internal/run"
)

func TestKataPerfContractsValidate(t *testing.T) {
	root := filepath.Join("..", "..")
	cases := []struct {
		schema string
		file   string
	}{
		{"schemas/suite.schema.json", "suites/kata-perf/suite.yml"},
		{"schemas/requirements.schema.json", "suites/kata-perf/requirements.yml"},
		{"schemas/mode.schema.json", "suites/kata-perf/vars/smoke.yml"},
		{"schemas/mode.schema.json", "suites/kata-perf/vars/full.yml"},
	}
	for _, tc := range cases {
		if err := config.ValidateYAML(filepath.Join(root, tc.schema), filepath.Join(root, tc.file)); err != nil {
			t.Fatalf("%s failed validation against %s: %v", tc.file, tc.schema, err)
		}
	}
}

func TestKataPerfSuiteHasGenericKataIdentity(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{
		"suites/kata-perf/suite.yml",
		"suites/kata-perf/requirements.yml",
		"suites/kata-perf/infra.bicepparam",
		"suites/kata-perf/workload.yml",
		"suites/kata-perf/metrics.yml",
		"suites/kata-perf/templates/pod.yml",
		"suites/kata-perf/vars/smoke.yml",
		"suites/kata-perf/vars/full.yml",
	}
	oldIdentifiers := []string{"kata-disk-perf", "kata-disk", "akskdisk", "kdisk"}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, oldIdentifier := range oldIdentifiers {
			if strings.Contains(text, oldIdentifier) {
				t.Fatalf("%s contains old disk-specific identifier %q", file, oldIdentifier)
			}
		}
	}
}

func TestKataPerfUsesKataRuntime(t *testing.T) {
	root := filepath.Join("..", "..")
	assertContains := func(file, want string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), want) {
			t.Fatalf("%s must contain %q", file, want)
		}
	}

	assertContains("infra/aks/main.bicep", "param userNodeWorkloadRuntime string = 'OCIContainer'")
	assertContains("infra/aks/main.bicep", "'KataMshvVmIsolation'")
	assertContains("infra/aks/main.bicep", "param userNodeOsSKU string = 'Ubuntu'")
	assertContains("infra/aks/main.bicep", "osSKU: userNodeOsSKU")
	assertContains("infra/aks/main.bicep", "workloadRuntime: userNodeWorkloadRuntime")
	assertContains("suites/kata-perf/infra.bicepparam", "param userNodeOsSKU = 'AzureLinux'")
	assertContains("suites/kata-perf/infra.bicepparam", "param userNodeWorkloadRuntime = 'KataMshvVmIsolation'")
	assertContains("suites/kata-perf/templates/pod.yml", "runtimeClassName: kata-vm-isolation")
	assertContains("suites/kata-perf/templates/pod.yml", "kubernetes.azure.com/os-sku: AzureLinux")
}

func TestKataPerfRequiresAzureLinuxWorkloadNode(t *testing.T) {
	root := filepath.Join("..", "..")
	var doc struct {
		Requires run.Requirements `yaml:"requires"`
	}
	if err := config.LoadYAML(filepath.Join(root, "suites/kata-perf/requirements.yml"), &doc); err != nil {
		t.Fatal(err)
	}

	for _, selector := range doc.Requires.NodeSelectors {
		if selector.Required && selector.MinNodes >= 1 && selector.Labels["perf.azure.com/node-role"] == "workload" {
			if selector.Name != "azurelinux-workload" {
				t.Fatalf("workload node selector name = %q, want azurelinux-workload", selector.Name)
			}
			if selector.Labels["kubernetes.azure.com/os-sku"] != "AzureLinux" {
				t.Fatalf("workload node selector labels = %#v, want kubernetes.azure.com/os-sku=AzureLinux", selector.Labels)
			}
			return
		}
	}
	t.Fatalf("kata-perf requirements must contain a required node selector for at least one AzureLinux workload node: %#v", doc.Requires.NodeSelectors)
}
