package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v57/github"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/nova"
	"golang.org/x/oauth2"
)

const (
	labelNovaScan   = "nova-scan"
	labelClaudeCode = "claude-code"
)

// IssueManager handles GitHub issue creation and deduplication.
type IssueManager struct {
	client *github.Client
	owner  string
	repo   string
	dryRun bool
	logger *logging.Logger
}

// NewIssueManager creates a new IssueManager instance.
func NewIssueManager(token, owner, repo string, dryRun bool, logger *logging.Logger) *IssueManager {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	return &IssueManager{
		client: client,
		owner:  owner,
		repo:   repo,
		dryRun: dryRun,
		logger: logger.WithComponent("github"),
	}
}

// CreateHelmIssue creates a GitHub issue for an outdated Helm release.
// Returns the issue URL if created, empty string if skipped.
func (im *IssueManager) CreateHelmIssue(ctx context.Context, release nova.ReleaseOutput) (string, error) {
	title := FormatHelmIssueTitle(release)

	// Check if issue already exists
	exists, err := im.issueExists(ctx, title)
	if err != nil {
		return "", fmt.Errorf("failed to check existing issues: %w", err)
	}
	if exists {
		im.logger.IssueSkipped("helm", title, "duplicate")
		return "", nil
	}

	body := FormatHelmIssueBody(release)

	if im.dryRun {
		im.logger.IssueDryRun("helm", title)
		return "", nil
	}

	issue, _, err := im.client.Issues.Create(ctx, im.owner, im.repo, &github.IssueRequest{
		Title:  github.String(title),
		Body:   github.String(body),
		Labels: &[]string{labelNovaScan, labelClaudeCode},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create issue: %w", err)
	}

	im.logger.IssueCreated("helm", title, issue.GetHTMLURL())
	return issue.GetHTMLURL(), nil
}

// CreateContainerIssue creates a GitHub issue for an outdated container image.
// Returns the issue URL if created, empty string if skipped.
func (im *IssueManager) CreateContainerIssue(ctx context.Context, container nova.ContainerOutput) (string, error) {
	title := FormatContainerIssueTitle(container)

	// Check if issue already exists
	exists, err := im.issueExists(ctx, title)
	if err != nil {
		return "", fmt.Errorf("failed to check existing issues: %w", err)
	}
	if exists {
		im.logger.IssueSkipped("container", title, "duplicate")
		return "", nil
	}

	body := FormatContainerIssueBody(container)

	if im.dryRun {
		im.logger.IssueDryRun("container", title)
		return "", nil
	}

	issue, _, err := im.client.Issues.Create(ctx, im.owner, im.repo, &github.IssueRequest{
		Title:  github.String(title),
		Body:   github.String(body),
		Labels: &[]string{labelNovaScan, labelClaudeCode},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create issue: %w", err)
	}

	im.logger.IssueCreated("container", title, issue.GetHTMLURL())
	return issue.GetHTMLURL(), nil
}

// issueExists checks if an open issue with the given title already exists.
func (im *IssueManager) issueExists(ctx context.Context, title string) (bool, error) {
	// Search for existing open issues with the nova-scan label
	query := fmt.Sprintf("repo:%s/%s is:issue is:open label:%s in:title \"%s\"",
		im.owner, im.repo, labelNovaScan, escapeSearchQuery(title))

	result, _, err := im.client.Search.Issues(ctx, query, &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: 1},
	})
	if err != nil {
		return false, err
	}

	return result.GetTotal() > 0, nil
}

// escapeSearchQuery escapes special characters for GitHub search.
func escapeSearchQuery(s string) string {
	// Remove characters that might break the search query
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, "\\", "")
	return s
}

// FormatHelmIssueTitle generates the issue title for a Helm release.
func FormatHelmIssueTitle(release nova.ReleaseOutput) string {
	return fmt.Sprintf("[Nova] Update Helm chart: %s (%s → %s)",
		release.ReleaseName,
		release.Installed.Version,
		release.Latest.Version,
	)
}

// FormatContainerIssueTitle generates the issue title for a container image.
func FormatContainerIssueTitle(container nova.ContainerOutput) string {
	return fmt.Sprintf("[Nova] Update container image: %s (%s → %s)",
		container.Name,
		container.CurrentTag,
		container.LatestTag,
	)
}

// FormatHelmIssueBody generates the issue body for a Helm release.
func FormatHelmIssueBody(release nova.ReleaseOutput) string {
	deprecated := "No"
	if release.Deprecated {
		deprecated = "Yes"
	}

	return fmt.Sprintf(`## Outdated Helm Chart Detected

| Field | Value |
|-------|-------|
| Release Name | %s |
| Chart Name | %s |
| Namespace | %s |
| Current Version | %s |
| Latest Version | %s |
| Deprecated | %s |

## Update Checklist

- [ ] Review changelog for breaking changes between %s and %s
- [ ] Update HelmRelease manifest with new version
- [ ] Commit and push to trigger Flux reconciliation
- [ ] Verify Flux successfully reconciles the HelmRelease
- [ ] Check application health post-upgrade

## Flux Update (GitOps)

Update your HelmRelease manifest:

%s

## Useful Commands

%s

---
*This issue was automatically created by nova-scanner*
`,
		backtick(release.ReleaseName),
		backtick(release.ChartName),
		backtick(release.Namespace),
		backtick(release.Installed.Version),
		backtick(release.Latest.Version),
		deprecated,
		release.Installed.Version,
		release.Latest.Version,
		formatYAMLSnippet(release.Latest.Version, release.Installed.Version),
		formatHelmCommands(release.ReleaseName, release.Namespace),
	)
}

// FormatContainerIssueBody generates the issue body for a container image.
func FormatContainerIssueBody(container nova.ContainerOutput) string {
	workloadTable := formatWorkloadTable(container.AffectedWorkloads)

	return fmt.Sprintf(`## Outdated Container Image Detected

| Field | Value |
|-------|-------|
| Image | %s |
| Current Tag | %s |
| Latest Tag | %s |

### Affected Workloads

%s

## Update Checklist

- [ ] Review release notes for breaking changes
- [ ] Update image tag in deployment manifest
- [ ] Commit and push to trigger Flux reconciliation
- [ ] Verify pods restart with new image
- [ ] Check application health

---
*This issue was automatically created by nova-scanner*
`,
		backtick(container.Name),
		backtick(container.CurrentTag),
		backtick(container.LatestTag),
		workloadTable,
	)
}

func backtick(s string) string {
	return "`" + s + "`"
}

func formatYAMLSnippet(latestVersion, currentVersion string) string {
	return fmt.Sprintf("```yaml\nspec:\n  chart:\n    spec:\n      version: \"%s\"  # was: %s\n```",
		latestVersion, currentVersion)
}

func formatHelmCommands(releaseName, namespace string) string {
	return fmt.Sprintf(`%s
# Check current HelmRelease status
flux get helmreleases -n %s | grep %s

# Force reconciliation after commit
flux reconcile helmrelease %s -n %s

# View Helm release history
helm history %s -n %s
%s`,
		"```bash",
		namespace, releaseName,
		releaseName, namespace,
		releaseName, namespace,
		"```",
	)
}

func formatWorkloadTable(workloads []nova.WorkloadOutput) string {
	if len(workloads) == 0 {
		return "_No workload information available_"
	}

	var sb strings.Builder
	sb.WriteString("| Workload | Namespace | Kind | Container |\n")
	sb.WriteString("|----------|-----------|------|----------|\n")

	for _, w := range workloads {
		sb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
			w.Name, w.Namespace, w.Kind, w.Container))
	}

	return sb.String()
}
