package examples

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/config"
	"github.com/Azure/aks-burner/internal/reporting"
	"github.com/Azure/aks-burner/internal/requirements"
	"github.com/Azure/aks-burner/internal/run"
	"gopkg.in/yaml.v3"
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

func TestKataPerfUsesStaticPauseImageWithoutBuilds(t *testing.T) {
	root := filepath.Join("..", "..")
	images, err := config.LoadImages(filepath.Join(root, "config/images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := images["pause"]; got != "mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2" {
		t.Fatalf("static pause image = %q, want mcr.microsoft.com/oss/v2/kubernetes/pause:3.10.2", got)
	}
	var requirements struct {
		Requires struct {
			Images any `yaml:"images"`
		} `yaml:"requires"`
	}
	if err := config.LoadYAML(filepath.Join(root, "suites/kata-perf/requirements.yml"), &requirements); err != nil {
		t.Fatal(err)
	}
	if requirements.Requires.Images != nil {
		t.Fatal("kata-perf requirements must omit images")
	}
	for _, mode := range []string{"smoke", "full"} {
		var vars struct {
			ImageVars map[string]string `yaml:"imageVars"`
		}
		if err := config.LoadYAML(filepath.Join(root, "suites/kata-perf/vars", mode+".yml"), &vars); err != nil {
			t.Fatal(err)
		}
		if got := vars.ImageVars["image"]; got != "pause" {
			t.Fatalf("kata-perf %s image key = %q, want pause", mode, got)
		}
	}
}

func TestKataPerfModesRenderSerializedLatencyJobs(t *testing.T) {
	root := filepath.Join("..", "..")
	images, err := config.LoadImages(filepath.Join(root, "config/images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workload map[string]any
	if err := config.LoadYAML(filepath.Join(root, "suites/kata-perf/workload.yml"), &workload); err != nil {
		t.Fatal(err)
	}

	for modeName, expectedIterations := range map[string]int{"smoke": 5, "full": 20} {
		var mode run.Mode
		if err := config.LoadYAML(filepath.Join(root, "suites/kata-perf/vars", modeName+".yml"), &mode); err != nil {
			t.Fatal(err)
		}
		rendered, err := run.RenderWorkload(workload, mode, images, "", true)
		if err != nil {
			t.Fatal(err)
		}
		jobs := rendered["jobs"].([]any)
		if len(jobs) != 2 {
			t.Fatalf("kata-perf %s rendered %d jobs, want 2", modeName, len(jobs))
		}
		for index, item := range jobs {
			job := item.(map[string]any)
			name := job["name"]
			expectedNames := []string{"startup-smoke", "startup-default-runtime"}
			if name != expectedNames[index] {
				t.Fatalf("kata-perf %s job %d name = %#v, want %#v", modeName, index, name, expectedNames[index])
			}
			if got, want := job["gc"], true; got != want {
				t.Fatalf("kata-perf %s job %v gc = %#v, want %#v", modeName, name, got, want)
			}
			if got, want := job["podWait"], true; got != want {
				t.Fatalf("kata-perf %s job %v podWait = %#v, want %#v", modeName, name, got, want)
			}
			if got, want := job["waitWhenFinished"], false; got != want {
				t.Fatalf("kata-perf %s job %v waitWhenFinished = %#v, want %#v", modeName, name, got, want)
			}
			if got, want := job["jobIterations"], expectedIterations; got != want {
				t.Fatalf("kata-perf %s job %v jobIterations = %#v, want %#v", modeName, name, got, want)
			}
			if got, want := job["iterationsPerNamespace"], 1; got != want {
				t.Fatalf("kata-perf %s job %v iterationsPerNamespace = %#v, want %#v", modeName, name, got, want)
			}
			if got, want := job["qps"], 5; got != want {
				t.Fatalf("kata-perf %s job %v qps = %#v, want %#v", modeName, name, got, want)
			}
			if got, want := job["burst"], 5; got != want {
				t.Fatalf("kata-perf %s job %v burst = %#v, want %#v", modeName, name, got, want)
			}
			if got, want := job["jobPause"], "1m"; got != want {
				t.Fatalf("kata-perf %s job %v jobPause = %#v, want %#v", modeName, name, got, want)
			}
			if got, want := job["metricsClosing"], "afterMeasurements"; got != want {
				t.Fatalf("kata-perf %s job %v metricsClosing = %#v, want %#v", modeName, name, got, want)
			}
		}
	}
}

func TestKataPerfReportsWorkloadSandboxMetrics(t *testing.T) {
	root := filepath.Join("..", "..")
	var metrics []struct {
		Query        string `yaml:"query"`
		MetricName   string `yaml:"metricName"`
		Instant      bool   `yaml:"instant"`
		CaptureStart bool   `yaml:"captureStart"`
	}
	if err := config.LoadYAML(filepath.Join(root, "suites/kata-perf/metrics.yml"), &metrics); err != nil {
		t.Fatal(err)
	}
	wantQueries := map[string][]string{
		"runPodSandboxCount": {
			"kubelet_run_podsandbox_duration_seconds_count",
			`perf_azure_com_node_role="workload"`,
			"sum by (runtime_handler)",
		},
		"runPodSandboxSum": {
			"kubelet_run_podsandbox_duration_seconds_sum",
			`perf_azure_com_node_role="workload"`,
			"sum by (runtime_handler)",
		},
	}
	if len(metrics) != len(wantQueries) {
		t.Fatalf("kata-perf metrics count = %d, want %d: %#v", len(metrics), len(wantQueries), metrics)
	}
	for _, metric := range metrics {
		if !metric.Instant {
			t.Fatalf("%s must be an instant query", metric.MetricName)
		}
		if !metric.CaptureStart {
			t.Fatalf("%s must capture the job-start baseline", metric.MetricName)
		}
		parts, ok := wantQueries[metric.MetricName]
		if !ok {
			t.Fatalf("unexpected kata-perf metric %q", metric.MetricName)
		}
		for _, part := range parts {
			if !strings.Contains(metric.Query, part) {
				t.Fatalf("%s query missing %q: %s", metric.MetricName, part, metric.Query)
			}
		}
		if strings.Contains(metric.Query, "_bucket") || strings.Contains(metric.Query, "histogram_quantile") {
			t.Fatalf("%s must not use underspecified sandbox histogram quantiles: %s", metric.MetricName, metric.Query)
		}
		if strings.Contains(metric.Query, "increase(") || strings.Contains(metric.Query, "offset") {
			t.Fatalf("%s must capture raw start/end counters: %s", metric.MetricName, metric.Query)
		}
	}

	req, err := requirements.Load(root, "kata-perf")
	if err != nil {
		t.Fatal(err)
	}
	wantUnits := map[string]string{"runPodSandboxCount": "count", "runPodSandboxSum": "seconds"}
	if !reflect.DeepEqual(req.Requires.Reporting.PrometheusMetricUnits, wantUnits) {
		t.Fatalf("kata-perf metric units = %#v, want %#v", req.Requires.Reporting.PrometheusMetricUnits, wantUnits)
	}
	wantRequired := []string{"kubelet_run_podsandbox_duration_seconds_count", "kubelet_run_podsandbox_duration_seconds_sum"}
	if !reflect.DeepEqual(req.Requires.Observability.Prometheus.Metrics, wantRequired) {
		t.Fatalf("kata-perf required metrics = %#v, want %#v", req.Requires.Observability.Prometheus.Metrics, wantRequired)
	}
}

func TestSuiteSchemaAcceptsSetupResources(t *testing.T) {
	root := filepath.Join("..", "..")
	dir := t.TempDir()
	path := filepath.Join(dir, "suite.yml")
	data := []byte(`name: setup-suite
description: Suite with setup resources
tests:
  - startup
setup:
  resources:
    - name: kata-runtimeclass
      path: setup/runtimeclass.yml
      wait:
        - kind: exists
          resource: runtimeclass/custom-kata
          timeout: 1m
    - name: node-prep
      path: setup/node-prep-daemonset.yml
      wait:
        - kind: rollout
          resource: daemonset/node-prep
          namespace: kube-system
          timeout: 10m
        - kind: condition
          resource: pod/node-prep-check
          namespace: default
          condition: Ready
          timeout: 5m
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas/suite.schema.json"), path); err != nil {
		t.Fatalf("suite schema rejected setup resources: %v", err)
	}
}

func TestSuiteSchemaRejectsInvalidSetupWait(t *testing.T) {
	root := filepath.Join("..", "..")
	dir := t.TempDir()
	path := filepath.Join(dir, "suite.yml")
	data := []byte(`name: setup-suite
description: Suite with invalid setup wait
tests:
  - startup
setup:
  resources:
    - name: node-prep
      path: setup/node-prep-daemonset.yml
      wait:
        - kind: sleep
          resource: daemonset/node-prep
          timeout: 10m
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas/suite.schema.json"), path); err == nil {
		t.Fatal("suite schema accepted invalid setup wait kind")
	}
}

func TestSuiteSchemaRequiresConditionForConditionWait(t *testing.T) {
	root := filepath.Join("..", "..")
	dir := t.TempDir()
	path := filepath.Join(dir, "suite.yml")
	data := []byte(`name: setup-suite
description: Suite with invalid condition wait
tests:
  - startup
setup:
  resources:
    - name: node-prep-check
      path: setup/node-prep-check.yml
      wait:
        - kind: condition
          resource: pod/node-prep-check
          namespace: default
          timeout: 5m
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateYAML(filepath.Join(root, "schemas/suite.schema.json"), path); err == nil {
		t.Fatal("suite schema accepted condition wait without condition")
	}
}

func TestSuiteSchemaRejectsUnsafeSetupResourcePaths(t *testing.T) {
	root := filepath.Join("..", "..")
	unsafePaths := []string{
		"/setup/runtimeclass.yml",
		"C:/setup/runtimeclass.yml",
		`C:\setup\runtimeclass.yml`,
		`\setup\runtimeclass.yml`,
		"../setup/runtimeclass.yml",
		`..\setup\runtimeclass.yml`,
		"setup/../runtimeclass.yml",
		`setup\..\runtimeclass.yml`,
	}
	for _, unsafePath := range unsafePaths {
		t.Run(unsafePath, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "suite.yml")
			data := []byte(`name: setup-suite
description: Suite with unsafe setup path
tests:
  - startup
setup:
  resources:
    - name: node-prep
      path: ` + unsafePath + `
`)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := config.ValidateYAML(filepath.Join(root, "schemas/suite.schema.json"), path); err == nil {
				t.Fatalf("suite schema accepted unsafe setup resource path %q", unsafePath)
			}
		})
	}
}

func TestKataPerfSuiteHasGenericKataIdentity(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{
		"suites/kata-perf/suite.yml",
		"suites/kata-perf/requirements.yml",
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

	assertContains("infra/aks/main.bicep", "param nodePools NodePool[]")
	assertContains("infra/aks/main.bicep", "'KataMshvVmIsolation'")
	assertContains("infra/aks/main.bicep", "osSKU: pool.osSKU")
	assertContains("infra/aks/main.bicep", "workloadRuntime: pool.workloadRuntime")
	assertContains("suites/kata-perf/templates/pod.yml", "runtimeClassName: kata-vm-isolation")
	assertContains("suites/kata-perf/templates/pod.yml", "kubernetes.azure.com/os-sku: AzureLinux")
}

func TestSuiteRequirementsDriveNodePoolsWithoutParameterFiles(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, test := range []struct {
		name      string
		wantCount int
		wantSize  string
	}{
		{name: "kata-perf", wantCount: 1, wantSize: "Standard_D16as_v5"},
		{name: "kata-io", wantCount: 4, wantSize: "Standard_D8s_v5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc, err := requirements.Load(root, test.name)
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Requires.Infrastructure.NodePools) != 2 {
				t.Fatalf("node pools = %#v, want system and user pools", doc.Requires.Infrastructure.NodePools)
			}
			system, user := doc.Requires.Infrastructure.NodePools[0], doc.Requires.Infrastructure.NodePools[1]
			if system.Name != "systempool" || system.Mode != "System" {
				t.Fatalf("system pool = %#v", system)
			}
			if user.Name != "userpool" || user.Mode != "User" || user.Count != test.wantCount || user.VMSize != test.wantSize {
				t.Fatalf("user pool = %#v", user)
			}
			if doc.Requires.NodeSelectors[0].Pool != "userpool" {
				t.Fatalf("selector pool = %q, want userpool", doc.Requires.NodeSelectors[0].Pool)
			}
			if _, err := os.Stat(filepath.Join(root, "suites", test.name, "infra.bicepparam")); !os.IsNotExist(err) {
				t.Fatalf("infra.bicepparam still exists: %v", err)
			}
		})
	}
}

func TestRequirementsSchemaRejectsInvalidNodePools(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, invalidPool := range []string{
		"name: UPPER\n        mode: System\n        count: 1\n        vmSize: Standard_D4s_v5\n        osType: Linux\n        osSKU: Ubuntu\n        workloadRuntime: OCIContainer\n        labels: {}\n        taints: []",
		"name: systempool\n        mode: System\n        count: 0\n        vmSize: Standard_D4s_v5\n        osType: Linux\n        osSKU: Ubuntu\n        workloadRuntime: OCIContainer\n        labels: {}\n        taints: []",
		"name: systempool\n        mode: System\n        count: 1\n        vmSize: Standard_D4s_v5\n        osType: Linux\n        osSKU: Ubuntu\n        workloadRuntime: OCIContainer\n        labels: {}\n        taints: [dedicated=true:Unknown]",
	} {
		t.Run(invalidPool[:12], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "requirements.yml")
			data := "suite: demo\nrequires:\n  infrastructure:\n    provider: aks\n    nodePools:\n      - " + invalidPool + "\n  kubernetes:\n    minVersion: \"1.36\"\n  nodeSelectors: []\n  reporting:\n    sources:\n      standardSummary: false\n      kubeBurner: true\n    prometheusMetricUnits: {}\n  observability:\n    prometheus:\n      required: false\n      install: false\n      namespace: monitoring\n      imageKey: prometheus\n      serviceName: prometheus\n      servicePort: 9090\n      localPort: 9090\n"
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := config.ValidateYAML(filepath.Join(root, "schemas", "requirements.schema.json"), path); err == nil {
				t.Fatal("schema accepted invalid node pool")
			}
		})
	}
}

func TestAKSTemplateUsesArbitraryNodePoolsAndDerivedRegistryName(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "infra", "aks", "main.bicep"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"param nodePools NodePool[]", "[for pool in nodePools:", "nodeLabels: pool.labels", "nodeTaints: pool.taints"} {
		if !strings.Contains(text, want) {
			t.Fatalf("main.bicep missing %q", want)
		}
	}
	for _, unwanted := range []string{"param userNodeCount", "param systemNodeCount", "param containerRegistryName"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("main.bicep still contains %q", unwanted)
		}
	}
}

func TestMakeClusterNameOverrideIsOptional(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, target := range []string{"provision", "run-suite"} {
		t.Run(target, func(t *testing.T) {
			defaultCommand := exec.Command("make", "-n", target, "TEST_SUITE=demo")
			defaultCommand.Dir = root
			defaultOutput, err := defaultCommand.CombinedOutput()
			if err != nil {
				t.Fatalf("make -n %s: %v\n%s", target, err, defaultOutput)
			}
			if strings.Contains(string(defaultOutput), "--cluster-name") {
				t.Fatalf("default %s command unexpectedly contains --cluster-name:\n%s", target, defaultOutput)
			}

			overrideCommand := exec.Command("make", "-n", target, "TEST_SUITE=demo", "CLUSTER_NAME=existing-aks")
			overrideCommand.Dir = root
			overrideOutput, err := overrideCommand.CombinedOutput()
			if err != nil {
				t.Fatalf("make -n %s with override: %v\n%s", target, err, overrideOutput)
			}
			if !strings.Contains(string(overrideOutput), `--cluster-name "existing-aks"`) {
				t.Fatalf("overridden %s command missing cluster name:\n%s", target, overrideOutput)
			}
		})
	}
}

func TestAKSTemplateConditionallyDeploysContainerRegistry(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "infra", "aks", "main.bicep"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"param deployContainerRegistry bool = true",
		"resource acr 'Microsoft.ContainerRegistry/registries@2023-07-01' = if (deployContainerRegistry)",
		"resource aksAcrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (deployContainerRegistry)",
		"output containerRegistryName string = deployContainerRegistry ? acr.name : ''",
		"output containerRegistryLoginServer string = deployContainerRegistry ? acr!.properties.loginServer : ''",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("infra/aks/main.bicep must contain %q", want)
		}
	}
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

func TestKataIOContractsValidate(t *testing.T) {
	root := filepath.Join("..", "..")
	cases := []struct {
		schema string
		file   string
	}{
		{"schemas/suite.schema.json", "suites/kata-io/suite.yml"},
		{"schemas/requirements.schema.json", "suites/kata-io/requirements.yml"},
		{"schemas/mode.schema.json", "suites/kata-io/vars/fio-fast.yml"},
		{"schemas/mode.schema.json", "suites/kata-io/vars/git-fast.yml"},
		{"schemas/mode.schema.json", "suites/kata-io/vars/fio.yml"},
		{"schemas/mode.schema.json", "suites/kata-io/vars/git.yml"},
	}
	for _, tc := range cases {
		if err := config.ValidateYAML(filepath.Join(root, tc.schema), filepath.Join(root, tc.file)); err != nil {
			t.Fatalf("%s failed validation against %s: %v", tc.file, tc.schema, err)
		}
	}
}

func TestKataIOExposesOnlyCurrentModes(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "suites", "kata-io", "vars", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	modes := make([]string, 0, len(matches))
	for _, match := range matches {
		modes = append(modes, strings.TrimSuffix(filepath.Base(match), ".yml"))
	}
	if got, want := modes, []string{"fio-fast", "fio", "git-fast", "git"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kata-io modes = %v, want %v", got, want)
	}
}

func TestKataIOMergeReadyContracts(t *testing.T) {
	root := filepath.Join("..", "..")
	modes := []string{"fio-fast", "git-fast", "fio", "git"}
	for _, mode := range modes {
		data, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "vars", mode+".yml"))
		if err != nil {
			t.Fatalf("missing mode %s: %v", mode, err)
		}
		var doc struct {
			Cleanup      bool           `yaml:"cleanup"`
			WorkloadFile string         `yaml:"workloadFile"`
			TemplateVars map[string]any `yaml:"templateVars"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Cleanup {
			t.Fatalf("%s cleanup must be false so results PVC survives artifact copy", mode)
		}
		if doc.TemplateVars["resultsStorageClass"] != nil || doc.TemplateVars["resultsVolumeSize"] != nil {
			t.Fatalf("%s must not define results PVC vars because setup/results-pvc.yml is static", mode)
		}
		if asString(doc.TemplateVars["k8sRunID"]) == "" || !strings.Contains(asString(doc.TemplateVars["k8sRunID"]), "{{.runTimestampDNS}}") {
			t.Fatalf("%s k8sRunID must contain {{.runTimestampDNS}}", mode)
		}
		if doc.WorkloadFile != "" {
			if _, err := os.Stat(filepath.Join(root, "suites", "kata-io", doc.WorkloadFile)); err != nil {
				t.Fatalf("%s workloadFile %s missing: %v", mode, doc.WorkloadFile, err)
			}
		}
	}
}

func TestKataIOWorkloadsCleanPreviousPodsAndWorkPVCs(t *testing.T) {
	root := filepath.Join("..", "..")
	workloads := kataIOWorkloadFiles(t)
	workPVCData, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "templates", "work-pvc.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workPVCData), "pvc-role: work") {
		t.Fatal("work-pvc.yml must label work PVCs with pvc-role: work")
	}
	for _, workloadFile := range workloads {
		t.Run(workloadFile, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", workloadFile))
			if err != nil {
				t.Fatal(err)
			}
			var workload struct {
				Jobs []struct {
					Name            string `yaml:"name"`
					JobType         string `yaml:"jobType"`
					Namespace       string `yaml:"namespace"`
					SkipIndexing    bool   `yaml:"skipIndexing"`
					NamespacedIters *bool  `yaml:"namespacedIterations"`
					Objects         []struct {
						Kind          string            `yaml:"kind"`
						LabelSelector map[string]string `yaml:"labelSelector"`
						InputVars     map[string]any    `yaml:"inputVars"`
					} `yaml:"objects"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(data, &workload); err != nil {
				t.Fatal(err)
			}
			foundCleanup := map[string]bool{"Job": false, "Pod": false, "PersistentVolumeClaim": false}
			for _, job := range workload.Jobs {
				if job.Namespace == "kata-io" && (job.NamespacedIters == nil || *job.NamespacedIters) {
					t.Fatalf("job %s must set namespacedIterations: false", job.Name)
				}
				if job.JobType != "delete" {
					continue
				}
				if !job.SkipIndexing {
					t.Fatalf("cleanup job %s must set skipIndexing: true", job.Name)
				}
				for _, object := range job.Objects {
					if object.LabelSelector["app"] == "kata-io" && object.LabelSelector["benchmark"] == "io" {
						foundCleanup[object.Kind] = true
					}
					if object.Kind == "PersistentVolumeClaim" && object.LabelSelector["pvc-role"] != "work" {
						t.Fatalf("work PVC cleanup selector = %#v, want pvc-role=work", object.LabelSelector)
					}
				}
			}
			for kind, found := range foundCleanup {
				if !found {
					t.Fatalf("missing cleanup delete job for %s", kind)
				}
			}
		})
	}
}

func TestKataIOFioWorkloadsUseOnePreloadJob(t *testing.T) {
	root := filepath.Join("..", "..")
	preloadTemplate, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "templates", "preload-pod.yml"))
	if err != nil {
		t.Fatalf("preload pod template missing: %v", err)
	}
	preloadTemplateText := string(preloadTemplate)
	for _, want := range []string{"image: {{.benchmarkImage}}", "command: [override, command]"} {
		if !strings.Contains(preloadTemplateText, want) {
			t.Fatalf("preload pod template missing %q", want)
		}
	}
	for _, forbidden := range []string{"run-fio.sh", "fioProfile", "/profiles/", "workload-type: fio"} {
		if strings.Contains(preloadTemplateText, forbidden) {
			t.Fatalf("preload pod template must not contain fio workload marker %q", forbidden)
		}
	}
	for _, workloadFile := range []string{"workload-fio-fast.yml", "workload-fio.yml"} {
		t.Run(workloadFile, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", workloadFile))
			if err != nil {
				t.Fatal(err)
			}
			var workload struct {
				Jobs []struct {
					Name          string `yaml:"name"`
					JobType       string `yaml:"jobType"`
					PreLoadImages *bool  `yaml:"preLoadImages"`
					Objects       []struct {
						ObjectTemplate string         `yaml:"objectTemplate"`
						InputVars      map[string]any `yaml:"inputVars"`
					} `yaml:"objects"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(data, &workload); err != nil {
				t.Fatal(err)
			}

			preloadJobs := 0
			for _, job := range workload.Jobs {
				if job.PreLoadImages == nil {
					t.Fatalf("job %s must explicitly set preLoadImages", job.Name)
				}
				if !*job.PreLoadImages {
					continue
				}
				preloadJobs++
				if job.Name != "kio-preload-images" {
					t.Fatalf("preload job name = %q, want kio-preload-images", job.Name)
				}
				if job.JobType != "create" {
					t.Fatalf("preload job type = %q, want create", job.JobType)
				}
				if len(job.Objects) != 1 {
					t.Fatalf("preload job objects = %d, want 1", len(job.Objects))
				}
				object := job.Objects[0]
				if object.ObjectTemplate != "templates/preload-pod.yml" {
					t.Fatalf("preload object template = %q, want templates/preload-pod.yml", object.ObjectTemplate)
				}
				if asString(object.InputVars["jobName"]) == "" {
					t.Fatalf("preload job must set jobName inputVars")
				}
			}
			if preloadJobs != 1 {
				t.Fatalf("%s preload-enabled jobs = %d, want 1", workloadFile, preloadJobs)
			}
		})
	}

	for _, mode := range []string{"fio-fast", "fio"} {
		var vars struct {
			PreLoadImages bool `yaml:"preLoadImages"`
		}
		if err := config.LoadYAML(filepath.Join(root, "suites", "kata-io", "vars", mode+".yml"), &vars); err != nil {
			t.Fatal(err)
		}
		if vars.PreLoadImages {
			t.Fatalf("%s mode must default preLoadImages to false", mode)
		}
	}
}

func kataIOWorkloadFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("..", "..", "suites", "kata-io", "workload*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	files := make([]string, 0, len(matches))
	for _, match := range matches {
		files = append(files, filepath.Base(match))
	}
	return files
}

func TestKataIOFullWorkloadCoversRequiredScenarios(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", "workload-full.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workload struct {
		Jobs []struct {
			Name          string `yaml:"name"`
			JobIterations int    `yaml:"jobIterations"`
			QPS           int    `yaml:"qps"`
			Burst         int    `yaml:"burst"`
			Objects       []struct {
				ObjectTemplate string         `yaml:"objectTemplate"`
				InputVars      map[string]any `yaml:"inputVars"`
			} `yaml:"objects"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workload); err != nil {
		t.Fatal(err)
	}

	type scenarioSpec struct {
		runtime     string
		storage     string
		workload    string
		profile     string
		concurrency string
	}
	expected := map[string]scenarioSpec{}
	runtimes := []string{"standard", "kata"}
	storages := []string{"emptydir", "azure-disk", "azure-files"}
	profiles := []string{"randread-4k", "randwrite-4k", "seqread", "seqwrite", "fsync-heavy"}
	cloneModes := []string{"full", "blobless"}
	concurrencies := []string{"1", "10"}
	for _, runtime := range runtimes {
		for _, storage := range storages {
			for _, concurrency := range concurrencies {
				for _, profile := range profiles {
					scenario := "runtime-" + runtime + "-storage-" + storage + "-fio-" + profile + "-concurrency-" + concurrency
					expected[scenario] = scenarioSpec{runtime: runtime, storage: storage, workload: "fio", profile: profile, concurrency: concurrency}
				}
				for _, cloneMode := range cloneModes {
					scenario := "runtime-" + runtime + "-storage-" + storage + "-git-" + cloneMode + "-concurrency-" + concurrency
					expected[scenario] = scenarioSpec{runtime: runtime, storage: storage, workload: "git", profile: cloneMode, concurrency: concurrency}
				}
			}
		}
	}
	if len(expected) != 84 {
		t.Fatalf("test generated %d expected scenarios, want 84", len(expected))
	}

	found := map[string]bool{}
	for _, job := range workload.Jobs {
		var mainObjectTemplate string
		var mainInputVars map[string]any
		var workPVCInputVars map[string]any
		for _, object := range job.Objects {
			if object.ObjectTemplate == "templates/work-pvc.yml" {
				workPVCInputVars = object.InputVars
				continue
			}
			if scenario, ok := object.InputVars["scenario"].(string); ok {
				mainObjectTemplate = object.ObjectTemplate
				mainInputVars = object.InputVars
				if found[scenario] {
					t.Fatalf("workload-full.yml contains duplicate scenario %q", scenario)
				}
				found[scenario] = true
			}
		}
		if mainInputVars == nil {
			continue
		}
		scenario := mainInputVars["scenario"].(string)
		spec, ok := expected[scenario]
		if !ok {
			t.Fatalf("workload-full.yml contains unexpected scenario %q", scenario)
		}

		wantIterations := 1
		if spec.concurrency == "10" {
			wantIterations = 10
		}
		if job.JobIterations != wantIterations || job.QPS != wantIterations || job.Burst != wantIterations {
			t.Fatalf("job %s for %s has jobIterations/qps/burst = %d/%d/%d, want %d/%d/%d", job.Name, scenario, job.JobIterations, job.QPS, job.Burst, wantIterations, wantIterations, wantIterations)
		}

		storageTemplate := "pvc"
		if spec.storage == "emptydir" {
			storageTemplate = "emptydir"
			if workPVCInputVars != nil {
				t.Fatalf("emptydir scenario %s includes work-pvc object", scenario)
			}
		} else {
			if workPVCInputVars == nil {
				t.Fatalf("PVC scenario %s missing work-pvc object", scenario)
			}
			wantClass := "managed-csi"
			wantAccessMode := "ReadWriteOnce"
			if spec.storage == "azure-files" {
				wantClass = "azurefile-csi"
				wantAccessMode = "ReadWriteMany"
			}
			if got := asString(workPVCInputVars["workStorageClass"]); got != wantClass {
				t.Fatalf("scenario %s workStorageClass = %q, want %q", scenario, got, wantClass)
			}
			if got := asString(workPVCInputVars["workAccessMode"]); got != wantAccessMode {
				t.Fatalf("scenario %s workAccessMode = %q, want %q", scenario, got, wantAccessMode)
			}
		}

		wantTemplate := "templates/" + spec.workload + "-" + storageTemplate + "-" + spec.runtime + "-job.yml"
		if mainObjectTemplate != wantTemplate {
			t.Fatalf("scenario %s objectTemplate = %q, want %q", scenario, mainObjectTemplate, wantTemplate)
		}
		if got := asString(mainInputVars["runtime"]); got != "runtime-"+spec.runtime {
			t.Fatalf("scenario %s runtime inputVar = %q, want %q", scenario, got, "runtime-"+spec.runtime)
		}
		if got := asString(mainInputVars["storageType"]); got != "storage-"+spec.storage {
			t.Fatalf("scenario %s storageType inputVar = %q, want %q", scenario, got, "storage-"+spec.storage)
		}
		if got := asString(mainInputVars["concurrency"]); got != spec.concurrency {
			t.Fatalf("scenario %s concurrency inputVar = %q, want %q", scenario, got, spec.concurrency)
		}
		if spec.workload == "fio" {
			if got := asString(mainInputVars["fioProfileName"]); got != spec.profile {
				t.Fatalf("scenario %s fioProfileName = %q, want %q", scenario, got, spec.profile)
			}
		} else if got := asString(mainInputVars["cloneMode"]); got != spec.profile {
			t.Fatalf("scenario %s cloneMode = %q, want %q", scenario, got, spec.profile)
		}

		templateData, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", mainObjectTemplate))
		if err != nil {
			t.Fatal(err)
		}
		hasRuntimeClass := strings.Contains(string(templateData), "runtimeClassName")
		if spec.runtime == "standard" && hasRuntimeClass {
			t.Fatalf("standard template %s includes runtimeClassName", mainObjectTemplate)
		}
		if spec.runtime == "kata" && !hasRuntimeClass {
			t.Fatalf("kata template %s omits runtimeClassName", mainObjectTemplate)
		}
	}
	if len(found) != 84 {
		t.Fatalf("workload-full.yml has %d unique scenarios, want 84", len(found))
	}
	for scenario := range expected {
		if !found[scenario] {
			t.Fatalf("workload-full.yml missing scenario %q", scenario)
		}
	}
}

func TestKataIOInfraDefaultsCanScheduleConcurrencyTen(t *testing.T) {
	doc, err := requirements.Load(filepath.Join("..", ".."), "kata-io")
	if err != nil {
		t.Fatal(err)
	}
	user := doc.Requires.Infrastructure.NodePools[1]
	if user.OSSKU != "AzureLinux" || user.WorkloadRuntime != "KataMshvVmIsolation" {
		t.Fatalf("user pool = %#v", user)
	}
	nodeCount, vmSize := user.Count, user.VMSize
	vcpuBySize := map[string]int{"Standard_D8s_v5": 8, "Standard_D16s_v5": 16}
	vcpu, ok := vcpuBySize[vmSize]
	if !ok {
		t.Fatalf("test does not know vCPU count for %s", vmSize)
	}
	podCPU := maxKataIOPodCPURequest(t)
	allocatableCPUPerNode := vcpu - 1
	if allocatableCPUPerNode < 1 {
		t.Fatalf("test assumes at least 1 CPU allocatable per node, got %d vCPU for %s", vcpu, vmSize)
	}
	podsPerNode := allocatableCPUPerNode / podCPU
	if got, want := nodeCount*podsPerNode, 10; got < want {
		t.Fatalf("default pool can schedule about %d concurrency-10 pods (%d nodes x %d pods/node after 1 CPU/node headroom), want at least %d for pods requesting %d CPU", got, nodeCount, podsPerNode, want, podCPU)
	}
}

func TestKataIOInfraDefaultsFitWestUS2Quota(t *testing.T) {
	doc, err := requirements.Load(filepath.Join("..", ".."), "kata-io")
	if err != nil {
		t.Fatal(err)
	}
	user := doc.Requires.Infrastructure.NodePools[1]
	nodeCount, vmSize := user.Count, user.VMSize
	vcpuBySize := map[string]int{"Standard_D8s_v5": 8, "Standard_D16s_v5": 16}
	vcpu, ok := vcpuBySize[vmSize]
	if !ok {
		t.Fatalf("test does not know vCPU count for %s", vmSize)
	}
	const systemPoolVCPU = 4
	const observedRemainingQuota = 40
	if requested := systemPoolVCPU + nodeCount*vcpu; requested > observedRemainingQuota {
		t.Fatalf("default kata-io pool requests %d DSv5-family vCPUs, want <= observed westus2 remaining quota %d", requested, observedRemainingQuota)
	}
}

func TestKataIOInfraPinsRequiredKubernetesVersion(t *testing.T) {
	doc, err := requirements.Load(filepath.Join("..", ".."), "kata-io")
	if err != nil {
		t.Fatal(err)
	}
	version := doc.Requires.Kubernetes.MinVersion
	if compareMajorMinor(version, "1.36") < 0 {
		t.Fatalf("kata-io kubernetesVersion = %q, want >= 1.36 to satisfy requirements.yml", version)
	}
}

func TestKataIOModesUsePerRunIDPlaceholder(t *testing.T) {
	for _, mode := range []string{"fio-fast", "git-fast", "fio", "git"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", "vars", mode+".yml"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			TemplateVars map[string]any `yaml:"templateVars"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		runID := asString(doc.TemplateVars["runID"])
		if !strings.Contains(runID, "{{.runTimestamp}}") {
			t.Fatalf("%s mode runID = %q, want per-run {{.runTimestamp}} placeholder", mode, runID)
		}
		if !strings.HasPrefix(runID, "kata-io-"+mode+"-") {
			t.Fatalf("%s mode runID = %q, want kata-io-%s prefix", mode, runID, mode)
		}
	}
}

func TestKataIOModesPreserveResultsForArtifactCopy(t *testing.T) {
	for _, mode := range []string{"fio-fast", "git-fast", "fio", "git"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", "vars", mode+".yml"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Cleanup bool `yaml:"cleanup"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Cleanup {
			t.Fatalf("%s mode cleanup must be false so kube-burner does not delete the results PVC before artifact copy", mode)
		}
	}
}

func TestKataIOMetricsDoNotUseUnsupportedNodeExporterDiskMetrics(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", "metrics.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var metrics []struct {
		Query      string `yaml:"query"`
		MetricName string `yaml:"metricName"`
	}
	if err := yaml.Unmarshal(data, &metrics); err != nil {
		t.Fatal(err)
	}
	for _, metric := range metrics {
		if strings.Contains(metric.Query, "node_disk_") {
			t.Fatalf("metrics.yml contains unsupported node-exporter disk query for %s: %s", metric.MetricName, metric.Query)
		}
	}
}

func TestKataIOMetricsUseValidElapsedDurationTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", "metrics.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "{{ .elapsed }}s") {
		t.Fatalf("metrics.yml must not append an extra s to .elapsed; kube-burner already renders a duration with units")
	}
}

func TestKataIOBenchmarkImageFilesExist(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{
		"suites/kata-io/images/benchmark/Dockerfile",
		"suites/kata-io/images/benchmark/scripts/override",
		"suites/kata-io/images/benchmark/scripts/run-fio.sh",
		"suites/kata-io/images/benchmark/scripts/run-git-clone.sh",
		"suites/kata-io/images/benchmark/fio-profiles/randread-4k.fio",
		"suites/kata-io/images/benchmark/fio-profiles/randwrite-4k.fio",
		"suites/kata-io/images/benchmark/fio-profiles/seqread.fio",
		"suites/kata-io/images/benchmark/fio-profiles/seqwrite.fio",
		"suites/kata-io/images/benchmark/fio-profiles/fsync-heavy.fio",
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("%s missing: %v", file, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s is empty", file)
		}
	}
}

func TestKataIOBenchmarkImageSupportsKubeBurnerPreloadCommand(t *testing.T) {
	root := filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark")
	script := filepath.Join(root, "scripts", "override")
	binDir := t.TempDir()
	target := filepath.Join(binDir, "override")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := exec.Command("override", "command")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("override command failed: %v\n%s", err, output)
	}

	for _, args := range [][]string{nil, {"command", "extra"}, {"other"}} {
		cmd := exec.Command("override", args...)
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "unsupported kube-burner preload command") {
			t.Fatalf("override %v error = %v, output = %q", args, err, output)
		}
	}

	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "COPY scripts/override /usr/local/bin/override") {
		t.Fatal("benchmark Dockerfile does not install the kube-burner override command")
	}
}

