package github

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v57/github"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/nova"
	"golang.org/x/oauth2"
)

const (
	labelNovaScan        = "nova-scan"
	labelClaudeCode      = "claude-code"
	labelHelmUpdate      = "helm-update"
	labelContainerUpdate = "container-update"
)

// IssueAction describes what the IssueManager did for a finding.
type IssueAction string

const (
	// ActionCreated means a new issue was opened.
	ActionCreated IssueAction = "created"
	// ActionUpdated means an existing open issue for the same component was retitled
	// to the new target version.
	ActionUpdated IssueAction = "updated"
	// ActionSkipped means nothing was done (exact duplicate or previously dismissed).
	ActionSkipped IssueAction = "skipped"
	// ActionDryRun means an action would have been taken but dry-run mode is on.
	ActionDryRun IssueAction = "dry_run"
)

// IssueResult is the outcome of handling one outdated component.
type IssueResult struct {
	Action IssueAction
	URL    string
	Reason string
}

const (
	kindHelm      = "helm"
	kindContainer = "container"

	defaultMaxRetries = 3
	defaultMaxWait    = 2 * time.Minute
)

// titlePattern parses the titles produced by FormatHelmIssueTitle and
// FormatContainerIssueTitle (also accepts "->" for hand-written titles).
var titlePattern = regexp.MustCompile(`^\[Nova\] Update (Helm chart|container image): (.+?) \((.+?) (?:→|->) (.+)\)$`)

// trackedIssue is an existing nova-scan issue parsed from GitHub.
type trackedIssue struct {
	number int
	title  string
	url    string
	key    string
}

// issueIndex caches the repository's nova-scan issues for the duration of a run, so
// deduplication needs one paginated list call instead of a search query per finding
// (the search API is limited to 30 requests/minute).
type issueIndex struct {
	openByTitle map[string]*trackedIssue
	openByKey   map[string][]*trackedIssue
	dismissed   map[string]bool
}

// IssueManager handles GitHub issue creation and deduplication.
//
// Deduplication rules, per component (Helm release name or container image name):
//   - an open issue with the exact same title exists → skip
//   - an issue with the exact same title was closed as "not planned" → skip (dismissed
//     false positives are not re-opened every day)
//   - open issues for the same component but another version exist → the newest one is
//     retitled to the new versions and the others are closed as superseded
//   - otherwise → create a new issue
type IssueManager struct {
	client *github.Client
	owner  string
	repo   string
	dryRun bool
	logger *logging.Logger

	index *issueIndex

	maxRetries int
	maxWait    time.Duration
	sleep      func(ctx context.Context, d time.Duration) error
}

// NewIssueManager creates a new IssueManager instance.
func NewIssueManager(token, owner, repo string, dryRun bool, logger *logging.Logger) *IssueManager {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	return newIssueManagerWithClient(github.NewClient(tc), owner, repo, dryRun, logger)
}

func newIssueManagerWithClient(client *github.Client, owner, repo string, dryRun bool, logger *logging.Logger) *IssueManager {
	return &IssueManager{
		client:     client,
		owner:      owner,
		repo:       repo,
		dryRun:     dryRun,
		logger:     logger.WithComponent("github"),
		maxRetries: defaultMaxRetries,
		maxWait:    defaultMaxWait,
		sleep:      sleepCtx,
	}
}

// CreateHelmIssue creates or updates the GitHub issue for an outdated Helm release.
func (im *IssueManager) CreateHelmIssue(ctx context.Context, release nova.ReleaseOutput) (IssueResult, error) {
	return im.ensureIssue(ctx, kindHelm, release.ReleaseName,
		FormatHelmIssueTitle(release), FormatHelmIssueBody(release),
		[]string{labelNovaScan, labelClaudeCode, labelHelmUpdate})
}

// CreateContainerIssue creates or updates the GitHub issue for an outdated container image.
func (im *IssueManager) CreateContainerIssue(ctx context.Context, container nova.ContainerOutput) (IssueResult, error) {
	return im.ensureIssue(ctx, kindContainer, container.Name,
		FormatContainerIssueTitle(container), FormatContainerIssueBody(container),
		[]string{labelNovaScan, labelClaudeCode, labelContainerUpdate})
}

