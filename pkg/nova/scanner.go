package nova

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/config"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
)

// Scanner wraps Nova CLI functionality.
type Scanner struct {
	config *config.Config
	logger *logging.Logger
}

// ReleaseOutput represents a Helm release from Nova's output.
type ReleaseOutput struct {
	ReleaseName string      `json:"release"`
	ChartName   string      `json:"chartName"`
	Namespace   string      `json:"namespace"`
	Description string      `json:"description"`
	Home        string      `json:"home"`
	Icon        string      `json:"icon"`
	Installed   VersionInfo `json:"Installed"`
	Latest      VersionInfo `json:"Latest"`
	IsOld       bool        `json:"outdated"`
	Deprecated  bool        `json:"deprecated"`
	HelmVersion string      `json:"helmVersion"`
	Overridden  bool        `json:"overridden"`
}

// VersionInfo holds version details.
type VersionInfo struct {
	Version    string `json:"version"`
	AppVersion string `json:"appVersion"`
}

// ContainerOutput represents a container image from Nova's output.
type ContainerOutput struct {
	Name              string           `json:"name"`
	CurrentTag        string           `json:"current_version"`
	LatestTag         string           `json:"latest_version"`
	IsOld             bool             `json:"outdated"`
	AffectedWorkloads []WorkloadOutput `json:"affectedWorkloads"`
}

// WorkloadOutput represents a Kubernetes workload.
type WorkloadOutput struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Container string `json:"container"`
}

// NovaOutput represents the full Nova JSON output.
type NovaOutput struct {
	HelmReleases []ReleaseOutput   `json:"helm_releases"`
	Containers   []ContainerOutput `json:"container_images"`
}

// HelmScanResult contains the results of a Helm scan.
type HelmScanResult struct {
	AllReleases []ReleaseOutput
	Outdated    []ReleaseOutput
	Duration    time.Duration
}

// OutdatedNamespaces returns a set of namespaces that have outdated Helm releases.
func (r *HelmScanResult) OutdatedNamespaces() map[string]bool {
	namespaces := make(map[string]bool)
	for _, release := range r.Outdated {
		namespaces[release.Namespace] = true
	}
	return namespaces
}

// ContainerScanResult contains the results of a container scan.
type ContainerScanResult struct {
	AllContainers []ContainerOutput
	Outdated      []ContainerOutput
	Skipped       []ContainerOutput // Containers skipped due to Helm deduplication
	Duration      time.Duration
}

// NewScanner creates a new Scanner instance.
func NewScanner(cfg *config.Config, logger *logging.Logger) (*Scanner, error) {
	return &Scanner{
		config: cfg,
		logger: logger.WithComponent("nova"),
	}, nil
}

// ScanHelm scans for outdated Helm releases using Nova CLI.
func (s *Scanner) ScanHelm(ctx context.Context) (*HelmScanResult, error) {
	s.logger.ScanStart("helm")
	start := time.Now()

	// Build Nova command
	args := []string{"find", "--format", "json", "--helm"}

	// Add ArtifactHub polling if enabled
	if s.config.PollArtifactHub {
		args = append(args, "--poll-artifacthub")
	}

	// Add kubeconfig if not running in-cluster
	if kubeconfig := getKubeconfig(s.config.Kubeconfig); kubeconfig != "" {
		args = append(args, "--kubeconfig", kubeconfig)
	}

	// Add context if specified
	if s.config.Context != "" {
		args = append(args, "--context", s.config.Context)
	}

	// Add include-all to get all releases, not just outdated
	args = append(args, "--include-all")

	cmd := exec.CommandContext(ctx, "nova", args...)
	s.logger.Debug().Strs("args", args).Msg("Executing nova command")

	output, err := cmd.Output()
	if err != nil {
		// Try to get stderr for more context
		if exitErr, ok := err.(*exec.ExitError); ok {
			s.logger.Error().
				Str("stderr", string(exitErr.Stderr)).
				Strs("args", args).
				Err(err).
				Msg("Nova command failed")
		}
		s.logger.ScanError("helm", err)
		return nil, fmt.Errorf("nova command failed: %w", err)
	}

	// Parse Nova output
	var novaOutput NovaOutput
	if err := json.Unmarshal(output, &novaOutput); err != nil {
		// Try parsing as array directly (older Nova versions)
		var releases []ReleaseOutput
		if err2 := json.Unmarshal(output, &releases); err2 != nil {
			return nil, fmt.Errorf("failed to parse nova output: %w", err)
		}
		novaOutput.HelmReleases = releases
	}

	// Filter by ignore lists
	var filtered []ReleaseOutput
	for _, release := range novaOutput.HelmReleases {
		if s.shouldIgnoreRelease(release) {
			continue
		}
		filtered = append(filtered, release)
	}

	// Filter outdated releases
	var outdated []ReleaseOutput
	for _, release := range filtered {
		if release.IsOld {
			// Check if latest version matches a blacklisted pattern
			if s.config.ShouldIgnoreVersion(release.Latest.Version) {
				s.logger.Debug().
					Str("release", release.ReleaseName).
					Str("latestVersion", release.Latest.Version).
					Msg("Skipping release: latest version matches blacklist pattern")
				continue
			}

			// Apply severity filtering
			if s.meetsMinSeverity(release.Installed.Version, release.Latest.Version) {
				outdated = append(outdated, release)
				s.logger.OutdatedFound(
					"helm",
					release.ReleaseName,
					release.Namespace,
					release.Installed.Version,
					release.Latest.Version,
				)
			}
		}
	}

	duration := time.Since(start)
	s.logger.ScanEnd("helm", duration, len(filtered), len(outdated))

	return &HelmScanResult{
		AllReleases: filtered,
		Outdated:    outdated,
		Duration:    duration,
	}, nil
}