func TestKataIOFioSummary(t *testing.T) {
	requireKataIOScriptTools(t)
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "time"), `#!/usr/bin/env bash
set -euo pipefail
while (($#)); do
  case "$1" in
    -v) shift ;;
    -o) printf 'fake time output\n' > "$2"; shift 2 ;;
    *) break ;;
  esac
done
"$@"
`)
	writeExecutable(t, filepath.Join(binDir, "fio"), `#!/usr/bin/env bash
set -euo pipefail
output=""
for arg in "$@"; do
  case "$arg" in --output=*) output="${arg#--output=}" ;; esac
done
if [[ "${FAKE_FIO_MALFORMED:-}" == "1" ]]; then
  printf '{malformed\n' > "$output"
elif [[ "${FAKE_FIO_INCOMPLETE:-}" == "1" ]]; then
  printf '{"jobs":[{"read":{"iops":1}}]}\n' > "$output"
elif [[ "${FAKE_FIO_READ_ONLY:-}" == "1" ]]; then
  printf '%s\n' '{"jobs":[{"read":{"iops":101.5,"bw_bytes":4096,"runtime":1250,"total_ios":100,"clat_ns":{"percentile":{"99.000000":700}}},"write":{"iops":0,"bw_bytes":0,"runtime":0,"total_ios":0,"clat_ns":{}}}]}' > "$output"
elif [[ "${FAKE_FIO_WRITE_ONLY:-}" == "1" ]]; then
  printf '%s\n' '{"jobs":[{"read":{"iops":0,"bw_bytes":0,"runtime":0,"total_ios":0,"clat_ns":{}},"write":{"iops":202.5,"bw_bytes":8192,"runtime":750,"total_ios":200,"clat_ns":{"percentile":{"99.000000":900}}}}]}' > "$output"
elif [[ "${FAKE_FIO_ACTIVE_MISSING_P99:-}" == "1" ]]; then
  printf '%s\n' '{"jobs":[{"read":{"iops":1,"bw_bytes":4096,"runtime":100,"total_ios":1,"clat_ns":{}},"write":{"iops":0,"bw_bytes":0,"runtime":0,"total_ios":0,"clat_ns":{}}}]}' > "$output"
elif [[ "${FAKE_FIO_NEGATIVE_TOTAL_IOS:-}" == "1" ]]; then
  printf '%s\n' '{"jobs":[{"read":{"iops":0,"bw_bytes":0,"runtime":0,"total_ios":-1,"clat_ns":{}},"write":{"iops":0,"bw_bytes":0,"runtime":0,"total_ios":0,"clat_ns":{}}}]}' > "$output"
elif [[ "${FAKE_FIO_MULTI_JOB:-}" == "1" ]]; then
  printf '%s\n' '{"jobs":[{"read":{"iops":100,"bw_bytes":1000,"runtime":1000,"total_ios":1,"clat_ns":{"percentile":{"99.000000":700}}},"write":{"iops":0,"bw_bytes":0,"runtime":0,"total_ios":0,"clat_ns":{}}},{"read":{"iops":0,"bw_bytes":0,"runtime":0,"total_ios":0,"clat_ns":{}},"write":{"iops":20,"bw_bytes":2000,"runtime":1500,"total_ios":2,"clat_ns":{"percentile":{"99.000000":900}}}},{"read":{"iops":50.5,"bw_bytes":500,"runtime":2000,"total_ios":3,"clat_ns":{"percentile":{"99.000000":800}}},"write":{"iops":30,"bw_bytes":3000,"runtime":500,"total_ios":4,"clat_ns":{"percentile":{"99.000000":1000}}}}]}' > "$output"
else
  cat > "$output" <<'EOF'
{"jobs":[{"read":{"iops":101.5,"bw_bytes":4096,"runtime":1250,"total_ios":100,"clat_ns":{"percentile":{"99.000000":700}}},"write":{"iops":2,"bw_bytes":8192,"runtime":500,"total_ios":2,"clat_ns":{"percentile":{"99.000000":900}}}}]}
EOF
fi
printf 'fake fio stdout\n'
printf 'fake fio stderr\n' >&2
exit "${FAKE_BENCHMARK_EXIT:?}"
`)

	resultsDir := filepath.Join(tempDir, "results")
	cmd := exec.Command("bash", filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark", "scripts", "run-fio.sh"))
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TIME_BIN="+filepath.Join(binDir, "time"),
		"FAKE_BENCHMARK_EXIT=0",
		"RUN_ID=run-1", "SCENARIO=fio-scenario", "SAMPLE_ID=sample-a",
		"FIO_PROFILE=/profiles/randread.fio", "FIO_PROFILE_NAME=randread-4k",
		"RUNTIME=kata", "STORAGE_TYPE=azure-disk", "CONCURRENCY=10",
		"WORK_DIR="+filepath.Join(tempDir, "work"), "RESULTS_DIR="+resultsDir,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run-fio.sh failed: %v\n%s", err, output)
	}

	sampleDir := filepath.Join(resultsDir, "run-1", "fio-scenario", "sample-a")
	assertKataIOSummary(t, resultsDir, tempDir, map[string]string{
		"runtime": "kata", "storage": "azure-disk", "workload": "fio",
		"profile": "randread-4k", "concurrency": "10", "sample": "sample-a",
	}, map[string]string{
		"total_duration": "seconds", "active_runtime": "seconds", "setup_overhead": "seconds",
		"exit_code": "code", "read_iops": "operations/second", "write_iops": "operations/second",
		"read_bandwidth": "bytes/second", "write_bandwidth": "bytes/second",
		"read_clat_p99": "nanoseconds", "write_clat_p99": "nanoseconds",
	}, map[string]string{"exit_code": "0", "active_runtime": "1.25", "read_iops": "101.5"})
	assertFilesExist(t, sampleDir, "fio.json", "time.txt", "stdout.log", "stderr.log", "proc-self-io-before.txt", "proc-self-io-after.txt", "df-before.txt", "df-after.txt")
	assertFileDoesNotExist(t, filepath.Join(sampleDir, "summary.prom"))

	malformedResultsDir := filepath.Join(tempDir, "malformed-results")
	cmd = exec.Command("bash", filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark", "scripts", "run-fio.sh"))
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TIME_BIN="+filepath.Join(binDir, "time"),
		"FAKE_BENCHMARK_EXIT=19", "FAKE_FIO_MALFORMED=1",
		"RUN_ID=run-2", "SCENARIO=fio-scenario", "SAMPLE_ID=sample-c",
		"FIO_PROFILE=/profiles/randread.fio", "FIO_PROFILE_NAME=randread-4k",
		"RUNTIME=kata", "STORAGE_TYPE=azure-disk", "CONCURRENCY=10",
		"WORK_DIR="+filepath.Join(tempDir, "malformed-work"), "RESULTS_DIR="+malformedResultsDir,
	)
	assertCommandExitCode(t, cmd, 19)
	assertKataIOSummary(t, malformedResultsDir, tempDir, map[string]string{
		"runtime": "kata", "storage": "azure-disk", "workload": "fio",
		"profile": "randread-4k", "concurrency": "10", "sample": "sample-c",
	}, map[string]string{
		"total_duration": "seconds", "active_runtime": "seconds", "setup_overhead": "seconds",
		"exit_code": "code", "read_iops": "operations/second", "write_iops": "operations/second",
		"read_bandwidth": "bytes/second", "write_bandwidth": "bytes/second",
		"read_clat_p99": "nanoseconds", "write_clat_p99": "nanoseconds",
	}, map[string]string{"exit_code": "19", "active_runtime": "0", "read_iops": "0"})

	malformedSuccessResultsDir := filepath.Join(tempDir, "malformed-success-results")
	cmd = exec.Command("bash", filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark", "scripts", "run-fio.sh"))
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TIME_BIN="+filepath.Join(binDir, "time"),
		"FAKE_BENCHMARK_EXIT=0", "FAKE_FIO_MALFORMED=1",
		"RUN_ID=run-3", "SCENARIO=fio-scenario", "SAMPLE_ID=sample-d",
		"FIO_PROFILE=/profiles/randread.fio", "FIO_PROFILE_NAME=randread-4k",
		"RUNTIME=kata", "STORAGE_TYPE=azure-disk", "CONCURRENCY=10",
		"WORK_DIR="+filepath.Join(tempDir, "malformed-success-work"), "RESULTS_DIR="+malformedSuccessResultsDir,
	)
	if err := cmd.Run(); err == nil {
		t.Fatal("run-fio.sh succeeded with malformed FIO JSON and exit 0")
	}
	assertFileDoesNotExist(t, filepath.Join(malformedSuccessResultsDir, "run-3", "fio-scenario", "sample-d", "summary.json"))

	incompleteResultsDir := filepath.Join(tempDir, "incomplete-results")
	cmd = exec.Command("bash", filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark", "scripts", "run-fio.sh"))
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TIME_BIN="+filepath.Join(binDir, "time"),
		"FAKE_BENCHMARK_EXIT=0", "FAKE_FIO_INCOMPLETE=1",
		"RUN_ID=run-4", "SCENARIO=fio-scenario", "SAMPLE_ID=sample-e",
		"FIO_PROFILE=/profiles/randread.fio", "FIO_PROFILE_NAME=randread-4k",
		"RUNTIME=kata", "STORAGE_TYPE=azure-disk", "CONCURRENCY=10",
		"WORK_DIR="+filepath.Join(tempDir, "incomplete-work"), "RESULTS_DIR="+incompleteResultsDir,
	)
	if err := cmd.Run(); err == nil {
		t.Fatal("run-fio.sh succeeded with incomplete FIO JSON and exit 0")
	}
	assertFileDoesNotExist(t, filepath.Join(incompleteResultsDir, "run-4", "fio-scenario", "sample-e", "summary.json"))

	for _, tc := range []struct {
		name       string
		env        string
		runID      string
		sampleID   string
		wantValues map[string]string
	}{
		{
			name: "read-only", env: "FAKE_FIO_READ_ONLY=1", runID: "run-5", sampleID: "sample-f",
			wantValues: map[string]string{"read_iops": "101.5", "write_iops": "0", "read_clat_p99": "700", "write_clat_p99": "0", "active_runtime": "1.25"},
		},
		{
			name: "write-only", env: "FAKE_FIO_WRITE_ONLY=1", runID: "run-6", sampleID: "sample-g",
			wantValues: map[string]string{"read_iops": "0", "write_iops": "202.5", "read_clat_p99": "0", "write_clat_p99": "900", "active_runtime": "0.75"},
		},
		{
			name: "multi-job", env: "FAKE_FIO_MULTI_JOB=1", runID: "run-7", sampleID: "sample-h",
			wantValues: map[string]string{
				"read_iops": "150.5", "write_iops": "50", "read_bandwidth": "1500", "write_bandwidth": "5000",
				"read_clat_p99": "800", "write_clat_p99": "1000", "active_runtime": "2",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caseResultsDir := filepath.Join(tempDir, tc.name+"-results")
			cmd := exec.Command("bash", filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark", "scripts", "run-fio.sh"))
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TIME_BIN="+filepath.Join(binDir, "time"), "FAKE_BENCHMARK_EXIT=0", tc.env,
				"RUN_ID="+tc.runID, "SCENARIO=fio-scenario", "SAMPLE_ID="+tc.sampleID,
				"FIO_PROFILE=/profiles/randread.fio", "FIO_PROFILE_NAME=randread-4k",
				"RUNTIME=kata", "STORAGE_TYPE=azure-disk", "CONCURRENCY=10",
				"WORK_DIR="+filepath.Join(tempDir, tc.name+"-work"), "RESULTS_DIR="+caseResultsDir,
			)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("run-fio.sh failed: %v\n%s", err, output)
			}
			assertKataIOSummary(t, caseResultsDir, tempDir, map[string]string{
				"runtime": "kata", "storage": "azure-disk", "workload": "fio",
				"profile": "randread-4k", "concurrency": "10", "sample": tc.sampleID,
			}, map[string]string{
				"total_duration": "seconds", "active_runtime": "seconds", "setup_overhead": "seconds",
				"exit_code": "code", "read_iops": "operations/second", "write_iops": "operations/second",
				"read_bandwidth": "bytes/second", "write_bandwidth": "bytes/second",
				"read_clat_p99": "nanoseconds", "write_clat_p99": "nanoseconds",
			}, tc.wantValues)
		})
	}

	for _, tc := range []struct {
		name     string
		env      string
		runID    string
		sampleID string
	}{
		{name: "active-missing-p99", env: "FAKE_FIO_ACTIVE_MISSING_P99=1", runID: "run-8", sampleID: "sample-i"},
		{name: "negative-total-ios", env: "FAKE_FIO_NEGATIVE_TOTAL_IOS=1", runID: "run-9", sampleID: "sample-j"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caseResultsDir := filepath.Join(tempDir, tc.name+"-results")
			cmd := exec.Command("bash", filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark", "scripts", "run-fio.sh"))
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TIME_BIN="+filepath.Join(binDir, "time"), "FAKE_BENCHMARK_EXIT=0", tc.env,
				"RUN_ID="+tc.runID, "SCENARIO=fio-scenario", "SAMPLE_ID="+tc.sampleID,
				"FIO_PROFILE=/profiles/randread.fio", "FIO_PROFILE_NAME=randread-4k",
				"RUNTIME=kata", "STORAGE_TYPE=azure-disk", "CONCURRENCY=10",
				"WORK_DIR="+filepath.Join(tempDir, tc.name+"-work"), "RESULTS_DIR="+caseResultsDir,
			)
			assertCommandExitCode(t, cmd, 1)
			assertFileDoesNotExist(t, filepath.Join(caseResultsDir, tc.runID, "fio-scenario", tc.sampleID, "summary.json"))
		})
	}
}

