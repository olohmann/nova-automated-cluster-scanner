package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the nova-scanner.
type Config struct {
	// Kubernetes
	Kubeconfig string   `yaml:"kubeconfig"`
	Context    string   `yaml:"context"`
	Namespaces []string `yaml:"namespaces"` // empty = all namespaces

	// Scanning
	ScanHelm              bool     `yaml:"scanHelm"`
	ScanContainers        bool     `yaml:"scanContainers"`
	IgnoreReleases        []string `yaml:"ignoreReleases"`
	IgnoreCharts          []string `yaml:"ignoreCharts"`
	IgnoreImages          []string `yaml:"ignoreImages"`
	IgnoreVersionPatterns []string `yaml:"ignoreVersionPatterns"` // Patterns to blacklist in target versions (e.g., "-develop", "-rc", "-alpha")

	// Severity filtering: minor, major, critical
	MinSeverity string `yaml:"minSeverity"`

	// GitHub
	GitHubToken string `yaml:"githubToken"`
	GitHubOwner string `yaml:"githubOwner"`
	GitHubRepo  string `yaml:"githubRepo"`
	DryRun      bool   `yaml:"dryRun"`

	// Output mode: "github" or "markdown"
	OutputMode     string `yaml:"outputMode"`
	MarkdownOutput string `yaml:"markdownOutput"` // file path, empty = stdout

	// Metrics
	PushgatewayURL string `yaml:"pushgatewayUrl"`
	JobName        string `yaml:"jobName"`

	// Logging
	LogLevel string `yaml:"logLevel"`

	// Nova options
	DesiredVersions map[string]string `yaml:"desiredVersions"`
	PollArtifactHub bool              `yaml:"pollArtifactHub"`
}

// IsMarkdownMode returns true if output mode is markdown.
func (c *Config) IsMarkdownMode() bool {
	return c.OutputMode == "markdown"
}

// Load reads configuration from a YAML file and applies environment variable overrides.
func Load(path string) (*Config, error) {
	cfg := &Config{
		// Defaults
		ScanHelm:        true,
		ScanContainers:  false,
		MinSeverity:     "minor",
		PollArtifactHub: true,
		LogLevel:        "info",
		JobName:         "nova-scanner",
		OutputMode:      "github",
	}

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	// Apply environment variable overrides
	cfg.applyEnvOverrides()

	// Validate required fields
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("KUBECONFIG"); v != "" {
		c.Kubeconfig = v
	}
	if v := os.Getenv("KUBE_CONTEXT"); v != "" {
		c.Context = v
	}
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		c.GitHubToken = v
	}
	if v := os.Getenv("GITHUB_OWNER"); v != "" {
		c.GitHubOwner = v
	}
	if v := os.Getenv("GITHUB_REPO"); v != "" {
		c.GitHubRepo = v
	}
	if v := os.Getenv("PUSHGATEWAY_URL"); v != "" {
		c.PushgatewayURL = v
	}
	if v := os.Getenv("JOB_NAME"); v != "" {
		c.JobName = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("DRY_RUN"); v != "" {
		c.DryRun = strings.ToLower(v) == "true" || v == "1"
	}
	if v := os.Getenv("SCAN_HELM"); v != "" {
		c.ScanHelm = strings.ToLower(v) == "true" || v == "1"
	}
	if v := os.Getenv("SCAN_CONTAINERS"); v != "" {
		c.ScanContainers = strings.ToLower(v) == "true" || v == "1"
	}
	if v := os.Getenv("MIN_SEVERITY"); v != "" {
		c.MinSeverity = v
	}
	if v := os.Getenv("OUTPUT_MODE"); v != "" {
		c.OutputMode = v
	}
	if v := os.Getenv("MARKDOWN_OUTPUT"); v != "" {
		c.MarkdownOutput = v
	}
}

func (c *Config) validate() error {
	// GitHub credentials only required in github output mode
	if !c.IsMarkdownMode() {
		if c.GitHubToken == "" {
			return fmt.Errorf("github token is required (set GITHUB_TOKEN or githubToken in config)")
		}
		if c.GitHubOwner == "" {
			return fmt.Errorf("github owner is required (set GITHUB_OWNER or githubOwner in config)")
		}
		if c.GitHubRepo == "" {
			return fmt.Errorf("github repo is required (set GITHUB_REPO or githubRepo in config)")
		}
	}

	validSeverities := map[string]bool{"minor": true, "major": true, "critical": true}
	if !validSeverities[c.MinSeverity] {
		return fmt.Errorf("invalid minSeverity: %s (must be minor, major, or critical)", c.MinSeverity)
	}

	validOutputModes := map[string]bool{"github": true, "markdown": true}
	if !validOutputModes[c.OutputMode] {
		return fmt.Errorf("invalid outputMode: %s (must be github or markdown)", c.OutputMode)
	}

	return nil
}

// SeverityLevel returns a numeric value for the severity level for comparison.
// higher value = more severe
func (c *Config) SeverityLevel() int {
	switch c.MinSeverity {
	case "critical":
		return 3
	case "major":
		return 2
	default:
		return 1 // minor
	}
}

// ShouldIgnoreVersion returns true if the version matches any of the blacklist patterns.
// Patterns are matched as substrings (e.g., "-develop" matches "9.2.0-develop.18").
func (c *Config) ShouldIgnoreVersion(version string) bool {
	for _, pattern := range c.IgnoreVersionPatterns {
		if strings.Contains(version, pattern) {
			return true
		}
	}
	return false
}