// ScanContainers scans for outdated container images using Nova CLI.
// skipNamespaces contains namespaces with outdated Helm releases - containers in these
// namespaces will be skipped to avoid duplicate issues (updating the Helm chart will update the containers).
func (s *Scanner) ScanContainers(ctx context.Context, skipNamespaces map[string]bool) (*ContainerScanResult, error) {
	s.logger.ScanStart("container")
	start := time.Now()

	// Build Nova command for container scanning
	args := []string{"find", "--format", "json", "--containers"}

	// Add kubeconfig if not running in-cluster
	if kubeconfig := getKubeconfig(s.config.Kubeconfig); kubeconfig != "" {
		args = append(args, "--kubeconfig", kubeconfig)
	}

	// Add context if specified
	if s.config.Context != "" {
		args = append(args, "--context", s.config.Context)
	}

	cmd := exec.CommandContext(ctx, "nova", args...)

	output, err := cmd.Output()
	if err != nil {
		// Try to get stderr for more context
		if exitErr, ok := err.(*exec.ExitError); ok {
			s.logger.Error().
				Str("stderr", string(exitErr.Stderr)).
				Err(err).
				Msg("Nova command failed")
		}
		s.logger.ScanError("container", err)
		return nil, fmt.Errorf("nova command failed: %w", err)
	}

	// Parse Nova output
	var novaOutput NovaOutput
	if err := json.Unmarshal(output, &novaOutput); err != nil {
		return nil, fmt.Errorf("failed to parse nova output: %w", err)
	}

	// Filter by ignore lists
	var filtered []ContainerOutput
	for _, container := range novaOutput.Containers {
		if s.shouldIgnoreContainer(container) {
			continue
		}
		filtered = append(filtered, container)
	}

	// Filter outdated containers, skipping those in namespaces with outdated Helm releases
	var outdated []ContainerOutput
	var skipped []ContainerOutput
	for _, container := range filtered {
		if container.IsOld {
			// Check if latest version matches a blacklisted pattern
			if s.config.ShouldIgnoreVersion(container.LatestTag) {
				s.logger.Debug().
					Str("image", container.Name).
					Str("latestTag", container.LatestTag).
					Msg("Skipping container: latest version matches blacklist pattern")
				continue
			}

			// Check if all affected workloads are in namespaces with outdated Helm releases
			if s.shouldSkipContainerForHelm(container, skipNamespaces) {
				skipped = append(skipped, container)
				s.logger.Debug().
					Str("image", container.Name).
					Str("reason", "namespace has outdated Helm release").
					Msg("Skipping container (will be updated with Helm chart)")
				continue
			}

			outdated = append(outdated, container)
			s.logger.OutdatedFound(
				"container",
				container.Name,
				"",
				container.CurrentTag,
				container.LatestTag,
			)
		}
	}

	duration := time.Since(start)
	s.logger.ScanEnd("container", duration, len(filtered), len(outdated))

	if len(skipped) > 0 {
		s.logger.Info().
			Int("skipped", len(skipped)).
			Msg("Skipped containers in namespaces with outdated Helm releases")
	}

	return &ContainerScanResult{
		AllContainers: filtered,
		Outdated:      outdated,
		Skipped:       skipped,
		Duration:      duration,
	}, nil
}

