package github

import (
	"strings"
	"testing"

	"github.com/olohmann/nova-automated-cluster-scanner/pkg/nova"
)

func TestBacktick(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "`hello`"},
		{"", "``"},
		{"1.0.0", "`1.0.0`"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := backtick(tt.input)
			if got != tt.want {
				t.Errorf("backtick(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestEscapeSearchQuery(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"normal text", "normal text"},
		{`with "quotes"`, "with quotes"},
		{`with \backslash`, "with backslash"},
		{`"both" and \slash`, "both and slash"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeSearchQuery(tt.input)
			if got != tt.want {
				t.Errorf("escapeSearchQuery(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatYAMLSnippet(t *testing.T) {
	result := formatYAMLSnippet("2.0.0", "1.0.0")

	if !strings.Contains(result, "```yaml") {
		t.Error("expected YAML code block")
	}
	if !strings.Contains(result, `version: "2.0.0"`) {
		t.Error("expected new version in snippet")
	}
	if !strings.Contains(result, "# was: 1.0.0") {
		t.Error("expected old version comment")
	}
}

func TestFormatHelmCommands(t *testing.T) {
	result := formatHelmCommands("my-release", "my-namespace")

	if !strings.Contains(result, "```bash") {
		t.Error("expected bash code block")
	}
	if !strings.Contains(result, "flux get helmreleases -n my-namespace") {
		t.Error("expected flux get command")
	}
	if !strings.Contains(result, "flux reconcile helmrelease my-release -n my-namespace") {
		t.Error("expected flux reconcile command")
	}
	if !strings.Contains(result, "helm history my-release -n my-namespace") {
		t.Error("expected helm history command")
	}
}

func TestFormatWorkloadTable_Empty(t *testing.T) {
	result := formatWorkloadTable(nil)
	if result != "_No workload information available_" {
		t.Errorf("expected placeholder text, got %q", result)
	}

	result = formatWorkloadTable([]nova.WorkloadOutput{})
	if result != "_No workload information available_" {
		t.Errorf("expected placeholder text, got %q", result)
	}
}

func TestFormatWorkloadTable_WithWorkloads(t *testing.T) {
	workloads := []nova.WorkloadOutput{
		{Name: "web", Namespace: "default", Kind: "Deployment", Container: "nginx"},
		{Name: "api", Namespace: "backend", Kind: "StatefulSet", Container: "app"},
	}

	result := formatWorkloadTable(workloads)

	// Check header
	if !strings.Contains(result, "| Workload | Namespace | Kind | Container |") {
		t.Error("expected table header")
	}

	// Check separator
	if !strings.Contains(result, "|----------|-----------|------|----------|") {
		t.Error("expected table separator")
	}

	// Check data rows
	if !strings.Contains(result, "| web | default | Deployment | nginx |") {
		t.Error("expected first workload row")
	}
	if !strings.Contains(result, "| api | backend | StatefulSet | app |") {
		t.Error("expected second workload row")
	}
}

func TestFormatHelmIssueBody(t *testing.T) {
	release := nova.ReleaseOutput{
		ReleaseName: "my-release",
		ChartName:   "my-chart",
		Namespace:   "default",
		Installed:   nova.VersionInfo{Version: "1.0.0"},
		Latest:      nova.VersionInfo{Version: "2.0.0"},
		Deprecated:  false,
	}

	body := FormatHelmIssueBody(release)

	// Check table content
	if !strings.Contains(body, "| Release Name | `my-release` |") {
		t.Error("expected release name in table")
	}
	if !strings.Contains(body, "| Chart Name | `my-chart` |") {
		t.Error("expected chart name in table")
	}
	if !strings.Contains(body, "| Namespace | `default` |") {
		t.Error("expected namespace in table")
	}
	if !strings.Contains(body, "| Current Version | `1.0.0` |") {
		t.Error("expected current version in table")
	}
	if !strings.Contains(body, "| Latest Version | `2.0.0` |") {
		t.Error("expected latest version in table")
	}
	if !strings.Contains(body, "| Deprecated | No |") {
		t.Error("expected deprecated status")
	}

	// Check checklist
	if !strings.Contains(body, "- [ ] Review changelog for breaking changes") {
		t.Error("expected checklist item")
	}

	// Check Flux section
	if !strings.Contains(body, "## Flux Update (GitOps)") {
		t.Error("expected Flux section")
	}

	// Check footer
	if !strings.Contains(body, "*This issue was automatically created by nova-scanner*") {
		t.Error("expected footer")
	}
}

func TestFormatHelmIssueBody_Deprecated(t *testing.T) {
	release := nova.ReleaseOutput{
		ReleaseName: "old-release",
		ChartName:   "deprecated-chart",
		Namespace:   "legacy",
		Installed:   nova.VersionInfo{Version: "0.1.0"},
		Latest:      nova.VersionInfo{Version: "1.0.0"},
		Deprecated:  true,
	}

	body := FormatHelmIssueBody(release)

	if !strings.Contains(body, "| Deprecated | Yes |") {
		t.Error("expected deprecated status to be Yes")
	}
}

func TestFormatContainerIssueBody(t *testing.T) {
	container := nova.ContainerOutput{
		Name:       "nginx",
		CurrentTag: "1.20",
		LatestTag:  "1.25",
		AffectedWorkloads: []nova.WorkloadOutput{
			{Name: "web", Namespace: "default", Kind: "Deployment", Container: "nginx"},
		},
	}

	body := FormatContainerIssueBody(container)

	// Check table content
	if !strings.Contains(body, "| Image | `nginx` |") {
		t.Error("expected image name in table")
	}
	if !strings.Contains(body, "| Current Tag | `1.20` |") {
		t.Error("expected current tag in table")
	}
	if !strings.Contains(body, "| Latest Tag | `1.25` |") {
		t.Error("expected latest tag in table")
	}

	// Check workloads section
	if !strings.Contains(body, "### Affected Workloads") {
		t.Error("expected affected workloads section")
	}
	if !strings.Contains(body, "| web | default | Deployment | nginx |") {
		t.Error("expected workload in table")
	}

	// Check checklist
	if !strings.Contains(body, "- [ ] Review release notes") {
		t.Error("expected checklist item")
	}
}

func TestFormatContainerIssueBody_NoWorkloads(t *testing.T) {
	container := nova.ContainerOutput{
		Name:              "redis",
		CurrentTag:        "6.0",
		LatestTag:         "7.0",
		AffectedWorkloads: nil,
	}

	body := FormatContainerIssueBody(container)

	if !strings.Contains(body, "_No workload information available_") {
		t.Error("expected no workload placeholder")
	}
}

func TestLabels(t *testing.T) {
	if labelNovaScan != "nova-scan" {
		t.Errorf("expected labelNovaScan to be 'nova-scan', got %q", labelNovaScan)
	}
	if labelClaudeCode != "claude-code" {
		t.Errorf("expected labelClaudeCode to be 'claude-code', got %q", labelClaudeCode)
	}
}

func TestFormatHelmIssueTitle(t *testing.T) {
	release := nova.ReleaseOutput{
		ReleaseName: "my-release",
		Installed:   nova.VersionInfo{Version: "1.0.0"},
		Latest:      nova.VersionInfo{Version: "2.0.0"},
	}

	title := FormatHelmIssueTitle(release)

	expected := "[Nova] Update Helm chart: my-release (1.0.0 → 2.0.0)"
	if title != expected {
		t.Errorf("expected title %q, got %q", expected, title)
	}
}

func TestFormatContainerIssueTitle(t *testing.T) {
	container := nova.ContainerOutput{
		Name:       "nginx",
		CurrentTag: "1.20",
		LatestTag:  "1.25",
	}

	title := FormatContainerIssueTitle(container)

	expected := "[Nova] Update container image: nginx (1.20 → 1.25)"
	if title != expected {
		t.Errorf("expected title %q, got %q", expected, title)
	}
}
