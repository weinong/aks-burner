package reporting

import (
	"fmt"
	"sort"

	"github.com/Azure/aks-burner/internal/config"
)

type Scheme string

const (
	SchemeStandardSummary Scheme = "standard-summary"
	SchemeKubeBurner      Scheme = "kube-burner"
	SchemePodReady        Scheme = "pod-ready"
	SchemeStorageStartup  Scheme = "storage-startup"
)

func (scheme Scheme) UsesStandardSummary() bool {
	return scheme == SchemeStandardSummary
}

func (scheme Scheme) UsesKubeBurner() bool {
	return scheme == SchemeKubeBurner || scheme == SchemePodReady || scheme == SchemeStorageStartup
}

func (scheme Scheme) ReportsPodReady() bool {
	return scheme == SchemePodReady
}

func (scheme Scheme) ReportsStorageStartup() bool {
	return scheme == SchemeStorageStartup
}

func (scheme Scheme) SupportsPartialResults() bool {
	return scheme == SchemePodReady || scheme == SchemeStorageStartup
}

type Config struct {
	Scheme                Scheme            `yaml:"-"`
	PrometheusMetricUnits map[string]string `yaml:"prometheusMetricUnits"`
	PrometheusMetricNames []string          `yaml:"-"`
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
	if cfg.Scheme == "" {
		return fmt.Errorf("reporting scheme is required")
	}
	if !cfg.Scheme.UsesStandardSummary() && !cfg.Scheme.UsesKubeBurner() {
		return fmt.Errorf("unsupported reporting scheme %q", cfg.Scheme)
	}
	if cfg.Scheme.UsesStandardSummary() && !artifactsEnabled {
		return fmt.Errorf("standardSummary reporting requires enabled artifact collection")
	}
	if !cfg.Scheme.UsesKubeBurner() {
		return nil
	}
	supportedMeasurement := hasMeasurement(workload, "podLatency")
	if cfg.Scheme.ReportsPodReady() {
		if !supportedMeasurement {
			return fmt.Errorf("pod Ready reporting requires podLatency")
		}
		if err := validatePodReadyWorkload(workload); err != nil {
			return err
		}
	}
	if cfg.Scheme.ReportsStorageStartup() {
		if !supportedMeasurement {
			return fmt.Errorf("storage startup reporting requires podLatency")
		}
		if err := validateStorageStartupWorkload(workload); err != nil {
			return err
		}
	}
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

func validateStorageStartupWorkload(workload map[string]any) error {
	jobs, _ := workload["jobs"].([]any)
	if len(jobs) != len(storageStartupSpecs) {
		return fmt.Errorf("storage startup reporting requires exactly six jobs")
	}
	for index, item := range jobs {
		job, _ := item.(map[string]any)
		name, _ := job["name"].(string)
		expected, exists := storageStartupSpecs[name]
		if !exists || expected.Order != index {
			return fmt.Errorf("storage startup reporting has unexpected job %q at index %d", name, index)
		}
		if job["jobType"] != "create" || job["gc"] != true || job["podWait"] != true || job["namespacedIterations"] != true {
			return fmt.Errorf("storage startup reporting job %q requires serialized create lifecycle", name)
		}
		objects, _ := job["objects"].([]any)
		if expected.StorageClass == "" {
			if hasJobMeasurement(job, "pvcLatency") || len(objects) != 1 || objectReplicas(objects[0]) != 1 {
				return fmt.Errorf("storage startup reporting job %q requires exactly one pod and no pvcLatency measurement", name)
			}
			continue
		}
		if !hasJobMeasurement(job, "pvcLatency") {
			return fmt.Errorf("storage startup reporting job %q requires pvcLatency", name)
		}
		if len(objects) != 2 || objectReplicas(objects[0]) != 1 || objectReplicas(objects[1]) != 1 {
			return fmt.Errorf("storage startup reporting job %q requires one PVC and one pod", name)
		}
		pvcObject, _ := objects[0].(map[string]any)
		inputVars, _ := pvcObject["inputVars"].(map[string]any)
		if inputVars["storageClass"] != expected.StorageClass {
			return fmt.Errorf("storage startup reporting job %q storageClass = %#v, want %q", name, inputVars["storageClass"], expected.StorageClass)
		}
	}
	return nil
}

func objectReplicas(value any) any {
	object, _ := value.(map[string]any)
	return object["replicas"]
}

func validatePodReadyWorkload(workload map[string]any) error {
	globalPodLatency := hasGlobalMeasurement(workload, "podLatency")
	jobs, _ := workload["jobs"].([]any)
	if len(jobs) == 0 {
		return fmt.Errorf("pod Ready reporting requires at least one job")
	}
	for _, item := range jobs {
		job, _ := item.(map[string]any)
		name, _ := job["name"].(string)
		if !globalPodLatency && !hasJobMeasurement(job, "podLatency") {
			return fmt.Errorf("pod Ready reporting job %q requires podLatency", name)
		}
		if jobType, _ := job["jobType"].(string); jobType != "" && jobType != "create" {
			return fmt.Errorf("pod Ready reporting job %q requires exactly one object with one replica per iteration", name)
		}
		objects, _ := job["objects"].([]any)
		if len(objects) != 1 {
			return fmt.Errorf("pod Ready reporting job %q requires exactly one object with one replica per iteration", name)
		}
		object, _ := objects[0].(map[string]any)
		if object["replicas"] != 1 || object["runOnce"] == true {
			return fmt.Errorf("pod Ready reporting job %q requires exactly one object with one replica per iteration", name)
		}
		if repeat, exists := object["repeatEveryNIterations"]; exists && repeat != 1 {
			return fmt.Errorf("pod Ready reporting job %q requires exactly one object with one replica per iteration", name)
		}
	}
	return nil
}

func hasMeasurement(workload map[string]any, wanted string) bool {
	if hasGlobalMeasurement(workload, wanted) {
		return true
	}
	jobs, _ := workload["jobs"].([]any)
	for _, item := range jobs {
		job, _ := item.(map[string]any)
		if hasJobMeasurement(job, wanted) {
			return true
		}
	}
	return false
}

func hasGlobalMeasurement(workload map[string]any, wanted string) bool {
	global, _ := workload["global"].(map[string]any)
	measurements, _ := global["measurements"].([]any)
	for _, item := range measurements {
		measurement, _ := item.(map[string]any)
		if measurement["name"] == wanted {
			return true
		}
	}
	return false
}

func hasJobMeasurement(job map[string]any, wanted string) bool {
	measurements, _ := job["measurements"].([]any)
	for _, measurementItem := range measurements {
		measurement, _ := measurementItem.(map[string]any)
		if measurement["name"] == wanted {
			return true
		}
	}
	return false
}
