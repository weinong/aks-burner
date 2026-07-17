package examples

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"text/template"
	"time"

	"github.com/Azure/aks-burner/internal/config"
	"github.com/Azure/aks-burner/internal/infra"
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
      restart:
        resource: daemonset/node-prep
        namespace: kube-system
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

func nodePoolByName(t *testing.T, pools []infra.NodePool, name string) infra.NodePool {
	t.Helper()
	for _, pool := range pools {
		if pool.Name == name {
			return pool
		}
	}
	t.Fatalf("node pool %q not found in %#v", name, pools)
	return infra.NodePool{}
}

func TestSuiteRequirementsDriveNodePoolsWithoutParameterFiles(t *testing.T) {
	root := filepath.Join("..", "..")

	t.Run("kata-perf", func(t *testing.T) {
		doc, err := requirements.Load(root, "kata-perf")
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Requires.Infrastructure.NodePools) != 2 {
			t.Fatalf("node pools = %#v, want system and user pools", doc.Requires.Infrastructure.NodePools)
		}
		system := nodePoolByName(t, doc.Requires.Infrastructure.NodePools, "systempool")
		if system.Mode != "System" {
			t.Fatalf("system pool = %#v", system)
		}
		user := nodePoolByName(t, doc.Requires.Infrastructure.NodePools, "userpool")
		if user.Mode != "User" || user.Count != 1 || user.VMSize != "Standard_D16as_v5" {
			t.Fatalf("user pool = %#v", user)
		}
		if doc.Requires.NodeSelectors[0].Pool != "userpool" {
			t.Fatalf("selector pool = %q, want userpool", doc.Requires.NodeSelectors[0].Pool)
		}
		if _, err := os.Stat(filepath.Join(root, "suites", "kata-perf", "infra.bicepparam")); !os.IsNotExist(err) {
			t.Fatalf("infra.bicepparam still exists: %v", err)
		}
	})

	t.Run("kata-io", func(t *testing.T) {
		doc, err := requirements.Load(root, "kata-io")
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Requires.Infrastructure.NodePools) != 3 {
			t.Fatalf("kata-io node pools = %#v, want systempool, userpool, patchpool", doc.Requires.Infrastructure.NodePools)
		}
		user := nodePoolByName(t, doc.Requires.Infrastructure.NodePools, "userpool")
		if user.Mode != "User" || user.Count != 4 || user.VMSize != "Standard_D8s_v5" || user.OSSKU != "AzureLinux" || user.WorkloadRuntime != "KataMshvVmIsolation" {
			t.Fatalf("userpool = %#v", user)
		}
		if user.Labels["perf.azure.com/node-role"] != "workload" {
			t.Fatalf("userpool label = %#v, want workload", user.Labels)
		}
		patch := nodePoolByName(t, doc.Requires.Infrastructure.NodePools, "patchpool")
		if patch.Mode != "User" || patch.Count != 4 || patch.VMSize != "Standard_D8s_v5" || patch.OSSKU != "AzureLinux" || patch.WorkloadRuntime != "KataMshvVmIsolation" {
			t.Fatalf("patchpool = %#v", patch)
		}
		if patch.Labels["perf.azure.com/node-role"] != "patchpool" {
			t.Fatalf("patchpool label = %#v, want patchpool", patch.Labels)
		}
		if len(patch.Taints) != 0 {
			t.Fatalf("patchpool taints = %#v, want no taints", patch.Taints)
		}

		selectorsByName := map[string]string{}
		for _, selector := range doc.Requires.NodeSelectors {
			selectorsByName[selector.Name] = selector.Pool
		}
		if selectorsByName["workload"] != "userpool" {
			t.Fatalf("workload selector pool = %q, want userpool", selectorsByName["workload"])
		}
		if selectorsByName["patched-kata"] != "patchpool" {
			t.Fatalf("patched-kata selector pool = %q, want patchpool", selectorsByName["patched-kata"])
		}
		if _, err := os.Stat(filepath.Join(root, "suites", "kata-io", "infra.bicepparam")); !os.IsNotExist(err) {
			t.Fatalf("infra.bicepparam still exists: %v", err)
		}
	})
}

