package nova

import (
	"encoding/json"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/config"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
)

func TestNewScanner(t *testing.T) {
	cfg := &config.Config{
		ScanHelm:        true,
		ScanContainers:  false,
		MinSeverity:     "minor",
		PollArtifactHub: true,
	}
	logger := logging.NewLogger("info")

	scanner, err := NewScanner(cfg, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scanner == nil {
		t.Fatal("expected non-nil scanner")
	}
	if scanner.config != cfg {
		t.Error("scanner config mismatch")
	}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		input   string
		want    bool
	}{
		// Exact match
		{"nginx", "nginx", true},
		{"nginx", "redis", false},

		// Wildcard all
		{"*", "anything", true},
		{"*", "", true},

		// Prefix wildcard (*/name pattern)
		{"*/pause:*", "k8s.gcr.io/pause:3.5", true},
		{"*/pause:*", "registry.io/pause:latest", true},
		{"*/pause:*", "nginx:latest", false},

		// Suffix wildcard
		{"nginx:*", "nginx:1.20", true},
		{"nginx:*", "nginx:latest", true},
		{"nginx:*", "redis:6.0", false},

		// Complex patterns
		{"*/coredns:*", "k8s.gcr.io/coredns:1.8.0", true},
		{"*/coredns:*", "docker.io/coredns/coredns:1.9.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.input, func(t *testing.T) {
			got := matchGlob(tt.pattern, tt.input)
			if got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.input, got, tt.want)
			}
		})
	}
}

