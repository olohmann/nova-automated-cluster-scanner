package github

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
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

// markerPattern matches the hidden marker identifying the component an issue tracks.
// Helm releases are identified as "helm|<namespace>/<release>", containers as
// "container|<image>". Issues created before markers existed are matched via their title.
var markerPattern = regexp.MustCompile(`<!-- nova-scanner:key=(.+?) -->`)

// trackedIssue is an existing nova-scan issue parsed from GitHub.
type trackedIssue struct {
	number int
	title  string
	url    string
	key    string
}

// component identifies the thing an issue is about.
type component struct {
	kind string
	// key is the stable identity written into the issue body.
	key string
	// legacyKey identifies issues created before body markers existed (title based).
	legacyKey string
}

// issueIndex caches the repository's nova-scan issues for the duration of a run, so
// deduplication needs one paginated list call instead of a search query per finding
// (the search API is limited to 30 requests/minute).
type issueIndex struct {
	openByKey map[string][]*trackedIssue
	// dismissed holds "<key>\x00<title>" for issues closed as "not planned"; legacyDismissed
	// holds the titles of such issues that carry no marker.
	dismissed       map[string]bool
	legacyDismissed map[string]bool
}

// IssueManager handles GitHub issue creation and deduplication.
//
// Deduplication rules, per component (Helm release namespace/name or container image name):
//   - an open issue with the exact same title exists → skip
//   - an issue with the exact same title was closed as "not planned" → skip (dismissed
//     false positives are not re-opened every day)
//   - open issues for the same component but another version exist → the newest one is
//     retitled to the new versions and the others are closed as superseded
//   - otherwise → create a new issue
//
// Open issues whose component is no longer reported are closed by CloseResolved.
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
	return im.ensureIssue(ctx, HelmComponent(release),
		FormatHelmIssueTitle(release), FormatHelmIssueBody(release),
		[]string{labelNovaScan, labelClaudeCode, labelHelmUpdate})
}

// CreateContainerIssue creates or updates the GitHub issue for an outdated container image.
func (im *IssueManager) CreateContainerIssue(ctx context.Context, container nova.ContainerOutput) (IssueResult, error) {
	return im.ensureIssue(ctx, ContainerComponent(container.Name),
		FormatContainerIssueTitle(container), FormatContainerIssueBody(container),
		[]string{labelNovaScan, labelClaudeCode, labelContainerUpdate})
}

// HelmComponent returns the identity of a Helm release.
func HelmComponent(release nova.ReleaseOutput) component {
	return component{
		kind:      kindHelm,
		key:       componentKey(kindHelm, release.Namespace+"/"+release.ReleaseName),
		legacyKey: componentKey(kindHelm, release.ReleaseName),
	}
}

// ContainerComponent returns the identity of a container image.
func ContainerComponent(image string) component {
	k := componentKey(kindContainer, image)
	return component{kind: kindContainer, key: k, legacyKey: k}
}

// ActiveKeys returns the component keys (current and legacy) of the given findings, for
// use with CloseResolved. Extra components can be added with Add.
type ActiveKeys map[string]bool

// Add marks a component as still present.
func (a ActiveKeys) Add(c component) {
	a[c.key] = true
	a[c.legacyKey] = true
}

// AddHelm marks a Helm release as still present.
func (a ActiveKeys) AddHelm(release nova.ReleaseOutput) { a.Add(HelmComponent(release)) }

// AddImage marks a container image (given with or without tag/digest) as still present.
func (a ActiveKeys) AddImage(image string) { a.Add(ContainerComponent(nova.ImageRepo(image))) }

func withMarker(body, key string) string {
	return strings.TrimRight(body, "\n") + "\n\n<!-- nova-scanner:key=" + key + " -->\n"
}

func (im *IssueManager) ensureIssue(ctx context.Context, comp component, title, body string, labels []string) (IssueResult, error) {
	if err := im.loadIndex(ctx); err != nil {
		return IssueResult{}, fmt.Errorf("failed to load existing issues: %w", err)
	}
	idx := im.index
	kind := comp.kind

	open := idx.openByKey[comp.key]
	if len(open) == 0 && comp.legacyKey != comp.key {
		open = idx.openByKey[comp.legacyKey]
	}

	for _, existing := range open {
		if existing.title == title {
			im.logger.IssueSkipped(kind, title, "duplicate")
			return IssueResult{Action: ActionSkipped, URL: existing.url, Reason: "duplicate"}, nil
		}
	}
	if idx.dismissed[comp.key+"\x00"+title] || idx.legacyDismissed[title] {
		im.logger.IssueSkipped(kind, title, "dismissed")
		return IssueResult{Action: ActionSkipped, Reason: "dismissed"}, nil
	}

	body = withMarker(body, comp.key)
	if len(open) > 0 {
		return im.updateExisting(ctx, comp, title, body, open)
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

	idx.add(&trackedIssue{number: issue.GetNumber(), title: title, url: issue.GetHTMLURL(), key: comp.key})
	im.logger.IssueCreated(kind, title, issue.GetHTMLURL())
	return IssueResult{Action: ActionCreated, URL: issue.GetHTMLURL()}, nil
}

// CloseResolved closes open nova-scan issues of the given kind whose component is not in
// active (i.e. it was updated, removed or is now ignored). Issues that cannot be
// attributed to a component are left alone. It returns the number of closed issues.
//
// Callers must only invoke this after a complete, successful scan of that kind, and must
// add every component whose state is unknown (scan errors, unresolved versions) to active.
func (im *IssueManager) CloseResolved(ctx context.Context, kind string, active ActiveKeys) (int, error) {
	if err := im.loadIndex(ctx); err != nil {
		return 0, fmt.Errorf("failed to load existing issues: %w", err)
	}
	prefix := kind + "|"
	var stale []*trackedIssue
	for key, issues := range im.index.openByKey {
		if !strings.HasPrefix(key, prefix) || active[key] {
			continue
		}
		stale = append(stale, issues...)
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].number < stale[j].number })

	closed := 0
	for _, t := range stale {
		if im.dryRun {
			im.logger.IssueDryRun(kind, fmt.Sprintf("close resolved #%d %q", t.number, t.title))
			continue
		}
		err := im.comment(ctx, t.number, "Closing: nova-scanner no longer reports this component as outdated (it was updated, removed, or is now ignored).")
		if err != nil {
			im.logger.Warn().Err(err).Int("issue", t.number).Msg("Failed to comment on resolved issue")
			continue
		}
		err = im.withRetry(ctx, "close issue", func() (*github.Response, error) {
			_, resp, err := im.client.Issues.Edit(ctx, im.owner, im.repo, t.number, &github.IssueRequest{
				State:       github.String("closed"),
				StateReason: github.String("completed"),
			})
			return resp, err
		})
		if err != nil {
			im.logger.Warn().Err(err).Int("issue", t.number).Msg("Failed to close resolved issue")
			continue
		}
		im.index.remove(t)
		im.logger.IssueResolved(t.number, t.title)
		closed++
	}
	return closed, nil
}

