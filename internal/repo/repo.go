package repo

import (
	"fmt"
	"os"
	"path/filepath"
)

func Root(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("could not find go.mod from %s", start)
		}
		current = parent
	}
}
