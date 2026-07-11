package reporting

import (
	"fmt"
	"sort"

	"github.com/Azure/aks-burner/internal/config"
)

type Config struct {
	Sources               Sources           `yaml:"sources"`
	PrometheusMetricUnits map[string]string `yaml:"prometheusMetricUnits"`
	PrometheusMetricNames []string          `yaml:"-"`
}

type Sources struct {
	StandardSummary bool `yaml:"standardSummary"`
	KubeBurner      bool `yaml:"kubeBurner"`
}

type metricProfileEntry struct {
	MetricName string `yaml:"metricName"`
}

func PrometheusMetricNames(path string) ([]string, error) {
	var entries []metricProfileEntry
	if err := config.LoadYAML(path, &entries); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.MetricName == "" {
			return nil, fmt.Errorf("%s contains an empty metricName", path)
		}
		names = append(names, entry.MetricName)
	}
	sort.Strings(names)
	return names, nil
}

func ValidateConfig(cfg *Config, artifactsEnabled, prometheusEnabled bool, workload map[string]any, prometheusMetricNames []string) error {
	if !cfg.Sources.StandardSummary && !cfg.Sources.KubeBurner {
		return fmt.Errorf("at least one viable reporting source is required")
	}
	if cfg.Sources.StandardSummary && !artifactsEnabled {
		return fmt.Errorf("standardSummary reporting requires enabled artifact collection")
	}
	if !cfg.Sources.KubeBurner {
		return nil
	}
	supportedMeasurement := hasMeasurement(workload, "podLatency")
	if prometheusEnabled {
		declared := map[string]bool{}
		for _, name := range prometheusMetricNames {
			declared[name] = true
		}
		for name := range cfg.PrometheusMetricUnits {
			if !declared[name] {
				return fmt.Errorf("Prometheus metric unit %q has no matching metric in metrics.yml", name)
			}
		}
		for _, name := range prometheusMetricNames {
			if cfg.PrometheusMetricUnits[name] == "" {
				return fmt.Errorf("Prometheus reporting metric %q requires a unit", name)
			}
		}
	}
	if !supportedMeasurement && (!prometheusEnabled || len(prometheusMetricNames) == 0) {
		return fmt.Errorf("kubeBurner reporting requires podLatency or declared Prometheus metrics")
	}
	cfg.PrometheusMetricNames = append([]string(nil), prometheusMetricNames...)
	return nil
}

func hasMeasurement(workload map[string]any, wanted string) bool {
	global, _ := workload["global"].(map[string]any)
	measurements, _ := global["measurements"].([]any)
	for _, item := range measurements {
		measurement, _ := item.(map[string]any)
		if measurement["name"] == wanted {
			return true
		}
	}
	jobs, _ := workload["jobs"].([]any)
	for _, item := range jobs {
		job, _ := item.(map[string]any)
		measurements, _ := job["measurements"].([]any)
		for _, measurementItem := range measurements {
			measurement, _ := measurementItem.(map[string]any)
			if measurement["name"] == wanted {
				return true
			}
		}
	}
	return false
}
