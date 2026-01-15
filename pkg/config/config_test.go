package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	// Set required env vars
	os.Setenv("GITHUB_TOKEN", "test-token")
	os.Setenv("GITHUB_OWNER", "test-owner")
	os.Setenv("GITHUB_REPO", "test-repo")
	defer func() {
		os.Unsetenv("GITHUB_TOKEN")
		os.Unsetenv("GITHUB_OWNER")
		os.Unsetenv("GITHUB_REPO")
	}()

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check defaults
	if !cfg.ScanHelm {
		t.Error("expected ScanHelm to default to true")
	}
	if cfg.ScanContainers {
		t.Error("expected ScanContainers to default to false")
	}
	if cfg.MinSeverity != "minor" {
		t.Errorf("expected MinSeverity to be 'minor', got %q", cfg.MinSeverity)
	}
	if !cfg.PollArtifactHub {
		t.Error("expected PollArtifactHub to default to true")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("expected LogLevel to be 'info', got %q", cfg.LogLevel)
	}
	if cfg.JobName != "nova-scanner" {
		t.Errorf("expected JobName to be 'nova-scanner', got %q", cfg.JobName)
	}
}

func TestLoad_FromFile(t *testing.T) {
	// Create temp config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
scanHelm: false
scanContainers: true
minSeverity: major
logLevel: debug
ignoreReleases:
  - release1
  - release2
ignoreCharts:
  - chart1
githubToken: file-token
githubOwner: file-owner
githubRepo: file-repo
pushgatewayUrl: http://localhost:9091
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ScanHelm {
		t.Error("expected ScanHelm to be false")
	}
	if !cfg.ScanContainers {
		t.Error("expected ScanContainers to be true")
	}
	if cfg.MinSeverity != "major" {
		t.Errorf("expected MinSeverity to be 'major', got %q", cfg.MinSeverity)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected LogLevel to be 'debug', got %q", cfg.LogLevel)
	}
	if len(cfg.IgnoreReleases) != 2 {
		t.Errorf("expected 2 ignoreReleases, got %d", len(cfg.IgnoreReleases))
	}
	if len(cfg.IgnoreCharts) != 1 {
		t.Errorf("expected 1 ignoreCharts, got %d", len(cfg.IgnoreCharts))
	}
	if cfg.PushgatewayURL != "http://localhost:9091" {
		t.Errorf("expected PushgatewayURL to be 'http://localhost:9091', got %q", cfg.PushgatewayURL)
	}
}

