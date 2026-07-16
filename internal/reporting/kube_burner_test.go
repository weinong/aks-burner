package reporting

// Fixture shapes are from kube-burner v2.7.3
// (9091baef070ca04e1116cc4d07e53d08490e6896) measurement and local-indexer
// output. To refresh them, run that exact version against the kata-perf smoke
// suite, copy the corresponding raw/metrics arrays, remove environment-specific
// names and UUIDs, and retain all structural fields before updating these tests.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadKubeBurnerMetricsMapsPodLatencyQuantiles(t *testing.T) {
	runDir := copyKubeBurnerFixtures(t, "podLatencyQuantilesMeasurement.json")
	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 5 {
		t.Fatalf("files/rows = %d/%d", files, len(rows))
	}
	wantValues := map[string]string{
		"pod_latency_p50": "2897",
		"pod_latency_p95": "3510",
		"pod_latency_p99": "3774",
		"pod_latency_max": "3774",
		"pod_latency_avg": "2876",
	}
	for _, row := range rows {
		if row.Source != "raw/metrics/podLatencyQuantilesMeasurement.json" || row.Unit != "milliseconds" || row.Dimensions["jobName"] != "startup-smoke" || row.Dimensions["quantileName"] != "Ready" {
			t.Fatalf("row = %#v", row)
		}
		if row.Value.Text != wantValues[row.Metric] {
			t.Fatalf("row = %#v", row)
		}
		delete(wantValues, row.Metric)
	}
	if len(wantValues) != 0 {
		t.Fatalf("missing metrics = %#v", wantValues)
	}
}

