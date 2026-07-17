package reporting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func ReadKubeBurnerMetrics(metricsDir, runDir string, prometheusMetricNames []string, metricUnits map[string]string) ([]Row, int, error) {
	return readKubeBurnerMetrics(metricsDir, runDir, prometheusMetricNames, metricUnits, false, false)
}

func readKubeBurnerMetrics(metricsDir, runDir string, prometheusMetricNames []string, metricUnits map[string]string, reportPodReadyMetrics, reportStorageStartupMetrics bool) ([]Row, int, error) {
	if reportPodReadyMetrics && reportStorageStartupMetrics {
		return nil, 0, fmt.Errorf("pod Ready and storage startup reporting are mutually exclusive")
	}
	if _, err := os.Stat(metricsDir); errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	} else if err != nil {
		return nil, 0, fmt.Errorf("stat kube-burner metrics root %s: %w", metricsDir, err)
	}

	declaredPrometheusUnits := make(map[string]string, len(prometheusMetricNames))
	for _, name := range prometheusMetricNames {
		declaredPrometheusUnits[name] = metricUnits[name]
	}

	paths := []string{}
	err := filepath.WalkDir(metricsDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("discover kube-burner metrics in %s: %w", metricsDir, err)
	}
	sort.Strings(paths)

	rows := []Row{}
	contributingFiles := 0
	launchLatencies := map[string][]float64{}
	launchJobs := map[string]bool{}
	sandboxCounters := map[string]map[string]float64{}
	podReadyJobs := map[string]podReadyJob{}
	podReadySamples := map[string]map[string]podReadySample{}
	podReadyUUID := ""
	storageJobs := map[string]podReadyJob{}
	storagePods := map[string]map[string]storagePodSample{}
	storagePVCs := map[string]map[string]storagePVCSample{}
	for _, path := range paths {
		source, err := filepath.Rel(runDir, path)
		if err != nil {
			return nil, 0, fmt.Errorf("kube-burner metric %s source path: %w", path, err)
		}
		source = filepath.ToSlash(source)
		documents, err := readKubeBurnerDocuments(path, source)
		if err != nil {
			return nil, 0, err
		}
		rowsBeforeFile := len(rows)
		launchSamplesBeforeFile := launchSampleCount(launchLatencies)
		fileHasSandboxCounters := false
		fileHasPodReadyData := false
		fileHasStorageData := false
		for index, document := range documents {
			metricName, err := requiredString(document, "metricName")
			if err != nil {
				return nil, 0, fmt.Errorf("%s: documents[%d].%w", source, index, err)
			}
			_, declaredPrometheusMetric := declaredPrometheusUnits[metricName]
			if (reportPodReadyMetrics || reportStorageStartupMetrics) && (metricName == "jobSummary" || metricName == "podLatencyQuantilesMeasurement" || metricName == "podLatencyMeasurement" || metricName == "pvcLatencyMeasurement" || metricName == "pvcLatencyQuantilesMeasurement" || isSandboxCounterMetric(metricName) || declaredPrometheusMetric) {
				uuid, uuidErr := requiredString(document, "uuid")
				if uuidErr != nil {
					return nil, 0, fmt.Errorf("%s: documents[%d]: %w", source, index, uuidErr)
				}
				if uuid == "" {
					return nil, 0, fmt.Errorf("%s: documents[%d]: uuid must not be empty", source, index)
				}
				if podReadyUUID != "" && uuid != podReadyUUID {
					return nil, 0, fmt.Errorf("%s: documents[%d]: multiple kube-burner UUIDs found in one run directory", source, index)
				}
				podReadyUUID = uuid
			}

			switch metricName {
			case "podLatencyQuantilesMeasurement":
				rows, err = appendPodLatencyQuantileRows(rows, source, document)
			case "podLatencyMeasurement":
				if reportPodReadyMetrics {
					sample, sampleErr := podReadySampleDocument(document)
					if sampleErr != nil {
						err = sampleErr
						break
					}
					if podReadySamples[sample.JobName] == nil {
						podReadySamples[sample.JobName] = map[string]podReadySample{}
					}
					identity := sample.Namespace + "\x00" + sample.PodName
					if _, duplicate := podReadySamples[sample.JobName][identity]; duplicate {
						err = fmt.Errorf("duplicate podLatencyMeasurement for pod %s/%s", sample.Namespace, sample.PodName)
						break
					}
					podReadySamples[sample.JobName][identity] = sample
					fileHasPodReadyData = true
				}
				if reportStorageStartupMetrics {
					sample, sampleErr := storagePodSampleDocument(document)
					if sampleErr != nil {
						err = sampleErr
						break
					}
					if storagePods[sample.JobName] == nil {
						storagePods[sample.JobName] = map[string]storagePodSample{}
					}
					identity := storageSampleIdentity(sample.Iteration, sample.Replica)
					if _, duplicate := storagePods[sample.JobName][identity]; duplicate {
						err = fmt.Errorf("duplicate storage pod sample for job %s iteration %d replica %d", sample.JobName, sample.Iteration, sample.Replica)
						break
					}
					storagePods[sample.JobName][identity] = sample
					fileHasStorageData = true
				}
				jobName, jobErr := requiredString(document, "jobName")
				if jobErr != nil {
					err = jobErr
					break
				}
				launchJobs[jobName] = true
				started, startErr := requiredNumber(document, "containersStartedLatency")
				if startErr != nil {
					err = startErr
					break
				}
				ready, readyErr := requiredNumber(document, "readyToStartContainersLatency")
				if readyErr != nil {
					err = readyErr
					break
				}
				startedValue, _ := strconv.ParseFloat(started.Text, 64)
				readyValue, _ := strconv.ParseFloat(ready.Text, 64)
				if startedValue <= 0 {
					err = fmt.Errorf("containersStartedLatency must be greater than zero")
					break
				}
				if readyValue == 0 {
					continue
				}
				latency := startedValue - readyValue
				if latency < 0 {
					err = fmt.Errorf("post-sandbox container launch latency must not be negative")
					break
				}
				launchLatencies[jobName] = append(launchLatencies[jobName], latency)
			case "jobSummary":
				if !reportPodReadyMetrics && !reportStorageStartupMetrics {
					continue
				}
				job, summaryErr := podReadyJobDocument(document)
				if summaryErr != nil {
					err = summaryErr
					break
				}
				if reportPodReadyMetrics {
					if _, duplicate := podReadyJobs[job.Name]; duplicate {
						err = fmt.Errorf("duplicate jobSummary for job %s", job.Name)
						break
					}
					podReadyJobs[job.Name] = job
					fileHasPodReadyData = true
				}
				if reportStorageStartupMetrics {
					if _, expected := storageStartupSpecs[job.Name]; !expected {
						err = fmt.Errorf("storage startup reporting has unexpected jobSummary for job %s", job.Name)
						break
					}
					if _, duplicate := storageJobs[job.Name]; duplicate {
						err = fmt.Errorf("duplicate jobSummary for job %s", job.Name)
						break
					}
					storageJobs[job.Name] = job
					fileHasStorageData = true
				}
			case "pvcLatencyMeasurement":
				if !reportStorageStartupMetrics {
					continue
				}
				namespace, namespaceErr := requiredNonEmptyString(document, "namespace")
				if namespaceErr != nil {
					err = namespaceErr
					break
				}
				jobName, jobErr := requiredNonEmptyString(document, "jobName")
				if jobErr != nil {
					err = jobErr
					break
				}
				spec, expected := storageStartupSpecs[jobName]
				if !expected || !strings.HasPrefix(namespace, spec.NamespacePrefix+"-") {
					continue
				}
				sample, sampleErr := storagePVCSampleDocument(document)
				if sampleErr != nil {
					err = sampleErr
					break
				}
				if storagePVCs[sample.JobName] == nil {
					storagePVCs[sample.JobName] = map[string]storagePVCSample{}
				}
				identity := storageSampleIdentity(sample.Iteration, sample.Replica)
				if _, duplicate := storagePVCs[sample.JobName][identity]; duplicate {
					err = fmt.Errorf("duplicate storage PVC sample for job %s iteration %d replica %d", sample.JobName, sample.Iteration, sample.Replica)
					break
				}
				storagePVCs[sample.JobName][identity] = sample
				fileHasStorageData = true
			default:
				if isSandboxCounterMetric(metricName) {
					jobName, value, handler, sandboxErr := sandboxCounterDocument(document)
					if sandboxErr != nil {
						err = sandboxErr
						break
					}
					key := jobName + "\x00" + handler
					if sandboxCounters[key] == nil {
						sandboxCounters[key] = map[string]float64{}
					}
					if _, duplicate := sandboxCounters[key][metricName]; duplicate {
						err = fmt.Errorf("duplicate %s for job %s handler %s", metricName, jobName, handler)
						break
					}
					sandboxCounters[key][metricName] = value
					fileHasSandboxCounters = true
					continue
				}
				unit, declared := declaredPrometheusUnits[metricName]
				if !declared {
					continue
				}
				rows, err = appendPrometheusRow(rows, source, metricName, unit, document)
			}
			if err != nil {
				return nil, 0, fmt.Errorf("%s: documents[%d]: %w", source, index, err)
			}
		}
		if len(rows) > rowsBeforeFile || launchSampleCount(launchLatencies) > launchSamplesBeforeFile || fileHasSandboxCounters || fileHasPodReadyData || fileHasStorageData {
			contributingFiles++
		}
	}
	for jobName := range launchJobs {
		values := launchLatencies[jobName]
		if len(values) == 0 {
			rows = append(rows, Row{Source: "derived/post-sandbox-container-launch", Dimensions: map[string]string{"jobName": jobName}, Metric: "post_sandbox_container_launch_latency_sample_count", Value: Number{Text: "0"}, Unit: "samples"})
			continue
		}
		rows = appendPostSandboxLaunchRows(rows, "derived/post-sandbox-container-launch", jobName, values)
	}
	rows, err = appendRunPodSandboxRows(rows, sandboxCounters, reportPodReadyMetrics)
	if err != nil {
		return nil, 0, err
	}
	if reportPodReadyMetrics {
		rows, err = appendPodReadyRows(rows, podReadyJobs, podReadySamples)
		if err != nil {
			return nil, 0, err
		}
		if err := enrichPodReadyDimensions(rows, podReadyJobs); err != nil {
			return nil, 0, err
		}
	}
	if reportStorageStartupMetrics {
		rows, err = appendStorageStartupRows(rows, storageJobs, storagePods, storagePVCs)
		if err != nil {
			return nil, 0, err
		}
	}
	if err := ValidateRows(rows); err != nil {
		return nil, 0, err
	}
	return rows, contributingFiles, nil
}

