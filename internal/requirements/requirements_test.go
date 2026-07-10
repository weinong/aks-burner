package requirements

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	root := writeRequirementsFixture(t, "demo", "demo")
	doc, err := Load(root, "demo")
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{name: "suite", got: doc.Suite, want: "demo"},
		{name: "provider", got: doc.Requires.Infrastructure.Provider, want: "aks"},
		{name: "node pool", got: doc.Requires.Infrastructure.NodePools[1].Name, want: "userpool"},
		{name: "Kubernetes version", got: doc.Requires.Kubernetes.MinVersion, want: "1.36"},
		{name: "selector pool", got: doc.Requires.NodeSelectors[0].Pool, want: "userpool"},
		{name: "image build", got: doc.Requires.Images.Builds[0].Key, want: "benchmark"},
		{name: "artifact PVC", got: doc.Requires.Artifacts.PVCName, want: "results"},
		{name: "Prometheus port", got: doc.Requires.Observability.Prometheus.LocalPort, want: 9090},
		{name: "kube-state-metrics service", got: doc.Requires.Observability.KubeStateMetrics.ServiceName, want: "kube-state-metrics"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if !reflect.DeepEqual(check.got, check.want) {
				t.Fatalf("got %#v, want %#v", check.got, check.want)
			}
		})
	}
}

func TestLoadRejectsRequirementsSuiteMismatch(t *testing.T) {
	root := writeRequirementsFixture(t, "demo", "other")
	_, err := Load(root, "demo")
	if err == nil || !strings.Contains(err.Error(), `requirements suite "other" does not match "demo"`) {
		t.Fatalf("Load() error = %v", err)
	}
}

func writeRequirementsFixture(t *testing.T, suiteName string, requirementsSuite string) string {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, filepath.Join(root, "schemas", "requirements.schema.json"), `{}`)
	writeFixtureFile(t, filepath.Join(root, "suites", suiteName, "suite.yml"), "name: "+suiteName+"\n")
	writeFixtureFile(t, filepath.Join(root, "suites", suiteName, "requirements.yml"), `suite: `+requirementsSuite+`
requires:
  infrastructure:
    provider: aks
    nodePools:
      - name: systempool
        mode: System
        count: 1
        vmSize: Standard_D4s_v5
        osType: Linux
        osSKU: Ubuntu
        workloadRuntime: OCIContainer
        labels: {}
        taints: []
      - name: userpool
        mode: User
        count: 4
        vmSize: Standard_D8s_v5
        osType: Linux
        osSKU: AzureLinux
        workloadRuntime: KataMshvVmIsolation
        labels:
          perf.azure.com/node-role: workload
        taints: []
  kubernetes:
    minVersion: "1.36"
  nodeSelectors:
    - name: workload
      pool: userpool
      required: true
      minNodes: 1
      labels:
        perf.azure.com/node-role: workload
  images:
    registry:
      nameParameter: containerRegistryName
    builds:
      - key: benchmark
        repository: demo/benchmark
        context: images/benchmark
        dockerfile: Dockerfile
  artifacts:
    enabled: true
    namespace: demo
    pvcName: results
    mountPath: /results
    copyImage: artifact-copy
  observability:
    prometheus:
      required: true
      install: true
      namespace: monitoring
      imageKey: prometheus
      serviceName: prometheus
      servicePort: 9090
      localPort: 9090
    kubeStateMetrics:
      required: true
      install: true
      namespace: monitoring
      imageKey: kube-state-metrics
      serviceName: kube-state-metrics
      servicePort: 8080
`)
	return root
}

func writeFixtureFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
