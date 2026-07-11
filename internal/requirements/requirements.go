package requirements

import (
	"fmt"
	"path/filepath"

	"github.com/Azure/aks-burner/internal/acr"
	"github.com/Azure/aks-burner/internal/artifacts"
	"github.com/Azure/aks-burner/internal/config"
	"github.com/Azure/aks-burner/internal/infra"
	"github.com/Azure/aks-burner/internal/kubestatemetrics"
	"github.com/Azure/aks-burner/internal/prometheus"
	"github.com/Azure/aks-burner/internal/reporting"
	"github.com/Azure/aks-burner/internal/run"
	"github.com/Azure/aks-burner/internal/suite"
)

type Document struct {
	Suite    string `yaml:"suite"`
	Requires struct {
		Infrastructure infra.Requirements            `yaml:"infrastructure"`
		Kubernetes     run.KubernetesRequirements    `yaml:"kubernetes"`
		NodeSelectors  []run.NodeSelectorRequirement `yaml:"nodeSelectors"`
		Images         *acr.Requirements             `yaml:"images"`
		Artifacts      artifacts.Config              `yaml:"artifacts"`
		Observability  ObservabilityRequirements     `yaml:"observability"`
		Reporting      reporting.Config              `yaml:"reporting"`
	} `yaml:"requires"`
}

type ObservabilityRequirements struct {
	Prometheus       prometheus.Config       `yaml:"prometheus"`
	KubeStateMetrics kubestatemetrics.Config `yaml:"kubeStateMetrics"`
}

func Load(root, suiteName string) (Document, error) {
	if _, err := suite.Load(root, suiteName); err != nil {
		return Document{}, err
	}
	path := filepath.Join(root, "suites", suiteName, "requirements.yml")
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), path); err != nil {
		return Document{}, err
	}
	var doc Document
	if err := config.LoadYAML(path, &doc); err != nil {
		return Document{}, err
	}
	if doc.Suite != suiteName {
		return Document{}, fmt.Errorf("requirements suite %q does not match %q", doc.Suite, suiteName)
	}
	return doc, nil
}
