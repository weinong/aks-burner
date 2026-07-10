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

type addSuiteOptions struct {
	SuiteName         string
	Description       string
	ClusterName       string
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
	clusterName := fs.String("cluster-name", "", "AKS cluster name")
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
	applyAddSuiteFlags(&opts, *description, *clusterName, *kubernetesVersion, *nodeCount, *nodeVMSize, *prometheus, *smokeIterations, *fullIterations)
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
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,61}[A-Za-z0-9]$`).MatchString(opts.ClusterName) {
		return fmt.Errorf("cluster name %q must contain only letters, numbers, and hyphens, start and end with a letter or number, and be at most 63 characters", opts.ClusterName)
	}
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
	clusterName := ""
	if name != "" {
		clusterName = "aks" + strings.ReplaceAll(name, "-", "")
	}
	return addSuiteOptions{
		SuiteName:         name,
		Description:       fmt.Sprintf("Dummy %s performance suite.", name),
		ClusterName:       clusterName,
		KubernetesVersion: "1.36",
		NodeCount:         1,
		NodeVMSize:        "Standard_D16as_v5",
		Prometheus:        true,
		SmokeIterations:   20,
		FullIterations:    500,
	}
}

func applyAddSuiteFlags(opts *addSuiteOptions, description string, clusterName string, kubernetesVersion string, nodeCount int, nodeVMSize string, prometheus bool, smokeIterations int, fullIterations int) {
	if description != "" {
		opts.Description = description
	}
	if clusterName != "" {
		opts.ClusterName = clusterName
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
	if opts.ClusterName == "" {
		opts.ClusterName = defaults.ClusterName
	}
	opts.Description, err = promptString(reader, out, "Description", opts.Description)
	if err != nil {
		return opts, err
	}
	opts.ClusterName, err = promptString(reader, out, "Cluster name", opts.ClusterName)
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
	if err := config.WriteYAML(filepath.Join(suiteDir, "suite.yml"), map[string]any{"name": opts.SuiteName, "description": opts.Description, "tests": []string{"startup-smoke"}}); err != nil {
		return err
	}
	if err := config.WriteYAML(filepath.Join(suiteDir, "requirements.yml"), suiteRequirements(opts)); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(suiteDir, "infra.bicepparam"), []byte(infraBicepParam(opts)), 0o644); err != nil {
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
	if err := config.WriteYAML(filepath.Join(suiteDir, "vars", "smoke.yml"), suiteMode(opts.SuiteName, opts.SmokeIterations, opts.SmokeIterations, 20)); err != nil {
		return err
	}
	return config.WriteYAML(filepath.Join(suiteDir, "vars", "full.yml"), suiteMode(opts.SuiteName, opts.FullIterations, 50, 50))
}

func suiteRequirements(opts addSuiteOptions) map[string]any {
	return map[string]any{
		"suite": opts.SuiteName,
		"requires": map[string]any{
			"infrastructure": map[string]any{
				"provider": "aks",
				"bicep":    map[string]any{"template": "infra/aks/main.bicep", "parameters": filepath.ToSlash(filepath.Join("suites", opts.SuiteName, "infra.bicepparam"))},
			},
			"kubernetes":    map[string]any{"minVersion": opts.KubernetesVersion},
			"nodeSelectors": []map[string]any{{"name": "workload", "required": true, "minNodes": 1, "labels": map[string]string{"perf.azure.com/node-role": "workload"}}},
			"observability": map[string]any{
				"prometheus": map[string]any{
					"required":        opts.Prometheus,
					"install":         opts.Prometheus,
					"namespace":       "perf-monitoring",
					"imageKey":        "prometheus",
					"serviceName":     "prometheus",
					"servicePort":     9090,
					"localPort":       9090,
					"requiredMetrics": []string{"container_cpu_usage_seconds_total", "container_memory_working_set_bytes"},
				},
			},
		},
	}
}

func infraBicepParam(opts addSuiteOptions) string {
	return fmt.Sprintf("using '../../infra/aks/main.bicep'\n\nparam clusterName = '%s'\nparam kubernetesVersion = '%s'\nparam userNodeCount = %d\nparam userNodeVmSize = '%s'\nparam userNodeOsSKU = 'Ubuntu'\nparam userNodeWorkloadRuntime = 'OCIContainer'\nparam userNodeLabels = {\n  'perf.azure.com/node-role': 'workload'\n}\n", opts.ClusterName, opts.KubernetesVersion, opts.NodeCount, opts.NodeVMSize)
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

func suiteMode(app string, iterations int, iterationsPerNamespace int, qps int) map[string]any {
	return map[string]any{
		"iterations":             iterations,
		"iterationsPerNamespace": iterationsPerNamespace,
		"qps":                    qps,
		"burst":                  qps,
		"cleanup":                true,
		"waitWhenFinished":       true,
		"preLoadImages":          true,
		"templateVars":           map[string]string{"app": app},
		"imageVars":              map[string]string{"image": "pause"},
	}
}

func metricsYAML() string {
	return "- query: sum(rate(container_cpu_usage_seconds_total[2m])) by (pod, namespace)\n  metricName: podCPUUsage\n- query: sum(container_memory_working_set_bytes) by (pod, namespace)\n  metricName: podMemoryWorkingSet\n"
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
			Images        acr.Requirements                 `yaml:"images"`
			Kubernetes    runpkg.KubernetesRequirements    `yaml:"kubernetes"`
			NodeSelectors []runpkg.NodeSelectorRequirement `yaml:"nodeSelectors"`
			Artifacts     artifacts.Config                 `yaml:"artifacts"`
			Observability struct {
				Prometheus       prometheus.Config       `yaml:"prometheus"`
				KubeStateMetrics kubestatemetrics.Config `yaml:"kubeStateMetrics"`
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
	staticImages, err := config.LoadImages(filepath.Join(root, "config", "images.yml"))
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
	runTimestamp := time.Now().UTC()
	runDir, err := runpkg.CreateRunDir(*suiteName, *modeName)
	if err != nil {
		return err
	}
	suiteDir := filepath.Join(root, "suites", *suiteName)
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
	mode.RunTimestamp = runTimestamp
	var workload map[string]any
	workloadFile, err := resolveSuitePath(root, *suiteName, mode.SelectedWorkloadFile())
	if err != nil {
		return err
	}
	if err := config.LoadYAML(workloadFile, &workload); err != nil {
		return err
	}
	if err := validateModeImageVars(mode.ImageVars, staticImages, req.Requires.Images.Builds); err != nil {
		return err
	}
	builtImages := []acr.BuiltImage(nil)
	builtImageMap := map[string]string{}
	if len(req.Requires.Images.Builds) > 0 {
		registryName, err := registryNameFromRequirements(parametersPath, req.Requires.Images)
		if err != nil {
			return err
		}
		registryServer := ""
		if registryName == "" {
			registryName, err = infra.DeploymentOutput(ctx, *resourceGroup, infra.DeploymentName, "containerRegistryName")
			if err != nil {
				return err
			}
			registryServer, err = infra.DeploymentOutput(ctx, *resourceGroup, infra.DeploymentName, "containerRegistryLoginServer")
			if err != nil {
				return err
			}
		} else {
			registryServer = registryName + ".azurecr.io"
		}
		builtImages, builtImageMap, err = acr.Build(ctx, acr.BuildOptions{
			SuiteDir:       suiteDir,
			RegistryName:   registryName,
			RegistryServer: registryServer,
			ResourceGroup:  *resourceGroup,
			Tag:            acr.RunTag(*suiteName, *modeName, runTimestamp),
			Builds:         req.Requires.Images.Builds,
			LogsDir:        filepath.Join(runDir, "logs"),
		})
		if err != nil {
			return err
		}
	}
	images := mergeImages(staticImages, builtImageMap)
	if err := runpkg.ApplySetup(ctx, suiteDir, suiteCfg.Setup, runpkg.KubectlOutput); err != nil {
		return err
	}
	if err := runpkg.WriteMetadata(runDir, runpkg.Metadata{Suite: *suiteName, Mode: *modeName, Timestamp: runTimestamp.Format(time.RFC3339), ResourceGroup: *resourceGroup, ClusterName: clusterName, Images: images, BuiltImages: builtImages, Setup: suiteCfg.Setup}); err != nil {
		return err
	}
	if req.Requires.Observability.KubeStateMetrics.Required && req.Requires.Observability.KubeStateMetrics.Install {
		kubeStateMetricsImage, err := config.ResolveImage(images, req.Requires.Observability.KubeStateMetrics.ImageKey)
		if err != nil {
			return err
		}
		if err := kubestatemetrics.Install(ctx, filepath.Join(root, "observability", "kube-state-metrics", "kube-state-metrics.yaml"), kubeStateMetricsImage); err != nil {
			return err
		}
		if err := kubestatemetrics.WaitRollout(ctx, req.Requires.Observability.KubeStateMetrics); err != nil {
			return err
		}
	}
	if req.Requires.Observability.Prometheus.Required && req.Requires.Observability.Prometheus.Install {
		prometheusImage, err := config.ResolveImage(images, req.Requires.Observability.Prometheus.ImageKey)
		if err != nil {
			return err
		}
		if err := prometheus.InstallWithScrapeTarget(ctx, filepath.Join(root, "observability", "prometheus", "prometheus.yaml"), prometheusImage, kubeStateMetricsScrapeTarget(req.Requires.Observability.KubeStateMetrics)); err != nil {
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
	return executeRunAndCopyArtifacts(ctx, workloadPath, filepath.Join(runDir, "logs", "kube-burner.log"), req.Requires.Artifacts, images, filepath.Join(runDir, "artifacts"), artifactSubpathFromRenderedWorkload(rendered), runpkg.ExecuteKubeBurner, waitArtifactJobsComplete, copyArtifacts)
}

func kubeStateMetricsScrapeTarget(cfg kubestatemetrics.Config) string {
	if !cfg.Required {
		return ""
	}
	return fmt.Sprintf("%s.%s.svc:%d", cfg.ServiceName, cfg.Namespace, cfg.ServicePort)
}

type kubeBurnerExecutor func(workloadPath string, logPath string) error

type artifactJobWaiter func(ctx context.Context, cfg artifacts.Config) error

type artifactCopier func(ctx context.Context, cfg artifacts.Config, destination string, subpath string) error

func executeRunAndCopyArtifacts(ctx context.Context, workloadPath string, logPath string, artifactCfg artifacts.Config, images map[string]string, artifactDestination string, artifactSubpath string, execute kubeBurnerExecutor, waitArtifactJobs artifactJobWaiter, copyArtifacts artifactCopier) error {
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
	executeErr := execute(workloadPath, logPath)
	if executeErr == nil && artifactCfg.Enabled {
		if err := waitArtifactJobs(ctx, artifactCfg); err != nil {
			executeErr = err
		}
	}
	artifactErr := copyArtifacts(ctx, artifactCfg, artifactDestination, artifactSubpath)
	if executeErr != nil {
		if artifactErr != nil {
			return fmt.Errorf("kube-burner failed: %w; artifact copy also failed: %v", executeErr, artifactErr)
		}
		return executeErr
	}
	return artifactErr
}

func waitArtifactJobsComplete(ctx context.Context, cfg artifacts.Config) error {
	if !cfg.Enabled || cfg.Namespace == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "kubectl", "wait", "--for=condition=complete", "job", "--all", "-n", cfg.Namespace, "--timeout=15m")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyArtifacts(ctx context.Context, cfg artifacts.Config, destination string, subpath string) error {
	if subpath == "" {
		return artifacts.Copy(ctx, cfg, destination)
	}
	return artifacts.CopySubpath(ctx, cfg, destination, subpath)
}

func artifactSubpathFromRenderedWorkload(rendered map[string]any) string {
	jobs, _ := rendered["jobs"].([]any)
	for _, item := range jobs {
		job, ok := item.(map[string]any)
		if !ok {
			continue
		}
		objects, _ := job["objects"].([]any)
		for _, objectItem := range objects {
			object, ok := objectItem.(map[string]any)
			if !ok {
				continue
			}
			inputVars, _ := object["inputVars"].(map[string]any)
			runID, _ := inputVars["runID"].(string)
			if runID != "" {
				return runID
			}
		}
	}
	return ""
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

func registryNameFromRequirements(parametersPath string, images acr.Requirements) (string, error) {
	if len(images.Builds) == 0 {
		return "", nil
	}
	if images.Registry.NameParameter == "" {
		return "", fmt.Errorf("requires.images.registry.nameParameter is required when image builds are configured")
	}
	registryName, err := readBicepParamString(parametersPath, images.Registry.NameParameter)
	if err != nil && strings.Contains(err.Error(), "parameter "+images.Registry.NameParameter+" not found") {
		return "", nil
	}
	return registryName, err
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
	known := map[string]bool{}
	for key := range staticImages {
		known[key] = true
	}
	for _, build := range builds {
		known[build.Key] = true
	}
	for _, imageKey := range imageVars {
		if !known[imageKey] {
			return fmt.Errorf("image key %q not found", imageKey)
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
