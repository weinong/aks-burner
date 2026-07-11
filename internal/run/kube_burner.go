package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const RequiredKubeBurnerVersion = "2.7.3"

var kubeBurnerVersionPattern = regexp.MustCompile(`(?m)^Version:\s*v?([^\s]+)\s*$`)

func KubeBurnerExecutable(root string) string {
	candidate := filepath.Join(root, "bin", "kube-burner")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return candidate
	}
	return "kube-burner"
}

func ValidateKubeBurnerVersion(root string) error {
	executable := KubeBurnerExecutable(root)
	output, err := exec.Command(executable, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("read kube-burner version from %s: %w: %s", executable, err, strings.TrimSpace(string(output)))
	}
	match := kubeBurnerVersionPattern.FindSubmatch(output)
	if match == nil {
		return fmt.Errorf("parse kube-burner version from %s: %q", executable, strings.TrimSpace(string(output)))
	}
	actual := string(match[1])
	if actual != RequiredKubeBurnerVersion {
		return fmt.Errorf("kube-burner version %s is unsupported; install %s", actual, RequiredKubeBurnerVersion)
	}
	return nil
}