type podReadyJob struct {
	Name       string
	Iterations int
	QPS        string
	Burst      int
}

type podReadySample struct {
	JobName   string
	Namespace string
	PodName   string
	Created   time.Time
	Ready     time.Time
}

type storageStartupSpec struct {
	Order           int
	NamespacePrefix string
	PodPrefix       string
	StorageClass    string
}

var storageStartupSpecs = map[string]storageStartupSpec{
	"storage-startup-kata-none":            {Order: 0, NamespacePrefix: "kata-perf-storage-kata-none", PodPrefix: "storage-kata-none"},
	"storage-startup-standard-none":        {Order: 1, NamespacePrefix: "kata-perf-storage-default-none", PodPrefix: "storage-default-none"},
	"storage-startup-kata-azure-disk":      {Order: 2, NamespacePrefix: "kata-perf-storage-kata-disk", PodPrefix: "storage-kata-pvc", StorageClass: "managed-csi"},
	"storage-startup-standard-azure-disk":  {Order: 3, NamespacePrefix: "kata-perf-storage-default-disk", PodPrefix: "storage-default-pvc", StorageClass: "managed-csi"},
	"storage-startup-kata-azure-files":     {Order: 4, NamespacePrefix: "kata-perf-storage-kata-files", PodPrefix: "storage-kata-pvc", StorageClass: "azurefile-csi"},
	"storage-startup-standard-azure-files": {Order: 5, NamespacePrefix: "kata-perf-storage-default-files", PodPrefix: "storage-default-pvc", StorageClass: "azurefile-csi"},
}