func (im *IssueManager) ensureIssue(ctx context.Context, kind, name, title, body string, labels []string) (IssueResult, error) {
	if err := im.loadIndex(ctx); err != nil {
		return IssueResult{}, fmt.Errorf("failed to load existing issues: %w", err)
	}
	idx := im.index
	key := componentKey(kind, name)

	if existing, ok := idx.openByTitle[title]; ok {
		im.logger.IssueSkipped(kind, title, "duplicate")
		return IssueResult{Action: ActionSkipped, URL: existing.url, Reason: "duplicate"}, nil
	}
	if idx.dismissed[title] {
		im.logger.IssueSkipped(kind, title, "dismissed")
		return IssueResult{Action: ActionSkipped, Reason: "dismissed"}, nil
	}

	if open := idx.openByKey[key]; len(open) > 0 {
		return im.updateExisting(ctx, kind, key, title, body, open)
	}

	if im.dryRun {
		im.logger.IssueDryRun(kind, title)
		return IssueResult{Action: ActionDryRun}, nil
	}

	var issue *github.Issue
	err := im.withRetry(ctx, "create issue", func() (*github.Response, error) {
		var resp *github.Response
		var err error
		issue, resp, err = im.client.Issues.Create(ctx, im.owner, im.repo, &github.IssueRequest{
			Title:  github.String(title),
			Body:   github.String(body),
			Labels: &labels,
		})
		return resp, err
	})
	if err != nil {
		return IssueResult{}, fmt.Errorf("failed to create issue: %w", err)
	}

	idx.add(&trackedIssue{number: issue.GetNumber(), title: title, url: issue.GetHTMLURL(), key: key})
	im.logger.IssueCreated(kind, title, issue.GetHTMLURL())
	return IssueResult{Action: ActionCreated, URL: issue.GetHTMLURL()}, nil
}

// updateExisting retitles the newest open issue for the component and closes older ones.
// Updating in place keeps labels (e.g. risk assessments) and discussion on one issue.
func (im *IssueManager) updateExisting(ctx context.Context, kind, key, title, body string, open []*trackedIssue) (IssueResult, error) {
	keep := open[0]
	for _, o := range open[1:] {
		if o.number > keep.number {
			keep = o
		}
	}

	if im.dryRun {
		im.logger.IssueDryRun(kind, fmt.Sprintf("%s (would update #%d %q)", title, keep.number, keep.title))
		return IssueResult{Action: ActionDryRun, URL: keep.url}, nil
	}

	previous := keep.title
	err := im.withRetry(ctx, "update issue", func() (*github.Response, error) {
		_, resp, err := im.client.Issues.Edit(ctx, im.owner, im.repo, keep.number, &github.IssueRequest{
			Title: github.String(title),
			Body:  github.String(body),
		})
		return resp, err
	})
	if err != nil {
		return IssueResult{}, fmt.Errorf("failed to update issue #%d: %w", keep.number, err)
	}
	comment := fmt.Sprintf("nova-scanner detected a different version for this component and updated this issue.\n\n- Before: %s\n- Now: %s", previous, title)
	if err := im.comment(ctx, keep.number, comment); err != nil {
		im.logger.Warn().Err(err).Int("issue", keep.number).Msg("Failed to comment on updated issue")
	}

	for _, o := range open {
		if o == keep {
			continue
		}
		if err := im.closeSuperseded(ctx, o, keep, title); err != nil {
			im.logger.Warn().Err(err).Int("issue", o.number).Msg("Failed to close superseded issue")
		}
	}

	im.index.remove(keep)
	keep.title = title
	im.index.openByKey[key] = []*trackedIssue{keep}
	im.index.openByTitle[title] = keep

	im.logger.IssueUpdated(kind, title, previous, keep.url)
	return IssueResult{Action: ActionUpdated, URL: keep.url}, nil
}

func (im *IssueManager) closeSuperseded(ctx context.Context, old, keep *trackedIssue, newTitle string) error {
	if err := im.comment(ctx, old.number, fmt.Sprintf("Closing: superseded by #%d (%s).", keep.number, newTitle)); err != nil {
		return err
	}
	err := im.withRetry(ctx, "close issue", func() (*github.Response, error) {
		_, resp, err := im.client.Issues.Edit(ctx, im.owner, im.repo, old.number, &github.IssueRequest{
			State:       github.String("closed"),
			StateReason: github.String("not_planned"),
		})
		return resp, err
	})
	if err != nil {
		return err
	}
	im.index.remove(old)
	im.logger.IssueSuperseded(old.number, keep.number)
	return nil
}

