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
	"github.com/Azure/aks-burner/internal/repo"
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
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
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