type storagePodSample struct {
	JobName    string
	Iteration  int
	Replica    int
	ReadyDelta int
}

type storagePVCSample struct {
	JobName   string
	Iteration int
	Replica   int
	Binding   int
}

func storagePodSampleDocument(document map[string]json.RawMessage) (storagePodSample, error) {
	jobName, err := requiredNonEmptyString(document, "jobName")
	if err != nil {
		return storagePodSample{}, err
	}
	spec, expected := storageStartupSpecs[jobName]
	if !expected {
		return storagePodSample{}, fmt.Errorf("storage startup reporting has unexpected pod job %s", jobName)
	}
	iteration, err := requiredNonNegativeInteger(document, "jobIteration")
	if err != nil {
		return storagePodSample{}, err
	}
	replica, err := requiredPositiveInteger(document, "replica")
	if err != nil {
		return storagePodSample{}, err
	}
	namespace, err := requiredNonEmptyString(document, "namespace")
	if err != nil {
		return storagePodSample{}, err
	}
	podName, err := requiredNonEmptyString(document, "podName")
	if err != nil {
		return storagePodSample{}, err
	}
	if want := fmt.Sprintf("%s-%d", spec.NamespacePrefix, iteration); namespace != want {
		return storagePodSample{}, fmt.Errorf("namespace = %q, want %q for job %s", namespace, want, jobName)
	}
	if want := fmt.Sprintf("%s-%d-%d", spec.PodPrefix, iteration, replica); podName != want {
		return storagePodSample{}, fmt.Errorf("podName = %q, want %q for job %s", podName, want, jobName)
	}
	scheduled, err := requiredNonNegativeInteger(document, "schedulingLatency")
	if err != nil {
		return storagePodSample{}, err
	}
	ready, err := requiredNonNegativeInteger(document, "readyToStartContainersLatency")
	if err != nil {
		return storagePodSample{}, err
	}
	return storagePodSample{JobName: jobName, Iteration: iteration, Replica: replica, ReadyDelta: ready - scheduled}, nil
}

