package kubestatemetrics

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

type Config struct {
	Required    bool     `yaml:"required"`
	Install     bool     `yaml:"install"`
	Namespace   string   `yaml:"namespace"`
	ImageKey    string   `yaml:"imageKey"`
	ServiceName string   `yaml:"serviceName"`
	ServicePort int      `yaml:"servicePort"`
	Metrics     []string `yaml:"requiredMetrics"`
}

func RenderManifest(manifest string, image string) string {
	return strings.ReplaceAll(manifest, "{{KUBE_STATE_METRICS_IMAGE}}", image)
}

func RolloutStatusArgs(cfg Config) []string {
	return []string{"kubectl", "rollout", "status", "deployment/kube-state-metrics", "-n", cfg.Namespace, "--timeout=2m"}
}

func Install(ctx context.Context, manifestPath string, image string) error {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	rendered := RenderManifest(string(manifest), image)
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(rendered)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func WaitRollout(ctx context.Context, cfg Config) error {
	args := RolloutStatusArgs(cfg)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
