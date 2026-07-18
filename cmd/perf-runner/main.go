package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/aks-burner/internal/acr"
	"github.com/Azure/aks-burner/internal/artifacts"
	"github.com/Azure/aks-burner/internal/config"
	"github.com/Azure/aks-burner/internal/infra"
	"github.com/Azure/aks-burner/internal/kubestatemetrics"
	"github.com/Azure/aks-burner/internal/kubetarget"
	"github.com/Azure/aks-burner/internal/prometheus"
	"github.com/Azure/aks-burner/internal/repo"
	"github.com/Azure/aks-burner/internal/reporting"
	"github.com/Azure/aks-burner/internal/requirements"
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
		return fmt.Errorf("usage: perf-runner <list-suites|add-suite|provision|run-suite|destroy> ...")
	}
	switch args[0] {
	case "list-suites":
		return listSuites()
	case "add-suite":
		return addSuite(args[1:])
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

type azureResourceNames struct {
	ResourceGroup string
	ClusterName   string
}

type azureUserAliasFunc func(context.Context) (string, error)

var currentAzureUserAlias azureUserAliasFunc = func(ctx context.Context) (string, error) {
	return infra.AzureUserAlias(ctx, nil)
}

func resolveAzureResourceNames(ctx context.Context, suiteName string, resourceGroup string, clusterNameOverride string, resourceGroupNeeded bool, userAlias azureUserAliasFunc) (azureResourceNames, error) {
	clusterInput := suiteName
	if resourceGroup == "" && resourceGroupNeeded {
		alias, err := userAlias(ctx)
		if err != nil {
			return azureResourceNames{}, err
		}
		resourceGroup = "rg-aks-burner-" + suiteName + "-" + alias
		if len(resourceGroup) > 90 {
			return azureResourceNames{}, fmt.Errorf("generated resource group %q is %d characters; Azure resource group names must be 90 characters or fewer; supply an explicit resource group with --resource-group", resourceGroup, len(resourceGroup))
		}
		clusterInput += "-" + alias
	}
	clusterName, err := infra.ClusterName(clusterInput, clusterNameOverride)
	if err != nil {
		return azureResourceNames{}, err
	}
	return azureResourceNames{ResourceGroup: resourceGroup, ClusterName: clusterName}, nil
}

func flagProvided(fs *flag.FlagSet, name string) bool {
	provided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			provided = true
		}
	})
	return provided
}

type addSuiteOptions struct {
	SuiteName         string
	Description       string
	KubernetesVersion string
	NodeCount         int
	NodeVMSize        string
	Prometheus        bool
	SmokeIterations   int
	FullIterations    int
}

func addSuite(args []string) error {
	return addSuiteWithIO(args, os.Stdin, os.Stdout)
}

func addSuiteWithIO(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("add-suite", flag.ContinueOnError)
	suiteName := fs.String("suite", "", "suite name")
	description := fs.String("description", "", "suite description")
	kubernetesVersion := fs.String("kubernetes-version", "1.36", "AKS Kubernetes version")
	nodeCount := fs.Int("node-count", 1, "AKS user node count")
	nodeVMSize := fs.String("node-vm-size", "Standard_D16as_v5", "AKS user node VM size")
	prometheus := fs.Bool("prometheus", true, "require and install Prometheus")
	smokeIterations := fs.Int("smoke-iterations", 20, "smoke mode iterations")
	fullIterations := fs.Int("full-iterations", 500, "full mode iterations")
	guided := fs.Bool("guided", false, "prompt for suite values")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: perf-runner add-suite --suite SUITE [--guided]")
	}
	opts := suiteDefaults(*suiteName)
	applyAddSuiteFlags(&opts, *description, *kubernetesVersion, *nodeCount, *nodeVMSize, *prometheus, *smokeIterations, *fullIterations)
	if *guided {
		var err error
		opts, err = promptAddSuiteOptions(in, out, opts)
		if err != nil {
			return err
		}
	}
	if opts.SuiteName == "" {
		return fmt.Errorf("usage: perf-runner add-suite --suite SUITE [--guided]")
	}
	if !suite.ValidName(opts.SuiteName) {
		return fmt.Errorf("invalid suite name %q", opts.SuiteName)
	}
	if err := validateAddSuiteOptions(opts); err != nil {
		return err
	}
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	return writeSuite(root, opts)
}