func storagePVCSampleDocument(document map[string]json.RawMessage) (storagePVCSample, error) {
	jobName, err := requiredNonEmptyString(document, "jobName")
	if err != nil {
		return storagePVCSample{}, err
	}
	spec, expected := storageStartupSpecs[jobName]
	if !expected {
		return storagePVCSample{}, fmt.Errorf("storage startup reporting has unexpected PVC job %s", jobName)
	}
	if spec.StorageClass == "" {
		return storagePVCSample{}, fmt.Errorf("storage startup job %s does not use PVCs", jobName)
	}
	storageClass, err := requiredNonEmptyString(document, "storageClass")
	if err != nil {
		return storagePVCSample{}, err
	}
	if storageClass != spec.StorageClass {
		return storagePVCSample{}, fmt.Errorf("storageClass = %q, want %q for job %s", storageClass, spec.StorageClass, jobName)
	}
	iteration, err := requiredNonNegativeInteger(document, "jobIteration")
	if err != nil {
		return storagePVCSample{}, err
	}
	replica, err := requiredPositiveInteger(document, "replica")
	if err != nil {
		return storagePVCSample{}, err
	}
	namespace, err := requiredNonEmptyString(document, "namespace")
	if err != nil {
		return storagePVCSample{}, err
	}
	pvcName, err := requiredNonEmptyString(document, "pvcName")
	if err != nil {
		return storagePVCSample{}, err
	}
	if want := fmt.Sprintf("%s-%d", spec.NamespacePrefix, iteration); namespace != want {
		return storagePVCSample{}, fmt.Errorf("namespace = %q, want %q for job %s", namespace, want, jobName)
	}
	if want := fmt.Sprintf("storage-%d-%d", iteration, replica); pvcName != want {
		return storagePVCSample{}, fmt.Errorf("pvcName = %q, want %q for job %s", pvcName, want, jobName)
	}
	binding, err := requiredNonNegativeInteger(document, "bindingLatency")
	if err != nil {
		return storagePVCSample{}, err
	}
	return storagePVCSample{JobName: jobName, Iteration: iteration, Replica: replica, Binding: binding}, nil
}

func storageSampleIdentity(iteration, replica int) string {
	return strconv.Itoa(iteration) + "\x00" + strconv.Itoa(replica)
}

func appendStorageStartupRows(rows []Row, jobs map[string]podReadyJob, pods map[string]map[string]storagePodSample, pvcs map[string]map[string]storagePVCSample) ([]Row, error) {
	if len(jobs) != len(storageStartupSpecs) {
		return nil, fmt.Errorf("storage startup reporting requires all six jobSummary documents")
	}
	jobNames := make([]string, 0, len(storageStartupSpecs))
	for jobName := range storageStartupSpecs {
		jobNames = append(jobNames, jobName)
	}
	sort.Strings(jobNames)
	expectedIterations := 0
	for _, jobName := range jobNames {
		job, exists := jobs[jobName]
		if !exists {
			return nil, fmt.Errorf("storage startup reporting requires jobSummary for job %s", jobName)
		}
		if job.Iterations <= 0 {
			return nil, fmt.Errorf("storage startup job %s has invalid expected sample count", jobName)
		}
		if expectedIterations == 0 {
			expectedIterations = job.Iterations
		} else if job.Iterations != expectedIterations {
			return nil, fmt.Errorf("storage startup jobs must have the same jobIterations: job %s has %d, want %d", jobName, job.Iterations, expectedIterations)
		}
		podValues := make([]float64, 0, len(pods[jobName]))
		ambiguous := 0
		for _, sample := range pods[jobName] {
			if sample.Iteration >= job.Iterations {
				return nil, fmt.Errorf("jobIteration %d exceeds expected iterations %d for job %s", sample.Iteration, job.Iterations, jobName)
			}
			if sample.Replica != 1 {
				return nil, fmt.Errorf("replica %d must be 1 for job %s", sample.Replica, jobName)
			}
			if sample.ReadyDelta <= 0 {
				ambiguous++
				continue
			}
			podValues = append(podValues, float64(sample.ReadyDelta))
		}
		if len(pods[jobName]) > job.Iterations {
			return nil, fmt.Errorf("pod sample count exceeds expected count for job %s", jobName)
		}
		dimensions := map[string]string{"jobName": jobName}
		rows = appendLifecycleSummaryRows(rows, "derived/scheduled-to-ready-to-start-containers", dimensions, "scheduled_to_ready_to_start_containers_latency", podValues, job.Iterations, len(pods[jobName]), ambiguous)

		spec := storageStartupSpecs[jobName]
		if spec.StorageClass == "" {
			if len(pvcs[jobName]) != 0 {
				return nil, fmt.Errorf("storage startup job %s does not use PVCs", jobName)
			}
			continue
		}
		pvcValues := make([]float64, 0, len(pvcs[jobName]))
		for identity, sample := range pvcs[jobName] {
			if sample.Iteration >= job.Iterations {
				return nil, fmt.Errorf("jobIteration %d exceeds expected iterations %d for job %s", sample.Iteration, job.Iterations, jobName)
			}
			if sample.Replica != 1 {
				return nil, fmt.Errorf("replica %d must be 1 for job %s", sample.Replica, jobName)
			}
			if _, paired := pods[jobName][identity]; !paired {
				return nil, fmt.Errorf("PVC sample for job %s iteration %d has no matching pod sample", jobName, sample.Iteration)
			}
			pvcValues = append(pvcValues, float64(sample.Binding))
		}
		if len(pvcs[jobName]) > job.Iterations {
			return nil, fmt.Errorf("PVC sample count exceeds expected count for job %s", jobName)
		}
		pvcDimensions := map[string]string{"jobName": jobName, "storageClass": spec.StorageClass}
		rows = appendLifecycleSummaryRows(rows, "derived/pvc-binding", pvcDimensions, "pvc_binding_latency", pvcValues, job.Iterations, len(pvcs[jobName]), 0)
	}
	return rows, nil
}