func TestKataIOShimPatchDaemonSetContract(t *testing.T) {
	root := filepath.Join("..", "..")
	suiteData, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "suite.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var suiteDoc struct {
		Setup struct {
			Resources []struct {
				Name    string `yaml:"name"`
				Path    string `yaml:"path"`
				Restart struct {
					Resource  string `yaml:"resource"`
					Namespace string `yaml:"namespace"`
				} `yaml:"restart"`
				Wait []struct {
					Kind      string `yaml:"kind"`
					Resource  string `yaml:"resource"`
					Namespace string `yaml:"namespace"`
					Timeout   string `yaml:"timeout"`
				} `yaml:"wait"`
			} `yaml:"resources"`
		} `yaml:"setup"`
	}
	if err := yaml.Unmarshal(suiteData, &suiteDoc); err != nil {
		t.Fatal(err)
	}
	if len(suiteDoc.Setup.Resources) != 3 {
		t.Fatalf("kata-io setup resources = %#v, want namespace, kata-shim-patch, results-pvc", suiteDoc.Setup.Resources)
	}
	patchResource := suiteDoc.Setup.Resources[1]
	if patchResource.Name != "kata-shim-patch" || patchResource.Path != "setup/patch-kata-shim.yml" || len(patchResource.Wait) != 1 {
		t.Fatalf("patch setup resource = %#v", patchResource)
	}
	if patchResource.Restart.Resource != "daemonset/kata-shim-patch" || patchResource.Restart.Namespace != "kube-system" {
		t.Fatalf("patch setup restart = %#v", patchResource.Restart)
	}
	wait := patchResource.Wait[0]
	if wait.Kind != "rollout" || wait.Resource != "daemonset/kata-shim-patch" || wait.Namespace != "kube-system" || wait.Timeout != "15m" {
		t.Fatalf("patch setup wait = %#v", wait)
	}

	data, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "setup", "patch-kata-shim.yml"))
	if err != nil {
		t.Fatal(err)
	}
	type container struct {
		Name    string   `yaml:"name"`
		Image   string   `yaml:"image"`
		Command []string `yaml:"command"`
		Env     []struct {
			Name      string `yaml:"name"`
			ValueFrom struct {
				FieldRef struct {
					FieldPath string `yaml:"fieldPath"`
				} `yaml:"fieldRef"`
			} `yaml:"valueFrom"`
		} `yaml:"env"`
		SecurityContext struct {
			RunAsNonRoot             bool  `yaml:"runAsNonRoot"`
			RunAsUser                int   `yaml:"runAsUser"`
			RunAsGroup               int   `yaml:"runAsGroup"`
			ReadOnlyRootFilesystem   bool  `yaml:"readOnlyRootFilesystem"`
			AllowPrivilegeEscalation *bool `yaml:"allowPrivilegeEscalation"`
			Capabilities             struct {
				Drop []string `yaml:"drop"`
			} `yaml:"capabilities"`
		} `yaml:"securityContext"`
		VolumeMounts []struct {
			Name      string `yaml:"name"`
			MountPath string `yaml:"mountPath"`
			ReadOnly  bool   `yaml:"readOnly"`
		} `yaml:"volumeMounts"`
	}
	var daemonSet struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					NodeSelector                 map[string]string `yaml:"nodeSelector"`
					AutomountServiceAccountToken *bool             `yaml:"automountServiceAccountToken"`
					ServiceAccountName           string            `yaml:"serviceAccountName"`
					Tolerations                  []struct {
						Key      string `yaml:"key"`
						Operator string `yaml:"operator"`
						Value    string `yaml:"value"`
						Effect   string `yaml:"effect"`
					} `yaml:"tolerations"`
					SecurityContext struct {
						SeccompProfile struct {
							Type string `yaml:"type"`
						} `yaml:"seccompProfile"`
					} `yaml:"securityContext"`
					InitContainers []container `yaml:"initContainers"`
					Containers     []container `yaml:"containers"`
					Volumes        []struct {
						Name     string `yaml:"name"`
						HostPath struct {
							Path string `yaml:"path"`
							Type string `yaml:"type"`
						} `yaml:"hostPath"`
						Projected struct {
							Sources []struct {
								ServiceAccountToken *struct {
									Path              string `yaml:"path"`
									Audience          string `yaml:"audience"`
									ExpirationSeconds int64  `yaml:"expirationSeconds"`
								} `yaml:"serviceAccountToken"`
								ConfigMap *struct {
									Name  string `yaml:"name"`
									Items []struct {
										Key  string `yaml:"key"`
										Path string `yaml:"path"`
									} `yaml:"items"`
								} `yaml:"configMap"`
								DownwardAPI *struct {
									Items []struct {
										Path     string `yaml:"path"`
										FieldRef struct {
											FieldPath string `yaml:"fieldPath"`
										} `yaml:"fieldRef"`
									} `yaml:"items"`
								} `yaml:"downwardAPI"`
							} `yaml:"sources"`
						} `yaml:"projected"`
					} `yaml:"volumes"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(data, &daemonSet); err != nil {
		t.Fatal(err)
	}
	if daemonSet.Kind != "DaemonSet" || daemonSet.Metadata.Name != "kata-shim-patch" || daemonSet.Metadata.Namespace != "kube-system" {
		t.Fatalf("patch DaemonSet identity = kind %q, metadata %#v", daemonSet.Kind, daemonSet.Metadata)
	}
	podSpec := daemonSet.Spec.Template.Spec
	if podSpec.NodeSelector["perf.azure.com/node-role"] != "patchpool" {
		t.Fatalf("patch DaemonSet selector = %#v, want patchpool", podSpec.NodeSelector)
	}
	if podSpec.AutomountServiceAccountToken == nil || *podSpec.AutomountServiceAccountToken {
		t.Fatalf("patch DaemonSet automountServiceAccountToken = %#v, want false", podSpec.AutomountServiceAccountToken)
	}
	if podSpec.ServiceAccountName != "kata-shim-patch" {
		t.Fatalf("patch DaemonSet serviceAccountName = %q, want kata-shim-patch", podSpec.ServiceAccountName)
	}
	if len(podSpec.Tolerations) != 0 {
		t.Fatalf("patch DaemonSet tolerations = %#v, want none", podSpec.Tolerations)
	}
	if podSpec.SecurityContext.SeccompProfile.Type != "RuntimeDefault" {
		t.Fatalf("patch DaemonSet seccomp profile = %#v, want RuntimeDefault", podSpec.SecurityContext.SeccompProfile)
	}
	if len(podSpec.Volumes) != 2 || podSpec.Volumes[0].Name != "host-bin" || podSpec.Volumes[0].HostPath.Path != "/usr/local/bin" || podSpec.Volumes[0].HostPath.Type != "Directory" {
		t.Fatalf("patch DaemonSet volumes = %#v", podSpec.Volumes)
	}
	serviceAccountVolume := podSpec.Volumes[1]
	if serviceAccountVolume.Name != "service-account" || len(serviceAccountVolume.Projected.Sources) != 3 {
		t.Fatalf("patch DaemonSet service account volume = %#v", serviceAccountVolume)
	}
	tokenSource := serviceAccountVolume.Projected.Sources[0].ServiceAccountToken
	if tokenSource == nil || tokenSource.Path != "token" || tokenSource.Audience != "" || tokenSource.ExpirationSeconds != 600 {
		t.Fatalf("patch DaemonSet projected token = %#v", tokenSource)
	}
	configMapSource := serviceAccountVolume.Projected.Sources[1].ConfigMap
	if configMapSource == nil || configMapSource.Name != "kube-root-ca.crt" || len(configMapSource.Items) != 1 || configMapSource.Items[0].Key != "ca.crt" || configMapSource.Items[0].Path != "ca.crt" {
		t.Fatalf("patch DaemonSet projected CA = %#v", configMapSource)
	}
	downwardAPISource := serviceAccountVolume.Projected.Sources[2].DownwardAPI
	if downwardAPISource == nil || len(downwardAPISource.Items) != 1 || downwardAPISource.Items[0].Path != "namespace" || downwardAPISource.Items[0].FieldRef.FieldPath != "metadata.namespace" {
		t.Fatalf("patch DaemonSet projected namespace = %#v", downwardAPISource)
	}
	if len(podSpec.InitContainers) != 1 {
		t.Fatalf("patch DaemonSet init containers = %#v, want one patch init container", podSpec.InitContainers)
	}
	patch := podSpec.InitContainers[0]
	const patchImage = "curlimages/curl@sha256:4026b29997dc7c823b51c164b71e2b51e0fd95cce4601f78202c513d97da2922"
	if patch.Name != "patch" || patch.Image != patchImage {
		t.Fatalf("patch init container name/image = %q/%q, want %q", patch.Name, patch.Image, patchImage)
	}
	if patch.SecurityContext.RunAsUser != 0 || patch.SecurityContext.RunAsGroup != 0 {
		t.Fatalf("patch init container UID/GID = %d/%d, want 0/0", patch.SecurityContext.RunAsUser, patch.SecurityContext.RunAsGroup)
	}
	if len(patch.VolumeMounts) != 2 || patch.VolumeMounts[0].Name != "host-bin" || patch.VolumeMounts[0].MountPath != "/host-bin" || patch.VolumeMounts[1].Name != "service-account" || patch.VolumeMounts[1].MountPath != "/var/run/secrets/kubernetes.io/serviceaccount" || !patch.VolumeMounts[1].ReadOnly {
		t.Fatalf("patch init container mounts = %#v", patch.VolumeMounts)
	}
	if len(patch.Env) != 1 || patch.Env[0].Name != "NODE_NAME" || patch.Env[0].ValueFrom.FieldRef.FieldPath != "spec.nodeName" {
		t.Fatalf("patch init container env = %#v, want NODE_NAME from spec.nodeName", patch.Env)
	}
	if len(patch.Command) != 3 || patch.Command[0] != "/bin/sh" || patch.Command[1] != "-ec" {
		t.Fatalf("patch init container command = %#v", patch.Command)
	}
	patchScript := patch.Command[2]
	for _, want := range []string{
		"https://abombo.blob.core.windows.net/public/containerd-shim-kata-v2.bin",
		"d78b0c859c25f795dee201f8ae1b28c987fd6b0537efd0430b5fd6ad47a93ec1",
		"66484264",
		`target="/host-bin/containerd-shim-kata-v2"`,
		`stat -c %a "$target"`,
		`stat -c %u "$target"`,
		`stat -c %g "$target"`,
		`chown "$target_uid:$target_gid" "$tmp"`,
		`chmod "$target_mode" "$tmp"`,
		`/api/v1/nodes/${NODE_NAME}`,
		`application/merge-patch+json`,
		`{"metadata":{"labels":{"perf.azure.com/kata-shim-revision":null}}}`,
		`{"metadata":{"labels":{"perf.azure.com/kata-shim-revision":"r1-d78b0c859c25f795"}}}`,
		`installed_size`,
		`installed_mode`,
		`installed_uid`,
		`installed_gid`,
	} {
		if !strings.Contains(patchScript, want) {
			t.Fatalf("patch init container command missing %q", want)
		}
	}
	chownIndex := strings.Index(patchScript, `chown "$target_uid:$target_gid" "$tmp"`)
	chmodIndex := strings.Index(patchScript, `chmod "$target_mode" "$tmp"`)
	if chownIndex < 0 || chmodIndex < 0 || chownIndex > chmodIndex {
		t.Fatalf("patch init container must restore ownership before mode: chown=%d chmod=%d", chownIndex, chmodIndex)
	}
	clearIndex := strings.Index(patchScript, `{"metadata":{"labels":{"perf.azure.com/kata-shim-revision":null}}}`)
	targetVerificationIndex := strings.Index(patchScript, `if [ ! -f "$target" ]`)
	installedVerificationIndex := strings.Index(patchScript, `if [ "$installed_sha" != "$expected_sha" ] || [ "$installed_size" != "$expected_size" ] || [ "$installed_mode" != "$target_mode" ] || [ "$installed_uid" != "$target_uid" ] || [ "$installed_gid" != "$target_gid" ]; then`)
	setIndex := strings.Index(patchScript, `{"metadata":{"labels":{"perf.azure.com/kata-shim-revision":"r1-d78b0c859c25f795"}}}`)
	if clearIndex < 0 || targetVerificationIndex < 0 || clearIndex > targetVerificationIndex {
		t.Fatalf("readiness clear must precede target verification: clear=%d target=%d", clearIndex, targetVerificationIndex)
	}
	if installedVerificationIndex < 0 || setIndex < 0 || installedVerificationIndex > setIndex {
		t.Fatalf("readiness set must follow full installed shim verification: verification=%d set=%d", installedVerificationIndex, setIndex)
	}
	for _, forbidden := range []string{"chmod --reference", "chown --reference", "application/json-patch+json", `"op":"remove"`, "taint_removed", "taint_present", "pending shim-patch taint"} {
		if strings.Contains(patchScript, forbidden) {
			t.Fatalf("patch init container command contains forbidden legacy or non-portable operation %q", forbidden)
		}
	}
	if strings.Contains(patchScript, "sleep infinity") {
		t.Fatal("patch init container must exit after successful verification")
	}
	if len(podSpec.Containers) != 1 {
		t.Fatalf("patch DaemonSet containers = %#v, want one sleeper", podSpec.Containers)
	}
	sleeper := podSpec.Containers[0]
	if sleeper.Name != "sleeper" || sleeper.Image != patch.Image || len(sleeper.Command) != 3 || sleeper.Command[0] != "/bin/sh" || sleeper.Command[1] != "-c" || sleeper.Command[2] != "sleep infinity" {
		t.Fatalf("sleeping main container = %#v", sleeper)
	}
	if len(sleeper.VolumeMounts) != 0 {
		t.Fatalf("sleeping main container must not mount host paths: %#v", sleeper.VolumeMounts)
	}
	if !sleeper.SecurityContext.RunAsNonRoot || sleeper.SecurityContext.RunAsUser != 65532 || sleeper.SecurityContext.RunAsGroup != 65532 || !sleeper.SecurityContext.ReadOnlyRootFilesystem || sleeper.SecurityContext.AllowPrivilegeEscalation == nil || *sleeper.SecurityContext.AllowPrivilegeEscalation || !reflect.DeepEqual(sleeper.SecurityContext.Capabilities.Drop, []string{"ALL"}) {
		t.Fatalf("sleeping main container security context = %#v", sleeper.SecurityContext)
	}

	type rbacDocument struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		Rules []struct {
			APIGroups []string `yaml:"apiGroups"`
			Resources []string `yaml:"resources"`
			Verbs     []string `yaml:"verbs"`
		} `yaml:"rules"`
		RoleRef struct {
			Kind string `yaml:"kind"`
			Name string `yaml:"name"`
		} `yaml:"roleRef"`
		Subjects []struct {
			Kind      string `yaml:"kind"`
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"subjects"`
	}
	documents := map[string]rbacDocument{}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	for {
		var document rbacDocument
		if err := decoder.Decode(&document); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		documents[document.Kind] = document
	}
	serviceAccount := documents["ServiceAccount"]
	if serviceAccount.Metadata.Name != "kata-shim-patch" || serviceAccount.Metadata.Namespace != "kube-system" {
		t.Fatalf("patch ServiceAccount = %#v", serviceAccount.Metadata)
	}
	role := documents["ClusterRole"]
	if role.Metadata.Name != "kata-shim-patch" || len(role.Rules) != 1 || !reflect.DeepEqual(role.Rules[0].APIGroups, []string{""}) || !reflect.DeepEqual(role.Rules[0].Resources, []string{"nodes"}) || !reflect.DeepEqual(role.Rules[0].Verbs, []string{"get", "patch"}) {
		t.Fatalf("patch ClusterRole = %#v", role)
	}
	binding := documents["ClusterRoleBinding"]
	if binding.Metadata.Name != "kata-shim-patch" || binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != "kata-shim-patch" || len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "ServiceAccount" || binding.Subjects[0].Name != "kata-shim-patch" || binding.Subjects[0].Namespace != "kube-system" {
		t.Fatalf("patch ClusterRoleBinding = %#v", binding)
	}
	manifest := string(data)
	for _, forbidden := range []string{"systemctl restart", "service containerd restart", "pkill containerd", "containerd-shim-v2"} {
		if strings.Contains(manifest, forbidden) {
			t.Fatalf("patch DaemonSet contains forbidden restart or wrong target %q", forbidden)
		}
	}
}