// shouldSkipContainerForHelm returns true if all workloads for this container
// are in namespaces that have outdated Helm releases.
func (s *Scanner) shouldSkipContainerForHelm(container ContainerOutput, skipNamespaces map[string]bool) bool {
	if len(skipNamespaces) == 0 {
		return false
	}
	if len(container.AffectedWorkloads) == 0 {
		return false
	}

	// Skip if ALL affected workloads are in namespaces with outdated Helm releases
	for _, workload := range container.AffectedWorkloads {
		if !skipNamespaces[workload.Namespace] {
			// At least one workload is in a namespace without outdated Helm release
			return false
		}
	}
	return true
}

func (s *Scanner) shouldIgnoreRelease(release ReleaseOutput) bool {
	for _, ignore := range s.config.IgnoreReleases {
		if release.ReleaseName == ignore {
			return true
		}
	}
	for _, ignore := range s.config.IgnoreCharts {
		if release.ChartName == ignore {
			return true
		}
	}
	return false
}

func (s *Scanner) shouldIgnoreContainer(container ContainerOutput) bool {
	for _, pattern := range s.config.IgnoreImages {
		if matchGlob(pattern, container.Name) {
			return true
		}
	}
	return false
}

// matchGlob performs simple glob matching with * wildcards.
func matchGlob(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	if pattern == s {
		return true
	}
	// Handle */name:* pattern
	if len(pattern) > 2 && pattern[0] == '*' && pattern[1] == '/' {
		// Match anything before /
		rest := pattern[2:]
		for i := 0; i < len(s); i++ {
			if s[i] == '/' && matchGlob(rest, s[i+1:]) {
				return true
			}
		}
	}
	// Handle *:* suffix pattern
	if len(pattern) > 1 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// expandTilde expands ~ to the user's home directory.
func expandTilde(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}

// isRunningInCluster returns true if the process is running inside a Kubernetes cluster.
// Detection is based on the presence of KUBERNETES_SERVICE_HOST environment variable,
// which is automatically set by Kubernetes for all pods.
func isRunningInCluster() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// getKubeconfig determines the kubeconfig path to use.
// Returns empty string when running in-cluster (nova will auto-detect).
// Otherwise returns the configured path, KUBECONFIG env var, or default ~/.kube/config.
func getKubeconfig(configuredPath string) string {
	// If running in-cluster, return empty to let nova use in-cluster config
	if isRunningInCluster() {
		return ""
	}

	// Use explicitly configured path
	if configuredPath != "" {
		return expandTilde(configuredPath)
	}

	// Check KUBECONFIG env var
	if envPath := os.Getenv("KUBECONFIG"); envPath != "" {
		return expandTilde(envPath)
	}

	// Fall back to default path
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/.kube/config"
	}

	return ""
}

// meetsMinSeverity checks if the version difference meets the minimum severity threshold.
func (s *Scanner) meetsMinSeverity(currentVersion, latestVersion string) bool {
	current, err := semver.NewVersion(currentVersion)
	if err != nil {
		// If we can't parse the version, include it
		return true
	}

	latest, err := semver.NewVersion(latestVersion)
	if err != nil {
		// If we can't parse the version, include it
		return true
	}

	severity := calculateSeverity(current, latest)
	return severity >= s.config.SeverityLevel()
}

// calculateSeverity determines the severity of a version difference.
// Returns: 3 = critical (major), 2 = major (minor), 1 = minor (patch)
func calculateSeverity(current, latest *semver.Version) int {
	if latest.Major() > current.Major() {
		return 3 // critical - major version bump
	}
	if latest.Minor() > current.Minor() {
		return 2 // major - minor version bump
	}
	if latest.Patch() > current.Patch() {
		return 1 // minor - patch version bump
	}
	return 0
}