func validateAddSuiteOptions(opts addSuiteOptions) error {
	if !regexp.MustCompile(`^v?[0-9]+\.[0-9]+(\.[0-9]+)?$`).MatchString(opts.KubernetesVersion) {
		return fmt.Errorf("kubernetes version %q must look like 1.36 or 1.36.0", opts.KubernetesVersion)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_]+$`).MatchString(opts.NodeVMSize) {
		return fmt.Errorf("node VM size %q must contain only letters, numbers, and underscores", opts.NodeVMSize)
	}
	if opts.NodeCount < 1 {
		return fmt.Errorf("node count must be a positive integer")
	}
	if opts.SmokeIterations < 1 {
		return fmt.Errorf("smoke iterations must be a positive integer")
	}
	if opts.FullIterations < 1 {
		return fmt.Errorf("full iterations must be a positive integer")
	}
	return nil
}

func suiteDefaults(name string) addSuiteOptions {
	return addSuiteOptions{
		SuiteName:         name,
		Description:       fmt.Sprintf("Dummy %s performance suite.", name),
		KubernetesVersion: "1.36",
		NodeCount:         1,
		NodeVMSize:        "Standard_D16as_v5",
		Prometheus:        true,
		SmokeIterations:   20,
		FullIterations:    500,
	}
}

func applyAddSuiteFlags(opts *addSuiteOptions, description string, kubernetesVersion string, nodeCount int, nodeVMSize string, prometheus bool, smokeIterations int, fullIterations int) {
	if description != "" {
		opts.Description = description
	}
	if kubernetesVersion != "" {
		opts.KubernetesVersion = kubernetesVersion
	}
	opts.NodeCount = nodeCount
	if nodeVMSize != "" {
		opts.NodeVMSize = nodeVMSize
	}
	opts.Prometheus = prometheus
	opts.SmokeIterations = smokeIterations
	opts.FullIterations = fullIterations
}

func promptAddSuiteOptions(in io.Reader, out io.Writer, opts addSuiteOptions) (addSuiteOptions, error) {
	reader := bufio.NewReader(in)
	var err error
	opts.SuiteName, err = promptString(reader, out, "Suite name", opts.SuiteName)
	if err != nil {
		return opts, err
	}
	defaults := suiteDefaults(opts.SuiteName)
	if opts.Description == "Dummy  performance suite." || opts.Description == "" {
		opts.Description = defaults.Description
	}
	opts.Description, err = promptString(reader, out, "Description", opts.Description)
	if err != nil {
		return opts, err
	}
	opts.KubernetesVersion, err = promptString(reader, out, "Kubernetes version", opts.KubernetesVersion)
	if err != nil {
		return opts, err
	}
	opts.NodeCount, err = promptInt(reader, out, "Node count", opts.NodeCount)
	if err != nil {
		return opts, err
	}
	opts.NodeVMSize, err = promptString(reader, out, "Node VM size", opts.NodeVMSize)
	if err != nil {
		return opts, err
	}
	opts.Prometheus, err = promptBool(reader, out, "Install Prometheus", opts.Prometheus)
	if err != nil {
		return opts, err
	}
	opts.SmokeIterations, err = promptInt(reader, out, "Smoke iterations", opts.SmokeIterations)
	if err != nil {
		return opts, err
	}
	opts.FullIterations, err = promptInt(reader, out, "Full iterations", opts.FullIterations)
	if err != nil {
		return opts, err
	}
	return opts, nil
}

func promptString(reader *bufio.Reader, out io.Writer, label string, defaultValue string) (string, error) {
	fmt.Fprintf(out, "%s [%s]: ", label, defaultValue)
	value, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func promptInt(reader *bufio.Reader, out io.Writer, label string, defaultValue int) (int, error) {
	value, err := promptString(reader, out, label, strconv.Itoa(defaultValue))
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", label)
	}
	return parsed, nil
}

func promptBool(reader *bufio.Reader, out io.Writer, label string, defaultValue bool) (bool, error) {
	defaultText := "n"
	if defaultValue {
		defaultText = "y"
	}
	value, err := promptString(reader, out, label+" (y/n)", defaultText)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(value) {
	case "y", "yes", "true":
		return true, nil
	case "n", "no", "false":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be y or n", label)
	}
}

func writeSuite(root string, opts addSuiteOptions) error {
	suiteDir := filepath.Join(root, "suites", opts.SuiteName)
	if err := os.Mkdir(suiteDir, 0o755); err == nil {
		// Directory ownership is established atomically before writing files.
	} else if os.IsExist(err) {
		return fmt.Errorf("suite %q already exists", opts.SuiteName)
	} else {
		return err
	}
	if err := os.MkdirAll(filepath.Join(suiteDir, "templates"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(suiteDir, "vars"), 0o755); err != nil {
		return err
	}
	if err := config.WriteYAML(filepath.Join(suiteDir, "suite.yml"), map[string]any{"name": opts.SuiteName, "description": opts.Description, "tests": []string{"startup-smoke"}, "modeDefaults": suiteModeDefaults(opts.SuiteName)}); err != nil {
		return err
	}
	if err := config.WriteYAML(filepath.Join(suiteDir, "requirements.yml"), suiteRequirements(opts)); err != nil {
		return err
	}
	if err := config.WriteYAML(filepath.Join(suiteDir, "workload.yml"), suiteWorkload(opts)); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "metrics.yml"), []byte(metricsYAML()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "templates", "pod.yml"), []byte(podTemplateYAML(opts)), 0o644); err != nil {
		return err
	}
	if err := config.WriteYAML(filepath.Join(suiteDir, "vars", "smoke.yml"), suiteMode(opts.SmokeIterations, opts.SmokeIterations, 20)); err != nil {
		return err
	}
	return config.WriteYAML(filepath.Join(suiteDir, "vars", "full.yml"), suiteMode(opts.FullIterations, 50, 50))
}

func suiteRequirements(opts addSuiteOptions) map[string]any {
	return map[string]any{
		"suite": opts.SuiteName,
		"requires": map[string]any{
			"infrastructure": map[string]any{
				"provider": "aks",
				"nodePools": []map[string]any{
					{"name": "systempool", "mode": "System", "count": 1, "vmSize": "Standard_D4s_v5", "osType": "Linux", "osSKU": "Ubuntu", "workloadRuntime": "OCIContainer", "labels": map[string]string{}, "taints": []string{}},
					{"name": "userpool", "mode": "User", "count": opts.NodeCount, "vmSize": opts.NodeVMSize, "osType": "Linux", "osSKU": "Ubuntu", "workloadRuntime": "OCIContainer", "labels": map[string]string{"perf.azure.com/node-role": "workload"}, "taints": []string{}},
				},
			},
			"kubernetes":    map[string]any{"minVersion": opts.KubernetesVersion},
			"nodeSelectors": []map[string]any{{"name": "workload", "pool": "userpool", "required": true, "minNodes": 1, "labels": map[string]string{"perf.azure.com/node-role": "workload"}}},
			"reporting": map[string]any{
				"prometheusMetricUnits": map[string]string{"prometheusTargetsUp": "count"},
			},
			"observability": map[string]any{
				"prometheus": map[string]any{
					"required":        opts.Prometheus,
					"install":         opts.Prometheus,
					"namespace":       "perf-monitoring",
					"imageKey":        "prometheus",
					"serviceName":     "prometheus",
					"servicePort":     9090,
					"localPort":       9090,
					"requiredMetrics": []string{"up"},
				},
			},
		},
	}
}

func suiteWorkload(opts addSuiteOptions) map[string]any {
	return map[string]any{
		"global": map[string]any{"measurements": []map[string]string{{"name": "podLatency"}}},
		"jobs": []map[string]any{{
			"name":                 "startup-smoke",
			"jobType":              "create",
			"namespace":            opts.SuiteName,
			"namespacedIterations": true,
			"objects":              []map[string]any{{"objectTemplate": "templates/pod.yml", "replicas": 1, "inputVars": map[string]any{}}},
		}},
	}
}

func suiteModeDefaults(app string) map[string]any {
	return map[string]any{
		"cleanup":          true,
		"waitWhenFinished": true,
		"preLoadImages":    true,
		"reporting":        map[string]any{"scheme": "kube-burner"},
		"templateVars":     map[string]string{"app": app},
		"imageVars":        map[string]string{"image": "pause"},
	}
}

func suiteMode(iterations int, iterationsPerNamespace int, qps int) map[string]any {
	return map[string]any{
		"iterations":             iterations,
		"iterationsPerNamespace": iterationsPerNamespace,
		"qps":                    qps,
		"burst":                  qps,
	}
}

func metricsYAML() string {
	return "- query: sum(up)\n  metricName: prometheusTargetsUp\n  instant: true\n"
}

func podTemplateYAML(opts addSuiteOptions) string {
	return fmt.Sprintf("apiVersion: v1\nkind: Pod\nmetadata:\n  name: %s-{{.Iteration}}-{{.Replica}}\n  labels:\n    app: %s\nspec:\n  nodeSelector:\n    perf.azure.com/node-role: workload\n  restartPolicy: Never\n  containers:\n    - name: pause\n      image: {{.image}}\n      imagePullPolicy: IfNotPresent\n", opts.SuiteName, opts.SuiteName)
}

func destroy(args []string) error {
	fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
	suiteName := fs.String("suite", "", "suite name")
	resourceGroup := fs.String("resource-group", "", "Azure resource group")
	allowNonDefaultResourceGroup := fs.Bool("allow-non-default-resource-group", false, "allow deleting a resource group outside the default suite naming convention")
	if err := fs.Parse(args); err != nil {
		return err
	}
	explicitResourceGroup := flagProvided(fs, "resource-group")
	if explicitResourceGroup && *resourceGroup == "" {
		return fmt.Errorf("resource-group must not be empty when explicitly supplied")
	}
	if *suiteName == "" {
		return fmt.Errorf("usage: perf-runner destroy --suite SUITE [--resource-group RG] [--allow-non-default-resource-group]")
	}
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	if _, err := suite.Load(root, *suiteName); err != nil {
		return err
	}
	names, err := resolveAzureResourceNames(context.Background(), *suiteName, *resourceGroup, "", true, currentAzureUserAlias)
	if err != nil {
		return err
	}
	if err := validateDestroyTarget(names.ResourceGroup, !explicitResourceGroup, *allowNonDefaultResourceGroup); err != nil {
		return err
	}
	return destroyInfra(context.Background(), names.ResourceGroup)
}

var destroyInfra = infra.Destroy

type deploymentOutputFunc func(context.Context, string, string, string) (string, error)
type getCredentialsFunc func(context.Context, string, string) error

type runSuiteDependencies struct {
	DeploymentOutput deploymentOutputFunc
	GetCredentials   getCredentialsFunc
	AzureUserAlias   azureUserAliasFunc
}

func prepareRunSuiteCluster(ctx context.Context, resourceGroup string, clusterName string, images *acr.Requirements, refreshCredentials bool, deps runSuiteDependencies) (string, string, error) {
	if deps.DeploymentOutput == nil {
		deps.DeploymentOutput = infra.DeploymentOutput
	}
	if deps.GetCredentials == nil {
		deps.GetCredentials = infra.GetCredentials
	}
	registryName, registryServer := "", ""
	if images != nil && len(images.Builds) > 0 {
		deployedClusterName, err := deps.DeploymentOutput(ctx, resourceGroup, infra.DeploymentName, "clusterName")
		if err != nil {
			return "", "", fmt.Errorf("suite image builds requires an aks-burner deployment with container registry outputs, including clusterName, so its kubelet identity has AcrPull on the deployment registry: %w", err)
		}
		if deployedClusterName != clusterName {
			return "", "", fmt.Errorf("suite image builds requested cluster %q, but the managed aks-burner deployment targets %q; use the deployed cluster so its kubelet identity has AcrPull on the deployment registry", clusterName, deployedClusterName)
		}
		registryName, err = deps.DeploymentOutput(ctx, resourceGroup, infra.DeploymentName, "containerRegistryName")
		if err != nil {
			return "", "", fmt.Errorf("suite image builds requires an aks-burner deployment with container registry outputs: %w", err)
		}
		registryServer, err = deps.DeploymentOutput(ctx, resourceGroup, infra.DeploymentName, "containerRegistryLoginServer")
		if err != nil {
			return "", "", fmt.Errorf("suite image builds requires an aks-burner deployment with container registry outputs: %w", err)
		}
	}
	if refreshCredentials {
		if err := deps.GetCredentials(ctx, resourceGroup, clusterName); err != nil {
			return "", "", err
		}
	}
	return registryName, registryServer, nil
}

func runSuite(args []string) error {
	return runSuiteWithDependencies(args, runSuiteDependencies{})
}

func runSuiteWithDependencies(args []string, deps runSuiteDependencies) error {
	fs := flag.NewFlagSet("run-suite", flag.ContinueOnError)
	suiteName := fs.String("suite", "", "suite name")
	modeName := fs.String("mode", "smoke", "mode")
	resourceGroup := fs.String("resource-group", "", "Azure resource group")
	clusterNameOverride := fs.String("cluster-name", "", "AKS cluster name override")
	kubeContext := fs.String("kube-context", "", "kube context")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if flagProvided(fs, "resource-group") && *resourceGroup == "" {
		return fmt.Errorf("resource-group must not be empty when explicitly supplied")
	}
	if *suiteName == "" {
		return fmt.Errorf("usage: perf-runner run-suite --suite SUITE --mode MODE [--resource-group RG] [--kube-context CONTEXT]")
	}
	target := kubetarget.Target{Context: *kubeContext}
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	if !suite.ValidName(*suiteName) || !suite.ValidName(*modeName) {
		return fmt.Errorf("invalid suite or mode name")
	}
	suitePath, err := resolveSuitePath(root, *suiteName, "suite.yml")
	if err != nil {
		return err
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas", "suite.schema.json"), suitePath); err != nil {
		return err
	}
	suiteCfg, err := suite.Load(root, *suiteName)
	if err != nil {
		return err
	}
	req, err := requirements.Load(root, *suiteName)
	if err != nil {
		return err
	}
	if req.Requires.Infrastructure.Provider != "aks" {
		return fmt.Errorf("unsupported infrastructure provider %q", req.Requires.Infrastructure.Provider)
	}
	var imageBuilds []acr.ImageBuild
	if req.Requires.Images != nil {
		imageBuilds = req.Requires.Images.Builds
	}
	if err := infra.ValidateNodePools(*suiteName, req.Requires.Infrastructure.NodePools, req.Requires.NodeSelectors); err != nil {
		return err
	}
	staticImages, err := config.LoadImages(filepath.Join(root, "config", "images.yml"))
	if err != nil {
		return err
	}
	suiteDir := filepath.Join(root, "suites", *suiteName)
	var mode runpkg.Mode
	modePath, err := resolveSuitePath(root, *suiteName, filepath.Join("vars", *modeName+".yml"))
	if err != nil {
		return err
	}
	if err := config.LoadMergedYAML(filepath.Join(root, "schemas", "mode.schema.json"), suiteCfg.ModeDefaults, modePath, &mode); err != nil {
		return err
	}
	var workload map[string]any
	workloadFile, err := resolveSuitePath(root, *suiteName, mode.SelectedWorkloadFile())
	if err != nil {
		return err
	}
	if err := config.LoadTemplateYAML(workloadFile, mode.TemplateVars, &workload); err != nil {
		return err
	}
	metricNames, err := reporting.PrometheusMetricNames(filepath.Join(suiteDir, "metrics.yml"))
	if err != nil {
		return err
	}
	reportingCfg := reporting.Config{
		Scheme:                mode.Reporting.Scheme,
		PrometheusMetricUnits: req.Requires.Reporting.PrometheusMetricUnits,
	}
	if req.Requires.Artifacts.Enabled && mode.ArtifactSubpath == "" {
		return fmt.Errorf("artifact collection requires mode artifactSubpath with {{.runTimestamp}}")
	}
	if err := reporting.ValidateConfig(&reportingCfg, req.Requires.Artifacts.Enabled, req.Requires.Observability.Prometheus.Required, workload, metricNames); err != nil {
		return err
	}
	runTimestamp := time.Now().UTC()
	runTag := acr.RunTag(*suiteName, *modeName, runTimestamp)
	if len(imageBuilds) > 0 {
		if _, _, err := acr.BuildCommands(acr.BuildOptions{
			SuiteDir:       suiteDir,
			RegistryName:   "preflight",
			RegistryServer: "preflight.invalid",
			ResourceGroup:  "preflight",
			Tag:            runTag,
			Builds:         imageBuilds,
		}); err != nil {
			return err
		}
	}
	if err := validateRunSuiteImageKeys(mode.ImageVars, staticImages, imageBuilds, req); err != nil {
		return err
	}
	if err := runpkg.ValidateKubeBurnerVersion(root); err != nil {
		return err
	}
	if deps.AzureUserAlias == nil {
		deps.AzureUserAlias = currentAzureUserAlias
	}
	resourceGroupNeeded := *kubeContext == "" || len(imageBuilds) > 0
	names, err := resolveAzureResourceNames(context.Background(), *suiteName, *resourceGroup, *clusterNameOverride, resourceGroupNeeded, deps.AzureUserAlias)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registryName, registryServer, err := prepareRunSuiteCluster(ctx, names.ResourceGroup, names.ClusterName, req.Requires.Images, *kubeContext == "", deps)
	if err != nil {
		return err
	}
	if err := runpkg.ValidateRequirements(ctx, runpkg.Requirements{Kubernetes: req.Requires.Kubernetes, NodeSelectors: req.Requires.NodeSelectors}, target.Output); err != nil {
		return err
	}
	var storageClasses []runpkg.StorageClassMetadata
	if reportingCfg.Scheme.ReportsStorageStartup() {
		storageClasses, err = runpkg.ValidateStorageClasses(ctx, req.Requires.StorageClasses, target.Output)
		if err != nil {
			return err
		}
	}
	runDir, err := runpkg.CreateRunDir(*suiteName, *modeName, runTimestamp)
	if err != nil {
		return err
	}
	mode.RunTimestamp = runTimestamp
	builtImages := []acr.BuiltImage(nil)
	builtImageMap := map[string]string{}
	if req.Requires.Images != nil && len(req.Requires.Images.Builds) > 0 {
		builtImages, builtImageMap, err = acr.Build(ctx, acr.BuildOptions{
			SuiteDir:       suiteDir,
			RegistryName:   registryName,
			RegistryServer: registryServer,
			ResourceGroup:  names.ResourceGroup,
			Tag:            runTag,
			Builds:         req.Requires.Images.Builds,
			LogsDir:        filepath.Join(runDir, "logs"),
		})
		if err != nil {
			return err
		}
	}
	images := mergeImages(staticImages, builtImageMap)
	if err := runpkg.ApplySetup(ctx, target, suiteDir, suiteCfg.Setup); err != nil {
		return err
	}
	metadataClusterName := names.ClusterName
	if *kubeContext != "" {
		metadataClusterName = ""
	}
	if err := runpkg.WriteMetadata(runDir, runpkg.Metadata{Suite: *suiteName, Mode: *modeName, Timestamp: runTimestamp.Format(time.RFC3339), ResourceGroup: names.ResourceGroup, ClusterName: metadataClusterName, KubeContext: *kubeContext, Images: images, BuiltImages: builtImages, Setup: suiteCfg.Setup, StorageClasses: storageClasses}); err != nil {
		return err
	}
	if req.Requires.Observability.KubeStateMetrics.Required && req.Requires.Observability.KubeStateMetrics.Install {
		kubeStateMetricsImage, err := config.ResolveImage(images, req.Requires.Observability.KubeStateMetrics.ImageKey)
		if err != nil {
			return err
		}
		if err := kubestatemetrics.Install(ctx, target, filepath.Join(root, "observability", "kube-state-metrics", "kube-state-metrics.yaml"), kubeStateMetricsImage); err != nil {
			return err
		}
		if err := kubestatemetrics.WaitRollout(ctx, target, req.Requires.Observability.KubeStateMetrics); err != nil {
			return err
		}
	}
	if req.Requires.Observability.Prometheus.Required && req.Requires.Observability.Prometheus.Install {
		prometheusImage, err := config.ResolveImage(images, req.Requires.Observability.Prometheus.ImageKey)
		if err != nil {
			return err
		}
		if err := prometheus.InstallWithScrapeTarget(ctx, target, filepath.Join(root, "observability", "prometheus", "prometheus.yaml"), prometheusImage, kubeStateMetricsScrapeTarget(req.Requires.Observability.KubeStateMetrics)); err != nil {
			return err
		}
	}
	prometheusURL := ""
	if req.Requires.Observability.Prometheus.Required {
		if shouldWaitPrometheusRollout(req.Requires.Observability.Prometheus.Required, req.Requires.Observability.Prometheus.Install) {
			if err := prometheus.WaitRollout(ctx, target, req.Requires.Observability.Prometheus); err != nil {
				return err
			}
		}
		portForwardCtx, portForwardCancel := context.WithCancel(ctx)
		cmd, endpoint, err := prometheus.PortForward(portForwardCtx, target, req.Requires.Observability.Prometheus)
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
	if err := runpkg.CopyRenderAssets(suiteDir, runDir); err != nil {
		return err
	}
	rendered, err := runpkg.RenderWorkload(workload, mode, images, prometheusURL, reportingCfg.Scheme.UsesKubeBurner())
	if err != nil {
		return err
	}
	workloadPath := filepath.Join(runDir, "rendered", "workload.yml")
	if err := config.WriteYAML(workloadPath, rendered); err != nil {
		return err
	}
	return runpkg.WithStorageRunLock(ctx, reportingCfg.Scheme.ReportsStorageStartup(), filepath.Base(runDir), target.Output, func() error {
		return executeRunCopyAndReport(
			ctx,
			target,
			workloadPath,
			filepath.Join(runDir, "logs", "kube-burner.log"),
			req.Requires.Artifacts,
			images,
			filepath.Join(runDir, "artifacts"),
			mode.RenderedArtifactSubpath(),
			runDir,
			reportingCfg,
			reporting.RunInfo{Suite: *suiteName, Mode: *modeName, Timestamp: runTimestamp.Format(time.RFC3339Nano), WorkspaceRoot: root},
			os.Stdout,
			runpkg.ExecuteKubeBurner,
			waitArtifactJobsComplete,
			copyArtifacts,
			reporting.Generate,
		)
	})
}

func kubeStateMetricsScrapeTarget(cfg kubestatemetrics.Config) string {
	if !cfg.Required {
		return ""
	}
	return fmt.Sprintf("%s.%s.svc:%d", cfg.ServiceName, cfg.Namespace, cfg.ServicePort)
}

type targetKubeBurnerExecutor func(workloadPath string, logPath string, target kubetarget.Target) error

type targetArtifactJobWaiter func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config) error

type targetArtifactCopier func(ctx context.Context, target kubetarget.Target, cfg artifacts.Config, destination string, subpath string) error

type resultReporter func(runDir string, cfg reporting.Config, info reporting.RunInfo, out io.Writer) (reporting.Result, error)

func executeRunCopyAndReport(
	ctx context.Context,
	target kubetarget.Target,
	workloadPath string,
	logPath string,
	artifactCfg artifacts.Config,
	images map[string]string,
	artifactDestination string,
	artifactSubpath string,
	runDir string,
	reportingCfg reporting.Config,
	runInfo reporting.RunInfo,
	out io.Writer,
	execute targetKubeBurnerExecutor,
	waitArtifactJobs targetArtifactJobWaiter,
	copyArtifacts targetArtifactCopier,
	report resultReporter,
) error {
	if artifactCfg.Enabled {
		copyImage, err := config.ResolveImage(images, artifactCfg.CopyImage)
		if err != nil {
			return err
		}
		if artifactSubpath != "" {
			if err := artifacts.ValidateSubpath(artifactSubpath); err != nil {
				return err
			}
		}
		artifactCfg.CopyImage = copyImage
	}
	workloadErr := execute(workloadPath, logPath, target)
	var waitErr error
	if workloadErr == nil && artifactCfg.Enabled {
		waitErr = waitArtifactJobs(ctx, target, artifactCfg)
	}
	var copyErr error
	if artifactCfg.Enabled {
		copyErr = copyArtifacts(ctx, target, artifactCfg, artifactDestination, artifactSubpath)
	}
	if workloadErr != nil {
		var reportErr error
		if reportingCfg.Scheme.SupportsPartialResults() {
			runInfo.Partial = true
			_, reportErr = report(runDir, reportingCfg, runInfo, out)
		}
		if copyErr != nil {
			if reportErr != nil {
				return fmt.Errorf("kube-burner failed: %w; artifact copy also failed: %v; reporting also failed: %v", workloadErr, copyErr, reportErr)
			}
			return fmt.Errorf("kube-burner failed: %w; artifact copy also failed: %v", workloadErr, copyErr)
		}
		if reportErr != nil {
			return fmt.Errorf("kube-burner failed: %w; reporting also failed: %v", workloadErr, reportErr)
		}
		return workloadErr
	}
	if waitErr != nil {
		if copyErr != nil {
			return fmt.Errorf("artifact wait failed: %w; artifact copy also failed: %v", waitErr, copyErr)
		}
		return waitErr
	}
	if copyErr != nil {
		return copyErr
	}
	_, err := report(runDir, reportingCfg, runInfo, out)
	return err
}

func waitArtifactJobsComplete(ctx context.Context, target kubetarget.Target, cfg artifacts.Config) error {
	if !cfg.Enabled || cfg.Namespace == "" {
		return nil
	}
	args := target.KubectlCommand("wait", "--for=condition=complete", "job", "--all", "-n", cfg.Namespace, "--timeout=15m")
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyArtifacts(ctx context.Context, target kubetarget.Target, cfg artifacts.Config, destination string, subpath string) error {
	if subpath == "" {
		return artifacts.Copy(ctx, target, cfg, destination)
	}
	return artifacts.CopySubpath(ctx, target, cfg, destination, subpath)
}

func shouldWaitPrometheusRollout(required bool, install bool) bool {
	return required && install
}

var provisionInfra = infra.Provision

type provisionFunc func(context.Context, infra.ProvisionOptions) error

type provisionDependencies struct {
	AzureUserAlias azureUserAliasFunc
	Provision      provisionFunc
}

func provision(args []string) error {
	return provisionWithIO(args, os.Stdout)
}

func provisionWithIO(args []string, out io.Writer) error {
	return provisionWithDependencies(args, out, provisionDependencies{})
}

func provisionWithDependencies(args []string, out io.Writer, deps provisionDependencies) error {
	fs := flag.NewFlagSet("provision", flag.ContinueOnError)
	suiteName := fs.String("suite", "", "suite name")
	resourceGroup := fs.String("resource-group", "", "Azure resource group")
	location := fs.String("location", "", "Azure location")
	clusterNameOverride := fs.String("cluster-name", "", "AKS cluster name override")
	dryRun := fs.Bool("dry-run", false, "print generated ARM parameters without provisioning")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if flagProvided(fs, "resource-group") && *resourceGroup == "" {
		return fmt.Errorf("resource-group must not be empty when explicitly supplied")
	}
	if *suiteName == "" || *location == "" {
		return fmt.Errorf("usage: perf-runner provision --suite SUITE [--resource-group RG] --location LOCATION")
	}
	root, err := repo.Root(".")
	if err != nil {
		return err
	}
	if !suite.ValidName(*suiteName) {
		return fmt.Errorf("invalid suite name %q", *suiteName)
	}
	req, err := requirements.Load(root, *suiteName)
	if err != nil {
		return err
	}
	if req.Requires.Infrastructure.Provider != "aks" {
		return fmt.Errorf("unsupported infrastructure provider %q", req.Requires.Infrastructure.Provider)
	}
	if deps.AzureUserAlias == nil {
		deps.AzureUserAlias = currentAzureUserAlias
	}
	if deps.Provision == nil {
		deps.Provision = provisionInfra
	}
	names, err := resolveAzureResourceNames(context.Background(), *suiteName, *resourceGroup, *clusterNameOverride, true, deps.AzureUserAlias)
	if err != nil {
		return err
	}
	if err := infra.ValidateNodePools(*suiteName, req.Requires.Infrastructure.NodePools, req.Requires.NodeSelectors); err != nil {
		return err
	}
	parameterJSON, err := infra.ParametersJSON(names.ClusterName, req.Requires.Kubernetes.MinVersion, req.Requires.Infrastructure.NodePools, shouldDeployContainerRegistry(req.Requires.Images))
	if err != nil {
		return err
	}
	if *dryRun {
		_, err = out.Write(parameterJSON)
		return err
	}
	return deps.Provision(context.Background(), infra.ProvisionOptions{
		ResourceGroup:  names.ResourceGroup,
		Location:       *location,
		TemplateFile:   filepath.Join(root, "infra", "aks", "main.bicep"),
		ParametersJSON: parameterJSON,
		ClusterName:    names.ClusterName,
	})
}

func shouldDeployContainerRegistry(images *acr.Requirements) bool {
	return images != nil
}

func validateDestroyTarget(resourceGroup string, derivedDefault bool, allowNonDefaultResourceGroup bool) error {
	if !derivedDefault && !allowNonDefaultResourceGroup {
		return fmt.Errorf("refusing to delete explicit resource group %q; omit --resource-group to delete the current user's default or pass --allow-non-default-resource-group", resourceGroup)
	}
	return nil
}

func mergeImages(base map[string]string, overlay map[string]string) map[string]string {
	merged := map[string]string{}
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return merged
}

func validateModeImageVars(imageVars map[string]string, staticImages map[string]string, builds []acr.ImageBuild) error {
	return validateImageKeys(imageVars, staticImages, builds)
}

func validateRunSuiteImageKeys(imageVars map[string]string, staticImages map[string]string, builds []acr.ImageBuild, req requirements.Document) error {
	keys := map[string]string{}
	for name, key := range imageVars {
		keys["mode image variable "+name] = key
	}
	if req.Requires.Observability.Prometheus.Required && req.Requires.Observability.Prometheus.Install {
		keys["Prometheus install"] = req.Requires.Observability.Prometheus.ImageKey
	}
	if req.Requires.Observability.KubeStateMetrics.Required && req.Requires.Observability.KubeStateMetrics.Install {
		keys["kube-state-metrics install"] = req.Requires.Observability.KubeStateMetrics.ImageKey
	}
	if req.Requires.Artifacts.Enabled {
		keys["artifact copy"] = req.Requires.Artifacts.CopyImage
	}
	return validateImageKeys(keys, staticImages, builds)
}

func validateImageKeys(imageKeys map[string]string, staticImages map[string]string, builds []acr.ImageBuild) error {
	known := map[string]bool{}
	for key, image := range staticImages {
		known[key] = image != ""
	}
	for _, build := range builds {
		known[build.Key] = true
	}
	for consumer, imageKey := range imageKeys {
		if !known[imageKey] {
			return fmt.Errorf("%s image key %q not found", consumer, imageKey)
		}
	}
	return nil
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

func pathInside(base string, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
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
		fmt.Printf("%s\t%s\t%s\n", cfg.Name, strings.Join(cfg.Modes, ", "), cfg.Description)
	}
	return nil
}