func TestKataIOShimReadinessMigrationDocumentation(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	readme := string(data)
	var migrationText string
	for _, paragraph := range strings.Split(readme, "\n\n") {
		lower := strings.ToLower(paragraph)
		if strings.Contains(lower, "patchpool") && strings.Contains(lower, "migrat") {
			migrationText = paragraph
			break
		}
	}
	if migrationText == "" {
		t.Fatal("README missing kata-io patchpool migration guidance")
	}

	for _, want := range []string{
		"perf.azure.com/kata-shim-patch=pending:NoSchedule",
	} {
		if !strings.Contains(migrationText, want) {
			t.Fatalf("README kata-io migration guidance missing %q", want)
		}
	}
	for description, pattern := range map[string]string{
		"existing AKS pool scope":             `(?i)existing\s+AKS\s+(?:node\s*)?patchpools?`,
		"migration before rollout or run":     `(?is)migrat(?:e|ed|ion).*?before.*?(?:roll\s*out|run)`,
		"reprovision or recreate alternative": `(?i)(?:reprovision|recreate)(?:/|\s+or\s+)(?:reprovision|recreate)`,
		"kubectl-only removal is not durable": `(?is)remov(?:e|ing).*?only\s+through\s+` + "`kubectl`" + `.*?not\s+durable`,
	} {
		if !regexp.MustCompile(pattern).MatchString(migrationText) {
			t.Fatalf("README kata-io migration guidance must document %s", description)
		}
	}

	command := regexp.MustCompile("`(az aks nodepool update[^`]*)`").FindStringSubmatch(migrationText)
	if len(command) != 2 {
		t.Fatal("README kata-io migration guidance must include an az aks nodepool update command")
	}
	for _, want := range []string{
		"--resource-group <resource-group>",
		"--cluster-name <cluster-name>",
		"--name patchpool",
		`--node-taints ""`,
	} {
		if !strings.Contains(command[1], want) {
			t.Fatalf("README kata-io migration command missing %q: %s", want, command[1])
		}
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
			var cleanupOrder []string
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
						cleanupOrder = append(cleanupOrder, object.Kind)
					}
					if object.Kind == "PersistentVolumeClaim" && object.LabelSelector["pvc-role"] != "work" {
						t.Fatalf("work PVC cleanup selector = %#v, want pvc-role=work", object.LabelSelector)
					}
				}
			}
			wantCleanupOrder := []string{"Job", "Pod", "PersistentVolumeClaim"}
			if !reflect.DeepEqual(cleanupOrder, wantCleanupOrder) {
				t.Fatalf("cleanup order = %v, want %v so pods release PVCs before PVC deletion", cleanupOrder, wantCleanupOrder)
			}
		})
	}
}