func TestKataIOGitSummary(t *testing.T) {
	requireKataIOScriptTools(t)
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "time"), `#!/usr/bin/env bash
set -euo pipefail
while (($#)); do
  case "$1" in
    -v) shift ;;
    -o) printf 'fake time output\n' > "$2"; shift 2 ;;
    *) break ;;
  esac
done
"$@"
`)
	writeExecutable(t, filepath.Join(binDir, "git"), `#!/usr/bin/env bash
set -euo pipefail
target="${@: -1}"
mkdir -p "$target"
printf 'repository data\n' > "$target/file.txt"
printf '{}\n' > "${GIT_TRACE2_EVENT:?}"
printf 'fake trace\n' > "${GIT_TRACE2_PERF:?}"
printf 'fake git stdout\n'
printf 'fake git stderr\n' >&2
exit "${FAKE_BENCHMARK_EXIT:?}"
`)

	resultsDir := filepath.Join(tempDir, "results")
	cmd := exec.Command("bash", filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark", "scripts", "run-git-clone.sh"))
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TIME_BIN="+filepath.Join(binDir, "time"),
		"FAKE_BENCHMARK_EXIT=23",
		"RUN_ID=run-1", "SCENARIO=git-scenario", "SAMPLE_ID=sample-b",
		"REPO_URL=https://example.invalid/repo.git", "CLONE_MODE=blobless",
		"RUNTIME=standard", "STORAGE_TYPE=emptydir", "CONCURRENCY=1",
		"WORK_DIR="+filepath.Join(tempDir, "work"), "RESULTS_DIR="+resultsDir,
	)
	assertCommandExitCode(t, cmd, 23)

	sampleDir := filepath.Join(resultsDir, "run-1", "git-scenario", "sample-b")
	assertKataIOSummary(t, resultsDir, tempDir, map[string]string{
		"runtime": "standard", "storage": "emptydir", "workload": "git",
		"profile": "blobless", "concurrency": "1", "sample": "sample-b",
	}, map[string]string{
		"clone_duration": "seconds", "exit_code": "code", "repository_size": "bytes", "file_count": "files",
	}, map[string]string{"exit_code": "23", "file_count": "1"})
	assertFilesExist(t, sampleDir, "time.txt", "git-stdout.log", "git-stderr.log", "git-trace2-event.json", "git-trace2-perf.log", "repo-size-bytes.txt", "file-count.txt", "proc-self-io-before.txt", "proc-self-io-after.txt", "df-before.txt", "df-after.txt")
	assertFileDoesNotExist(t, filepath.Join(sampleDir, "summary.prom"))
}