func TestReadKubeBurnerMetricsPreservesRangeSamples(t *testing.T) {
	runDir := copyKubeBurnerFixtures(t, "podCPUUsage.json")
	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, []string{"podCPUUsage"}, map[string]string{"podCPUUsage": "cores"})
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 2 || rows[0].Dimensions["kubeBurner.timestamp"] == rows[1].Dimensions["kubeBurner.timestamp"] {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
	if rows[0].Metric != "podCPUUsage" || rows[0].Unit != "cores" || rows[0].Source != "raw/metrics/podCPUUsage.json" {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Dimensions["label.pod"] != "demo-0" || rows[0].Dimensions["label.namespace"] != "demo" || rows[0].Dimensions["kubeBurner.jobName"] != "startup-smoke" {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Value.Text != "0.3300880234732172" || rows[1].Value.Text != "0.31978102677038506" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestReadKubeBurnerMetricsAcceptsUnlabeledPrometheusScalar(t *testing.T) {
	runDir := copyKubeBurnerFixtures(t, "podStartTotalP95.json")
	rows, files, err := ReadKubeBurnerMetrics(
		filepath.Join(runDir, "raw", "metrics"),
		runDir,
		[]string{"podStartTotalP95"},
		map[string]string{"podStartTotalP95": "seconds"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 1 {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
	wantDimensions := map[string]string{
		"kubeBurner.jobName":   "fio-fast",
		"kubeBurner.timestamp": "2026-07-11T00:00:00Z",
	}
	if rows[0].Metric != "podStartTotalP95" || rows[0].Value.Text != "1.284" || rows[0].Unit != "seconds" || !reflect.DeepEqual(rows[0].Dimensions, wantDimensions) {
		t.Fatalf("row = %#v", rows[0])
	}
}

func TestReadKubeBurnerMetricsNormalizesDefaultRuntimeHandler(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels string
		want   string
	}{
		{name: "absent", labels: `{}`, want: "default"},
		{name: "empty", labels: `{"runtime_handler":""}`, want: "default"},
		{name: "kata", labels: `{"runtime_handler":"kata"}`, want: "kata"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := map[string]json.RawMessage{
				"jobName": json.RawMessage(`"job"`),
				"value":   json.RawMessage(`1`),
				"labels":  json.RawMessage(tc.labels),
			}
			_, _, handler, err := sandboxCounterDocument(document)
			if err != nil {
				t.Fatal(err)
			}
			if handler != tc.want {
				t.Fatalf("handler = %q, want %q", handler, tc.want)
			}
		})
	}
}

func TestReadKubeBurnerMetricsDerivesSandboxCountAndMeanFromCapturedCounters(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "runPodSandboxCount-start.json", `[
  {"metricName":"runPodSandboxCount-start","value":10,"timestamp":"2026-07-14T00:00:00Z","jobName":"startup-smoke","labels":{"runtime_handler":"kata"}}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxCount.json", `[
  {"metricName":"runPodSandboxCount","value":15,"timestamp":"2026-07-14T00:01:00Z","jobName":"startup-smoke","labels":{"runtime_handler":"kata"}},
  {"metricName":"runPodSandboxCount","value":2,"timestamp":"2026-07-14T00:01:00Z","jobName":"startup-default-runtime","labels":{}}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxSum-start.json", `[
  {"metricName":"runPodSandboxSum-start","value":20,"timestamp":"2026-07-14T00:00:00Z","jobName":"startup-smoke","labels":{"runtime_handler":"kata"}}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxSum.json", `[
  {"metricName":"runPodSandboxSum","value":25,"timestamp":"2026-07-14T00:01:00Z","jobName":"startup-smoke","labels":{"runtime_handler":"kata"}},
  {"metricName":"runPodSandboxSum","value":0.5,"timestamp":"2026-07-14T00:01:00Z","jobName":"startup-default-runtime","labels":{}}
]`)
	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, []string{"runPodSandboxCount", "runPodSandboxSum"}, map[string]string{"runPodSandboxCount": "count", "runPodSandboxSum": "seconds"})
	if err != nil {
		t.Fatal(err)
	}
	if files != 4 || len(rows) != 4 {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
	got := map[string]string{}
	for _, row := range rows {
		if row.Source != "derived/run-podsandbox" {
			t.Fatalf("source = %q", row.Source)
		}
		got[row.Dimensions["kubeBurner.jobName"]+"/"+row.Dimensions["label.runtime_handler"]+"/"+row.Metric] = row.Value.Text
	}
	want := map[string]string{
		"startup-smoke/kata/runPodSandboxCount":              "5",
		"startup-smoke/kata/runPodSandboxMean":               "1",
		"startup-default-runtime/default/runPodSandboxCount": "2",
		"startup-default-runtime/default/runPodSandboxMean":  "0.25",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sandbox rows = %#v, want %#v", got, want)
	}
}

func TestReadKubeBurnerMetricsSkipsInactiveSandboxCounterSeries(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "runPodSandboxCount-start.json", `[
  {"metricName":"runPodSandboxCount-start","value":351,"timestamp":"2026-07-14T21:01:46Z","jobName":"startup-default-runtime","labels":{}},
  {"metricName":"runPodSandboxCount-start","value":335,"timestamp":"2026-07-14T21:01:46Z","jobName":"startup-default-runtime","labels":{"runtime_handler":"kata"}}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxCount.json", `[
  {"metricName":"runPodSandboxCount","value":371,"timestamp":"2026-07-14T21:03:09Z","jobName":"startup-default-runtime","labels":{}},
  {"metricName":"runPodSandboxCount","value":335,"timestamp":"2026-07-14T21:03:09Z","jobName":"startup-default-runtime","labels":{"runtime_handler":"kata"}}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxSum-start.json", `[
  {"metricName":"runPodSandboxSum-start","value":2108.892588011,"timestamp":"2026-07-14T21:01:46Z","jobName":"startup-default-runtime","labels":{}},
  {"metricName":"runPodSandboxSum-start","value":5004.852577800997,"timestamp":"2026-07-14T21:01:46Z","jobName":"startup-default-runtime","labels":{"runtime_handler":"kata"}}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxSum.json", `[
  {"metricName":"runPodSandboxSum","value":2116.3522677230003,"timestamp":"2026-07-14T21:03:09Z","jobName":"startup-default-runtime","labels":{}},
  {"metricName":"runPodSandboxSum","value":5004.852577800997,"timestamp":"2026-07-14T21:03:09Z","jobName":"startup-default-runtime","labels":{"runtime_handler":"kata"}}
]`)

	rows, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, []string{"runPodSandboxCount", "runPodSandboxSum"}, map[string]string{"runPodSandboxCount": "count", "runPodSandboxSum": "seconds"})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, row := range rows {
		got[row.Dimensions["kubeBurner.jobName"]+"/"+row.Dimensions["label.runtime_handler"]+"/"+row.Metric] = row.Value.Text
	}
	want := map[string]string{
		"startup-default-runtime/default/runPodSandboxCount": "20",
		"startup-default-runtime/default/runPodSandboxMean":  "0.3729839856000126",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sandbox rows = %#v, want %#v", got, want)
	}
}

func TestReadKubeBurnerMetricsRejectsJobWithNoActiveSandboxCounters(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "runPodSandboxCount-start.json", `[
  {"metricName":"runPodSandboxCount-start","value":335,"jobName":"startup-default-runtime","labels":{"runtime_handler":"kata"}}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxCount.json", `[
  {"metricName":"runPodSandboxCount","value":335,"jobName":"startup-default-runtime","labels":{"runtime_handler":"kata"}}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxSum-start.json", `[
  {"metricName":"runPodSandboxSum-start","value":5004.852577800997,"jobName":"startup-default-runtime","labels":{"runtime_handler":"kata"}}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxSum.json", `[
  {"metricName":"runPodSandboxSum","value":5004.852577800997,"jobName":"startup-default-runtime","labels":{"runtime_handler":"kata"}}
]`)

	_, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "RunPodSandbox count delta must be positive for job startup-default-runtime") {
		t.Fatalf("error = %v, want inactive job failure", err)
	}
}

func TestReadKubeBurnerMetricsAllowsInactiveSandboxCountersForOfferedLoad(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "jobSummary.json", `[
  {"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"startup-load-kata","jobIterations":20,"qps":5,"burst":1}}
]`)
	writeKubeBurnerMetric(t, runDir, "sandbox.json", `[
  {"metricName":"runPodSandboxCount-start","uuid":"run-1","value":10,"jobName":"startup-load-kata","labels":{"runtime_handler":"kata"}},
  {"metricName":"runPodSandboxCount","uuid":"run-1","value":10,"jobName":"startup-load-kata","labels":{"runtime_handler":"kata"}},
  {"metricName":"runPodSandboxSum-start","uuid":"run-1","value":20,"jobName":"startup-load-kata","labels":{"runtime_handler":"kata"}},
  {"metricName":"runPodSandboxSum","uuid":"run-1","value":20,"jobName":"startup-load-kata","labels":{"runtime_handler":"kata"}}
]`)

	rows, _, err := readKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row.Metric] = row.Value.Text
	}
	want := map[string]string{"pod_ready_throughput": "0", "pod_ready_missing_count": "20"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %#v, want readiness-only rows %#v", got, want)
	}
}

func TestReadKubeBurnerMetricsRejectsInvalidSandboxCounterSets(t *testing.T) {
	for _, tc := range []struct {
		name      string
		documents string
		want      string
	}{
		{name: "duplicate", documents: `[
  {"metricName":"runPodSandboxCount","value":2,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxCount","value":3,"jobName":"job","labels":{}}
]`, want: "duplicate runPodSandboxCount"},
		{name: "missing sum end", documents: `[
  {"metricName":"runPodSandboxCount","value":2,"jobName":"job","labels":{}}
]`, want: "incomplete RunPodSandbox counters"},
		{name: "count reset", documents: `[
  {"metricName":"runPodSandboxCount-start","value":5,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxCount","value":2,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxSum-start","value":5,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxSum","value":6,"jobName":"job","labels":{}}
]`, want: "RunPodSandbox count delta must be positive"},
		{name: "nonintegral count", documents: `[
  {"metricName":"runPodSandboxCount","value":2.5,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxSum","value":1,"jobName":"job","labels":{}}
]`, want: "RunPodSandbox count delta must be an integer"},
		{name: "sum reset", documents: `[
  {"metricName":"runPodSandboxCount-start","value":1,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxCount","value":2,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxSum-start","value":5,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxSum","value":4,"jobName":"job","labels":{}}
]`, want: "RunPodSandbox sum delta must not be negative"},
		{name: "zero count with positive sum", documents: `[
  {"metricName":"runPodSandboxCount-start","value":2,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxCount","value":2,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxSum-start","value":5,"jobName":"job","labels":{}},
  {"metricName":"runPodSandboxSum","value":6,"jobName":"job","labels":{}}
]`, want: "RunPodSandbox sum delta must be zero when count delta is zero"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			writeKubeBurnerMetric(t, runDir, "sandbox.json", tc.documents)
			_, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadKubeBurnerMetricsDerivesPostSandboxContainerLaunchSummary(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "podLatencyMeasurement.json", `[
  {"metricName":"podLatencyMeasurement","jobName":"startup-smoke","readyToStartContainersLatency":1000,"containersStartedLatency":1010},
  {"metricName":"podLatencyMeasurement","jobName":"startup-smoke","readyToStartContainersLatency":1000,"containersStartedLatency":1020},
  {"metricName":"podLatencyMeasurement","jobName":"startup-smoke","readyToStartContainersLatency":1000,"containersStartedLatency":1030},
  {"metricName":"podLatencyMeasurement","jobName":"startup-smoke","readyToStartContainersLatency":1000,"containersStartedLatency":1040},
  {"metricName":"podLatencyMeasurement","jobName":"startup-smoke","readyToStartContainersLatency":1000,"containersStartedLatency":1050}
]`)
	writeKubeBurnerMetric(t, runDir, "podLatencyMeasurement-part2.json", `[
  {"metricName":"podLatencyMeasurement","jobName":"startup-default-runtime","readyToStartContainersLatency":2000,"containersStartedLatency":2100},
  {"metricName":"podLatencyMeasurement","jobName":"startup-smoke","readyToStartContainersLatency":1000,"containersStartedLatency":1060}
]`)
	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || len(rows) != 12 {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
	got := map[string]string{}
	for _, row := range rows {
		if row.Source != "derived/post-sandbox-container-launch" {
			t.Fatalf("derived row source = %q", row.Source)
		}
		got[row.Dimensions["jobName"]+"/"+row.Metric+"/"+row.Unit] = row.Value.Text
	}
	want := map[string]string{
		"startup-smoke/post_sandbox_container_launch_latency_p50/milliseconds":               "30",
		"startup-smoke/post_sandbox_container_launch_latency_p95/milliseconds":               "55",
		"startup-smoke/post_sandbox_container_launch_latency_p99/milliseconds":               "55",
		"startup-smoke/post_sandbox_container_launch_latency_max/milliseconds":               "60",
		"startup-smoke/post_sandbox_container_launch_latency_avg/milliseconds":               "35",
		"startup-smoke/post_sandbox_container_launch_latency_sample_count/samples":           "6",
		"startup-default-runtime/post_sandbox_container_launch_latency_p50/milliseconds":     "100",
		"startup-default-runtime/post_sandbox_container_launch_latency_p95/milliseconds":     "100",
		"startup-default-runtime/post_sandbox_container_launch_latency_p99/milliseconds":     "100",
		"startup-default-runtime/post_sandbox_container_launch_latency_max/milliseconds":     "100",
		"startup-default-runtime/post_sandbox_container_launch_latency_avg/milliseconds":     "100",
		"startup-default-runtime/post_sandbox_container_launch_latency_sample_count/samples": "1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derived metrics = %#v, want %#v", got, want)
	}
}

func TestReadKubeBurnerMetricsDerivesOfferedLoadReadiness(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "jobSummary.json", `[
  {"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"startup-load-kata","jobIterations":5,"qps":2,"burst":1}}
]`)
	writeKubeBurnerMetric(t, runDir, "podLatencyQuantilesMeasurement.json", `[
  {"metricName":"podLatencyQuantilesMeasurement","uuid":"run-1","jobName":"startup-load-kata","quantileName":"Ready","P50":1000,"P95":2000,"P99":2000,"max":2000,"avg":1250}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxCount.json", `[
  {"metricName":"runPodSandboxCount","uuid":"run-1","value":4,"jobName":"startup-load-kata","labels":{"runtime_handler":"kata"}}
]`)
	writeKubeBurnerMetric(t, runDir, "runPodSandboxSum.json", `[
  {"metricName":"runPodSandboxSum","uuid":"run-1","value":2,"jobName":"startup-load-kata","labels":{"runtime_handler":"kata"}}
]`)
	writeKubeBurnerMetric(t, runDir, "podLatencyMeasurement.json", `[
  {"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"startup-load-kata","namespace":"kata-perf-load-0","podName":"kata-perf-0-1","timestamp":"2026-07-15T00:00:00Z","podReadyLatency":1000,"readyToStartContainersLatency":500,"containersStartedLatency":700},
  {"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"startup-load-kata","namespace":"kata-perf-load-0","podName":"kata-perf-1-1","timestamp":"2026-07-15T00:00:01Z","podReadyLatency":1000,"readyToStartContainersLatency":500,"containersStartedLatency":700},
  {"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"startup-load-kata","namespace":"kata-perf-load-0","podName":"kata-perf-2-1","timestamp":"2026-07-15T00:00:02Z","podReadyLatency":2000,"readyToStartContainersLatency":500,"containersStartedLatency":700},
  {"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"startup-load-kata","namespace":"kata-perf-load-0","podName":"kata-perf-3-1","timestamp":"2026-07-15T00:00:03Z","podReadyLatency":1000,"readyToStartContainersLatency":0,"containersStartedLatency":200}
]`)

	rows, files, err := readKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if files != 5 {
		t.Fatalf("contributing files = %d, want 5", files)
	}
	got := map[string]string{}
	for _, row := range rows {
		if row.Dimensions["jobName"] == "startup-load-kata" || row.Dimensions["kubeBurner.jobName"] == "startup-load-kata" {
			if row.Dimensions["offeredQPS"] != "2" || row.Dimensions["burst"] != "1" {
				t.Fatalf("load row missing offered-load dimensions: %#v", row)
			}
		}
		if row.Source != "derived/pod-ready" {
			continue
		}
		if !reflect.DeepEqual(row.Dimensions, map[string]string{"jobName": "startup-load-kata", "offeredQPS": "2", "burst": "1"}) {
			t.Fatalf("readiness dimensions = %#v", row.Dimensions)
		}
		got[row.Metric+"/"+row.Unit] = row.Value.Text
	}
	want := map[string]string{
		"pod_ready_throughput/pods/second": "1",
		"pod_ready_missing_count/pods":     "1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readiness metrics = %#v, want %#v", got, want)
	}
}

func TestReadKubeBurnerMetricsReportsNoReadyPods(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "jobSummary.json", `[
  {"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"startup-load-kata","jobIterations":20,"qps":5,"burst":1}}
]`)

	rows, files, err := readKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 2 {
		t.Fatalf("files/rows = %d/%#v, want 1/2", files, rows)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row.Metric] = row.Value.Text
	}
	want := map[string]string{"pod_ready_throughput": "0", "pod_ready_missing_count": "20"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readiness metrics = %#v, want %#v", got, want)
	}
}

func TestReadKubeBurnerMetricsRejectsOfferedLoadMetricWithoutJobName(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "jobSummary.json", `[
  {"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"startup-load-kata","jobIterations":20,"qps":5,"burst":1}}
]`)
	writeKubeBurnerMetric(t, runDir, "metric.json", `[
  {"metricName":"declared","uuid":"run-1","value":1,"timestamp":"2026-07-15T00:00:00Z"}
]`)

	_, _, err := readKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, []string{"declared"}, map[string]string{"declared": "count"}, true)
	if err == nil || !strings.Contains(err.Error(), "has no jobName") {
		t.Fatalf("error = %v, want missing jobName failure", err)
	}
}

func TestReadKubeBurnerMetricsLeavesSerializedRowsUnchanged(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "jobSummary.json", `[
  {"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"startup-smoke","jobIterations":1,"qps":5,"burst":5}}
]`)
	writeKubeBurnerMetric(t, runDir, "podLatencyMeasurement.json", `[
  {"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"startup-smoke","namespace":"kata-perf-0","podName":"kata-perf-0-1","timestamp":"2026-07-15T00:00:00Z","podReadyLatency":1000,"readyToStartContainersLatency":500,"containersStartedLatency":700}
]`)

	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 6 {
		t.Fatalf("files/rows = %d/%#v, want 1/6", files, rows)
	}
	for _, row := range rows {
		if row.Source == "derived/pod-ready" || row.Dimensions["offeredQPS"] != "" || row.Dimensions["burst"] != "" {
			t.Fatalf("serialized row was enriched with load dimensions: %#v", row)
		}
	}
}

func TestReadKubeBurnerMetricsRejectsInvalidOfferedLoadReadiness(t *testing.T) {
	for _, tc := range []struct {
		name      string
		summaries string
		pods      string
		want      string
	}{
		{
			name:      "missing summaries",
			summaries: `[]`,
			want:      "requires jobSummary",
		},
		{
			name:      "duplicate summary",
			summaries: `[{"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"job","jobIterations":1,"qps":1,"burst":1}},{"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"job","jobIterations":1,"qps":1,"burst":1}}]`,
			want:      "duplicate jobSummary",
		},
		{
			name:      "mixed run UUIDs",
			summaries: `[{"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"job-1","jobIterations":1,"qps":1,"burst":1}},{"metricName":"jobSummary","uuid":"run-2","jobConfig":{"name":"job-2","jobIterations":1,"qps":1,"burst":1}}]`,
			want:      "multiple kube-burner UUIDs",
		},
		{
			name:      "mixed metric UUID",
			summaries: `[{"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"job","jobIterations":1,"qps":1,"burst":1}}]`,
			pods:      `[{"metricName":"podLatencyQuantilesMeasurement","uuid":"run-2","jobName":"job","quantileName":"Ready","P50":1,"P95":1,"P99":1,"max":1,"avg":1}]`,
			want:      "multiple kube-burner UUIDs",
		},
		{
			name:      "latency without summary",
			summaries: `[{"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"other","jobIterations":1,"qps":1,"burst":1}}]`,
			pods:      `[{"metricName":"podLatencyQuantilesMeasurement","uuid":"run-1","jobName":"job","quantileName":"Ready","P50":1,"P95":1,"P99":1,"max":1,"avg":1}]`,
			want:      "has no matching jobSummary",
		},
		{
			name:      "pod without summary",
			summaries: `[{"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"other","jobIterations":1,"qps":1,"burst":1}}]`,
			pods:      `[{"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"job","namespace":"ns","podName":"pod","timestamp":"2026-07-15T00:00:00Z","podReadyLatency":1000,"readyToStartContainersLatency":500,"containersStartedLatency":700}]`,
			want:      "has no matching jobSummary",
		},
		{
			name:      "too many ready pods",
			summaries: `[{"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"job","jobIterations":1,"qps":1,"burst":1}}]`,
			pods:      `[{"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"job","namespace":"ns","podName":"pod-1","timestamp":"2026-07-15T00:00:00Z","podReadyLatency":1000,"readyToStartContainersLatency":500,"containersStartedLatency":700},{"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"job","namespace":"ns","podName":"pod-2","timestamp":"2026-07-15T00:00:01Z","podReadyLatency":1000,"readyToStartContainersLatency":500,"containersStartedLatency":700}]`,
			want:      "exceeds expected pod count",
		},
		{
			name:      "duplicate pod",
			summaries: `[{"metricName":"jobSummary","uuid":"run-1","jobConfig":{"name":"job","jobIterations":2,"qps":1,"burst":1}}]`,
			pods:      `[{"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"job","namespace":"ns","podName":"pod","timestamp":"2026-07-15T00:00:00Z","podReadyLatency":1000,"readyToStartContainersLatency":500,"containersStartedLatency":700},{"metricName":"podLatencyMeasurement","uuid":"run-1","jobName":"job","namespace":"ns","podName":"pod","timestamp":"2026-07-15T00:00:01Z","podReadyLatency":1000,"readyToStartContainersLatency":500,"containersStartedLatency":700}]`,
			want:      "duplicate podLatencyMeasurement",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			writeKubeBurnerMetric(t, runDir, "jobSummary.json", tc.summaries)
			if tc.pods != "" {
				writeKubeBurnerMetric(t, runDir, "podLatencyMeasurement.json", tc.pods)
			}
			_, _, err := readKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadKubeBurnerMetricsRejectsInvalidPostSandboxSamples(t *testing.T) {
	tests := []struct {
		name     string
		document string
		want     string
	}{
		{name: "missing started", document: `{"metricName":"podLatencyMeasurement","jobName":"job","readyToStartContainersLatency":10}`, want: "containersStartedLatency field is required"},
		{name: "nonnumeric ready", document: `{"metricName":"podLatencyMeasurement","jobName":"job","readyToStartContainersLatency":"bad","containersStartedLatency":20}`, want: "readyToStartContainersLatency must be a JSON number"},
		{name: "zero started", document: `{"metricName":"podLatencyMeasurement","jobName":"job","readyToStartContainersLatency":0,"containersStartedLatency":0}`, want: "containersStartedLatency must be greater than zero"},
		{name: "negative difference", document: `{"metricName":"podLatencyMeasurement","jobName":"job","readyToStartContainersLatency":20,"containersStartedLatency":10}`, want: "post-sandbox container launch latency must not be negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			writeKubeBurnerMetric(t, runDir, "podLatencyMeasurement.json", "["+tc.document+"]")
			_, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadKubeBurnerMetricsExcludesAmbiguousZeroSandboxReadySample(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "podLatencyMeasurement.json", `[
  {"metricName":"podLatencyMeasurement","jobName":"job","readyToStartContainersLatency":0,"containersStartedLatency":10},
  {"metricName":"podLatencyMeasurement","jobName":"job","readyToStartContainersLatency":100,"containersStartedLatency":120}
]`)
	rows, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Metric == "post_sandbox_container_launch_latency_sample_count" && row.Value.Text != "1" {
			t.Fatalf("sample count = %s, want 1", row.Value.Text)
		}
	}
}

func TestReadKubeBurnerMetricsReportsZeroValidPostSandboxSamples(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "podLatencyMeasurement.json", `[
  {"metricName":"podLatencyMeasurement","jobName":"job","readyToStartContainersLatency":0,"containersStartedLatency":10}
]`)
	rows, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Metric != "post_sandbox_container_launch_latency_sample_count" || rows[0].Value.Text != "0" {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestReadKubeBurnerMetricsReadsPinnedPodLatencyFixture(t *testing.T) {
	runDir := copyKubeBurnerFixtures(t, "podLatencyMeasurement.json")
	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 6 {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
}

func TestReadKubeBurnerMetricsIgnoresUnsupportedDocuments(t *testing.T) {
	runDir := copyKubeBurnerFixtures(t, "jobSummary.json")
	writeKubeBurnerMetric(t, runDir, "unknown.json", `[{"metricName":"notDeclared","value":1,"timestamp":"2026-07-11T00:00:00Z","labels":{}}]`)
	writeKubeBurnerMetric(t, runDir, "unit-only.json", `[{"metricName":"unitOnly","value":1,"timestamp":"2026-07-11T00:00:00Z","labels":{}}]`)

	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, map[string]string{"unitOnly": "count"})
	if err != nil {
		t.Fatal(err)
	}
	if files != 0 || len(rows) != 0 {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
}

func TestReadKubeBurnerMetricsAcceptsEmptyDocumentArray(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "empty.json", `[]`)

	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files != 0 || len(rows) != 0 {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
}

func TestReadKubeBurnerMetricsRejectsNonArrayTopLevelValues(t *testing.T) {
	for _, document := range []string{`null`, `{}`, `"document"`, `1`, `true`} {
		t.Run(document, func(t *testing.T) {
			runDir := t.TempDir()
			writeKubeBurnerMetric(t, runDir, "invalid.json", document)

			_, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "invalid JSON document array") {
				t.Fatalf("ReadKubeBurnerMetrics() error = %v, want top-level array error", err)
			}
		})
	}
}

func TestReadKubeBurnerMetricsCountsMixedFileOnce(t *testing.T) {
	runDir := t.TempDir()
	writeKubeBurnerMetric(t, runDir, "mixed.json", `[
		{"metricName":"notDeclared","value":1},
		{"metricName":"declared","value":2,"timestamp":"2026-07-11T00:00:00Z"}
	]`)

	rows, files, err := ReadKubeBurnerMetrics(
		filepath.Join(runDir, "raw", "metrics"),
		runDir,
		[]string{"declared"},
		map[string]string{"declared": "count"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || len(rows) != 1 {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
}

func TestReadKubeBurnerMetricsRejectsMalformedAcceptedDocuments(t *testing.T) {
	tests := []struct {
		name     string
		document string
		field    string
	}{
		{name: "missing quantile", document: `[{"metricName":"podLatencyQuantilesMeasurement","P50":1,"P95":2,"max":4,"avg":3,"jobName":"job","quantileName":"Ready"}]`, field: "P99"},
		{name: "malformed value", document: `[{"metricName":"declared","value":"1","timestamp":"2026-07-11T00:00:00Z","labels":{}}]`, field: "value"},
		{name: "malformed labels", document: `[{"metricName":"declared","value":1,"timestamp":"2026-07-11T00:00:00Z","labels":{"pod":1}}]`, field: "labels"},
		{name: "null labels", document: `[{"metricName":"declared","value":1,"timestamp":"2026-07-11T00:00:00Z","labels":null}]`, field: "labels"},
		{name: "array labels", document: `[{"metricName":"declared","value":1,"timestamp":"2026-07-11T00:00:00Z","labels":[]}]`, field: "labels"},
		{name: "scalar labels", document: `[{"metricName":"declared","value":1,"timestamp":"2026-07-11T00:00:00Z","labels":"pod=demo"}]`, field: "labels"},
		{name: "malformed timestamp", document: `[{"metricName":"declared","value":1,"timestamp":"not-a-time","labels":{}}]`, field: "timestamp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runDir := t.TempDir()
			writeKubeBurnerMetric(t, runDir, "invalid.json", tt.document)
			_, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, []string{"declared"}, map[string]string{"declared": "count"})
			if err == nil || !strings.Contains(err.Error(), "raw/metrics/invalid.json") || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("ReadKubeBurnerMetrics() error = %v, want source and %q", err, tt.field)
			}
		})
	}
}

func TestReadKubeBurnerMetricsRequiresStringMetricName(t *testing.T) {
	for _, document := range []string{`[{"value":1}]`, `[{"metricName":1}]`} {
		runDir := t.TempDir()
		writeKubeBurnerMetric(t, runDir, "invalid.json", document)
		_, _, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "metricName") {
			t.Fatalf("ReadKubeBurnerMetrics() error = %v", err)
		}
	}
}

func TestReadKubeBurnerMetricsMissingDirectory(t *testing.T) {
	runDir := t.TempDir()
	rows, files, err := ReadKubeBurnerMetrics(filepath.Join(runDir, "raw", "metrics"), runDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if files != 0 || !reflect.DeepEqual(rows, []Row(nil)) {
		t.Fatalf("files/rows = %d/%#v", files, rows)
	}
}

func copyKubeBurnerFixtures(t *testing.T, names ...string) string {
	t.Helper()
	runDir := t.TempDir()
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join("testdata", "kube-burner-2.7.3", name))
		if err != nil {
			t.Fatal(err)
		}
		writeKubeBurnerMetric(t, runDir, name, string(data))
	}
	return runDir
}

func writeKubeBurnerMetric(t *testing.T, runDir, name, document string) {
	t.Helper()
	metricsDir := filepath.Join(runDir, "raw", "metrics")
	if err := os.MkdirAll(metricsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metricsDir, name), []byte(document), 0o644); err != nil {
		t.Fatal(err)
	}
}