func TestCalculateSeverity(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    int
	}{
		{"major version bump", "1.0.0", "2.0.0", 3},
		{"minor version bump", "1.0.0", "1.1.0", 2},
		{"patch version bump", "1.0.0", "1.0.1", 1},
		{"no change", "1.0.0", "1.0.0", 0},
		{"major jump multiple", "1.5.3", "3.0.0", 3},
		{"minor with patch", "1.0.0", "1.2.3", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current, _ := semver.NewVersion(tt.current)
			latest, _ := semver.NewVersion(tt.latest)

			got := calculateSeverity(current, latest)
			if got != tt.want {
				t.Errorf("calculateSeverity(%s, %s) = %d, want %d", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestScanner_MeetsMinSeverity(t *testing.T) {
	tests := []struct {
		name        string
		minSeverity string
		current     string
		latest      string
		want        bool
	}{
		// Minor threshold (level 1) - everything passes
		{"minor: patch bump", "minor", "1.0.0", "1.0.1", true},
		{"minor: minor bump", "minor", "1.0.0", "1.1.0", true},
		{"minor: major bump", "minor", "1.0.0", "2.0.0", true},

		// Major threshold (level 2) - only minor+ passes
		{"major: patch bump", "major", "1.0.0", "1.0.1", false},
		{"major: minor bump", "major", "1.0.0", "1.1.0", true},
		{"major: major bump", "major", "1.0.0", "2.0.0", true},

		// Critical threshold (level 3) - only major passes
		{"critical: patch bump", "critical", "1.0.0", "1.0.1", false},
		{"critical: minor bump", "critical", "1.0.0", "1.1.0", false},
		{"critical: major bump", "critical", "1.0.0", "2.0.0", true},

		// Invalid versions should pass (include them to be safe)
		{"invalid current", "minor", "invalid", "1.0.0", true},
		{"invalid latest", "minor", "1.0.0", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{MinSeverity: tt.minSeverity}
			logger := logging.NewLogger("error") // suppress logs
			scanner := &Scanner{config: cfg, logger: logger}

			got := scanner.meetsMinSeverity(tt.current, tt.latest)
			if got != tt.want {
				t.Errorf("meetsMinSeverity(%s, %s) with threshold %s = %v, want %v",
					tt.current, tt.latest, tt.minSeverity, got, tt.want)
			}
		})
	}
}

func TestScanner_ShouldIgnoreRelease(t *testing.T) {
	cfg := &config.Config{
		IgnoreReleases: []string{"ignored-release", "another-ignored"},
		IgnoreCharts:   []string{"ignored-chart"},
	}
	logger := logging.NewLogger("error")
	scanner := &Scanner{config: cfg, logger: logger}

	tests := []struct {
		name    string
		release ReleaseOutput
		want    bool
	}{
		{
			name:    "not ignored",
			release: ReleaseOutput{ReleaseName: "my-release", ChartName: "my-chart"},
			want:    false,
		},
		{
			name:    "ignored by release name",
			release: ReleaseOutput{ReleaseName: "ignored-release", ChartName: "my-chart"},
			want:    true,
		},
		{
			name:    "ignored by chart name",
			release: ReleaseOutput{ReleaseName: "my-release", ChartName: "ignored-chart"},
			want:    true,
		},
		{
			name:    "another ignored release",
			release: ReleaseOutput{ReleaseName: "another-ignored", ChartName: "some-chart"},
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanner.shouldIgnoreRelease(tt.release)
			if got != tt.want {
				t.Errorf("shouldIgnoreRelease() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScanner_ShouldIgnoreContainer(t *testing.T) {
	cfg := &config.Config{
		IgnoreImages: []string{"*/pause:*", "*/coredns:*", "nginx:*"},
	}
	logger := logging.NewLogger("error")
	scanner := &Scanner{config: cfg, logger: logger}

	tests := []struct {
		name      string
		container ContainerOutput
		want      bool
	}{
		{
			name:      "not ignored",
			container: ContainerOutput{Name: "redis:6.0"},
			want:      false,
		},
		{
			name:      "ignored pause",
			container: ContainerOutput{Name: "k8s.gcr.io/pause:3.5"},
			want:      true,
		},
		{
			name:      "ignored coredns",
			container: ContainerOutput{Name: "docker.io/coredns/coredns:1.9.0"},
			want:      true,
		},
		{
			name:      "ignored nginx prefix",
			container: ContainerOutput{Name: "nginx:latest"},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanner.shouldIgnoreContainer(tt.container)
			if got != tt.want {
				t.Errorf("shouldIgnoreContainer(%s) = %v, want %v", tt.container.Name, got, tt.want)
			}
		})
	}
}

func TestHelmScanResult_OutdatedNamespaces(t *testing.T) {
	result := &HelmScanResult{
		Outdated: []ReleaseOutput{
			{ReleaseName: "release1", Namespace: "ns1"},
			{ReleaseName: "release2", Namespace: "ns2"},
			{ReleaseName: "release3", Namespace: "ns1"}, // duplicate namespace
		},
	}

	namespaces := result.OutdatedNamespaces()

	if len(namespaces) != 2 {
		t.Errorf("expected 2 unique namespaces, got %d", len(namespaces))
	}
	if !namespaces["ns1"] {
		t.Error("expected ns1 in outdated namespaces")
	}
	if !namespaces["ns2"] {
		t.Error("expected ns2 in outdated namespaces")
	}
}

func TestScanner_ShouldSkipContainerForHelm(t *testing.T) {
	cfg := &config.Config{}
	logger := logging.NewLogger("error")
	scanner := &Scanner{config: cfg, logger: logger}

	tests := []struct {
		name           string
		container      ContainerOutput
		skipNamespaces map[string]bool
		want           bool
	}{
		{
			name: "skip when all workloads in outdated namespace",
			container: ContainerOutput{
				Name: "nginx",
				AffectedWorkloads: []WorkloadOutput{
					{Name: "web", Namespace: "cert-manager"},
					{Name: "api", Namespace: "cert-manager"},
				},
			},
			skipNamespaces: map[string]bool{"cert-manager": true},
			want:           true,
		},
		{
			name: "don't skip when some workloads in non-outdated namespace",
			container: ContainerOutput{
				Name: "nginx",
				AffectedWorkloads: []WorkloadOutput{
					{Name: "web", Namespace: "cert-manager"},
					{Name: "api", Namespace: "default"},
				},
			},
			skipNamespaces: map[string]bool{"cert-manager": true},
			want:           false,
		},
		{
			name: "don't skip when no workloads",
			container: ContainerOutput{
				Name:              "nginx",
				AffectedWorkloads: []WorkloadOutput{},
			},
			skipNamespaces: map[string]bool{"cert-manager": true},
			want:           false,
		},
		{
			name: "don't skip when skipNamespaces is empty",
			container: ContainerOutput{
				Name: "nginx",
				AffectedWorkloads: []WorkloadOutput{
					{Name: "web", Namespace: "cert-manager"},
				},
			},
			skipNamespaces: map[string]bool{},
			want:           false,
		},
		{
			name: "don't skip when skipNamespaces is nil",
			container: ContainerOutput{
				Name: "nginx",
				AffectedWorkloads: []WorkloadOutput{
					{Name: "web", Namespace: "cert-manager"},
				},
			},
			skipNamespaces: nil,
			want:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanner.shouldSkipContainerForHelm(tt.container, tt.skipNamespaces)
			if got != tt.want {
				t.Errorf("shouldSkipContainerForHelm() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReleaseOutput_JSONParsing(t *testing.T) {
	// Test that our struct can parse Nova's JSON output format
	jsonData := `{
		"release": "my-release",
		"chartName": "my-chart",
		"namespace": "default",
		"Installed": {"version": "1.0.0", "appVersion": "1.0"},
		"Latest": {"version": "2.0.0", "appVersion": "2.0"},
		"outdated": true,
		"deprecated": false
	}`

	var release ReleaseOutput
	if err := unmarshalJSON([]byte(jsonData), &release); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if release.ReleaseName != "my-release" {
		t.Errorf("expected ReleaseName 'my-release', got %q", release.ReleaseName)
	}
	if release.ChartName != "my-chart" {
		t.Errorf("expected ChartName 'my-chart', got %q", release.ChartName)
	}
	if release.Installed.Version != "1.0.0" {
		t.Errorf("expected Installed.Version '1.0.0', got %q", release.Installed.Version)
	}
	if release.Latest.Version != "2.0.0" {
		t.Errorf("expected Latest.Version '2.0.0', got %q", release.Latest.Version)
	}
	if !release.IsOld {
		t.Error("expected IsOld to be true")
	}
	if release.Deprecated {
		t.Error("expected Deprecated to be false")
	}
}

func TestContainerOutput_JSONParsing(t *testing.T) {
	jsonData := `{
		"name": "nginx",
		"current_version": "1.20",
		"latest_version": "1.25",
		"outdated": true,
		"affectedWorkloads": [
			{"name": "web", "namespace": "default", "kind": "Deployment", "container": "nginx"}
		]
	}`

	var container ContainerOutput
	if err := unmarshalJSON([]byte(jsonData), &container); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if container.Name != "nginx" {
		t.Errorf("expected Name 'nginx', got %q", container.Name)
	}
	if container.CurrentTag != "1.20" {
		t.Errorf("expected CurrentTag '1.20', got %q", container.CurrentTag)
	}
	if container.LatestTag != "1.25" {
		t.Errorf("expected LatestTag '1.25', got %q", container.LatestTag)
	}
	if !container.IsOld {
		t.Error("expected IsOld to be true")
	}
	if len(container.AffectedWorkloads) != 1 {
		t.Errorf("expected 1 affected workload, got %d", len(container.AffectedWorkloads))
	}
	if container.AffectedWorkloads[0].Kind != "Deployment" {
		t.Errorf("expected workload kind 'Deployment', got %q", container.AffectedWorkloads[0].Kind)
	}
}

// Helper to unmarshal JSON (using encoding/json)
func unmarshalJSON(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
