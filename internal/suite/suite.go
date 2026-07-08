package suite

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/Azure/aks-burner/internal/config"
)

type Config struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tests       []string `yaml:"tests"`
}

func Load(root string, name string) (Config, error) {
	var cfg Config
	path := filepath.Join(root, "suites", name, "suite.yml")
	if err := config.LoadYAML(path, &cfg); err != nil {
		return Config{}, err
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
		suites = append(suites, cfg)
	}
	sort.Slice(suites, func(i, j int) bool { return suites[i].Name < suites[j].Name })
	return suites, nil
}
