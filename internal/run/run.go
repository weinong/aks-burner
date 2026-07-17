package run

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/aks-burner/internal/acr"
	"github.com/Azure/aks-burner/internal/kubetarget"
	"github.com/Azure/aks-burner/internal/repo"
	"github.com/Azure/aks-burner/internal/suite"
	"gopkg.in/yaml.v3"
)

type Mode struct {
	Iterations                  int               `yaml:"iterations"`
	IterationsPerNamespace      int               `yaml:"iterationsPerNamespace"`
	QPS                         int               `yaml:"qps"`
	Burst                       int               `yaml:"burst"`
	Cleanup                     bool              `yaml:"cleanup"`
	WaitWhenFinished            bool              `yaml:"waitWhenFinished"`
	PreLoadImages               bool              `yaml:"preLoadImages"`
	JobPause                    string            `yaml:"jobPause,omitempty"`
	MetricsClosing              string            `yaml:"metricsClosing,omitempty"`
	WorkloadFile                string            `yaml:"workloadFile,omitempty"`
	ReportPodReadyMetrics       bool              `yaml:"reportPodReadyMetrics,omitempty"`
	ReportStorageStartupMetrics bool              `yaml:"reportStorageStartupMetrics,omitempty"`
	RunTimestamp                time.Time         `yaml:"-"`
	TemplateVars                map[string]any    `yaml:"templateVars"`
	ImageVars                   map[string]string `yaml:"imageVars"`
}

func (m Mode) SelectedWorkloadFile() string {
	if m.WorkloadFile == "" {
		return "workload.yml"
	}
	return m.WorkloadFile
}

type Requirements struct {
	Kubernetes    KubernetesRequirements    `yaml:"kubernetes"`
	NodeSelectors []NodeSelectorRequirement `yaml:"nodeSelectors"`
}

type StorageClassRequirement struct {
	Name        string `yaml:"name"`
	Provisioner string `yaml:"provisioner"`
}

type StorageClassMetadata struct {
	Name              string            `yaml:"name"`
	Provisioner       string            `yaml:"provisioner"`
	ReclaimPolicy     string            `yaml:"reclaimPolicy"`
	VolumeBindingMode string            `yaml:"volumeBindingMode,omitempty"`
	Parameters        map[string]string `yaml:"parameters,omitempty"`
}

type KubernetesRequirements struct {
	MinVersion string `yaml:"minVersion"`
}

type NodeSelectorRequirement struct {
	Name     string            `yaml:"name"`
	Pool     string            `yaml:"pool"`
	Required bool              `yaml:"required"`
	MinNodes int               `yaml:"minNodes"`
	Labels   map[string]string `yaml:"labels"`
}

type KubectlRunner func(ctx context.Context, args ...string) ([]byte, error)

type Metadata struct {
	Suite          string                 `yaml:"suite"`
	Mode           string                 `yaml:"mode"`
	Timestamp      string                 `yaml:"timestamp"`
	ResourceGroup  string                 `yaml:"resourceGroup"`
	ClusterName    string                 `yaml:"clusterName,omitempty"`
	KubeContext    string                 `yaml:"kubeContext,omitempty"`
	Images         map[string]string      `yaml:"images"`
	BuiltImages    []acr.BuiltImage       `yaml:"builtImages,omitempty"`
	Setup          suite.Setup            `yaml:"setup,omitempty"`
	StorageClasses []StorageClassMetadata `yaml:"storageClasses,omitempty"`
}

func RenderWorkload(workload map[string]any, mode Mode, images map[string]string, prometheusEndpoint string, kubeBurnerReporting bool) (map[string]any, error) {
	rendered := cloneMap(workload)
	runTimestamp := mode.RunTimestamp
	if runTimestamp.IsZero() {
		runTimestamp = time.Now().UTC()
	}
	templateVars := renderTemplateVars(mode.TemplateVars, runTimestamp)
	global := ensureMap(rendered, "global")
	global["gc"] = mode.Cleanup
	if kubeBurnerReporting {
		endpoint := map[string]any{
			"indexer": map[string]any{
				"type":             "local",
				"metricsDirectory": "../raw/metrics",
			},
		}
		if prometheusEndpoint != "" {
			endpoint["endpoint"] = prometheusEndpoint
			endpoint["metrics"] = []any{"metrics.yml"}
		}
		rendered["metricsEndpoints"] = []any{endpoint}
	}
	jobs, _ := rendered["jobs"].([]any)
	for _, item := range jobs {
		job, ok := item.(map[string]any)
		if !ok {
			continue
		}
		setDefault(job, "jobIterations", mode.Iterations)
		setDefault(job, "iterationsPerNamespace", mode.IterationsPerNamespace)
		setDefault(job, "qps", mode.QPS)
		setDefault(job, "burst", mode.Burst)
		setDefault(job, "cleanup", mode.Cleanup)
		setDefault(job, "waitWhenFinished", mode.WaitWhenFinished)
		setDefault(job, "preLoadImages", mode.PreLoadImages)
		if mode.JobPause != "" {
			setDefault(job, "jobPause", mode.JobPause)
		}
		if mode.MetricsClosing != "" {
			setDefault(job, "metricsClosing", mode.MetricsClosing)
		}
		objects, _ := job["objects"].([]any)
		for _, objectItem := range objects {
			object, ok := objectItem.(map[string]any)
			if !ok {
				continue
			}
			inputVars := ensureMap(object, "inputVars")
			for key, value := range templateVars {
				inputVars[key] = value
			}
			renderInputVarPlaceholders(inputVars, templateVars)
			for key, imageKey := range mode.ImageVars {
				image, ok := images[imageKey]
				if !ok || image == "" {
					return nil, fmt.Errorf("image key %q not found", imageKey)
				}
				inputVars[key] = image
			}
		}
	}
	return rendered, nil
}