func appendLifecycleSummaryRows(rows []Row, source string, dimensions map[string]string, prefix string, values []float64, expected, observed, ambiguous int) []Row {
	if len(values) > 0 {
		sort.Float64s(values)
		total := 0.0
		for _, value := range values {
			total += value
		}
		for _, metric := range []struct {
			name  string
			value float64
		}{
			{name: prefix + "_p50", value: percentile(values, 50)},
			{name: prefix + "_p95", value: percentile(values, 95)},
			{name: prefix + "_max", value: values[len(values)-1]},
			{name: prefix + "_avg", value: total / float64(len(values))},
		} {
			rows = append(rows, Row{Source: source, Dimensions: dimensions, Metric: metric.name, Value: Number{Text: strconv.Itoa(int(metric.value))}, Unit: "milliseconds"})
		}
	}
	for _, count := range []struct {
		name  string
		value int
	}{
		{name: prefix + "_expected_count", value: expected},
		{name: prefix + "_valid_count", value: len(values)},
		{name: prefix + "_missing_count", value: expected - observed},
		{name: prefix + "_ambiguous_count", value: ambiguous},
	} {
		rows = append(rows, Row{Source: source, Dimensions: dimensions, Metric: count.name, Value: Number{Text: strconv.Itoa(count.value)}, Unit: "samples"})
	}
	return rows
}

func enrichPodReadyDimensions(rows []Row, jobs map[string]podReadyJob) error {
	for index := range rows {
		jobName := rows[index].Dimensions["jobName"]
		if jobName == "" {
			jobName = rows[index].Dimensions["kubeBurner.jobName"]
		}
		if jobName == "" {
			return fmt.Errorf("offered-load metric %s has no jobName", rows[index].Metric)
		}
		job, exists := jobs[jobName]
		if !exists {
			return fmt.Errorf("job %s has no matching jobSummary for metrics", jobName)
		}
		rows[index].Dimensions["offeredQPS"] = job.QPS
		rows[index].Dimensions["burst"] = strconv.Itoa(job.Burst)
	}
	return nil
}

func podReadyJobDocument(document map[string]json.RawMessage) (podReadyJob, error) {
	jobConfig, err := requiredObject(document, "jobConfig")
	if err != nil {
		return podReadyJob{}, err
	}
	name, err := requiredString(jobConfig, "name")
	if err != nil {
		return podReadyJob{}, fmt.Errorf("jobConfig.%w", err)
	}
	if name == "" {
		return podReadyJob{}, fmt.Errorf("jobConfig.name must not be empty")
	}
	iterations, err := requiredPositiveInteger(jobConfig, "jobIterations")
	if err != nil {
		return podReadyJob{}, fmt.Errorf("jobConfig.%w", err)
	}
	qps, err := requiredNumber(jobConfig, "qps")
	if err != nil {
		return podReadyJob{}, fmt.Errorf("jobConfig.%w", err)
	}
	qpsValue, _ := strconv.ParseFloat(qps.Text, 64)
	if qpsValue <= 0 {
		return podReadyJob{}, fmt.Errorf("jobConfig.qps must be greater than zero")
	}
	burst, err := requiredPositiveInteger(jobConfig, "burst")
	if err != nil {
		return podReadyJob{}, fmt.Errorf("jobConfig.%w", err)
	}
	return podReadyJob{Name: name, Iterations: iterations, QPS: qps.Text, Burst: burst}, nil
}