func TestKataIOTemplatesProvideSummaryDimensions(t *testing.T) {
	templates, err := filepath.Glob(filepath.Join("..", "..", "suites", "kata-io", "templates", "*-job.yml"))
	if err != nil {
		t.Fatal(err)
	}
	wantTemplates := []string{
		"fio-emptydir-kata-job.yml", "fio-emptydir-standard-job.yml", "fio-pvc-kata-job.yml", "fio-pvc-standard-job.yml",
		"git-emptydir-kata-job.yml", "git-emptydir-standard-job.yml", "git-pvc-kata-job.yml", "git-pvc-standard-job.yml",
	}
	gotTemplates := make([]string, 0, len(templates))
	for _, template := range templates {
		gotTemplates = append(gotTemplates, filepath.Base(template))
	}
	if !reflect.DeepEqual(gotTemplates, wantTemplates) {
		t.Fatalf("Kata IO job templates = %v, want exactly %v", gotTemplates, wantTemplates)
	}
	for _, template := range templates {
		name := filepath.Base(template)
		data, err := os.ReadFile(template)
		if err != nil {
			t.Fatal(err)
		}
		required := []string{"RUNTIME", "STORAGE_TYPE", "CONCURRENCY"}
		if strings.HasPrefix(name, "fio-") {
			required = append(required, "FIO_PROFILE_NAME")
		} else {
			required = append(required, "CLONE_MODE")
		}
		for _, variable := range required {
			if !regexp.MustCompile(`(?m)^\s+- name: ` + variable + `$`).Match(data) {
				t.Errorf("%s missing %s environment variable", name, variable)
			}
		}
	}
}

func requireKataIOScriptTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "awk", "date", "df", "du", "find", "wc", "tr"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("required Kata IO script test tool %q not found in PATH; install jq, gawk, coreutils, and findutils: %v", tool, err)
		}
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertCommandExitCode(t *testing.T, cmd *exec.Cmd, want int) {
	t.Helper()
	output, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("command error = %v, output:\n%s; want exit code %d", err, output, want)
	}
	if got := exitErr.ExitCode(); got != want {
		t.Fatalf("command exit code = %d, output:\n%s; want %d", got, output, want)
	}
}

func assertKataIOSummary(t *testing.T, artifactsDir, runDir string, wantDimensions, wantMetrics, wantValues map[string]string) {
	t.Helper()
	rows, files, err := reporting.ReadStandardSummaries(artifactsDir, runDir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatalf("standard summary files = %d, want 1", files)
	}
	if len(rows) == 0 {
		t.Fatal("standard summary has no metric rows")
	}
	if got := rows[0].Dimensions; !reflect.DeepEqual(got, wantDimensions) {
		t.Fatalf("summary dimensions = %#v, want %#v", got, wantDimensions)
	}
	gotMetrics := make(map[string]string, len(rows))
	gotValues := make(map[string]string, len(rows))
	for _, row := range rows {
		gotMetrics[row.Metric] = row.Unit
		gotValues[row.Metric] = row.Value.Text
	}
	if !reflect.DeepEqual(gotMetrics, wantMetrics) {
		t.Fatalf("summary metrics = %#v, want %#v", gotMetrics, wantMetrics)
	}
	for metric, wantValue := range wantValues {
		if gotValue := gotValues[metric]; gotValue != wantValue {
			t.Fatalf("%s = %s, want %s", metric, gotValue, wantValue)
		}
	}
}