func renderInputVarPlaceholders(inputVars map[string]any, templateVars map[string]any) {
	for key, value := range inputVars {
		text, ok := value.(string)
		if !ok {
			continue
		}
		for templateKey, templateValue := range templateVars {
			templateText, ok := templateValue.(string)
			if !ok {
				continue
			}
			text = strings.ReplaceAll(text, "{{."+templateKey+"}}", templateText)
		}
		inputVars[key] = text
	}
}

func renderTemplateVars(vars map[string]any, timestamp time.Time) map[string]any {
	rendered := map[string]any{}
	for key, value := range vars {
		text, ok := value.(string)
		if !ok {
			rendered[key] = value
			continue
		}
		text = strings.ReplaceAll(text, "{{.runTimestamp}}", timestamp.UTC().Format("20060102T150405.000000000Z"))
		text = strings.ReplaceAll(text, "{{.runTimestampDNS}}", timestamp.UTC().Format("20060102t150405")+fmt.Sprintf("%09d", timestamp.UTC().Nanosecond()))
		rendered[key] = text
	}
	return rendered
}

func CreateRunDir(suiteName string, mode string, timestamp time.Time) (string, error) {
	if err := os.MkdirAll("results", 0o755); err != nil {
		return "", err
	}
	dir := filepath.Join("results", runDirName(suiteName, mode, timestamp))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	for _, child := range []string{"metadata", "rendered", "logs", "raw", "summary"} {
		if err := os.MkdirAll(filepath.Join(dir, child), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func runDirName(suiteName string, mode string, timestamp time.Time) string {
	safeSuite := strings.ReplaceAll(suiteName, "/", "_")
	safeTimestamp := strings.ReplaceAll(timestamp.UTC().Format(time.RFC3339Nano), ":", "-")
	return safeTimestamp + "_" + safeSuite + "_" + mode
}

func ExecuteKubeBurner(workloadPath string, logPath string, target kubetarget.Target) error {
	root, err := repo.Root(filepath.Dir(workloadPath))
	if err != nil {
		return err
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	args := target.KubeBurnerArgs("init", "-c", filepath.Base(workloadPath))
	cmd := exec.Command(KubeBurnerExecutable(root), args...)
	cmd.Dir = filepath.Dir(workloadPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "AKS_BURNER_KUBE_CONTEXT="+target.Context)
	return cmd.Run()
}

func ValidateStorageClasses(ctx context.Context, requirements []StorageClassRequirement, runner KubectlRunner) ([]StorageClassMetadata, error) {
	if len(requirements) == 0 {
		return nil, fmt.Errorf("storage preflight requires declared StorageClasses")
	}
	data, err := runner(ctx, "get", "storageclasses", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("fetch StorageClasses for storage preflight (verify cluster-scoped get RBAC): %w", err)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Provisioner       string            `json:"provisioner"`
			ReclaimPolicy     string            `json:"reclaimPolicy"`
			VolumeBindingMode string            `json:"volumeBindingMode"`
			Parameters        map[string]string `json:"parameters"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("decode StorageClasses for storage preflight: %w", err)
	}
	classes := make(map[string]StorageClassMetadata, len(list.Items))
	for _, item := range list.Items {
		classes[item.Metadata.Name] = StorageClassMetadata{
			Name:              item.Metadata.Name,
			Provisioner:       item.Provisioner,
			ReclaimPolicy:     item.ReclaimPolicy,
			VolumeBindingMode: item.VolumeBindingMode,
			Parameters:        item.Parameters,
		}
	}
	result := make([]StorageClassMetadata, 0, len(requirements))
	for _, requirement := range requirements {
		class, ok := classes[requirement.Name]
		if !ok {
			return nil, fmt.Errorf("storage preflight requires StorageClass %q, but it was not found", requirement.Name)
		}
		if class.Provisioner != requirement.Provisioner {
			return nil, fmt.Errorf("StorageClass %q provisioner = %q, want %q", requirement.Name, class.Provisioner, requirement.Provisioner)
		}
		if class.ReclaimPolicy != "Delete" {
			return nil, fmt.Errorf("StorageClass %q reclaimPolicy = %q, want Delete so benchmark volumes are removed", requirement.Name, class.ReclaimPolicy)
		}
		result = append(result, class)
	}
	return result, nil
}

const storageRunLockName = "aks-burner-storage-startup-lock"
const storageRunLockLabel = "aks-burner.azure.com/storage-lock"

func AcquireStorageRunLock(ctx context.Context, holder string, runner KubectlRunner) (func(context.Context) error, error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate storage run lock token: %w", err)
	}
	token := fmt.Sprintf("%x", tokenBytes)
	_, err := runner(ctx, "create", "configmap", storageRunLockName, "-n", "kube-system", "--from-literal=holder="+holder, "--labels="+storageRunLockLabel+"="+token)
	if err != nil {
		return nil, fmt.Errorf("another storage run may hold ConfigMap kube-system/%s: %w; if no run is active, this may be a stale lock and requires manual deletion with `kubectl -n kube-system delete configmap %s` using the same kube context", storageRunLockName, err, storageRunLockName)
	}
	return func(releaseCtx context.Context) error {
		if _, err := runner(releaseCtx, "delete", "configmap", "-l", storageRunLockLabel+"="+token, "-n", "kube-system", "--ignore-not-found=true"); err != nil {
			return fmt.Errorf("delete storage run lock kube-system/%s: %w", storageRunLockName, err)
		}
		return nil
	}, nil
}

func WithStorageRunLock(ctx context.Context, enabled bool, holder string, runner KubectlRunner, execute func() error) (err error) {
	if !enabled {
		return execute()
	}
	release, err := AcquireStorageRunLock(ctx, holder, runner)
	if err != nil {
		return err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if releaseErr := release(releaseCtx); releaseErr != nil {
			if err == nil {
				err = releaseErr
			} else {
				err = fmt.Errorf("%w; storage lock release also failed: %v; manual deletion may be required", err, releaseErr)
			}
		}
	}()
	return execute()
}

func ValidateRequirements(ctx context.Context, req Requirements, runner KubectlRunner) error {
	if req.Kubernetes.MinVersion != "" {
		data, err := runner(ctx, "version", "-o", "json")
		if err != nil {
			return err
		}
		var version struct {
			ServerVersion struct {
				GitVersion string `json:"gitVersion"`
			} `json:"serverVersion"`
		}
		if err := json.Unmarshal(data, &version); err != nil {
			return err
		}
		if compareVersions(version.ServerVersion.GitVersion, req.Kubernetes.MinVersion) < 0 {
			return fmt.Errorf("Kubernetes version %s is below required %s", version.ServerVersion.GitVersion, req.Kubernetes.MinVersion)
		}
	}
	for _, selector := range req.NodeSelectors {
		if !selector.Required || selector.MinNodes == 0 {
			continue
		}
		data, err := runner(ctx, NodeSelectorArgs(selector.Labels)...)
		if err != nil {
			return err
		}
		if countLines(string(data)) < selector.MinNodes {
			return fmt.Errorf("node selector %s requires %d matching nodes", selector.Name, selector.MinNodes)
		}
	}
	return nil
}

func KubectlOutput(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "kubectl", args...).Output()
}

func NodeSelectorArgs(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return []string{"get", "nodes", "-l", strings.Join(parts, ","), "-o", "name"}
}

func WriteMetadata(runDir string, metadata Metadata) error {
	if err := os.MkdirAll(filepath.Join(runDir, "metadata"), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(metadata)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, "metadata", "run.yml"), data, 0o644)
}

func CopyRenderAssets(suiteDir string, runDir string) error {
	if err := copyDir(filepath.Join(suiteDir, "templates"), filepath.Join(runDir, "rendered", "templates")); err != nil {
		return err
	}
	hooksDir := filepath.Join(suiteDir, "hooks")
	if _, err := os.Stat(hooksDir); err == nil {
		if err := copyDir(hooksDir, filepath.Join(runDir, "rendered", "hooks")); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return copyFile(filepath.Join(suiteDir, "metrics.yml"), filepath.Join(runDir, "rendered", "metrics.yml"))
}

func copyDir(src string, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src string, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func ensureMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	parent[key] = created
	return created
}

func setDefault(parent map[string]any, key string, value any) {
	if _, exists := parent[key]; exists {
		return
	}
	parent[key] = value
}

func cloneMap(input map[string]any) map[string]any {
	data, err := yaml.Marshal(input)
	if err != nil {
		panic(fmt.Sprintf("marshal clone: %v", err))
	}
	var output map[string]any
	if err := yaml.Unmarshal(data, &output); err != nil {
		panic(fmt.Sprintf("unmarshal clone: %v", err))
	}
	return output
}

func countLines(text string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func compareVersions(actual string, minimum string) int {
	actualParts := versionParts(actual)
	minimumParts := versionParts(minimum)
	for i := 0; i < 3; i++ {
		if actualParts[i] > minimumParts[i] {
			return 1
		}
		if actualParts[i] < minimumParts[i] {
			return -1
		}
	}
	return 0
}

func versionParts(version string) [3]int {
	version = strings.TrimPrefix(version, "v")
	version = strings.Split(version, "+")[0]
	version = strings.Split(version, "-")[0]
	pieces := strings.Split(version, ".")
	var parts [3]int
	for i := 0; i < len(pieces) && i < 3; i++ {
		value, _ := strconv.Atoi(pieces[i])
		parts[i] = value
	}
	return parts
}