func podReadySampleDocument(document map[string]json.RawMessage) (podReadySample, error) {
	jobName, err := requiredString(document, "jobName")
	if err != nil {
		return podReadySample{}, err
	}
	namespace, err := requiredString(document, "namespace")
	if err != nil {
		return podReadySample{}, err
	}
	podName, err := requiredString(document, "podName")
	if err != nil {
		return podReadySample{}, err
	}
	if jobName == "" || namespace == "" || podName == "" {
		return podReadySample{}, fmt.Errorf("jobName, namespace, and podName must not be empty")
	}
	timestamp, err := requiredString(document, "timestamp")
	if err != nil {
		return podReadySample{}, err
	}
	created, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return podReadySample{}, fmt.Errorf("timestamp must be RFC3339: %w", err)
	}
	readyLatency, err := requiredNonNegativeInteger(document, "podReadyLatency")
	if err != nil {
		return podReadySample{}, err
	}
	return podReadySample{
		JobName:   jobName,
		Namespace: namespace,
		PodName:   podName,
		Created:   created,
		Ready:     created.Add(time.Duration(readyLatency) * time.Millisecond),
	}, nil
}

func appendPodReadyRows(rows []Row, jobs map[string]podReadyJob, samples map[string]map[string]podReadySample) ([]Row, error) {
	if len(jobs) == 0 {
		return nil, fmt.Errorf("pod Ready reporting requires jobSummary documents")
	}
	for jobName, jobSamples := range samples {
		if _, exists := jobs[jobName]; !exists {
			for _, sample := range jobSamples {
				return nil, fmt.Errorf("pod %s/%s for job %s has no matching jobSummary", sample.Namespace, sample.PodName, sample.JobName)
			}
		}
	}
	jobNames := make([]string, 0, len(jobs))
	for jobName := range jobs {
		jobNames = append(jobNames, jobName)
	}
	sort.Strings(jobNames)
	for _, jobName := range jobNames {
		job := jobs[jobName]
		jobSamples := samples[jobName]
		readyCount := len(jobSamples)
		if readyCount > job.Iterations {
			return nil, fmt.Errorf("ready pod count %d exceeds expected pod count %d for job %s", readyCount, job.Iterations, job.Name)
		}
		throughput := 0.0
		if readyCount > 0 {
			var firstCreated, lastReady time.Time
			for _, sample := range jobSamples {
				if firstCreated.IsZero() || sample.Created.Before(firstCreated) {
					firstCreated = sample.Created
				}
				if lastReady.IsZero() || sample.Ready.After(lastReady) {
					lastReady = sample.Ready
				}
			}
			elapsed := lastReady.Sub(firstCreated).Seconds()
			if elapsed <= 0 {
				return nil, fmt.Errorf("pod Ready window must be positive for job %s", job.Name)
			}
			throughput = float64(readyCount) / elapsed
		}
		dimensions := map[string]string{"jobName": job.Name, "offeredQPS": job.QPS, "burst": strconv.Itoa(job.Burst)}
		rows = append(rows,
			Row{Source: "derived/pod-ready", Dimensions: dimensions, Metric: "pod_ready_throughput", Value: Number{Text: strconv.FormatFloat(throughput, 'g', -1, 64)}, Unit: "pods/second"},
			Row{Source: "derived/pod-ready", Dimensions: dimensions, Metric: "pod_ready_missing_count", Value: Number{Text: strconv.Itoa(job.Iterations - readyCount)}, Unit: "pods"},
		)
	}
	return rows, nil
}

func sandboxCounterDocument(document map[string]json.RawMessage) (string, float64, string, error) {
	jobName, err := requiredString(document, "jobName")
	if err != nil {
		return "", 0, "", err
	}
	value, err := requiredNumber(document, "value")
	if err != nil {
		return "", 0, "", err
	}
	parsed, _ := strconv.ParseFloat(value.Text, 64)
	handler := "default"
	if encoded, ok := document["labels"]; ok {
		labels := map[string]string{}
		if err := json.Unmarshal(encoded, &labels); err != nil {
			return "", 0, "", fmt.Errorf("labels must be an object containing only string values: %w", err)
		}
		if labels["runtime_handler"] != "" {
			handler = labels["runtime_handler"]
		}
	}
	return jobName, parsed, handler, nil
}

func isSandboxCounterMetric(metricName string) bool {
	return metricName == "runPodSandboxCount" || metricName == "runPodSandboxCount-start" || metricName == "runPodSandboxSum" || metricName == "runPodSandboxSum-start"
}

