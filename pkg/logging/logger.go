package logging

import (
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Logger wraps zerolog with trace ID support and structured logging helpers.
type Logger struct {
	zerolog.Logger
	traceID string
}

// NewLogger creates a new structured logger with the specified level.
func NewLogger(level string) *Logger {
	zerolog.TimeFieldFormat = time.RFC3339

	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}

	traceID := uuid.New().String()[:8]

	logger := zerolog.New(os.Stdout).
		Level(lvl).
		With().
		Timestamp().
		Str("trace_id", traceID).
		Logger()

	return &Logger{
		Logger:  logger,
		traceID: traceID,
	}
}

// TraceID returns the current trace ID.
func (l *Logger) TraceID() string {
	return l.traceID
}

// WithComponent returns a new logger with the component field set.
func (l *Logger) WithComponent(component string) *Logger {
	return &Logger{
		Logger:  l.With().Str("component", component).Logger(),
		traceID: l.traceID,
	}
}

// ScanStart logs the start of a scan operation.
func (l *Logger) ScanStart(scanType string) {
	l.Info().
		Str("event", "scan_start").
		Str("scan_type", scanType).
		Msg("Starting scan")
}

// ScanEnd logs the end of a scan operation with duration and results.
func (l *Logger) ScanEnd(scanType string, duration time.Duration, total, outdated int) {
	l.Info().
		Str("event", "scan_end").
		Str("scan_type", scanType).
		Dur("duration", duration).
		Int("total_found", total).
		Int("outdated_found", outdated).
		Msg("Scan completed")
}

// OutdatedFound logs when an outdated component is detected.
func (l *Logger) OutdatedFound(componentType, name, namespace, currentVersion, latestVersion string) {
	l.Warn().
		Str("event", "outdated_found").
		Str("component_type", componentType).
		Str("name", name).
		Str("namespace", namespace).
		Str("current_version", currentVersion).
		Str("latest_version", latestVersion).
		Msg("Outdated component detected")
}

// IssueCreated logs when a GitHub issue is created.
func (l *Logger) IssueCreated(issueType, title, url string) {
	l.Info().
		Str("event", "issue_created").
		Str("issue_type", issueType).
		Str("title", title).
		Str("url", url).
		Msg("GitHub issue created")
}

// IssueSkipped logs when a GitHub issue is skipped (e.g., duplicate).
func (l *Logger) IssueSkipped(issueType, title, reason string) {
	l.Debug().
		Str("event", "issue_skipped").
		Str("issue_type", issueType).
		Str("title", title).
		Str("reason", reason).
		Msg("GitHub issue skipped")
}

// IssueDryRun logs when an issue would be created in dry-run mode.
func (l *Logger) IssueDryRun(issueType, title string) {
	l.Info().
		Str("event", "issue_dry_run").
		Str("issue_type", issueType).
		Str("title", title).
		Msg("Would create GitHub issue (dry-run mode)")
}

// MetricsPushed logs when metrics are pushed to the pushgateway.
func (l *Logger) MetricsPushed(url string) {
	l.Info().
		Str("event", "metrics_pushed").
		Str("pushgateway_url", url).
		Msg("Metrics pushed to Pushgateway")
}

// ScanError logs a scan error.
func (l *Logger) ScanError(scanType string, err error) {
	l.Error().
		Str("event", "scan_error").
		Str("scan_type", scanType).
		Err(err).
		Msg("Scan failed")
}