func TestKataIOWorkloadsPreloadBenchmarkImageOnBothPools(t *testing.T) {
	root := filepath.Join("..", "..")
	preloadTemplate, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "templates", "preload-pod.yml"))
	if err != nil {
		t.Fatalf("preload pod template missing: %v", err)
	}
	preloadTemplateText := string(preloadTemplate)
	for _, want := range []string{
		"apiVersion: batch/v1",
		"kind: Job",
		"backoffLimit: 0",
		"restartPolicy: Never",
		"image: {{.benchmarkImage}}",
		"command: [override, command]",
		"perf.azure.com/node-role: workload",
	} {
		if !strings.Contains(preloadTemplateText, want) {
			t.Fatalf("preload pod template missing %q", want)
		}
	}
	if strings.Count(preloadTemplateText, "app: kata-io") != 2 || strings.Count(preloadTemplateText, "benchmark: io") != 2 {
		t.Fatal("preload Job and pod template must both carry cleanup labels")
	}
	for _, forbidden := range []string{"apiVersion: v1\nkind: Pod", "run-fio.sh", "fioProfile", "/profiles/", "workload-type: fio", "{{.nodeRole}}", "perf.azure.com/kata-shim-revision"} {
		if strings.Contains(preloadTemplateText, forbidden) {
			t.Fatalf("preload pod template must not contain fio workload marker %q", forbidden)
		}
	}
	parsedPreload, err := template.New("preload-pod.yml").Option("missingkey=error").Parse(preloadTemplateText)
	if err != nil {
		t.Fatal(err)
	}
	var renderedPreload bytes.Buffer
	if err := parsedPreload.Execute(&renderedPreload, map[string]any{
		"jobName": "test-preload", "Iteration": 0, "namespace": "kata-io", "benchmarkImage": "example.invalid/benchmark:test",
	}); err != nil {
		t.Fatalf("render baseline preload template with workload inputs: %v", err)
	}
	var preloadManifest map[string]any
	if err := yaml.Unmarshal(renderedPreload.Bytes(), &preloadManifest); err != nil {
		t.Fatalf("decode rendered baseline preload template: %v", err)
	}
	patchedPreloadTemplate, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "templates", "preload-patch-pod.yml"))
	if err != nil {
		t.Fatalf("patched preload pod template missing: %v", err)
	}
	patchedPreloadTemplateText := string(patchedPreloadTemplate)
	for _, want := range []string{"apiVersion: batch/v1", "kind: Job", "backoffLimit: 0", "restartPolicy: Never", "image: {{.benchmarkImage}}", "command: [override, command]", "perf.azure.com/node-role: patchpool", "perf.azure.com/kata-shim-revision: r1-d78b0c859c25f795"} {
		if !strings.Contains(patchedPreloadTemplateText, want) {
			t.Fatalf("patched preload pod template missing %q", want)
		}
	}
	if strings.Contains(patchedPreloadTemplateText, "apiVersion: v1\nkind: Pod") {
		t.Fatal("patched preload template must be a Job, not a Pod")
	}
	for _, workloadFile := range []string{"workload-fio-fast.yml", "workload-git-fast.yml", "workload-fio.yml", "workload-git.yml"} {
		t.Run(workloadFile, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", workloadFile))
			if err != nil {
				t.Fatal(err)
			}
			var workload struct {
				Jobs []struct {
					Name          string `yaml:"name"`
					JobType       string `yaml:"jobType"`
					MaxWaitTime   string `yaml:"maxWaitTimeout"`
					PreLoadImages *bool  `yaml:"preLoadImages"`
					WaitFinished  bool   `yaml:"waitWhenFinished"`
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
			seenBenchmark := false
			for _, job := range workload.Jobs {
				if job.Name != "kio-preload-images" {
					if job.PreLoadImages == nil || *job.PreLoadImages {
						t.Fatalf("job %s must set preLoadImages: false", job.Name)
					}
					for _, object := range job.Objects {
						if _, benchmark := object.InputVars["scenario"]; benchmark {
							seenBenchmark = true
						}
					}
					continue
				}
				if seenBenchmark {
					t.Fatalf("preload job must run before benchmark jobs")
				}
				if job.PreLoadImages == nil || !*job.PreLoadImages {
					t.Fatalf("preload job must set preLoadImages: true")
				}
				preloadJobs++
				if job.JobType != "create" {
					t.Fatalf("preload job type = %q, want create", job.JobType)
				}
				if !job.WaitFinished {
					t.Fatal("preload job must wait for Kubernetes Job completion")
				}
				if job.MaxWaitTime != "10m" {
					t.Fatalf("preload job maxWaitTimeout = %q, want 10m", job.MaxWaitTime)
				}
				if len(job.Objects) != 2 {
					t.Fatalf("preload job objects = %d, want 2", len(job.Objects))
				}
				wantObjects := []struct {
					template string
					jobName  string
				}{
					{template: "templates/preload-pod.yml", jobName: "{{.k8sRunID}}-preload-workload"},
					{template: "templates/preload-patch-pod.yml", jobName: "{{.k8sRunID}}-preload-patchpool"},
				}
				for i, object := range job.Objects {
					if object.ObjectTemplate != wantObjects[i].template {
						t.Fatalf("preload object %d template = %q, want %q", i, object.ObjectTemplate, wantObjects[i].template)
					}
					if got := asString(object.InputVars["jobName"]); got != wantObjects[i].jobName {
						t.Fatalf("preload object %d jobName = %q, want %q", i, got, wantObjects[i].jobName)
					}
					if _, exists := object.InputVars["nodeRole"]; exists {
						t.Fatalf("preload object %d must not pass nodeRole: %#v", i, object.InputVars)
					}
				}
			}
			if preloadJobs != 1 {
				t.Fatalf("%s preload-enabled jobs = %d, want 1", workloadFile, preloadJobs)
			}
		})
	}

	for _, mode := range []string{"fio-fast", "git-fast", "fio", "git"} {
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

func TestKataIOFioWorkloadCoversActiveScenarios(t *testing.T) {
	assertKataIOActiveWorkloadScenarios(t, "workload-fio.yml", 70, map[string]int{
		"storage-emptydir":         20,
		"storage-azure-disk":       20,
		"storage-azure-files":      20,
		"storage-azure-disk-block": 10,
	}, map[string]bool{
		"runtime-kata-patched-storage-azure-disk-block-fio-randread-4k-concurrency-1":   true,
		"runtime-kata-patched-storage-azure-disk-block-fio-randwrite-4k-concurrency-1":  true,
		"runtime-kata-patched-storage-azure-disk-block-fio-seqread-concurrency-1":       true,
		"runtime-kata-patched-storage-azure-disk-block-fio-seqwrite-concurrency-1":      true,
		"runtime-kata-patched-storage-azure-disk-block-fio-fsync-heavy-concurrency-1":   true,
		"runtime-kata-patched-storage-azure-disk-block-fio-randread-4k-concurrency-10":  true,
		"runtime-kata-patched-storage-azure-disk-block-fio-randwrite-4k-concurrency-10": true,
		"runtime-kata-patched-storage-azure-disk-block-fio-seqread-concurrency-10":      true,
		"runtime-kata-patched-storage-azure-disk-block-fio-seqwrite-concurrency-10":     true,
		"runtime-kata-patched-storage-azure-disk-block-fio-fsync-heavy-concurrency-10":  true,
	})
}

func TestKataIOGitWorkloadCoversActiveScenarios(t *testing.T) {
	assertKataIOActiveWorkloadScenarios(t, "workload-git.yml", 28, map[string]int{
		"storage-emptydir":         8,
		"storage-azure-disk":       8,
		"storage-azure-files":      8,
		"storage-azure-disk-block": 4,
	}, map[string]bool{
		"runtime-kata-patched-storage-azure-disk-block-git-full-concurrency-1":      true,
		"runtime-kata-patched-storage-azure-disk-block-git-blobless-concurrency-1":  true,
		"runtime-kata-patched-storage-azure-disk-block-git-full-concurrency-10":     true,
		"runtime-kata-patched-storage-azure-disk-block-git-blobless-concurrency-10": true,
	})
}

func TestKataIOObsoleteFullWorkloadRemoved(t *testing.T) {
	path := filepath.Join("..", "..", "suites", "kata-io", "workload-full.yml")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("workload-full.yml should be removed, stat err = %v", err)
	}
}

func assertKataIOActiveWorkloadScenarios(t *testing.T, workloadFile string, wantTotal int, wantStorageCounts map[string]int, expectedBlock map[string]bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", workloadFile))
	if err != nil {
		t.Fatal(err)
	}
	var workload struct {
		Jobs []struct {
			Name          string `yaml:"name"`
			JobIterations int    `yaml:"jobIterations"`
			QPS           int    `yaml:"qps"`
			Burst         int    `yaml:"burst"`
			Cleanup       bool   `yaml:"cleanup"`
			WaitFinished  bool   `yaml:"waitWhenFinished"`
			Objects       []struct {
				ObjectTemplate string         `yaml:"objectTemplate"`
				Replicas       int            `yaml:"replicas"`
				InputVars      map[string]any `yaml:"inputVars"`
			} `yaml:"objects"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workload); err != nil {
		t.Fatal(err)
	}

	workloadType := strings.TrimSuffix(strings.TrimPrefix(workloadFile, "workload-"), ".yml")
	profiles := []string{"full", "blobless"}
	if workloadType == "fio" {
		profiles = []string{"randread-4k", "randwrite-4k", "seqread", "seqwrite", "fsync-heavy"}
	}
	expectedFilesystem := map[string]bool{}
	for _, runtime := range []string{"standard", "kata"} {
		for _, storage := range []string{"emptydir", "azure-disk", "azure-files"} {
			for _, concurrency := range []string{"1", "10"} {
				for _, profile := range profiles {
					expectedFilesystem["runtime-"+runtime+"-storage-"+storage+"-"+workloadType+"-"+profile+"-concurrency-"+concurrency] = true
				}
			}
		}
	}

	found := map[string]bool{}
	storageCounts := map[string]int{}
	blockJobNames := map[string]string{}
	foundBlock := map[string]bool{}
	seenBlock := false
	for _, job := range workload.Jobs {
		var mainObjectTemplate string
		var mainInputVars map[string]any
		var workPVCInputVars map[string]any
		var workPVCObjectTemplate string
		var workPVCReplicas int
		for _, object := range job.Objects {
			if object.ObjectTemplate == "templates/work-pvc.yml" || object.ObjectTemplate == "templates/work-block-pvc.yml" {
				workPVCInputVars = object.InputVars
				workPVCObjectTemplate = object.ObjectTemplate
				workPVCReplicas = object.Replicas
				continue
			}
			if scenario, ok := object.InputVars["scenario"].(string); ok {
				mainObjectTemplate = object.ObjectTemplate
				mainInputVars = object.InputVars
				if found[scenario] {
					t.Fatalf("%s contains duplicate scenario %q", workloadFile, scenario)
				}
				found[scenario] = true
			}
		}
		if mainInputVars == nil {
			continue
		}
		scenario := mainInputVars["scenario"].(string)
		storage := asString(mainInputVars["storageType"])
		runtime := asString(mainInputVars["runtime"])
		concurrency := asString(mainInputVars["concurrency"])
		storageCounts[storage]++
		wantIterations := 1
		if concurrency == "10" {
			wantIterations = 10
		}
		if job.JobIterations != wantIterations || job.QPS != wantIterations || job.Burst != wantIterations {
			t.Fatalf("job %s for %s has jobIterations/qps/burst = %d/%d/%d, want %d/%d/%d", job.Name, scenario, job.JobIterations, job.QPS, job.Burst, wantIterations, wantIterations, wantIterations)
		}

		if storage == "storage-azure-disk-block" {
			seenBlock = true
			if !expectedBlock[scenario] {
				t.Fatalf("%s contains unexpected block scenario %q", workloadFile, scenario)
			}
			foundBlock[scenario] = true
			if !job.Cleanup || !job.WaitFinished {
				t.Fatalf("block job %s cleanup/waitWhenFinished = %t/%t, want true/true", job.Name, job.Cleanup, job.WaitFinished)
			}
			if runtime != "runtime-kata-patched" {
				t.Fatalf("block scenario %s runtime = %q, want runtime-kata-patched", scenario, runtime)
			}
			wantTemplate := "templates/" + workloadType + "-block-kata-job.yml"
			if mainObjectTemplate != wantTemplate {
				t.Fatalf("block scenario %s template = %q, want %q", scenario, mainObjectTemplate, wantTemplate)
			}
			if workPVCInputVars == nil || workPVCObjectTemplate != "templates/work-block-pvc.yml" {
				t.Fatalf("block scenario %s missing work-block-pvc.yml", scenario)
			}
			if workPVCReplicas != 1 {
				t.Fatalf("block scenario %s PVC replicas = %d, want 1", scenario, workPVCReplicas)
			}
			if got := asString(workPVCInputVars["workStorageClass"]); got != "managed-csi" {
				t.Fatalf("block scenario %s workStorageClass = %q, want managed-csi", scenario, got)
			}
			if got, want := asString(workPVCInputVars["jobName"]), asString(mainInputVars["jobName"]); got != want {
				t.Fatalf("block scenario %s PVC jobName = %q, benchmark jobName = %q", scenario, got, want)
			}
			jobName := asString(mainInputVars["jobName"])
			if previousScenario, duplicate := blockJobNames[jobName]; duplicate {
				t.Fatalf("block scenarios %s and %s share jobName %q", previousScenario, scenario, jobName)
			}
			blockJobNames[jobName] = scenario
			continue
		}
		if seenBlock {
			t.Fatalf("filesystem scenario %s appears after raw-block jobs in %s", scenario, workloadFile)
		}

		if !expectedFilesystem[scenario] {
			t.Fatalf("%s contains unexpected filesystem scenario %q", workloadFile, scenario)
		}
		delete(expectedFilesystem, scenario)
		storageTemplate := "pvc"
		if storage == "storage-emptydir" {
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
			if storage == "storage-azure-files" {
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
		runtimeName := strings.TrimPrefix(runtime, "runtime-")
		wantTemplate := "templates/" + workloadType + "-" + storageTemplate + "-" + runtimeName + "-job.yml"
		if mainObjectTemplate != wantTemplate {
			t.Fatalf("scenario %s objectTemplate = %q, want %q", scenario, mainObjectTemplate, wantTemplate)
		}
	}
	if len(found) != wantTotal {
		t.Fatalf("%s has %d unique scenarios, want %d", workloadFile, len(found), wantTotal)
	}
	if len(expectedFilesystem) != 0 {
		t.Fatalf("%s missing filesystem scenarios: %#v", workloadFile, expectedFilesystem)
	}
	for scenario := range expectedBlock {
		if !foundBlock[scenario] {
			t.Fatalf("%s missing block scenario %q", workloadFile, scenario)
		}
	}
	if !reflect.DeepEqual(storageCounts, wantStorageCounts) {
		t.Fatalf("%s storage scenario counts = %#v, want %#v", workloadFile, storageCounts, wantStorageCounts)
	}

}

func TestKataIORawBlockTemplatesUseIterationNames(t *testing.T) {
	for _, template := range []string{"work-block-pvc.yml", "fio-block-kata-job.yml", "git-block-kata-job.yml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", "templates", template))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "name: {{.jobName}}-{{.Iteration}}") {
			t.Fatalf("%s must use metadata name {{.jobName}}-{{.Iteration}}", template)
		}
	}
}

func TestKataIOInfraDefaultsCanScheduleConcurrencyTen(t *testing.T) {
	doc, err := requirements.Load(filepath.Join("..", ".."), "kata-io")
	if err != nil {
		t.Fatal(err)
	}
	vcpuBySize := map[string]int{"Standard_D8s_v5": 8, "Standard_D16s_v5": 16}
	podCPU := maxKataIOPodCPURequest(t)
	for _, poolName := range []string{"userpool", "patchpool"} {
		pool := nodePoolByName(t, doc.Requires.Infrastructure.NodePools, poolName)
		if pool.OSSKU != "AzureLinux" || pool.WorkloadRuntime != "KataMshvVmIsolation" {
			t.Fatalf("%s = %#v", poolName, pool)
		}
		vcpu, ok := vcpuBySize[pool.VMSize]
		if !ok {
			t.Fatalf("test does not know vCPU count for %s", pool.VMSize)
		}
		allocatableCPUPerNode := vcpu - 1
		if allocatableCPUPerNode < 1 {
			t.Fatalf("test assumes at least 1 CPU allocatable per node, got %d vCPU for %s", vcpu, pool.VMSize)
		}
		podsPerNode := allocatableCPUPerNode / podCPU
		if got, want := pool.Count*podsPerNode, 10; got < want {
			t.Fatalf("%s can schedule about %d concurrency-10 pods (%d nodes x %d pods/node after 1 CPU/node headroom), want at least %d for pods requesting %d CPU", poolName, got, pool.Count, podsPerNode, want, podCPU)
		}
	}
}

func TestKataIOInfraDefaultsReportDSv5VCPURequest(t *testing.T) {
	doc, err := requirements.Load(filepath.Join("..", ".."), "kata-io")
	if err != nil {
		t.Fatal(err)
	}
	vcpuBySize := map[string]int{"Standard_D4s_v5": 4, "Standard_D8s_v5": 8, "Standard_D16s_v5": 16}
	requested := 0
	for _, pool := range doc.Requires.Infrastructure.NodePools {
		vcpu, ok := vcpuBySize[pool.VMSize]
		if !ok {
			t.Fatalf("test does not know vCPU count for %s", pool.VMSize)
		}
		requested += pool.Count * vcpu
	}
	t.Logf("default kata-io pools request %d DSv5-family vCPUs", requested)
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

func TestKataIOReportsArtifactSummariesOnly(t *testing.T) {
	root := filepath.Join("..", "..")
	doc, err := requirements.Load(root, "kata-io")
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Requires.Reporting.Sources.StandardSummary || doc.Requires.Reporting.Sources.KubeBurner {
		t.Fatalf("kata-io reporting sources = %#v, want artifact summaries only", doc.Requires.Reporting.Sources)
	}
	if len(doc.Requires.Reporting.PrometheusMetricUnits) != 0 {
		t.Fatalf("kata-io Prometheus metric units = %#v, want none", doc.Requires.Reporting.PrometheusMetricUnits)
	}
	if cfg := doc.Requires.Observability.Prometheus; cfg.Required || cfg.Install {
		t.Fatalf("kata-io Prometheus = %#v, want disabled", cfg)
	}
	if cfg := doc.Requires.Observability.KubeStateMetrics; cfg.Required || cfg.Install {
		t.Fatalf("kata-io kube-state-metrics = %#v, want disabled", cfg)
	}
	metricNames, err := reporting.PrometheusMetricNames(filepath.Join(root, "suites", "kata-io", "metrics.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(metricNames) != 0 {
		t.Fatalf("kata-io Prometheus metrics = %v, want none", metricNames)
	}
}

func TestKataIOBenchmarkImageFilesExist(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{
		"suites/kata-io/images/benchmark/Dockerfile",
		"suites/kata-io/images/benchmark/scripts/override",
		"suites/kata-io/images/benchmark/scripts/run-block-benchmark.sh",
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
	dockerfile, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "images", "benchmark", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FROM ubuntu:24.04@sha256:4fbb8e6a8395de5a7550b33509421a2bafbc0aab6c06ba2cef9ebffbc7092d90",
		"e2fsprogs", "mount", "util-linux", "COPY scripts/run-block-benchmark.sh /usr/local/bin/run-block-benchmark.sh",
	} {
		if !strings.Contains(string(dockerfile), want) {
			t.Fatalf("benchmark Dockerfile missing %q", want)
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
		"BLOCK_SETUP_DURATION_SECONDS=0.125",
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
		"total_duration": "seconds", "active_runtime": "seconds", "setup_overhead": "seconds", "block_setup_duration": "seconds",
		"exit_code": "code", "read_iops": "operations/second", "write_iops": "operations/second",
		"read_bandwidth": "bytes/second", "write_bandwidth": "bytes/second",
		"read_clat_p99": "nanoseconds", "write_clat_p99": "nanoseconds",
	}, map[string]string{"exit_code": "0", "active_runtime": "1.25", "read_iops": "101.5", "block_setup_duration": "0.125"})
	assertFilesExist(t, sampleDir, "fio.json", "time.txt", "stdout.log", "stderr.log", "proc-self-io-before.txt", "proc-self-io-after.txt", "df-before.txt", "df-after.txt", "tool-versions.txt")
	for _, want := range []string{"fio --version:", "git --version:", "mkfs.ext4 -V:", "mount --version:"} {
		assertFileContains(t, filepath.Join(sampleDir, "tool-versions.txt"), want)
	}
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
		"total_duration": "seconds", "active_runtime": "seconds", "setup_overhead": "seconds", "block_setup_duration": "seconds",
		"exit_code": "code", "read_iops": "operations/second", "write_iops": "operations/second",
		"read_bandwidth": "bytes/second", "write_bandwidth": "bytes/second",
		"read_clat_p99": "nanoseconds", "write_clat_p99": "nanoseconds",
	}, map[string]string{"exit_code": "19", "active_runtime": "0", "read_iops": "0", "block_setup_duration": "0"})

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
				"total_duration": "seconds", "active_runtime": "seconds", "setup_overhead": "seconds", "block_setup_duration": "seconds",
				"exit_code": "code", "read_iops": "operations/second", "write_iops": "operations/second",
				"read_bandwidth": "bytes/second", "write_bandwidth": "bytes/second",
				"read_clat_p99": "nanoseconds", "write_clat_p99": "nanoseconds",
			}, mergeExpectedValues(tc.wantValues, map[string]string{"block_setup_duration": "0"}))
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
		"block_setup_duration": "seconds", "clone_duration": "seconds", "exit_code": "code", "repository_size": "bytes", "file_count": "files",
	}, map[string]string{"exit_code": "23", "file_count": "1", "block_setup_duration": "0"})
	assertFilesExist(t, sampleDir, "time.txt", "git-stdout.log", "git-stderr.log", "git-trace2-event.json", "git-trace2-perf.log", "repo-size-bytes.txt", "file-count.txt", "proc-self-io-before.txt", "proc-self-io-after.txt", "df-before.txt", "df-after.txt", "tool-versions.txt")
	for _, want := range []string{"fio --version:", "git --version:", "mkfs.ext4 -V:", "mount --version:"} {
		assertFileContains(t, filepath.Join(sampleDir, "tool-versions.txt"), want)
	}
	assertFileDoesNotExist(t, filepath.Join(sampleDir, "summary.prom"))
}

func TestKataIOTemplatesProvideSummaryDimensions(t *testing.T) {
	templates, err := filepath.Glob(filepath.Join("..", "..", "suites", "kata-io", "templates", "*-job.yml"))
	if err != nil {
		t.Fatal(err)
	}
	wantTemplates := []string{
		"fio-block-kata-job.yml", "fio-emptydir-kata-job.yml", "fio-emptydir-standard-job.yml", "fio-pvc-kata-job.yml", "fio-pvc-standard-job.yml",
		"git-block-kata-job.yml", "git-emptydir-kata-job.yml", "git-emptydir-standard-job.yml", "git-pvc-kata-job.yml", "git-pvc-standard-job.yml",
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

func TestKataIORawBlockTemplates(t *testing.T) {
	root := filepath.Join("..", "..")
	pvc, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "templates", "work-block-pvc.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"volumeMode: Block", "storageClassName: {{.workStorageClass}}", "ReadWriteOnce", "pvc-role: work", "storage-type: {{.storageType}}"} {
		if !strings.Contains(string(pvc), want) {
			t.Fatalf("work-block-pvc.yml missing %q", want)
		}
	}

	for _, file := range []string{"fio-block-kata-job.yml", "git-block-kata-job.yml"} {
		data, err := os.ReadFile(filepath.Join(root, "suites", "kata-io", "templates", file))
		if err != nil {
			t.Fatal(err)
		}
		manifest := string(data)
		for _, want := range []string{
			"runtimeClassName: {{.kataRuntimeClassName}}",
			"perf.azure.com/node-role: patchpool",
			"perf.azure.com/kata-shim-revision: r1-d78b0c859c25f795",
			"volumeDevices:",
			"devicePath: /dev/work-block",
			"claimName: {{.jobName}}-{{.Iteration}}",
			"SYS_ADMIN",
			"run-block-benchmark.sh",
			"storageType",
			"kata-io-results",
		} {
			if !strings.Contains(manifest, want) {
				t.Fatalf("%s missing %q", file, want)
			}
		}
		if strings.Contains(manifest, "mountPath: /work") {
			t.Fatalf("%s must not mount the raw-block work volume as /work", file)
		}
		if strings.Contains(manifest, "tolerations:") || strings.Contains(manifest, "perf.azure.com/kata-shim-patch") {
			t.Fatalf("%s must not contain legacy shim-patch taint placement", file)
		}
	}
}

func TestKataIOBlockBenchmarkWrapper(t *testing.T) {
	requireKataIOScriptTools(t)
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "mkfs.ext4"), `#!/usr/bin/env bash
set -euo pipefail
printf 'mkfs %s\n' "$*" >> "${FAKE_BLOCK_LOG:?}"
if [[ -n "${FAKE_MKFS_STARTED_FILE:-}" ]]; then printf 'started\n' > "$FAKE_MKFS_STARTED_FILE"; fi
while [[ -n "${FAKE_MKFS_RELEASE_FILE:-}" && ! -e "$FAKE_MKFS_RELEASE_FILE" ]]; do sleep 0.01; done
exit "${FAKE_MKFS_EXIT:-0}"
`)
	writeExecutable(t, filepath.Join(binDir, "mount"), `#!/usr/bin/env bash
set -euo pipefail
printf 'mount %s\n' "$*" >> "${FAKE_BLOCK_LOG:?}"
if [[ "${FAKE_MOUNT_EXIT:-0}" != 0 ]]; then exit "$FAKE_MOUNT_EXIT"; fi
if [[ -n "${FAKE_MOUNT_STARTED_FILE:-}" ]]; then printf 'started\n' > "$FAKE_MOUNT_STARTED_FILE"; fi
while [[ -n "${FAKE_MOUNT_RELEASE_FILE:-}" && ! -e "$FAKE_MOUNT_RELEASE_FILE" ]]; do sleep 0.01; done
mkdir -p "${@: -1}"
printf 'mount complete\n' >> "${FAKE_BLOCK_LOG:?}"
if [[ -n "${FAKE_MOUNT_COMPLETED_FILE:-}" ]]; then printf 'completed\n' > "$FAKE_MOUNT_COMPLETED_FILE"; fi
while [[ -n "${FAKE_MOUNT_RETURN_FILE:-}" && ! -e "$FAKE_MOUNT_RETURN_FILE" ]]; do sleep 0.01; done
`)
	writeExecutable(t, filepath.Join(binDir, "umount"), `#!/usr/bin/env bash
set -euo pipefail
printf 'umount %s\n' "$*" >> "${FAKE_BLOCK_LOG:?}"
exit "${FAKE_UMOUNT_EXIT:-0}"
`)
	writeExecutable(t, filepath.Join(binDir, "benchmark"), `#!/usr/bin/env bash
set -euo pipefail
test -d "${WORK_DIR:?}"
[[ "${BLOCK_SETUP_DURATION_SECONDS:?}" =~ ^[0-9]+([.][0-9]+)?$ ]]
printf 'benchmark setup=%s args=%s\n' "$BLOCK_SETUP_DURATION_SECONDS" "$*" >> "${FAKE_BLOCK_LOG:?}"
exit "${FAKE_BENCHMARK_EXIT:?}"
`)
	writeExecutable(t, filepath.Join(binDir, "long-benchmark"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$$" > "${FAKE_CHILD_PID_FILE:?}"
trap 'printf "child TERM\n" >> "${FAKE_BLOCK_LOG:?}"; exit 143' TERM
trap 'printf "child INT\n" >> "${FAKE_BLOCK_LOG:?}"; exit 130' INT
printf 'child ready\n' >> "${FAKE_BLOCK_LOG:?}"
long-grandchild
`)
	writeExecutable(t, filepath.Join(binDir, "long-grandchild"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$$" > "${FAKE_GRANDCHILD_PID_FILE:?}"
trap 'printf "grandchild TERM\n" >> "${FAKE_BLOCK_LOG:?}"; exit 143' TERM
trap 'printf "grandchild INT\n" >> "${FAKE_BLOCK_LOG:?}"; exit 130' INT
printf 'grandchild ready\n' >> "${FAKE_BLOCK_LOG:?}"
while true; do
  sleep 0.05
done
`)
	writeExecutable(t, filepath.Join(binDir, "stubborn-benchmark"), `#!/usr/bin/env bash
set -euo pipefail
trap '' TERM HUP
printf '%s\n' "$$" > "${FAKE_CHILD_PID_FILE:?}"
stubborn-grandchild &
while true; do sleep 0.05; done
`)
	writeExecutable(t, filepath.Join(binDir, "stubborn-grandchild"), `#!/usr/bin/env bash
set -euo pipefail
trap '' TERM HUP
printf '%s\n' "$$" > "${FAKE_GRANDCHILD_PID_FILE:?}"
printf 'grandchild ready and ignores TERM\n' >> "${FAKE_BLOCK_LOG:?}"
while true; do sleep 0.05; done
`)
	writeExecutable(t, filepath.Join(binDir, "split-benchmark"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$$" > "${FAKE_CHILD_PID_FILE:?}"
trap 'printf "leader TERM\n" >> "${FAKE_BLOCK_LOG:?}"; exit 143' TERM
stubborn-grandchild &
while true; do sleep 0.05; done
`)

	script := filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark", "scripts", "run-block-benchmark.sh")
	run := func(t *testing.T, benchmarkExit, umountExit int, extraEnv ...string) (*exec.Cmd, string) {
		t.Helper()
		logPath := filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+".log")
		cmd := exec.Command("bash", script, "benchmark", "argument with spaces")
		cmd.Env = append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"FAKE_BLOCK_DEVICE="+filepath.Join(tempDir, "device"),
			"FAKE_BLOCK_LOG="+logPath,
			"FAKE_BENCHMARK_EXIT="+strconv.Itoa(benchmarkExit),
			"FAKE_UMOUNT_EXIT="+strconv.Itoa(umountExit),
			"WORK_DIR="+filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+"-work"),
		)
		cmd.Env = append(cmd.Env, extraEnv...)
		return cmd, logPath
	}

	t.Run("preserves benchmark failure and always unmounts", func(t *testing.T) {
		cmd, logPath := run(t, 17, 9)
		assertCommandExitCode(t, cmd, 17)
		for _, want := range []string{"mkfs -F", "mount", "benchmark setup=", "args=argument with spaces", "umount"} {
			assertFileContains(t, logPath, want)
		}
	})

	t.Run("reports cleanup failure after successful benchmark", func(t *testing.T) {
		cmd, logPath := run(t, 0, 9)
		output, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 9 {
			t.Fatalf("command error = %v, output:\n%s; want exit code 9", err, output)
		}
		if !strings.Contains(string(output), "failed to unmount") {
			t.Fatalf("cleanup output = %q, want unmount diagnostic", output)
		}
		assertFileContains(t, logPath, "umount")
	})

	for _, tc := range []struct {
		name       string
		env        string
		wantExit   int
		wantOutput string
		forbidden  string
	}{
		{name: "format failure", env: "FAKE_MKFS_EXIT=31", wantExit: 31, wantOutput: "failed to format block device", forbidden: "mount "},
		{name: "mount failure", env: "FAKE_MOUNT_EXIT=32", wantExit: 32, wantOutput: "failed to mount block device", forbidden: "benchmark "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, logPath := run(t, 0, 0, tc.env)
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.wantExit {
				t.Fatalf("command error = %v, output:\n%s; want exit code %d", err, output, tc.wantExit)
			}
			if !strings.Contains(string(output), tc.wantOutput) {
				t.Fatalf("setup output = %q, want %q", output, tc.wantOutput)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), tc.forbidden) {
				t.Fatalf("setup log = %q, must not contain %q", data, tc.forbidden)
			}
		})
	}

	t.Run("stops after signaled mkfs completes", func(t *testing.T) {
		logPath := filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+".log")
		startedPath := logPath + ".started"
		releasePath := logPath + ".release"
		cmd := exec.Command("bash", script, "benchmark")
		cmd.Env = append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"FAKE_BLOCK_DEVICE="+filepath.Join(tempDir, "device"),
			"FAKE_BLOCK_LOG="+logPath,
			"FAKE_MKFS_STARTED_FILE="+startedPath,
			"FAKE_MKFS_RELEASE_FILE="+releasePath,
			"WORK_DIR="+filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+"-work"),
		)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		waitForFile(t, startedPath, 2*time.Second)
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(releasePath, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		assertStartedCommandExitCode(t, cmd, 143, 2*time.Second)
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "mount ") || strings.Contains(string(data), "benchmark ") {
			t.Fatalf("commands ran after mkfs signal:\n%s", data)
		}
	})

	t.Run("work directory failure", func(t *testing.T) {
		workParent := filepath.Join(tempDir, "not-a-directory")
		if err := os.WriteFile(workParent, []byte("file"), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd, logPath := run(t, 0, 0, "WORK_DIR="+filepath.Join(workParent, "work"))
		output, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
			t.Fatalf("command error = %v, output:\n%s; want nonzero exit", err, output)
		}
		if !strings.Contains(string(output), "failed to create work directory") {
			t.Fatalf("setup output = %q, want work directory diagnostic", output)
		}
		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatalf("setup commands ran after work directory failure: %v", err)
		}
	})

	for _, tc := range []struct {
		name               string
		waitForMountOutput bool
	}{
		{name: "defers TERM while mount is about to complete"},
		{name: "defers TERM after mount succeeds before command returns", waitForMountOutput: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+".log")
			startedPath := logPath + ".started"
			completedPath := logPath + ".completed"
			releasePath := logPath + ".release"
			returnPath := logPath + ".return"
			cmd := exec.Command("bash", script, "benchmark")
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_BLOCK_DEVICE="+filepath.Join(tempDir, "device"),
				"FAKE_BLOCK_LOG="+logPath,
				"FAKE_BENCHMARK_EXIT=0",
				"FAKE_UMOUNT_EXIT=0",
				"FAKE_MOUNT_STARTED_FILE="+startedPath,
				"WORK_DIR="+filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+"-work"),
			)
			if tc.waitForMountOutput {
				cmd.Env = append(cmd.Env, "FAKE_MOUNT_COMPLETED_FILE="+completedPath, "FAKE_MOUNT_RETURN_FILE="+returnPath)
			} else {
				cmd.Env = append(cmd.Env, "FAKE_MOUNT_RELEASE_FILE="+releasePath)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitPath := startedPath
			if tc.waitForMountOutput {
				waitPath = completedPath
			}
			waitForFile(t, waitPath, 2*time.Second)
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if tc.waitForMountOutput {
				if err := os.WriteFile(returnPath, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(releasePath, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			assertStartedCommandExitCode(t, cmd, 143, 2*time.Second)
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(data), "umount "); got != 1 {
				t.Fatalf("umount count = %d, log:\n%s; want exactly 1", got, data)
			}
			if !strings.Contains(string(data), "mount complete") || strings.Contains(string(data), "benchmark ") {
				t.Fatalf("setup signal lifecycle log:\n%s\nwant completed mount, no benchmark", data)
			}
		})
	}

	t.Run("kills non-cooperative benchmark descendants after grace period", func(t *testing.T) {
		logPath := filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+".log")
		pidPath := logPath + ".pid"
		grandchildPIDPath := logPath + ".grandchild.pid"
		cmd := exec.Command("bash", script, "stubborn-benchmark")
		cmd.Env = append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"FAKE_BLOCK_DEVICE="+filepath.Join(tempDir, "device"),
			"FAKE_BLOCK_LOG="+logPath,
			"FAKE_CHILD_PID_FILE="+pidPath,
			"FAKE_GRANDCHILD_PID_FILE="+grandchildPIDPath,
			"SIGNAL_GRACE_SECONDS=0.1",
			"POST_KILL_GRACE_SECONDS=0.1",
			"WORK_DIR="+filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+"-work"),
		)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		childPID := waitForPIDFile(t, pidPath, 2*time.Second)
		grandchildPID := waitForPIDFile(t, grandchildPIDPath, 2*time.Second)
		started := time.Now()
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		assertStartedCommandExitCode(t, cmd, 143, 2*time.Second)
		if elapsed := time.Since(started); elapsed < 100*time.Millisecond || elapsed > time.Second {
			t.Fatalf("wrapper signal completion took %s, want bounded TERM and post-KILL grace periods", elapsed)
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(data), "umount "); got != 1 {
			t.Fatalf("umount count = %d, log:\n%s; want exactly 1", got, data)
		}
		if !strings.Contains(string(data), "grandchild ready and ignores TERM") {
			t.Fatalf("log:\n%s\nwant proof descendant ignored TERM", data)
		}
		if processRunning(childPID) || processRunning(grandchildPID) {
			t.Fatalf("benchmark process group remains after wrapper exit: child=%t grandchild=%t", processRunning(childPID), processRunning(grandchildPID))
		}
	})

	t.Run("kills descendant when leader exits on TERM", func(t *testing.T) {
		logPath := filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+".log")
		pidPath := logPath + ".pid"
		grandchildPIDPath := logPath + ".grandchild.pid"
		cmd := exec.Command("bash", script, "split-benchmark")
		cmd.Env = append(os.Environ(),
			"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"FAKE_BLOCK_DEVICE="+filepath.Join(tempDir, "device"),
			"FAKE_BLOCK_LOG="+logPath,
			"FAKE_CHILD_PID_FILE="+pidPath,
			"FAKE_GRANDCHILD_PID_FILE="+grandchildPIDPath,
			"SIGNAL_GRACE_SECONDS=0.1",
			"POST_KILL_GRACE_SECONDS=0.1",
			"WORK_DIR="+filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+"-work"),
		)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = waitForPIDFile(t, pidPath, 2*time.Second)
		grandchildPID := waitForPIDFile(t, grandchildPIDPath, 2*time.Second)
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		assertStartedCommandExitCode(t, cmd, 143, 2*time.Second)
		if processRunning(grandchildPID) {
			t.Fatalf("TERM-ignoring descendant %d remains after wrapper exit", grandchildPID)
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "leader TERM") || strings.Count(string(data), "umount ") != 1 {
			t.Fatalf("split benchmark lifecycle log:\n%s", data)
		}
	})

	for _, tc := range []struct {
		name       string
		signal     syscall.Signal
		wantExit   int
		wantSignal string
	}{
		{name: "forwards SIGTERM to benchmark tree", signal: syscall.SIGTERM, wantExit: 143, wantSignal: "TERM"},
		{name: "forwards SIGINT to benchmark tree", signal: syscall.SIGINT, wantExit: 130, wantSignal: "INT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+".log")
			pidPath := filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+".pid")
			grandchildPIDPath := filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+"-grandchild.pid")
			cmd := exec.Command("bash", script, "long-benchmark")
			cmd.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"FAKE_BLOCK_DEVICE="+filepath.Join(tempDir, "device"),
				"FAKE_BLOCK_LOG="+logPath,
				"FAKE_CHILD_PID_FILE="+pidPath,
				"FAKE_GRANDCHILD_PID_FILE="+grandchildPIDPath,
				"WORK_DIR="+filepath.Join(tempDir, strings.ReplaceAll(t.Name(), "/", "-")+"-work"),
			)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}

			childPID := waitForPIDFile(t, pidPath, 2*time.Second)
			grandchildPID := waitForPIDFile(t, grandchildPIDPath, 2*time.Second)
			waitResult := make(chan error, 1)
			go func() { waitResult <- cmd.Wait() }()
			completed := false
			defer func() {
				if processRunning(childPID) {
					_ = syscall.Kill(childPID, syscall.SIGKILL)
				}
				if processRunning(grandchildPID) {
					_ = syscall.Kill(grandchildPID, syscall.SIGKILL)
				}
				if !completed {
					_ = cmd.Process.Kill()
					select {
					case <-waitResult:
					case <-time.After(2 * time.Second):
					}
				}
			}()

			if err := cmd.Process.Signal(tc.signal); err != nil {
				t.Fatal(err)
			}
			var waitErr error
			select {
			case waitErr = <-waitResult:
				completed = true
			case <-time.After(2 * time.Second):
				t.Fatalf("wrapper did not exit promptly after %s", tc.signal)
			}
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != tc.wantExit {
				t.Fatalf("wrapper wait error = %v, want exit code %d", waitErr, tc.wantExit)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(string(data), "umount "); got != 1 {
				t.Fatalf("umount count = %d, log:\n%s; want exactly 1", got, data)
			}
			childSignal := "child " + tc.wantSignal
			grandchildSignal := "grandchild " + tc.wantSignal
			for _, want := range []string{childSignal, grandchildSignal} {
				if !strings.Contains(string(data), want) {
					t.Fatalf("log:\n%s\nmissing %q", data, want)
				}
			}
			umountIndex := strings.Index(string(data), "umount ")
			if strings.Index(string(data), childSignal) > umountIndex || strings.Index(string(data), grandchildSignal) > umountIndex {
				t.Fatalf("unmount occurred before benchmark tree terminated, log:\n%s", data)
			}
			if processRunning(childPID) {
				t.Fatalf("benchmark child PID %d is still running", childPID)
			}
			if processRunning(grandchildPID) {
				t.Fatalf("benchmark grandchild PID %d is still running", grandchildPID)
			}
			if processRunning(cmd.Process.Pid) {
				t.Fatalf("wrapper PID %d is still running", cmd.Process.Pid)
			}
		})
	}
}