func TestLoad_EnvOverrides(t *testing.T) {
	// Create temp config file with base values
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
githubToken: file-token
githubOwner: file-owner
githubRepo: file-repo
logLevel: info
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Set env vars to override
	os.Setenv("GITHUB_TOKEN", "env-token")
	os.Setenv("LOG_LEVEL", "debug")
	os.Setenv("DRY_RUN", "true")
	defer func() {
		os.Unsetenv("GITHUB_TOKEN")
		os.Unsetenv("LOG_LEVEL")
		os.Unsetenv("DRY_RUN")
	}()

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Env should override file
	if cfg.GitHubToken != "env-token" {
		t.Errorf("expected GitHubToken to be 'env-token', got %q", cfg.GitHubToken)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected LogLevel to be 'debug', got %q", cfg.LogLevel)
	}
	if !cfg.DryRun {
		t.Error("expected DryRun to be true")
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		envVars map[string]string
		wantErr string
	}{
		{
			name:    "missing github token",
			envVars: map[string]string{"GITHUB_OWNER": "owner", "GITHUB_REPO": "repo"},
			wantErr: "github token is required",
		},
		{
			name:    "missing github owner",
			envVars: map[string]string{"GITHUB_TOKEN": "token", "GITHUB_REPO": "repo"},
			wantErr: "github owner is required",
		},
		{
			name:    "missing github repo",
			envVars: map[string]string{"GITHUB_TOKEN": "token", "GITHUB_OWNER": "owner"},
			wantErr: "github repo is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear env vars
			os.Unsetenv("GITHUB_TOKEN")
			os.Unsetenv("GITHUB_OWNER")
			os.Unsetenv("GITHUB_REPO")

			// Set test env vars
			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}
			defer func() {
				for k := range tt.envVars {
					os.Unsetenv(k)
				}
			}()

			_, err := Load("")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestLoad_InvalidSeverity(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
githubToken: token
githubOwner: owner
githubRepo: repo
minSeverity: invalid
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for invalid severity")
	}
	if !contains(err.Error(), "invalid minSeverity") {
		t.Errorf("expected error about invalid minSeverity, got %q", err.Error())
	}
}

func TestSeverityLevel(t *testing.T) {
	tests := []struct {
		severity string
		want     int
	}{
		{"minor", 1},
		{"major", 2},
		{"critical", 3},
		{"unknown", 1}, // defaults to minor
	}

	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			cfg := &Config{MinSeverity: tt.severity}
			got := cfg.SeverityLevel()
			if got != tt.want {
				t.Errorf("SeverityLevel() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestIsMarkdownMode(t *testing.T) {
	tests := []struct {
		outputMode string
		want       bool
	}{
		{"markdown", true},
		{"github", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.outputMode, func(t *testing.T) {
			cfg := &Config{OutputMode: tt.outputMode}
			got := cfg.IsMarkdownMode()
			if got != tt.want {
				t.Errorf("IsMarkdownMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoad_MarkdownMode_NoGitHubCredentials(t *testing.T) {
	// In markdown mode, GitHub credentials should not be required
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
outputMode: markdown
markdownOutput: /tmp/issues.md
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	// Clear any GitHub env vars
	os.Unsetenv("GITHUB_TOKEN")
	os.Unsetenv("GITHUB_OWNER")
	os.Unsetenv("GITHUB_REPO")

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("expected no error in markdown mode without GitHub credentials, got: %v", err)
	}

	if !cfg.IsMarkdownMode() {
		t.Error("expected IsMarkdownMode() to be true")
	}
	if cfg.MarkdownOutput != "/tmp/issues.md" {
		t.Errorf("expected MarkdownOutput to be '/tmp/issues.md', got %q", cfg.MarkdownOutput)
	}
}

func TestLoad_InvalidOutputMode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
outputMode: invalid
githubToken: token
githubOwner: owner
githubRepo: repo
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for invalid output mode")
	}
	if !contains(err.Error(), "invalid outputMode") {
		t.Errorf("expected error about invalid outputMode, got %q", err.Error())
	}
}

func TestShouldIgnoreVersion(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		version  string
		want     bool
	}{
		{
			name:     "no patterns",
			patterns: nil,
			version:  "9.2.0-develop.18",
			want:     false,
		},
		{
			name:     "matches develop pattern",
			patterns: []string{"-develop"},
			version:  "9.2.0-develop.18",
			want:     true,
		},
		{
			name:     "matches rc pattern",
			patterns: []string{"-rc", "-alpha", "-beta"},
			version:  "1.0.0-rc1",
			want:     true,
		},
		{
			name:     "matches alpha pattern",
			patterns: []string{"-rc", "-alpha", "-beta"},
			version:  "2.0.0-alpha.5",
			want:     true,
		},
		{
			name:     "matches beta pattern",
			patterns: []string{"-rc", "-alpha", "-beta"},
			version:  "3.0.0-beta",
			want:     true,
		},
		{
			name:     "does not match stable version",
			patterns: []string{"-develop", "-rc", "-alpha", "-beta"},
			version:  "1.2.3",
			want:     false,
		},
		{
			name:     "does not match different prerelease",
			patterns: []string{"-develop"},
			version:  "1.0.0-rc1",
			want:     false,
		},
		{
			name:     "matches snapshot pattern",
			patterns: []string{"-SNAPSHOT"},
			version:  "1.0.0-SNAPSHOT",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{IgnoreVersionPatterns: tt.patterns}
			got := cfg.ShouldIgnoreVersion(tt.version)
			if got != tt.want {
				t.Errorf("ShouldIgnoreVersion(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestShouldIgnoreChartVersion(t *testing.T) {
	tests := []struct {
		name                       string
		globalPatterns             []string
		chartVersionIgnorePatterns map[string][]string
		chartName                  string
		version                    string
		want                       bool
	}{
		{
			name:                       "no patterns at all",
			globalPatterns:             nil,
			chartVersionIgnorePatterns: nil,
			chartName:                  "gateway-helm",
			version:                    "2023.9.18",
			want:                       false,
		},
		{
			name:                       "matches global pattern",
			globalPatterns:             []string{"-rc", "-alpha"},
			chartVersionIgnorePatterns: nil,
			chartName:                  "some-chart",
			version:                    "1.0.0-rc1",
			want:                       true,
		},
		{
			name:           "matches chart-specific pattern",
			globalPatterns: nil,
			chartVersionIgnorePatterns: map[string][]string{
				"gateway-helm": {"2023.", "2024."},
			},
			chartName: "gateway-helm",
			version:   "2023.9.18",
			want:      true,
		},
		{
			name:           "does not match other chart's pattern",
			globalPatterns: nil,
			chartVersionIgnorePatterns: map[string][]string{
				"gateway-helm": {"2023.", "2024."},
			},
			chartName: "other-chart",
			version:   "2023.9.18",
			want:      false,
		},
		{
			name:           "stable version passes chart-specific filter",
			globalPatterns: nil,
			chartVersionIgnorePatterns: map[string][]string{
				"gateway-helm": {"2023.", "2024."},
			},
			chartName: "gateway-helm",
			version:   "1.6.1",
			want:      false,
		},
		{
			name:           "global pattern takes precedence",
			globalPatterns: []string{"-develop"},
			chartVersionIgnorePatterns: map[string][]string{
				"some-chart": {"2023."},
			},
			chartName: "other-chart",
			version:   "1.0.0-develop.5",
			want:      true,
		},
		{
			name:           "both global and chart-specific checked",
			globalPatterns: []string{"-alpha"},
			chartVersionIgnorePatterns: map[string][]string{
				"gateway-helm": {"2023."},
			},
			chartName: "gateway-helm",
			version:   "2023.9.18",
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				IgnoreVersionPatterns:      tt.globalPatterns,
				ChartVersionIgnorePatterns: tt.chartVersionIgnorePatterns,
			}
			got := cfg.ShouldIgnoreChartVersion(tt.chartName, tt.version)
			if got != tt.want {
				t.Errorf("ShouldIgnoreChartVersion(%q, %q) = %v, want %v", tt.chartName, tt.version, got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
