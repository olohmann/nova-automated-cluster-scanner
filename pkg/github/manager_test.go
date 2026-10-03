package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gh "github.com/google/go-github/v57/github"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/nova"
)

type fakeIssue struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	StateReason string `json:"state_reason,omitempty"`
	HTMLURL     string `json:"html_url"`
}

// fakeGitHub is a minimal in-memory GitHub issues API.
type fakeGitHub struct {
	mu        sync.Mutex
	issues    []*fakeIssue
	next      int
	creates   int
	edits     map[int][]map[string]any
	comments  map[int][]string
	listCalls int
	// failCreates makes the first N create calls fail with a secondary rate limit.
	failCreates int
}

func newFakeGitHub(issues ...*fakeIssue) *fakeGitHub {
	f := &fakeGitHub{next: 1000, edits: map[int][]map[string]any{}, comments: map[int][]string{}}
	for _, is := range issues {
		if is.HTMLURL == "" {
			is.HTMLURL = fmt.Sprintf("https://github.com/o/r/issues/%d", is.Number)
		}
		f.issues = append(f.issues, is)
	}
	return f
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/repos/o/r/issues")
	switch {
	case r.Method == http.MethodGet && path == "":
		f.listCalls++
		state := r.URL.Query().Get("state")
		var out []*fakeIssue
		for _, is := range f.issues {
			if is.State == state {
				out = append(out, is)
			}
		}
		_ = json.NewEncoder(w).Encode(out)

	case r.Method == http.MethodPost && path == "":
		if f.failCreates > 0 {
			f.failCreates--
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit","documentation_url":"https://docs.github.com/rest/overview/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`))
			return
		}
		var req struct{ Title string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.next++
		f.creates++
		is := &fakeIssue{Number: f.next, Title: req.Title, State: "open", HTMLURL: fmt.Sprintf("https://github.com/o/r/issues/%d", f.next)}
		f.issues = append(f.issues, is)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(is)

	case r.Method == http.MethodPatch:
		n, _ := strconv.Atoi(strings.TrimPrefix(path, "/"))
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.edits[n] = append(f.edits[n], req)
		for _, is := range f.issues {
			if is.Number == n {
				if t, ok := req["title"].(string); ok {
					is.Title = t
				}
				if s, ok := req["state"].(string); ok {
					is.State = s
				}
				_ = json.NewEncoder(w).Encode(is)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)

	case r.Method == http.MethodPost && strings.HasSuffix(path, "/comments"):
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/comments"))
		var req struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.comments[n] = append(f.comments[n], req.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))

	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotImplemented)
	}
}

func newTestManager(t *testing.T, f *fakeGitHub, dryRun bool) *IssueManager {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	client := gh.NewClient(nil)
	u, _ := url.Parse(srv.URL + "/")
	client.BaseURL = u
	im := newIssueManagerWithClient(client, "o", "r", dryRun, logging.NewLogger("error"))
	im.sleep = func(context.Context, time.Duration) error { return nil }
	return im
}

func container(name, current, latest string) nova.ContainerOutput {
	return nova.ContainerOutput{Name: name, CurrentTag: current, LatestTag: latest}
}

func TestParseIssueTitle(t *testing.T) {
	tests := []struct {
		title    string
		kind     string
		name     string
		wantOkay bool
	}{
		{"[Nova] Update Helm chart: kutt (9.6.13 → 9.11.2)", kindHelm, "kutt", true},
		{"[Nova] Update container image: ghcr.io/fluxcd/helm-controller (v1.5.2 → v1.6.5)", kindContainer, "ghcr.io/fluxcd/helm-controller", true},
		{"[Nova] Update container image: alpine/k8s (1.32.13 -> 1.37.1)", kindContainer, "alpine/k8s", true},
		{"[Nova] Update Helm chart: homepage (4.14.0+2a3a2f7bbf30 → 5.4.0)", kindHelm, "homepage", true},
		{"Some unrelated issue", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			kind, name, ok := parseIssueTitle(tt.title)
			if ok != tt.wantOkay || kind != tt.kind || name != tt.name {
				t.Errorf("parseIssueTitle(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.title, kind, name, ok, tt.kind, tt.name, tt.wantOkay)
			}
		})
	}
}

