package artifacts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Config struct {
	Enabled   bool   `yaml:"enabled"`
	Namespace string `yaml:"namespace"`
	PVCName   string `yaml:"pvcName"`
	MountPath string `yaml:"mountPath"`
	CopyImage string `yaml:"copyImage"`
}

type Runner func(ctx context.Context, stdin string, args ...string) error

func Copy(ctx context.Context, cfg Config, destination string) error {
	return copyWithRunnerAndPodName(ctx, cfg, destination, kubectlRunner, uniqueCopyPodName())
}

func CopyWithRunner(ctx context.Context, cfg Config, destination string, runner Runner) error {
	return copyWithRunnerAndPodName(ctx, cfg, destination, runner, uniqueCopyPodName())
}

func CopySubpath(ctx context.Context, cfg Config, destination string, subpath string) error {
	if err := ValidateSubpath(subpath); err != nil {
		return err
	}
	return copySubpathWithRunnerAndPodName(ctx, cfg, destination, subpath, kubectlRunner, uniqueCopyPodName())
}

func CopySubpathWithRunner(ctx context.Context, cfg Config, destination string, subpath string, runner Runner) error {
	if err := ValidateSubpath(subpath); err != nil {
		return err
	}
	return copySubpathWithRunnerAndPodName(ctx, cfg, destination, subpath, runner, uniqueCopyPodName())
}

func copyWithRunnerAndPodName(ctx context.Context, cfg Config, destination string, runner Runner, podName string) error {
	return copySubpathWithRunnerAndPodName(ctx, cfg, destination, "", runner, podName)
}

func copySubpathWithRunnerAndPodName(ctx context.Context, cfg Config, destination string, subpath string, runner Runner, podName string) error {
	if !cfg.Enabled {
		return nil
	}
	if subpath != "" {
		if err := ValidateSubpath(subpath); err != nil {
			return err
		}
	}
	if cfg.Namespace == "" || cfg.PVCName == "" || cfg.MountPath == "" || cfg.CopyImage == "" {
		return fmt.Errorf("artifact namespace, pvcName, mountPath, and copyImage are required when artifacts are enabled")
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	manifest := CopyPodManifest(cfg, podName)
	if err := runner(ctx, manifest, "apply", "-f", "-"); err != nil {
		return err
	}
	if err := runner(ctx, "", "wait", "--for=condition=Ready", "pod/"+podName, "-n", cfg.Namespace, "--timeout=2m"); err != nil {
		return withCleanupError(err, cleanupCopyPod(cfg, runner, podName))
	}
	source := strings.TrimRight(cfg.MountPath, "/")
	if subpath != "" {
		source += "/" + subpath
	}
	copyErr := runner(ctx, "", "cp", cfg.Namespace+"/"+podName+":"+source+"/.", filepath.Clean(destination))
	return withCleanupError(copyErr, cleanupCopyPod(cfg, runner, podName))
}

var safeSubpathPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func ValidateSubpath(subpath string) error {
	if !safeSubpathPattern.MatchString(subpath) || strings.Contains(subpath, "..") {
		return fmt.Errorf("invalid artifact subpath %q", subpath)
	}
	return nil
}

func uniqueCopyPodName() string {
	return fmt.Sprintf("kata-io-artifact-copy-%d", time.Now().UTC().UnixNano())
}

func cleanupCopyPod(cfg Config, runner Runner, podName string) error {
	return runner(context.Background(), "", "delete", "pod", podName, "-n", cfg.Namespace, "--ignore-not-found=true")
}

func withCleanupError(primaryErr error, cleanupErr error) error {
	if primaryErr == nil {
		return cleanupErr
	}
	if cleanupErr == nil {
		return primaryErr
	}
	return fmt.Errorf("%w; artifact cleanup also failed: %v", primaryErr, cleanupErr)
}

func CopyPodManifest(cfg Config, podName string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: kata-io-artifact-copy
spec:
  restartPolicy: Never
  containers:
    - name: copy
      image: %s
      command: [/bin/sh, -c, sleep 3600]
      volumeMounts:
        - name: results
          mountPath: %s
  volumes:
    - name: results
      persistentVolumeClaim:
        claimName: %s
`, podName, cfg.Namespace, cfg.CopyImage, cfg.MountPath, cfg.PVCName)
}

func kubectlRunner(ctx context.Context, stdin string, args ...string) error {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	if stdin != "" {
		pipe, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		go func() {
			_, _ = pipe.Write([]byte(stdin))
			_ = pipe.Close()
		}()
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
