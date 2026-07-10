package kubestatemetrics

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/Azure/aks-burner/internal/kubetarget"
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

func RolloutStatusArgs(target kubetarget.Target, cfg Config) []string {
	return target.KubectlCommand("rollout", "status", "deployment/kube-state-metrics", "-n", cfg.Namespace, "--timeout=2m")
}

func Install(ctx context.Context, target kubetarget.Target, manifestPath string, image string) error {
	return installWithRunner(ctx, target, manifestPath, image, commandRunner)
}

type Runner func(context.Context, string, ...string) error

func installWithRunner(ctx context.Context, target kubetarget.Target, manifestPath string, image string, runner Runner) error {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	return runner(ctx, RenderManifest(string(manifest), image), target.KubectlCommand("apply", "-f", "-")...)
}

func WaitRollout(ctx context.Context, target kubetarget.Target, cfg Config) error {
	args := RolloutStatusArgs(target, cfg)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func commandRunner(ctx context.Context, stdin string, command ...string) error {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
