package kubetarget

import (
	"context"
	"os/exec"
)

type Target struct {
	Context string
}

func (t Target) KubectlCommand(args ...string) []string {
	command := []string{"kubectl"}
	if t.Context != "" {
		command = append(command, "--context", t.Context)
	}
	return append(command, args...)
}

func (t Target) KubeBurnerArgs(args ...string) []string {
	result := append([]string(nil), args...)
	if t.Context != "" {
		result = append(result, "--kube-context", t.Context)
	}
	return result
}

func (t Target) Output(ctx context.Context, args ...string) ([]byte, error) {
	command := t.KubectlCommand(args...)
	return exec.CommandContext(ctx, command[0], command[1:]...).Output()
}
