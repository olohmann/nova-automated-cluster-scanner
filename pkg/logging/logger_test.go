package logging

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestNewLogger(t *testing.T) {
	tests := []struct {
		name  string
		level string
		want  zerolog.Level
	}{
		{"debug level", "debug", zerolog.DebugLevel},
		{"info level", "info", zerolog.InfoLevel},
		{"warn level", "warn", zerolog.WarnLevel},
		{"error level", "error", zerolog.ErrorLevel},
		{"invalid defaults to info", "invalid", zerolog.InfoLevel},
		{"empty defaults to info", "", zerolog.InfoLevel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := NewLogger(tt.level)
			if logger == nil {
				t.Fatal("expected non-nil logger")
			}
			if logger.traceID == "" {
				t.Error("expected non-empty traceID")
			}
			if len(logger.traceID) != 8 {
				t.Errorf("expected traceID length 8, got %d", len(logger.traceID))
			}
		})
	}
}

func TestLogger_TraceID(t *testing.T) {
	logger := NewLogger("info")
	traceID := logger.TraceID()

	if traceID == "" {
		t.Error("expected non-empty trace ID")
	}
	if traceID != logger.traceID {
		t.Error("TraceID() should return internal traceID")
	}
}

func TestLogger_WithComponent(t *testing.T) {
	logger := NewLogger("info")
	componentLogger := logger.WithComponent("test-component")

	if componentLogger == nil {
		t.Fatal("expected non-nil logger")
	}
	if componentLogger.traceID != logger.traceID {
		t.Error("WithComponent should preserve traceID")
	}
}

func TestLogger_OutputFormat(t *testing.T) {
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	logger := NewLogger("info")
	logger.Info().Msg("test message")

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	// Parse as JSON
	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("log output should be valid JSON: %v\nOutput: %s", err, output)
	}

	// Check required fields
	if _, ok := logEntry["time"]; !ok {
		t.Error("log entry should have 'time' field")
	}
	if _, ok := logEntry["level"]; !ok {
		t.Error("log entry should have 'level' field")
	}
	if _, ok := logEntry["trace_id"]; !ok {
		t.Error("log entry should have 'trace_id' field")
	}
	if msg, ok := logEntry["message"]; !ok || msg != "test message" {
		t.Errorf("expected message 'test message', got %v", msg)
	}
}

func TestLogger_ScanStart(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	logger := NewLogger("info")
	logger.ScanStart("helm")

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if logEntry["event"] != "scan_start" {
		t.Errorf("expected event 'scan_start', got %v", logEntry["event"])
	}
	if logEntry["scan_type"] != "helm" {
		t.Errorf("expected scan_type 'helm', got %v", logEntry["scan_type"])
	}
}

func TestLogger_ScanEnd(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	logger := NewLogger("info")
	logger.ScanEnd("container", 5*time.Second, 10, 3)

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if logEntry["event"] != "scan_end" {
		t.Errorf("expected event 'scan_end', got %v", logEntry["event"])
	}
	if logEntry["scan_type"] != "container" {
		t.Errorf("expected scan_type 'container', got %v", logEntry["scan_type"])
	}
	if logEntry["total_found"] != float64(10) {
		t.Errorf("expected total_found 10, got %v", logEntry["total_found"])
	}
	if logEntry["outdated_found"] != float64(3) {
		t.Errorf("expected outdated_found 3, got %v", logEntry["outdated_found"])
	}
}

func TestLogger_OutdatedFound(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	logger := NewLogger("warn") // warn level to capture warn logs
	logger.OutdatedFound("helm", "my-release", "default", "1.0.0", "2.0.0")

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if logEntry["event"] != "outdated_found" {
		t.Errorf("expected event 'outdated_found', got %v", logEntry["event"])
	}
	if logEntry["name"] != "my-release" {
		t.Errorf("expected name 'my-release', got %v", logEntry["name"])
	}
	if logEntry["current_version"] != "1.0.0" {
		t.Errorf("expected current_version '1.0.0', got %v", logEntry["current_version"])
	}
	if logEntry["latest_version"] != "2.0.0" {
		t.Errorf("expected latest_version '2.0.0', got %v", logEntry["latest_version"])
	}
}

func TestLogger_IssueCreated(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	logger := NewLogger("info")
	logger.IssueCreated("helm", "Test Issue", "https://github.com/test/repo/issues/1")

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if logEntry["event"] != "issue_created" {
		t.Errorf("expected event 'issue_created', got %v", logEntry["event"])
	}
	if logEntry["url"] != "https://github.com/test/repo/issues/1" {
		t.Errorf("unexpected url: %v", logEntry["url"])
	}
}

func TestLogger_IssueSkipped(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	logger := NewLogger("debug") // debug level to capture debug logs
	logger.IssueSkipped("helm", "Test Issue", "duplicate")

	w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	var logEntry map[string]interface{}
	if err := json.Unmarshal([]byte(output), &logEntry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if logEntry["event"] != "issue_skipped" {
		t.Errorf("expected event 'issue_skipped', got %v", logEntry["event"])
	}
	if logEntry["reason"] != "duplicate" {
		t.Errorf("expected reason 'duplicate', got %v", logEntry["reason"])
	}
}
