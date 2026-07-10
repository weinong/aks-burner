package examples

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Azure/aks-burner/internal/config"
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
		{"schemas/mode.schema.json", "suites/kata-io/vars/smoke.yml"},
		{"schemas/mode.schema.json", "suites/kata-io/vars/full.yml"},
	}
	for _, tc := range cases {
		if err := config.ValidateYAML(filepath.Join(root, tc.schema), filepath.Join(root, tc.file)); err != nil {
			t.Fatalf("%s failed validation against %s: %v", tc.file, tc.schema, err)
		}
	}
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
	infraPath := filepath.Join("..", "..", "suites", "kata-io", "infra.bicepparam")
	data, err := os.ReadFile(infraPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "param userNodeOsSKU = 'AzureLinux'") {
		t.Fatalf("infra.bicepparam must preserve AzureLinux OS SKU")
	}
	if !strings.Contains(text, "param userNodeWorkloadRuntime = 'KataMshvVmIsolation'") {
		t.Fatalf("infra.bicepparam must preserve KataMshvVmIsolation workload runtime")
	}

	nodeCount := bicepIntParam(t, text, "userNodeCount")
	vmSize := bicepStringParam(t, text, "userNodeVmSize")
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
	data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", "infra.bicepparam"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	nodeCount := bicepIntParam(t, text, "userNodeCount")
	vmSize := bicepStringParam(t, text, "userNodeVmSize")
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
	data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", "infra.bicepparam"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	version := bicepStringParam(t, text, "kubernetesVersion")
	if compareMajorMinor(version, "1.36") < 0 {
		t.Fatalf("kata-io kubernetesVersion = %q, want >= 1.36 to satisfy requirements.yml", version)
	}
}

func TestKataIOModesUsePerRunIDPlaceholder(t *testing.T) {
	for _, mode := range []string{"smoke", "full"} {
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
	for _, mode := range []string{"smoke", "full"} {
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

func asString(value any) string {
	if value == nil {
		return ""
	}
	return value.(string)
}

func bicepIntParam(t *testing.T, text string, name string) int {
	t.Helper()
	match := regexp.MustCompile(`(?m)^param\s+` + regexp.QuoteMeta(name) + `\s*=\s*(\d+)\s*$`).FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("missing integer bicep param %s", name)
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func bicepStringParam(t *testing.T, text string, name string) string {
	t.Helper()
	match := regexp.MustCompile(`(?m)^param\s+` + regexp.QuoteMeta(name) + `\s*=\s*'([^']+)'\s*$`).FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("missing string bicep param %s", name)
	}
	return match[1]
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
