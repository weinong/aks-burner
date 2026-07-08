package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Azure/aks-burner/internal/config"
	"github.com/Azure/aks-burner/internal/infra"
	"github.com/Azure/aks-burner/internal/prometheus"
	"github.com/Azure/aks-burner/internal/repo"
	runpkg "github.com/Azure/aks-burner/internal/run"
	"github.com/Azure/aks-burner/internal/suite"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: perf-runner <list-suites|provision|run-suite|destroy> ...")
	}
	switch args[0] {
	case "list-suites":
		return listSuites()
	case "provision":
		return provision(args[1:])
	case "run-suite":
		return runSuite(args[1:])
	case "destroy":
		return destroy(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func destroy(args []string) error {
	fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
	suiteName := fs.String("suite", "", "suite name")
	resourceGroup := fs.String("resource-group", "", "Azure resource group")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *suiteName == "" || *resourceGroup == "" {
		return fmt.Errorf("usage: perf-runner destroy --suite SUITE --resource-group RG")
	}
	return infra.Destroy(context.Background(), *resourceGroup)
}

func runSuite(args []string) error {
	fs := flag.NewFlagSet("run-suite", flag.ContinueOnError)
	suiteName := fs.String("suite", "", "suite name")
	modeName := fs.String("mode", "smoke", "mode")
	resourceGroup := fs.String("resource-group", "", "Azure resource group")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *suiteName == "" || *resourceGroup == "" {
		return fmt.Errorf("usage: perf-runner run-suite --suite SUITE --mode MODE --resource-group RG")
	}
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	_ = resourceGroup
	reqPath := filepath.Join(root, "suites", *suiteName, "requirements.yml")
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), reqPath); err != nil {
		return err
	}
	var req struct {
		Requires struct {
			Observability struct {
				Prometheus prometheus.Config `yaml:"prometheus"`
			} `yaml:"observability"`
		} `yaml:"requires"`
	}
	if err := config.LoadYAML(reqPath, &req); err != nil {
		return err
	}
	images, err := config.LoadImages(filepath.Join(root, "config", "images.yml"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if req.Requires.Observability.Prometheus.Required && req.Requires.Observability.Prometheus.Install {
		prometheusImage, err := config.ResolveImage(images, req.Requires.Observability.Prometheus.ImageKey)
		if err != nil {
			return err
		}
		if err := prometheus.Install(ctx, filepath.Join(root, "observability", "prometheus", "prometheus.yaml"), prometheusImage); err != nil {
			return err
		}
	}
	prometheusURL := ""
	if req.Requires.Observability.Prometheus.Required {
		portForwardCtx, portForwardCancel := context.WithCancel(ctx)
		cmd, endpoint, err := prometheus.PortForward(portForwardCtx, req.Requires.Observability.Prometheus)
		if err != nil {
			portForwardCancel()
			return err
		}
		defer func() { _ = prometheus.StopPortForward(portForwardCancel, cmd) }()
		if err := prometheus.WaitReady(portForwardCtx, endpoint); err != nil {
			return err
		}
		prometheusURL = endpoint
	}
	var workload map[string]any
	if err := config.LoadYAML(filepath.Join(root, "suites", *suiteName, "workload.yml"), &workload); err != nil {
		return err
	}
	var mode runpkg.Mode
	modePath := filepath.Join(root, "suites", *suiteName, "vars", *modeName+".yml")
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "mode.schema.json"), modePath); err != nil {
		return err
	}
	if err := config.LoadYAML(modePath, &mode); err != nil {
		return err
	}
	runDir, err := runpkg.CreateRunDir(*suiteName, *modeName)
	if err != nil {
		return err
	}
	suiteDir := filepath.Join(root, "suites", *suiteName)
	if err := runpkg.CopyRenderAssets(suiteDir, runDir); err != nil {
		return err
	}
	rendered, err := runpkg.RenderWorkload(workload, mode, images, prometheusURL)
	if err != nil {
		return err
	}
	workloadPath := filepath.Join(runDir, "rendered", "workload.yml")
	if err := config.WriteYAML(workloadPath, rendered); err != nil {
		return err
	}
	return runpkg.ExecuteKubeBurner(workloadPath, filepath.Join(runDir, "logs", "kube-burner.log"))
}

func provision(args []string) error {
	fs := flag.NewFlagSet("provision", flag.ContinueOnError)
	suiteName := fs.String("suite", "", "suite name")
	resourceGroup := fs.String("resource-group", "", "Azure resource group")
	location := fs.String("location", "", "Azure location")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *suiteName == "" || *resourceGroup == "" || *location == "" {
		return fmt.Errorf("usage: perf-runner provision --suite SUITE --resource-group RG --location LOCATION")
	}
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	var req struct {
		Requires struct {
			Infrastructure struct {
				Bicep struct {
					Template   string `yaml:"template"`
					Parameters string `yaml:"parameters"`
				} `yaml:"bicep"`
			} `yaml:"infrastructure"`
		} `yaml:"requires"`
	}
	reqPath := filepath.Join(root, "suites", *suiteName, "requirements.yml")
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), reqPath); err != nil {
		return err
	}
	if err := config.LoadYAML(reqPath, &req); err != nil {
		return err
	}
	parametersPath := filepath.Join(root, req.Requires.Infrastructure.Bicep.Parameters)
	clusterName, err := readBicepParamString(parametersPath, "clusterName")
	if err != nil {
		return err
	}
	return infra.Provision(context.Background(), infra.ProvisionOptions{
		ResourceGroup:  *resourceGroup,
		Location:       *location,
		ParametersFile: parametersPath,
		ClusterName:    clusterName,
	})
}

func readBicepParamString(path string, name string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	pattern := regexp.MustCompile(`(?m)^param\s+` + regexp.QuoteMeta(name) + `\s*=\s*'([^']+)'\s*$`)
	matches := pattern.FindStringSubmatch(string(data))
	if len(matches) != 2 {
		return "", fmt.Errorf("parameter %s not found in %s", name, path)
	}
	return matches[1], nil
}

func listSuites() error {
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	suites, err := suite.List(root)
	if err != nil {
		return err
	}
	for _, cfg := range suites {
		fmt.Printf("%s\t%s\n", cfg.Name, cfg.Description)
	}
	return nil
}
