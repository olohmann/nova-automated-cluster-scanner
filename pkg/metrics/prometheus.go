package metrics

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

// Metrics holds all Prometheus metrics for the nova-scanner.
type Metrics struct {
	// Gauges
	OutdatedHelmChartsTotal  prometheus.Gauge
	OutdatedContainersTotal  prometheus.Gauge
	ScanLastSuccessTimestamp prometheus.Gauge

	// Info metrics (GaugeVec set to 1)
	HelmChartVersionInfo *prometheus.GaugeVec
	ContainerVersionInfo *prometheus.GaugeVec

	// Histogram
	ScanDurationSeconds *prometheus.HistogramVec

	// Counters
	IssuesCreatedTotal *prometheus.CounterVec
	ScanErrorsTotal    prometheus.Counter

	registry *prometheus.Registry
	pushURL  string
	jobName  string
}

// NewMetrics creates a new Metrics instance with all metrics registered.
func NewMetrics(pushgatewayURL, jobName string) *Metrics {
	registry := prometheus.NewRegistry()

	m := &Metrics{
		OutdatedHelmChartsTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nova_outdated_helm_charts_total",
			Help: "Total number of outdated Helm releases detected",
		}),
		OutdatedContainersTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nova_outdated_containers_total",
			Help: "Total number of outdated container images detected",
		}),
		ScanLastSuccessTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "nova_scan_last_success_timestamp",
			Help: "Unix timestamp of the last successful scan",
		}),
		HelmChartVersionInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "nova_helm_chart_version_info",
				Help: "Information about Helm chart versions (value is always 1)",
			},
			[]string{"release", "namespace", "chart", "current_version", "latest_version", "deprecated"},
		),
		ContainerVersionInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "nova_container_version_info",
				Help: "Information about container image versions (value is always 1)",
			},
			[]string{"image", "current_tag", "latest_tag"},
		),
		ScanDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "nova_scan_duration_seconds",
				Help:    "Duration of scans in seconds",
				Buckets: prometheus.ExponentialBuckets(1, 2, 8), // 1s to ~4m
			},
			[]string{"type"},
		),
		IssuesCreatedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "nova_issues_created_total",
				Help: "Total number of GitHub issues created",
			},
			[]string{"type"},
		),
		ScanErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "nova_scan_errors_total",
			Help: "Total number of scan errors",
		}),
		registry: registry,
		pushURL:  pushgatewayURL,
		jobName:  jobName,
	}

	// Register all metrics
	registry.MustRegister(
		m.OutdatedHelmChartsTotal,
		m.OutdatedContainersTotal,
		m.ScanLastSuccessTimestamp,
		m.HelmChartVersionInfo,
		m.ContainerVersionInfo,
		m.ScanDurationSeconds,
		m.IssuesCreatedTotal,
		m.ScanErrorsTotal,
	)

	return m
}

// RecordHelmScan records metrics for a completed Helm scan.
func (m *Metrics) RecordHelmScan(outdated int, duration time.Duration) {
	m.OutdatedHelmChartsTotal.Set(float64(outdated))
	m.ScanDurationSeconds.WithLabelValues("helm").Observe(duration.Seconds())
	m.ScanLastSuccessTimestamp.SetToCurrentTime()
}

// RecordContainerScan records metrics for a completed container scan.
func (m *Metrics) RecordContainerScan(outdated int, duration time.Duration) {
	m.OutdatedContainersTotal.Set(float64(outdated))
	m.ScanDurationSeconds.WithLabelValues("container").Observe(duration.Seconds())
	m.ScanLastSuccessTimestamp.SetToCurrentTime()
}

// RecordHelmChartInfo records version info for a Helm release.
func (m *Metrics) RecordHelmChartInfo(release, namespace, chart, currentVersion, latestVersion string, deprecated bool) {
	deprecatedStr := "false"
	if deprecated {
		deprecatedStr = "true"
	}
	m.HelmChartVersionInfo.WithLabelValues(release, namespace, chart, currentVersion, latestVersion, deprecatedStr).Set(1)
}

// RecordContainerInfo records version info for a container image.
func (m *Metrics) RecordContainerInfo(image, currentTag, latestTag string) {
	m.ContainerVersionInfo.WithLabelValues(image, currentTag, latestTag).Set(1)
}

// RecordIssueCreated increments the issues created counter.
func (m *Metrics) RecordIssueCreated(issueType string) {
	m.IssuesCreatedTotal.WithLabelValues(issueType).Inc()
}

// RecordError increments the error counter.
func (m *Metrics) RecordError() {
	m.ScanErrorsTotal.Inc()
}

// Reset clears the version info metrics before a new scan.
func (m *Metrics) Reset() {
	m.HelmChartVersionInfo.Reset()
	m.ContainerVersionInfo.Reset()
}

// Push pushes all metrics to the Pushgateway.
func (m *Metrics) Push() error {
	if m.pushURL == "" {
		return nil
	}

	pusher := push.New(m.pushURL, m.jobName).Gatherer(m.registry)
	if err := pusher.Push(); err != nil {
		return fmt.Errorf("failed to push metrics: %w", err)
	}

	return nil
}