func TestEnsureIssue_CreatesNewIssue(t *testing.T) {
	f := newFakeGitHub()
	im := newTestManager(t, f, false)

	res, err := im.CreateContainerIssue(context.Background(), container("redis", "7.2", "7.4"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionCreated || f.creates != 1 {
		t.Fatalf("got action %q with %d creates, want created/1", res.Action, f.creates)
	}

	// Same finding again in the same run: index is updated, no second issue.
	res, err = im.CreateContainerIssue(context.Background(), container("redis", "7.2", "7.4"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionSkipped || f.creates != 1 {
		t.Fatalf("second call: got action %q with %d creates, want skipped/1", res.Action, f.creates)
	}
}

func TestEnsureIssue_SkipsExactDuplicate(t *testing.T) {
	f := newFakeGitHub(&fakeIssue{Number: 1, Title: "[Nova] Update container image: redis (7.2 → 7.4)", State: "open"})
	im := newTestManager(t, f, false)

	res, err := im.CreateContainerIssue(context.Background(), container("redis", "7.2", "7.4"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionSkipped || res.Reason != "duplicate" || f.creates != 0 {
		t.Fatalf("got %+v with %d creates, want skipped duplicate", res, f.creates)
	}
}

func TestEnsureIssue_SkipsDismissed(t *testing.T) {
	f := newFakeGitHub(
		&fakeIssue{Number: 1, Title: "[Nova] Update container image: ghcr.io/netbootxyz/netbootxyz (0.7.6-nbxyz24 → 0.7.6-nbxyz9)", State: "closed", StateReason: "not_planned"},
		&fakeIssue{Number: 2, Title: "[Nova] Update container image: redis (7.2 → 7.4)", State: "closed", StateReason: "completed"},
	)
	im := newTestManager(t, f, false)

	res, err := im.CreateContainerIssue(context.Background(), container("ghcr.io/netbootxyz/netbootxyz", "0.7.6-nbxyz24", "0.7.6-nbxyz9"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionSkipped || res.Reason != "dismissed" {
		t.Fatalf("got %+v, want skipped dismissed", res)
	}

	// Closed as completed is not a dismissal: the finding is reported again.
	res, err = im.CreateContainerIssue(context.Background(), container("redis", "7.2", "7.4"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionCreated {
		t.Fatalf("got %+v, want created for completed issue", res)
	}
}

func TestEnsureIssue_UpdatesExistingComponentIssue(t *testing.T) {
	f := newFakeGitHub(
		&fakeIssue{Number: 5, Title: "[Nova] Update Helm chart: kutt (9.6.13 → 9.10.0)", State: "open"},
		&fakeIssue{Number: 9, Title: "[Nova] Update Helm chart: kutt (9.6.13 → 9.11.1)", State: "open"},
		&fakeIssue{Number: 7, Title: "[Nova] Update Helm chart: other (1.0.0 → 1.1.0)", State: "open"},
	)
	im := newTestManager(t, f, false)

	release := nova.ReleaseOutput{
		ReleaseName: "kutt",
		ChartName:   "kutt",
		Namespace:   "kutt",
		Installed:   nova.VersionInfo{Version: "9.6.13"},
		Latest:      nova.VersionInfo{Version: "9.11.2"},
	}
	res, err := im.CreateHelmIssue(context.Background(), release)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionUpdated || f.creates != 0 {
		t.Fatalf("got %+v with %d creates, want updated without create", res, f.creates)
	}

	// Newest issue (#9) is retitled and gets a comment.
	if len(f.edits[9]) != 1 || f.edits[9][0]["title"] != "[Nova] Update Helm chart: kutt (9.6.13 → 9.11.2)" {
		t.Errorf("issue #9 edits = %v, want retitle to 9.11.2", f.edits[9])
	}
	if len(f.comments[9]) != 1 {
		t.Errorf("issue #9 comments = %v, want 1", f.comments[9])
	}

	// Older issue (#5) is closed as superseded by #9.
	if len(f.edits[5]) != 1 || f.edits[5][0]["state"] != "closed" || f.edits[5][0]["state_reason"] != "not_planned" {
		t.Errorf("issue #5 edits = %v, want closed not_planned", f.edits[5])
	}
	if len(f.comments[5]) != 1 || !strings.Contains(f.comments[5][0], "superseded by #9") {
		t.Errorf("issue #5 comments = %v, want superseded note", f.comments[5])
	}

	// Unrelated component untouched.
	if len(f.edits[7]) != 0 || len(f.comments[7]) != 0 {
		t.Errorf("issue #7 should be untouched, got edits=%v comments=%v", f.edits[7], f.comments[7])
	}

	// Re-running with the same version is now an exact duplicate.
	res, err = im.CreateHelmIssue(context.Background(), release)
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionSkipped {
		t.Fatalf("re-run: got %+v, want skipped", res)
	}
}

func TestEnsureIssue_HelmAndContainerWithSameNameAreSeparate(t *testing.T) {
	f := newFakeGitHub(&fakeIssue{Number: 3, Title: "[Nova] Update Helm chart: coredns (1.47.0 → 1.48.2)", State: "open"})
	im := newTestManager(t, f, false)

	res, err := im.CreateContainerIssue(context.Background(), container("coredns", "1.14.6", "1.14.7"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != ActionCreated || len(f.edits[3]) != 0 {
		t.Fatalf("got %+v, edits on #3 = %v; want new container issue and helm issue untouched", res, f.edits[3])
	}
}

func TestEnsureIssue_DryRunMakesNoWrites(t *testing.T) {
	f := newFakeGitHub(&fakeIssue{Number: 4, Title: "[Nova] Update container image: redis (7.2 → 7.4)", State: "open"})
	im := newTestManager(t, f, true)

	for _, c := range []nova.ContainerOutput{container("redis", "7.2", "8.0"), container("nginx", "1.27", "1.29")} {
		res, err := im.CreateContainerIssue(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if res.Action != ActionDryRun {
			t.Errorf("%s: got %+v, want dry_run", c.Name, res)
		}
	}
	if f.creates != 0 || len(f.edits) != 0 || len(f.comments) != 0 {
		t.Fatalf("dry run wrote to GitHub: creates=%d edits=%v comments=%v", f.creates, f.edits, f.comments)
	}
}

func TestEnsureIssue_LoadsIndexOnce(t *testing.T) {
	f := newFakeGitHub()
	im := newTestManager(t, f, false)

	for i := 0; i < 5; i++ {
		if _, err := im.CreateContainerIssue(context.Background(), container(fmt.Sprintf("img%d", i), "1.0", "1.1")); err != nil {
			t.Fatal(err)
		}
	}
	// One list call for open + one for closed issues, regardless of the number of findings.
	if f.listCalls != 2 {
		t.Fatalf("listCalls = %d, want 2", f.listCalls)
	}
}

func TestEnsureIssue_RetriesSecondaryRateLimit(t *testing.T) {
	f := newFakeGitHub()
	f.failCreates = 2
	im := newTestManager(t, f, false)
	// go-github refuses requests locally until Retry-After (1s here) has passed, so this
	// test must really wait, exactly as production does.
	im.sleep = sleepCtx

	res, err := im.CreateContainerIssue(context.Background(), container("redis", "7.2", "7.4"))
	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if res.Action != ActionCreated || f.creates != 1 {
		t.Fatalf("got %+v with %d creates, want created/1", res, f.creates)
	}
}

func TestEnsureIssue_GivesUpAfterMaxRetries(t *testing.T) {
	f := newFakeGitHub()
	f.failCreates = 10
	im := newTestManager(t, f, false)

	if _, err := im.CreateContainerIssue(context.Background(), container("redis", "7.2", "7.4")); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if f.creates != 0 {
		t.Fatalf("creates = %d, want 0", f.creates)
	}
}