func appendRunPodSandboxRows(rows []Row, counters map[string]map[string]float64, allowInactiveJobs bool) ([]Row, error) {
	activeJobs := map[string]bool{}
	seenJobs := map[string]bool{}
	for key, values := range counters {
		parts := strings.SplitN(key, "\x00", 2)
		jobName, handler := parts[0], parts[1]
		seenJobs[jobName] = true
		countEnd, hasCountEnd := values["runPodSandboxCount"]
		sumEnd, hasSumEnd := values["runPodSandboxSum"]
		countStart, hasCountStart := values["runPodSandboxCount-start"]
		sumStart, hasSumStart := values["runPodSandboxSum-start"]
		if !hasCountEnd || !hasSumEnd || hasCountStart != hasSumStart {
			return nil, fmt.Errorf("incomplete RunPodSandbox counters for job %s handler %s", jobName, handler)
		}
		count := countEnd - countStart
		sum := sumEnd - sumStart
		if count < 0 {
			return nil, fmt.Errorf("RunPodSandbox count delta must be positive for job %s handler %s", jobName, handler)
		}
		if count != math.Trunc(count) {
			return nil, fmt.Errorf("RunPodSandbox count delta must be an integer for job %s handler %s", jobName, handler)
		}
		if sum < 0 {
			return nil, fmt.Errorf("RunPodSandbox sum delta must not be negative for job %s handler %s", jobName, handler)
		}
		if count == 0 {
			if sum != 0 {
				return nil, fmt.Errorf("RunPodSandbox sum delta must be zero when count delta is zero for job %s handler %s", jobName, handler)
			}
			continue
		}
		activeJobs[jobName] = true
		dimensions := map[string]string{"kubeBurner.jobName": jobName, "label.runtime_handler": handler}
		rows = append(rows,
			Row{Source: "derived/run-podsandbox", Dimensions: dimensions, Metric: "runPodSandboxCount", Value: Number{Text: strconv.FormatFloat(count, 'g', -1, 64)}, Unit: "count"},
			Row{Source: "derived/run-podsandbox", Dimensions: dimensions, Metric: "runPodSandboxMean", Value: Number{Text: strconv.FormatFloat(sum/count, 'g', -1, 64)}, Unit: "seconds"},
		)
	}
	for jobName := range seenJobs {
		if !activeJobs[jobName] && !allowInactiveJobs {
			return nil, fmt.Errorf("RunPodSandbox count delta must be positive for job %s", jobName)
		}
	}
	return rows, nil
}

func launchSampleCount(valuesByJob map[string][]float64) int {
	total := 0
	for _, values := range valuesByJob {
		total += len(values)
	}
	return total
}

func appendPostSandboxLaunchRows(rows []Row, source, jobName string, values []float64) []Row {
	sort.Float64s(values)
	total := 0.0
	for _, value := range values {
		total += value
	}
	dimensions := map[string]string{"jobName": jobName}
	metrics := []struct {
		name  string
		value float64
		unit  string
	}{
		{name: "post_sandbox_container_launch_latency_p50", value: percentile(values, 50), unit: "milliseconds"},
		{name: "post_sandbox_container_launch_latency_p95", value: percentile(values, 95), unit: "milliseconds"},
		{name: "post_sandbox_container_launch_latency_p99", value: percentile(values, 99), unit: "milliseconds"},
		{name: "post_sandbox_container_launch_latency_max", value: values[len(values)-1], unit: "milliseconds"},
		{name: "post_sandbox_container_launch_latency_avg", value: total / float64(len(values)), unit: "milliseconds"},
		{name: "post_sandbox_container_launch_latency_sample_count", value: float64(len(values)), unit: "samples"},
	}
	for _, metric := range metrics {
		rows = append(rows, Row{Source: source, Dimensions: dimensions, Metric: metric.name, Value: Number{Text: strconv.Itoa(int(metric.value))}, Unit: metric.unit})
	}
	return rows
}

func percentile(sortedValues []float64, percent float64) float64 {
	if len(sortedValues) == 1 {
		return sortedValues[0]
	}
	index := percent / 100 * float64(len(sortedValues))
	if index == float64(int64(index)) {
		return sortedValues[int(index)-1]
	}
	if index > 1 {
		i := int(index)
		return (sortedValues[i-1] + sortedValues[i]) / 2
	}
	return sortedValues[0]
}

func readKubeBurnerDocuments(path, source string) ([]map[string]json.RawMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: open JSON: %w", source, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var documents []map[string]json.RawMessage
	if err := decoder.Decode(&documents); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON document array: %w", source, err)
	}
	if documents == nil {
		return nil, fmt.Errorf("%s: invalid JSON document array: top-level value must be an array", source)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return nil, fmt.Errorf("%s: invalid trailing JSON: %w", source, err)
	}
	return documents, nil
}

