package run

import (
	"context"
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
	"github.com/Azure/aks-burner/internal/repo"
	"github.com/Azure/aks-burner/internal/suite"
	"gopkg.in/yaml.v3"
)

type Mode struct {
	Iterations             int               `yaml:"iterations"`
	IterationsPerNamespace int               `yaml:"iterationsPerNamespace"`
	QPS                    int               `yaml:"qps"`
	Burst                  int               `yaml:"burst"`
	Cleanup                bool              `yaml:"cleanup"`
	WaitWhenFinished       bool              `yaml:"waitWhenFinished"`
	PreLoadImages          bool              `yaml:"preLoadImages"`
	WorkloadFile           string            `yaml:"workloadFile,omitempty"`
	RunTimestamp           time.Time         `yaml:"-"`
	TemplateVars           map[string]any    `yaml:"templateVars"`
	ImageVars              map[string]string `yaml:"imageVars"`
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
	Suite         string            `yaml:"suite"`
	Mode          string            `yaml:"mode"`
	Timestamp     string            `yaml:"timestamp"`
	ResourceGroup string            `yaml:"resourceGroup"`
	ClusterName   string            `yaml:"clusterName"`
	Images        map[string]string `yaml:"images"`
	BuiltImages   []acr.BuiltImage  `yaml:"builtImages,omitempty"`
	Setup         suite.Setup       `yaml:"setup,omitempty"`
}

func RenderWorkload(workload map[string]any, mode Mode, images map[string]string, prometheusEndpoint string) (map[string]any, error) {
	rendered := cloneMap(workload)
	runTimestamp := mode.RunTimestamp
	if runTimestamp.IsZero() {
		runTimestamp = time.Now().UTC()
	}
	templateVars := renderTemplateVars(mode.TemplateVars, runTimestamp)
	global := ensureMap(rendered, "global")
	global["gc"] = mode.Cleanup
	if prometheusEndpoint != "" {
		rendered["metricsEndpoints"] = []any{map[string]any{
			"endpoint": prometheusEndpoint,
			"metrics":  []any{"metrics.yml"},
			"indexer": map[string]any{
				"type":             "local",
				"metricsDirectory": "../raw/metrics",
			},
		}}
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

func CreateRunDir(suiteName string, mode string) (string, error) {
	if err := os.MkdirAll("results", 0o755); err != nil {
		return "", err
	}
	dir := filepath.Join("results", runDirName(suiteName, mode, time.Now().UTC()))
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

func ExecuteKubeBurner(workloadPath string, logPath string) error {
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(kubeBurnerExecutable(workloadPath), "init", "-c", filepath.Base(workloadPath))
	cmd.Dir = filepath.Dir(workloadPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
}

func kubeBurnerExecutable(workloadPath string) string {
	root, err := repo.Root(filepath.Dir(workloadPath))
	if err == nil {
		candidate := filepath.Join(root, "bin", "kube-burner")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return "kube-burner"
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
