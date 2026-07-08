package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
	allowNonDefaultResourceGroup := fs.Bool("allow-non-default-resource-group", false, "allow deleting a resource group outside the default suite naming convention")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *suiteName == "" || *resourceGroup == "" {
		return fmt.Errorf("usage: perf-runner destroy --suite SUITE --resource-group RG [--allow-non-default-resource-group]")
	}
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	if _, err := suite.Load(root, *suiteName); err != nil {
		return err
	}
	if err := validateDestroyTarget(*suiteName, *resourceGroup, *allowNonDefaultResourceGroup); err != nil {
		return err
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
	if !suite.ValidName(*suiteName) || !suite.ValidName(*modeName) {
		return fmt.Errorf("invalid suite or mode name")
	}
	reqPath, err := resolveSuitePath(root, *suiteName, "requirements.yml")
	if err != nil {
		return err
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), reqPath); err != nil {
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
			Kubernetes    runpkg.KubernetesRequirements    `yaml:"kubernetes"`
			NodeSelectors []runpkg.NodeSelectorRequirement `yaml:"nodeSelectors"`
			Observability struct {
				Prometheus prometheus.Config `yaml:"prometheus"`
			} `yaml:"observability"`
		} `yaml:"requires"`
	}
	if err := config.LoadYAML(reqPath, &req); err != nil {
		return err
	}
	parametersPath, err := resolveSuitePath(root, *suiteName, req.Requires.Infrastructure.Bicep.Parameters)
	if err != nil {
		return err
	}
	if _, err := resolveRepoPath(root, req.Requires.Infrastructure.Bicep.Template); err != nil {
		return err
	}
	clusterName, err := readBicepParamString(parametersPath, "clusterName")
	if err != nil {
		return err
	}
	images, err := config.LoadImages(filepath.Join(root, "config", "images.yml"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := infra.GetCredentials(ctx, *resourceGroup, clusterName); err != nil {
		return err
	}
	if err := runpkg.ValidateRequirements(ctx, runpkg.Requirements{Kubernetes: req.Requires.Kubernetes, NodeSelectors: req.Requires.NodeSelectors}, runpkg.KubectlOutput); err != nil {
		return err
	}
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
		if shouldWaitPrometheusRollout(req.Requires.Observability.Prometheus.Required, req.Requires.Observability.Prometheus.Install) {
			if err := prometheus.WaitRollout(ctx, req.Requires.Observability.Prometheus); err != nil {
				return err
			}
		}
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
	workloadFile, err := resolveSuitePath(root, *suiteName, "workload.yml")
	if err != nil {
		return err
	}
	if err := config.LoadYAML(workloadFile, &workload); err != nil {
		return err
	}
	var mode runpkg.Mode
	modePath, err := resolveSuitePath(root, *suiteName, filepath.Join("vars", *modeName+".yml"))
	if err != nil {
		return err
	}
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
	if err := runpkg.WriteMetadata(runDir, runpkg.Metadata{Suite: *suiteName, Mode: *modeName, Timestamp: time.Now().UTC().Format(time.RFC3339), ResourceGroup: *resourceGroup, ClusterName: clusterName, Images: images}); err != nil {
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

func shouldWaitPrometheusRollout(required bool, install bool) bool {
	return required && install
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
	if !suite.ValidName(*suiteName) {
		return fmt.Errorf("invalid suite name %q", *suiteName)
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
	reqPath, err := resolveSuitePath(root, *suiteName, "requirements.yml")
	if err != nil {
		return err
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), reqPath); err != nil {
		return err
	}
	if err := config.LoadYAML(reqPath, &req); err != nil {
		return err
	}
	parametersPath, err := resolveSuitePath(root, *suiteName, req.Requires.Infrastructure.Bicep.Parameters)
	if err != nil {
		return err
	}
	if _, err := resolveRepoPath(root, req.Requires.Infrastructure.Bicep.Template); err != nil {
		return err
	}
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

func validateDestroyTarget(suiteName string, resourceGroup string, allowNonDefaultResourceGroup bool) error {
	defaultResourceGroup := defaultResourceGroup(suiteName)
	if resourceGroup != defaultResourceGroup && !allowNonDefaultResourceGroup {
		return fmt.Errorf("refusing to delete resource group %q for suite %q; expected %q or pass --allow-non-default-resource-group", resourceGroup, suiteName, defaultResourceGroup)
	}
	return nil
}

func defaultResourceGroup(suiteName string) string {
	return "rg-aks-burner-" + suiteName
}

func resolveSuitePath(root string, suiteName string, value string) (string, error) {
	if !suite.ValidName(suiteName) {
		return "", fmt.Errorf("invalid suite name %q", suiteName)
	}
	suiteDir := filepath.Join(root, "suites", suiteName)
	cleanValue := filepath.Clean(value)
	var resolved string
	if filepath.IsAbs(cleanValue) {
		resolved = cleanValue
	} else if strings.HasPrefix(filepath.ToSlash(cleanValue), "suites/") {
		resolved = filepath.Join(root, cleanValue)
	} else {
		resolved = filepath.Join(suiteDir, cleanValue)
	}
	resolved = filepath.Clean(resolved)
	if !pathInside(suiteDir, resolved) {
		return "", fmt.Errorf("path %q resolves outside suite directory %q", value, suiteDir)
	}
	return resolved, nil
}

func resolveRepoPath(root string, value string) (string, error) {
	cleanValue := filepath.Clean(value)
	resolved := cleanValue
	if !filepath.IsAbs(cleanValue) {
		resolved = filepath.Join(root, cleanValue)
	}
	resolved = filepath.Clean(resolved)
	if !pathInsideOrEqual(root, resolved) {
		return "", fmt.Errorf("path %q resolves outside repo %q", value, root)
	}
	return resolved, nil
}

func pathInside(base string, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func pathInsideOrEqual(base string, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && (rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)))
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