func (im *IssueManager) comment(ctx context.Context, number int, body string) error {
	return im.withRetry(ctx, "comment", func() (*github.Response, error) {
		_, resp, err := im.client.Issues.CreateComment(ctx, im.owner, im.repo, number, &github.IssueComment{Body: github.String(body)})
		return resp, err
	})
}

// loadIndex lists open nova-scan issues and the titles of issues closed as "not planned".
func (im *IssueManager) loadIndex(ctx context.Context) error {
	if im.index != nil {
		return nil
	}
	idx := &issueIndex{
		openByTitle: map[string]*trackedIssue{},
		openByKey:   map[string][]*trackedIssue{},
		dismissed:   map[string]bool{},
	}

	err := im.listIssues(ctx, "open", func(is *github.Issue) {
		t := &trackedIssue{number: is.GetNumber(), title: is.GetTitle(), url: is.GetHTMLURL()}
		if kind, name, ok := parseIssueTitle(t.title); ok {
			t.key = componentKey(kind, name)
		}
		idx.add(t)
	})
	if err != nil {
		return err
	}

	err = im.listIssues(ctx, "closed", func(is *github.Issue) {
		if is.GetStateReason() == "not_planned" {
			idx.dismissed[is.GetTitle()] = true
		}
	})
	if err != nil {
		return err
	}

	im.index = idx
	im.logger.Debug().Int("open", len(idx.openByTitle)).Int("dismissed", len(idx.dismissed)).Msg("Loaded existing nova-scan issues")
	return nil
}

func (im *IssueManager) listIssues(ctx context.Context, state string, fn func(*github.Issue)) error {
	opts := &github.IssueListByRepoOptions{
		State:       state,
		Labels:      []string{labelNovaScan},
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		var issues []*github.Issue
		var resp *github.Response
		err := im.withRetry(ctx, "list issues", func() (*github.Response, error) {
			var err error
			issues, resp, err = im.client.Issues.ListByRepo(ctx, im.owner, im.repo, opts)
			return resp, err
		})
		if err != nil {
			return err
		}
		for _, is := range issues {
			if is.IsPullRequest() {
				continue
			}
			fn(is)
		}
		if resp == nil || resp.NextPage == 0 {
			return nil
		}
		opts.Page = resp.NextPage
	}
}

// withRetry retries fn on GitHub primary/secondary rate limit errors, waiting as
// instructed by the API (bounded by maxWait).
func (im *IssueManager) withRetry(ctx context.Context, op string, fn func() (*github.Response, error)) error {
	for attempt := 0; ; attempt++ {
		_, err := fn()
		if err == nil {
			return nil
		}

		var wait time.Duration
		var abuse *github.AbuseRateLimitError
		var rate *github.RateLimitError
		switch {
		case errors.As(err, &abuse):
			wait = abuse.GetRetryAfter()
			if wait <= 0 {
				wait = time.Minute
			}
		case errors.As(err, &rate):
			wait = time.Until(rate.Rate.Reset.Time) + time.Second
		default:
			return err
		}

		if attempt >= im.maxRetries || wait > im.maxWait {
			return err
		}
		im.logger.Warn().Str("operation", op).Dur("retry_after", wait).Int("attempt", attempt+1).Msg("GitHub rate limit hit, backing off")
		if err := im.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (idx *issueIndex) add(t *trackedIssue) {
	idx.openByTitle[t.title] = t
	if t.key != "" {
		idx.openByKey[t.key] = append(idx.openByKey[t.key], t)
	}
}

func (idx *issueIndex) remove(t *trackedIssue) {
	delete(idx.openByTitle, t.title)
	if t.key == "" {
		return
	}
	list := idx.openByKey[t.key]
	for i, o := range list {
		if o == t {
			idx.openByKey[t.key] = append(list[:i], list[i+1:]...)
			break
		}
	}
}

func componentKey(kind, name string) string {
	return kind + "|" + name
}

// parseIssueTitle extracts the component kind and name from a nova issue title.
func parseIssueTitle(title string) (kind, name string, ok bool) {
	m := titlePattern.FindStringSubmatch(title)
	if m == nil {
		return "", "", false
	}
	kind = kindContainer
	if m[1] == "Helm chart" {
		kind = kindHelm
	}
	return kind, m[2], true
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