// updateExisting retitles the newest open issue for the component and closes older ones.
// Updating in place keeps labels (e.g. risk assessments) and discussion on one issue.
func (im *IssueManager) updateExisting(ctx context.Context, comp component, title, body string, open []*trackedIssue) (IssueResult, error) {
	kind, key := comp.kind, comp.key
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

	for _, o := range open {
		im.index.remove(o)
	}
	keep.title = title
	keep.key = key
	im.index.add(keep)

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
		openByKey:       map[string][]*trackedIssue{},
		dismissed:       map[string]bool{},
		legacyDismissed: map[string]bool{},
	}

	open := 0
	err := im.listIssues(ctx, "open", func(is *github.Issue) {
		t := &trackedIssue{number: is.GetNumber(), title: is.GetTitle(), url: is.GetHTMLURL(), key: issueKey(is)}
		idx.add(t)
		open++
	})
	if err != nil {
		return err
	}

	err = im.listIssues(ctx, "closed", func(is *github.Issue) {
		if is.GetStateReason() != "not_planned" {
			return
		}
		if m := markerPattern.FindStringSubmatch(is.GetBody()); m != nil {
			idx.dismissed[m[1]+"\x00"+is.GetTitle()] = true
		} else {
			idx.legacyDismissed[is.GetTitle()] = true
		}
	})
	if err != nil {
		return err
	}

	im.index = idx
	im.logger.Debug().Int("open", open).Int("dismissed", len(idx.dismissed)+len(idx.legacyDismissed)).Msg("Loaded existing nova-scan issues")
	return nil
}

// issueKey returns the component key of an issue: the body marker if present, otherwise
// the legacy key parsed from the title ("" if the issue is not recognisable).
func issueKey(is *github.Issue) string {
	if m := markerPattern.FindStringSubmatch(is.GetBody()); m != nil {
		return m[1]
	}
	if kind, name, ok := parseIssueTitle(is.GetTitle()); ok {
		return componentKey(kind, name)
	}
	return ""
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
	if t.key != "" {
		idx.openByKey[t.key] = append(idx.openByKey[t.key], t)
	}
}

func (idx *issueIndex) remove(t *trackedIssue) {
	if t.key == "" {
		return
	}
	list := idx.openByKey[t.key]
	for i, o := range list {
		if o == t {
			idx.openByKey[t.key] = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(idx.openByKey[t.key]) == 0 {
		delete(idx.openByKey, t.key)
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
%s
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
		formatSourceRow(release.Source),
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
%s
### Affected Workloads

%s
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
		formatAlternativeTags(container),
		workloadTable,
		formatHelmOwners(container.HelmReleases),
	)
}

func formatSourceRow(source string) string {
	if source == "" {
		return ""
	}
	return fmt.Sprintf("| Version Source | %s |\n", source)
}

// formatAlternativeTags lists the other candidate tags and further running tags.
func formatAlternativeTags(c nova.ContainerOutput) string {
	var sb strings.Builder
	row := func(label, tag string) {
		if tag != "" && tag != c.CurrentTag && tag != c.LatestTag {
			sb.WriteString(fmt.Sprintf("| %s | %s |\n", label, backtick(tag)))
		}
	}
	row("Newest Tag (any major)", c.OverallLatestTag)
	row("Newest Tag (same major)", c.LatestMinorTag)
	row("Newest Tag (same minor)", c.LatestPatchTag)
	if len(c.OtherCurrentTags) > 0 {
		tags := make([]string, len(c.OtherCurrentTags))
		for i, t := range c.OtherCurrentTags {
			tags[i] = backtick(t)
		}
		sb.WriteString(fmt.Sprintf("| Other Tags In Use | %s |\n", strings.Join(tags, ", ")))
	}
	return sb.String()
}

func formatHelmOwners(refs []nova.HelmRef) string {
	if len(refs) == 0 {
		return ""
	}
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = backtick(r.Namespace + "/" + r.Release)
	}
	return fmt.Sprintf("\n### Helm Ownership\n\nManaged by Helm release %s. Update the chart if a newer chart ships this image, otherwise override the image tag in the release values.\n", strings.Join(names, ", "))
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