func appendPodLatencyQuantileRows(rows []Row, source string, document map[string]json.RawMessage) ([]Row, error) {
	jobName, err := requiredString(document, "jobName")
	if err != nil {
		return nil, err
	}
	quantileName, err := requiredString(document, "quantileName")
	if err != nil {
		return nil, err
	}
	dimensions := map[string]string{"jobName": jobName, "quantileName": quantileName}
	for _, metric := range []struct {
		field string
		name  string
	}{
		{field: "P50", name: "pod_latency_p50"},
		{field: "P95", name: "pod_latency_p95"},
		{field: "P99", name: "pod_latency_p99"},
		{field: "max", name: "pod_latency_max"},
		{field: "avg", name: "pod_latency_avg"},
	} {
		value, err := requiredNumber(document, metric.field)
		if err != nil {
			return nil, err
		}
		rows = append(rows, Row{Source: source, Dimensions: dimensions, Metric: metric.name, Value: value, Unit: "milliseconds"})
	}
	return rows, nil
}

func appendPrometheusRow(rows []Row, source, metricName, unit string, document map[string]json.RawMessage) ([]Row, error) {
	value, err := requiredNumber(document, "value")
	if err != nil {
		return nil, err
	}
	timestamp, err := requiredString(document, "timestamp")
	if err != nil {
		return nil, err
	}
	if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
		return nil, fmt.Errorf("timestamp must be RFC3339: %w", err)
	}

	labels := map[string]string{}
	if encodedLabels, ok := document["labels"]; ok {
		if err := json.Unmarshal(encodedLabels, &labels); err != nil {
			return nil, fmt.Errorf("labels must be an object containing only string values: %w", err)
		}
		if labels == nil {
			return nil, fmt.Errorf("labels must be an object")
		}
	}
	if metricName == "runPodSandboxCount" || metricName == "runPodSandboxMean" {
		if handler, exists := labels["runtime_handler"]; !exists || handler == "" {
			labels["runtime_handler"] = "default"
		}
	}
	dimensions := make(map[string]string, len(labels)+2)
	for key, value := range labels {
		dimensions["label."+key] = value
	}
	dimensions["kubeBurner.timestamp"] = timestamp
	if encodedJobName, ok := document["jobName"]; ok {
		var jobName string
		if err := json.Unmarshal(encodedJobName, &jobName); err != nil {
			return nil, fmt.Errorf("jobName must be a string: %w", err)
		}
		dimensions["kubeBurner.jobName"] = jobName
	}
	for key := range dimensions {
		if reservedColumns[key] {
			return nil, fmt.Errorf("dimension %q uses a reserved column name", key)
		}
	}
	return append(rows, Row{Source: source, Dimensions: dimensions, Metric: metricName, Value: value, Unit: unit}), nil
}

func requiredString(document map[string]json.RawMessage, field string) (string, error) {
	encoded, ok := document[field]
	if !ok {
		return "", fmt.Errorf("%s field is required", field)
	}
	var value string
	if err := json.Unmarshal(encoded, &value); err != nil {
		return "", fmt.Errorf("%s must be a string: %w", field, err)
	}
	return value, nil
}

func requiredNonEmptyString(document map[string]json.RawMessage, field string) (string, error) {
	value, err := requiredString(document, field)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", field)
	}
	return value, nil
}

func requiredNumber(document map[string]json.RawMessage, field string) (Number, error) {
	encoded, ok := document[field]
	if !ok {
		return Number{}, fmt.Errorf("%s field is required", field)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return Number{}, fmt.Errorf("%s: %w", field, err)
	}
	number, ok := value.(json.Number)
	if !ok {
		return Number{}, fmt.Errorf("%s must be a JSON number", field)
	}
	parsed, err := ParseNumber(number)
	if err != nil {
		return Number{}, fmt.Errorf("%s: %w", field, err)
	}
	return parsed, nil
}

func requiredObject(document map[string]json.RawMessage, field string) (map[string]json.RawMessage, error) {
	encoded, ok := document[field]
	if !ok {
		return nil, fmt.Errorf("%s field is required", field)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, fmt.Errorf("%s must be an object: %w", field, err)
	}
	if value == nil {
		return nil, fmt.Errorf("%s must be an object", field)
	}
	return value, nil
}

func requiredPositiveInteger(document map[string]json.RawMessage, field string) (int, error) {
	value, err := requiredNonNegativeInteger(document, field)
	if err != nil {
		return 0, err
	}
	if value == 0 {
		return 0, fmt.Errorf("%s must be greater than zero", field)
	}
	return value, nil
}

func requiredNonNegativeInteger(document map[string]json.RawMessage, field string) (int, error) {
	value, err := requiredNumber(document, field)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(value.Text, 10, 64)
	if err != nil || parsed < 0 || int64(int(parsed)) != parsed {
		return 0, fmt.Errorf("%s must be a non-negative integer", field)
	}
	return int(parsed), nil
}