func TestKataIOBlockBenchmarkWrapperBoundsPostKillDrain(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "suites", "kata-io", "images", "benchmark", "scripts", "run-block-benchmark.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, want := range []string{
		`POST_KILL_GRACE_SECONDS="${POST_KILL_GRACE_SECONDS:-1}"`,
		`post_kill_deadline=`,
		`process_group_has_live_members`,
		`timed out waiting for benchmark process group`,
		`mkfs.ext4 -F -E lazy_itable_init=0,lazy_journal_init=0 "$BLOCK_DEVICE"`,
		`sync "$WORK_DIR"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("run-block-benchmark.sh missing bounded post-KILL drain contract %q", want)
		}
	}
	if strings.Contains(script, `while [ -n "$child_pid" ] && kill -0 -- "-$child_pid"`) {
		t.Fatal("run-block-benchmark.sh still uses an unbounded kill -0 process-group drain")
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

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s = %q, want content %q", path, data, want)
	}
}

func mergeExpectedValues(values ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, source := range values {
		for key, value := range source {
			merged[key] = value
		}
	}
	return merged
}

func waitForPIDFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			value := strings.TrimSpace(string(data))
			if value == "" {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			pid, err := strconv.Atoi(value)
			if err == nil {
				return pid
			}
			t.Fatalf("invalid PID file %s: %v", path, err)
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("PID file %s was not created within %s", path, timeout)
	return 0
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s was not created within %s", path, timeout)
}

func assertStartedCommandExitCode(t *testing.T, cmd *exec.Cmd, want int, timeout time.Duration) {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- cmd.Wait() }()
	select {
	case err := <-result:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != want {
			t.Fatalf("command error = %v, want exit code %d", err, want)
		}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-result
		t.Fatalf("command did not exit within %s", timeout)
	}
}

func processRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
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
