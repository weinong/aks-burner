package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	TemplateVars           map[string]any    `yaml:"templateVars"`
	ImageVars              map[string]string `yaml:"imageVars"`
}

func RenderWorkload(workload map[string]any, mode Mode, images map[string]string, prometheusEndpoint string) (map[string]any, error) {
	rendered := cloneMap(workload)
	global := ensureMap(rendered, "global")
	global["gc"] = mode.Cleanup
	global["waitWhenFinished"] = mode.WaitWhenFinished
	rendered["metricsEndpoints"] = []any{map[string]any{
		"endpoint": prometheusEndpoint,
		"metrics":  []any{"metrics.yml"},
		"indexer": map[string]any{
			"type":             "local",
			"metricsDirectory": "raw/metrics",
		},
	}}
	jobs, _ := rendered["jobs"].([]any)
	for _, item := range jobs {
		job, ok := item.(map[string]any)
		if !ok {
			continue
		}
		job["jobIterations"] = mode.Iterations
		job["iterationsPerNamespace"] = mode.IterationsPerNamespace
		job["qps"] = mode.QPS
		job["burst"] = mode.Burst
		job["cleanup"] = mode.Cleanup
		job["waitWhenFinished"] = mode.WaitWhenFinished
		job["preLoadImages"] = mode.PreLoadImages
		objects, _ := job["objects"].([]any)
		for _, objectItem := range objects {
			object, ok := objectItem.(map[string]any)
			if !ok {
				continue
			}
			inputVars := ensureMap(object, "inputVars")
			for key, value := range mode.TemplateVars {
				inputVars[key] = value
			}
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

func CreateRunDir(suiteName string, mode string) (string, error) {
	safeSuite := strings.ReplaceAll(suiteName, "/", "_")
	dir := filepath.Join("results", time.Now().UTC().Format("2006-01-02T15-04-05Z")+"_"+safeSuite+"_"+mode)
	for _, child := range []string{"metadata", "rendered", "logs", "raw", "summary"} {
		if err := os.MkdirAll(filepath.Join(dir, child), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func ExecuteKubeBurner(workloadPath string, logPath string) error {
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command("kube-burner", "init", "-c", filepath.Base(workloadPath))
	cmd.Dir = filepath.Dir(workloadPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
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