func assertFilesExist(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("raw artifact %s missing: %v", name, err)
		}
	}
}

func assertFileDoesNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists or cannot be checked: %v", path, err)
	}
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	return value.(string)
}

func maxKataIOPodCPURequest(t *testing.T) int {
	t.Helper()
	templates, err := filepath.Glob(filepath.Join("..", "..", "suites", "kata-io", "templates", "*-job.yml"))
	if err != nil {
		t.Fatal(err)
	}
	maxCPU := 0
	for _, template := range templates {
		data, err := os.ReadFile(template)
		if err != nil {
			t.Fatal(err)
		}
		match := regexp.MustCompile(`(?m)^\s+requests:\n\s+cpu:\s+"?(\d+)"?`).FindStringSubmatch(string(data))
		if match == nil {
			t.Fatalf("template %s missing CPU request", template)
		}
		cpu, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatal(err)
		}
		if cpu > maxCPU {
			maxCPU = cpu
		}
	}
	return maxCPU
}

func compareMajorMinor(got string, want string) int {
	parse := func(value string) (int, int) {
		parts := strings.Split(value, ".")
		if len(parts) < 2 {
			return 0, 0
		}
		major, _ := strconv.Atoi(parts[0])
		minor, _ := strconv.Atoi(parts[1])
		return major, minor
	}
	gotMajor, gotMinor := parse(got)
	wantMajor, wantMinor := parse(want)
	if gotMajor != wantMajor {
		return gotMajor - wantMajor
	}
	return gotMinor - wantMinor
}
