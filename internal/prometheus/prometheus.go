package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Azure/aks-burner/internal/kubetarget"
)

type Config struct {
	Required    bool     `yaml:"required"`
	Install     bool     `yaml:"install"`
	Namespace   string   `yaml:"namespace"`
	ImageKey    string   `yaml:"imageKey"`
	ServiceName string   `yaml:"serviceName"`
	ServicePort int      `yaml:"servicePort"`
	LocalPort   int      `yaml:"localPort"`
	Metrics     []string `yaml:"requiredMetrics"`
}

const kubeStateMetricsScrapeConfigPlaceholder = "{{KUBE_STATE_METRICS_SCRAPE_CONFIG}}"

func EndpointURL(cfg Config) string {
	return fmt.Sprintf("http://127.0.0.1:%d", cfg.LocalPort)
}

func PortForwardArgs(target kubetarget.Target, cfg Config) []string {
	return target.KubectlCommand("-n", cfg.Namespace, "port-forward", "service/"+cfg.ServiceName, fmt.Sprintf("%d:%d", cfg.LocalPort, cfg.ServicePort))
}

func RolloutStatusArgs(target kubetarget.Target, cfg Config) []string {
	return target.KubectlCommand("rollout", "status", "deployment/prometheus", "-n", cfg.Namespace, "--timeout=2m")
}

func RenderManifest(manifest string, image string) string {
	return RenderManifestWithScrapeTarget(manifest, image, "")
}

func RenderManifestWithScrapeTarget(manifest string, image string, target string) string {
	rendered := strings.ReplaceAll(manifest, "{{PROMETHEUS_IMAGE}}", image)
	scrapeConfig := ""
	if target != "" {
		scrapeConfig = fmt.Sprintf(`  - job_name: kube-state-metrics
        static_configs:
          - targets:
              - %s
`, target)
	}
	return strings.ReplaceAll(rendered, kubeStateMetricsScrapeConfigPlaceholder, scrapeConfig)
}

func Install(ctx context.Context, target kubetarget.Target, manifestPath string, image string) error {
	return InstallWithScrapeTarget(ctx, target, manifestPath, image, "")
}

func InstallWithScrapeTarget(ctx context.Context, target kubetarget.Target, manifestPath string, image string, scrapeTarget string) error {
	return installWithRunner(ctx, target, manifestPath, image, scrapeTarget, commandRunner)
}

type Runner func(context.Context, string, ...string) error

func installWithRunner(ctx context.Context, target kubetarget.Target, manifestPath string, image string, scrapeTarget string, runner Runner) error {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	rendered := RenderManifestWithScrapeTarget(string(manifest), image, scrapeTarget)
	return runner(ctx, rendered, target.KubectlCommand("apply", "-f", "-")...)
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

func PortForward(ctx context.Context, target kubetarget.Target, cfg Config) (*exec.Cmd, string, error) {
	args := PortForwardArgs(target, cfg)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	return cmd, EndpointURL(cfg), nil
}

func StopPortForward(cancel context.CancelFunc, cmd *exec.Cmd) error {
	cancel()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Wait(); err != nil && cmd.ProcessState == nil {
		return err
	}
	return nil
}

func WaitReady(ctx context.Context, endpoint string) error {
	return WaitReadyWithTimeout(ctx, endpoint, 2*time.Minute)
}

func WaitReadyWithTimeout(ctx context.Context, endpoint string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			resp, err := client.Get(endpoint + "/-/ready")
			if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				_ = resp.Body.Close()
				return nil
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
		}
	}
}
