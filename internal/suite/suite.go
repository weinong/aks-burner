package suite

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Azure/aks-burner/internal/config"
)

var validNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

type Config struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tests       []string `yaml:"tests"`
	Setup       Setup    `yaml:"setup"`
	Modes       []string `yaml:"-"`
}

type Setup struct {
	Resources []SetupResource `yaml:"resources"`
}

type SetupResource struct {
	Name string     `yaml:"name"`
	Path string     `yaml:"path"`
	Wait []WaitRule `yaml:"wait"`
}

type WaitRule struct {
	Kind      string `yaml:"kind"`
	Resource  string `yaml:"resource"`
	Namespace string `yaml:"namespace"`
	Condition string `yaml:"condition"`
	Timeout   string `yaml:"timeout"`
}

func Load(root string, name string) (Config, error) {
	if !ValidName(name) {
		return Config{}, fmt.Errorf("invalid suite name %q", name)
	}
	var cfg Config
	path := filepath.Join(root, "suites", name, "suite.yml")
	if err := config.LoadYAML(path, &cfg); err != nil {
		return Config{}, err
	}
	if !ValidName(cfg.Name) {
		return Config{}, fmt.Errorf("invalid suite name %q in %s", cfg.Name, path)
	}
	return cfg, nil
}

func List(root string) ([]Config, error) {
	entries, err := os.ReadDir(filepath.Join(root, "suites"))
	if err != nil {
		return nil, err
	}
	var suites []Config
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cfg, err := Load(root, entry.Name())
		if err != nil {
			return nil, err
		}
		modes, err := listModes(root, entry.Name())
		if err != nil {
			return nil, err
		}
		cfg.Modes = modes
		suites = append(suites, cfg)
	}
	sort.Slice(suites, func(i, j int) bool { return suites[i].Name < suites[j].Name })
	return suites, nil
}

func listModes(root string, suiteName string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "suites", suiteName, "vars"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no mode files found for suite %q", suiteName)
		}
		return nil, fmt.Errorf("read mode files for suite %q: %w", suiteName, err)
	}

	var modes []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yml" {
			continue
		}
		mode := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if !ValidName(mode) {
			return nil, fmt.Errorf("invalid mode name %q for suite %q", mode, suiteName)
		}
		modes = append(modes, mode)
	}
	if len(modes) == 0 {
		return nil, fmt.Errorf("no mode files found for suite %q", suiteName)
	}
	sortModes(modes)
	return modes, nil
}

func sortModes(modes []string) {
	priority := map[string]int{"smoke": 0, "full": 1}
	sort.Slice(modes, func(i, j int) bool {
		leftPriority, leftOK := priority[modes[i]]
		rightPriority, rightOK := priority[modes[j]]
		if leftOK || rightOK {
			if !leftOK {
				leftPriority = len(priority)
			}
			if !rightOK {
				rightPriority = len(priority)
			}
			if leftPriority != rightPriority {
				return leftPriority < rightPriority
			}
		}
		return modes[i] < modes[j]
	})
}

func ValidName(name string) bool {
	return validNamePattern.MatchString(name)
}
