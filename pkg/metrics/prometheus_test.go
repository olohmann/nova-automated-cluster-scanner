package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics("http://localhost:9091", "test-job")

	if m == nil {
		t.Fatal("expected non-nil Metrics")
	}
	if m.pushURL != "http://localhost:9091" {
		t.Errorf("expected pushURL 'http://localhost:9091', got %q", m.pushURL)
	}
	if m.jobName != "test-job" {
		t.Errorf("expected jobName 'test-job', got %q", m.jobName)
	}
	if m.registry == nil {
		t.Error("expected non-nil registry")
	}
}

func TestMetrics_RecordHelmScan(t *testing.T) {
	m := NewMetrics("", "test")

	m.RecordHelmScan(5, 10*time.Second)

	// Check outdated count
	val := getGaugeValue(t, m.OutdatedHelmChartsTotal)
	if val != 5 {
		t.Errorf("expected OutdatedHelmChartsTotal to be 5, got %f", val)
	}

	// Check that last success timestamp was set
	ts := getGaugeValue(t, m.ScanLastSuccessTimestamp)
	if ts <= 0 {
		t.Error("expected ScanLastSuccessTimestamp to be set")
	}
}

func TestMetrics_RecordContainerScan(t *testing.T) {
	m := NewMetrics("", "test")

	m.RecordContainerScan(3, 5*time.Second)

	val := getGaugeValue(t, m.OutdatedContainersTotal)
	if val != 3 {
		t.Errorf("expected OutdatedContainersTotal to be 3, got %f", val)
	}
}

func TestMetrics_RecordHelmChartInfo(t *testing.T) {
	m := NewMetrics("", "test")

	m.RecordHelmChartInfo("my-release", "default", "my-chart", "1.0.0", "2.0.0", false)
	m.RecordHelmChartInfo("deprecated-release", "kube-system", "old-chart", "0.1.0", "1.0.0", true)

	// Collect metrics
	ch := make(chan prometheus.Metric, 10)
	m.HelmChartVersionInfo.Collect(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}

	if count != 2 {
		t.Errorf("expected 2 helm chart info metrics, got %d", count)
	}
}

func TestMetrics_RecordContainerInfo(t *testing.T) {
	m := NewMetrics("", "test")

	m.RecordContainerInfo("nginx", "1.20", "1.25")
	m.RecordContainerInfo("redis", "6.0", "7.0")

	ch := make(chan prometheus.Metric, 10)
	m.ContainerVersionInfo.Collect(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}

	if count != 2 {
		t.Errorf("expected 2 container info metrics, got %d", count)
	}
}

func TestMetrics_RecordIssueCreated(t *testing.T) {
	m := NewMetrics("", "test")

	m.RecordIssueCreated("helm")
	m.RecordIssueCreated("helm")
	m.RecordIssueCreated("container")

	// Check helm counter
	helmVal := getCounterValue(t, m.IssuesCreatedTotal, "helm")
	if helmVal != 2 {
		t.Errorf("expected helm issues count to be 2, got %f", helmVal)
	}

	// Check container counter
	containerVal := getCounterValue(t, m.IssuesCreatedTotal, "container")
	if containerVal != 1 {
		t.Errorf("expected container issues count to be 1, got %f", containerVal)
	}
}

func TestMetrics_RecordError(t *testing.T) {
	m := NewMetrics("", "test")

	m.RecordError()
	m.RecordError()
	m.RecordError()

	val := getCounterValueSimple(t, m.ScanErrorsTotal)
	if val != 3 {
		t.Errorf("expected error count to be 3, got %f", val)
	}
}

func TestMetrics_Reset(t *testing.T) {
	m := NewMetrics("", "test")

	// Add some metrics
	m.RecordHelmChartInfo("release1", "ns1", "chart1", "1.0", "2.0", false)
	m.RecordContainerInfo("image1", "1.0", "2.0")

	// Reset
	m.Reset()

	// Verify reset
	ch := make(chan prometheus.Metric, 10)
	m.HelmChartVersionInfo.Collect(ch)
	close(ch)

	count := 0
	for range ch {
		count++
	}

	if count != 0 {
		t.Errorf("expected 0 helm chart info metrics after reset, got %d", count)
	}
}

func TestMetrics_Push_NoURL(t *testing.T) {
	m := NewMetrics("", "test")

	// Should not error when pushURL is empty
	err := m.Push()
	if err != nil {
		t.Errorf("expected no error when pushURL is empty, got %v", err)
	}
}

// Helper functions

func getGaugeValue(t *testing.T, gauge prometheus.Gauge) float64 {
	t.Helper()

	ch := make(chan prometheus.Metric, 1)
	gauge.Collect(ch)
	close(ch)

	metric := <-ch
	var m dto.Metric
	if err := metric.Write(&m); err != nil {
		t.Fatalf("failed to write metric: %v", err)
	}

	return m.GetGauge().GetValue()
}

func getCounterValue(t *testing.T, counterVec *prometheus.CounterVec, labelValue string) float64 {
	t.Helper()

	counter, err := counterVec.GetMetricWithLabelValues(labelValue)
	if err != nil {
		t.Fatalf("failed to get counter: %v", err)
	}

	ch := make(chan prometheus.Metric, 1)
	counter.Collect(ch)
	close(ch)

	metric := <-ch
	var m dto.Metric
	if err := metric.Write(&m); err != nil {
		t.Fatalf("failed to write metric: %v", err)
	}

	return m.GetCounter().GetValue()
}

func getCounterValueSimple(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()

	ch := make(chan prometheus.Metric, 1)
	counter.Collect(ch)
	close(ch)

	metric := <-ch
	var m dto.Metric
	if err := metric.Write(&m); err != nil {
		t.Fatalf("failed to write metric: %v", err)
	}

	return m.GetCounter().GetValue()
}
