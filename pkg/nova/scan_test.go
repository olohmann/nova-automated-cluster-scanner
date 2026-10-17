package nova

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/olohmann/nova-automated-cluster-scanner/pkg/config"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
)

func testConfig() *config.Config {
	return &config.Config{
		MinSeverity:          "minor",
		NovaTimeoutSeconds:   120,
		ContainerUpdateScope: "any",
		IgnoreWorkloadKinds:  []string{"Node"},
		PollArtifactHub:      true,
	}
}

// newFixtureScanner returns a scanner whose nova binary is replaced by a fixture; the
// executed arguments are recorded in *calls.
func newFixtureScanner(t *testing.T, cfg *config.Config, fixture string, calls *[][]string) *Scanner {
	t.Helper()
	data, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewScanner(cfg, logging.NewLogger("error"))
	if err != nil {
		t.Fatal(err)
	}
	s.run = func(_ context.Context, args []string) ([]byte, error) {
		if calls != nil {
			*calls = append(*calls, args)
		}
		return data, nil
	}
	return s
}

func outdatedImages(r *ContainerScanResult) map[string]string {
	m := map[string]string{}
	for _, c := range r.Outdated {
		m[c.Name] = c.CurrentTag + "->" + c.LatestTag
	}
	return m
}

func TestScanContainers_FiltersFalsePositivesFromRealOutput(t *testing.T) {
	var calls [][]string
	s := newFixtureScanner(t, testConfig(), "containers.json", &calls)

	res, err := s.ScanContainers(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := outdatedImages(res)

	for _, name := range []string{
		"ghcr.io/netbootxyz/netbootxyz",         // nova proposes -nbxyz9 for -nbxyz24 (a downgrade)
		"docker.io/memcached",                   // -alpine variant would be replaced by the bare tag
		"docker.io/nginxinc/nginx-unprivileged", // same
		"registry.k8s.io/kube-apiserver",        // static pod owned by a Node
	} {
		if _, ok := got[name]; ok {
			t.Errorf("%s must not be reported, got %v", name, got[name])
		}
	}
	for name, want := range map[string]string{
		"ghcr.io/fluxcd/source-controller": "v1.8.1->v1.9.6",
		"docker.io/grafana/loki":           "2.9.13->3.7.8",
	} {
		if got[name] != want {
			t.Errorf("%s = %q, want %q", name, got[name], want)
		}
	}

	if len(res.Errored) != 1 || !strings.Contains(res.Errored[0].Image, "minio") {
		t.Errorf("expected the errored minio image to be surfaced, got %+v", res.Errored)
	}

	args := strings.Join(calls[0], " ")
	for _, want := range []string{"--containers", "--show-errored-containers", "--timeout 120"} {
		if !strings.Contains(args, want) {
			t.Errorf("nova args %q missing %q", args, want)
		}
	}
}

func TestScanContainers_UpdateScope(t *testing.T) {
	cfg := testConfig()
	cfg.ContainerUpdateScope = "minor"
	cfg.ImageUpdateScope = map[string]string{"*/loki": "patch"}
	s := newFixtureScanner(t, cfg, "containers.json", nil)

	res, err := s.ScanContainers(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := outdatedImages(res)

	if _, ok := got["ghcr.io/gethomepage/homepage"]; ok {
		t.Errorf("major-only update must not be reported in minor scope: %v", got["ghcr.io/gethomepage/homepage"])
	}
	if got["docker.io/grafana/loki"] != "2.9.13->2.9.17" {
		t.Errorf("loki = %q", got["docker.io/grafana/loki"])
	}
	if got["ghcr.io/fluxcd/source-controller"] != "v1.8.1->v1.9.6" {
		t.Errorf("source-controller = %q", got["ghcr.io/fluxcd/source-controller"])
	}
}

func TestScanContainers_AppliesMinSeverity(t *testing.T) {
	cfg := testConfig()
	cfg.MinSeverity = "critical"
	s := newFixtureScanner(t, cfg, "containers.json", nil)

	res, err := s.ScanContainers(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := outdatedImages(res)
	if _, ok := got["docker.io/grafana/alloy"]; ok {
		t.Error("patch update must be filtered with minSeverity=critical")
	}
	if got["ghcr.io/gethomepage/homepage"] == "" {
		t.Error("major update must pass minSeverity=critical")
	}
}

func TestScanContainers_FallsBackWhenLatestIsBlacklisted(t *testing.T) {
	cfg := testConfig()
	cfg.IgnoreVersionPatterns = []string{"v1.9.6"}
	s := newFixtureScanner(t, cfg, "containers.json", nil)

	res, err := s.ScanContainers(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// latest is blacklisted, the patch release of the current minor is still proposed
	if got := outdatedImages(res)["ghcr.io/fluxcd/source-controller"]; got != "v1.8.1->v1.8.5" {
		t.Errorf("source-controller = %q", got)
	}
}

type fakeOwners map[string]HelmRef

func (f fakeOwners) HelmRelease(_ context.Context, w WorkloadOutput) (HelmRef, bool) {
	r, ok := f[w.Namespace+"/"+w.Name]
	return r, ok
}

func TestScanContainers_SkipsOnlyContainersOwnedByOutdatedReleases(t *testing.T) {
	s := newFixtureScanner(t, testConfig(), "containers.json", nil)
	var alloy, loki WorkloadOutput
	data, _ := parseNovaOutput(mustRead(t, "testdata/containers.json"))
	for _, c := range data.Containers {
		switch c.Name {
		case "docker.io/grafana/alloy":
			alloy = c.AffectedWorkloads[0]
		case "docker.io/grafana/loki":
			loki = c.AffectedWorkloads[0]
		}
	}
	s.SetWorkloadOwnerResolver(fakeOwners{
		alloy.Namespace + "/" + alloy.Name: {Release: "alloy", Namespace: "alloy"},
		loki.Namespace + "/" + loki.Name:   {Release: "lgtm", Namespace: "lgtm"},
	})

	res, err := s.ScanContainers(context.Background(), map[string]bool{"alloy/alloy": true})
	if err != nil {
		t.Fatal(err)
	}
	got := outdatedImages(res)
	if _, ok := got["docker.io/grafana/alloy"]; ok {
		t.Error("alloy image belongs to the outdated alloy release and must be skipped")
	}
	if _, ok := got["docker.io/grafana/loki"]; !ok {
		t.Error("loki belongs to an up-to-date release; its container update must still be reported")
	}
	if _, ok := got["ghcr.io/fluxcd/source-controller"]; !ok {
		t.Error("unowned workloads must not be suppressed by namespace")
	}
	if len(res.Skipped) != 1 {
		t.Errorf("expected 1 skipped container, got %d", len(res.Skipped))
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type fakeCharts map[string]ChartResolution

func (f fakeCharts) Resolve(_ context.Context, r ReleaseOutput) (ChartResolution, bool) {
	res, ok := f[r.Key()]
	return res, ok
}

func TestScanHelm_UsesResolverAndReportsUnresolved(t *testing.T) {
	s := newFixtureScanner(t, testConfig(), "helm.json", nil)
	s.SetChartResolver(fakeCharts{
		// OCI charts that nova mismatched against unrelated ArtifactHub packages
		"envoy-gateway-system/envoy-gateway": {Latest: "1.9.2", Source: "flux:ocirepository"},
		"homepage/homepage":                  {Latest: "4.14.0", Source: "flux:ocirepository"},
		"synology-csi/synology-csi":          {Unresolvable: "chart is installed from a GitRepository", Source: "flux:gitrepository"},
	})

	res, err := s.ScanHelm(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	outdated := map[string]string{}
	for _, r := range res.Outdated {
		outdated[r.ReleaseName] = r.Installed.Version + "->" + r.Latest.Version
	}
	for _, name := range []string{"envoy-gateway", "homepage", "cert-manager"} {
		if _, ok := outdated[name]; ok {
			t.Errorf("%s must not be reported: %v", name, outdated[name])
		}
	}
	if outdated["alloy"] != "1.13.0->1.13.1" || outdated["kutt"] != "9.11.2->10.0.0" {
		t.Errorf("expected alloy and kutt to stay outdated, got %v", outdated)
	}

	unresolved := map[string]bool{}
	for _, r := range res.Unresolved {
		unresolved[r.ReleaseName] = true
	}
	if !unresolved["synology-csi"] || !unresolved["cloudspire"] || len(unresolved) != 2 {
		t.Errorf("unexpected unresolved set: %v", unresolved)
	}
}

func TestScanHelm_PassesNamespacesAndDesiredVersions(t *testing.T) {
	cfg := testConfig()
	cfg.Namespaces = []string{"alloy", "kutt"}
	cfg.DesiredVersions = map[string]string{"kutt": "9.11.2", "alloy": "1.13.0"}
	var calls [][]string
	s := newFixtureScanner(t, cfg, "helm.json", &calls)

	res, err := s.ScanHelm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected one nova call per namespace, got %d", len(calls))
	}
	if a := strings.Join(calls[0], " "); !strings.Contains(a, "--namespace alloy") || !strings.Contains(a, "--desired-versions alloy=1.13.0,kutt=9.11.2") {
		t.Errorf("unexpected args: %s", a)
	}
	// identical releases returned by both calls are deduplicated
	if len(res.AllReleases) != 7 {
		t.Errorf("expected 7 releases, got %d", len(res.AllReleases))
	}
}

func TestParseNovaOutput_EmptyMarkers(t *testing.T) {
	for _, in := range []string{"No releases found\n", "No images found", "", "  \n"} {
		out, err := parseNovaOutput([]byte(in))
		if err != nil || len(out.HelmReleases) != 0 || len(out.Containers) != 0 {
			t.Errorf("parseNovaOutput(%q) = %+v, %v; want empty", in, out, err)
		}
	}
	if _, err := parseNovaOutput([]byte("{broken")); err == nil {
		t.Error("expected parse error for invalid JSON")
	}
}

func TestGroupByImage_MergesTagsOfSameImage(t *testing.T) {
	in := []ContainerOutput{
		{Name: "postgres", CurrentTag: "16.1", LatestTag: "16.4", AffectedWorkloads: []WorkloadOutput{{Name: "a"}}},
		{Name: "postgres", CurrentTag: "15.2", LatestTag: "16.4", AffectedWorkloads: []WorkloadOutput{{Name: "b"}}},
		{Name: "redis", CurrentTag: "7.0.0", LatestTag: "7.2.0"},
	}
	out := groupByImage(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 images, got %d", len(out))
	}
	pg := out[0]
	if pg.CurrentTag != "15.2" || len(pg.OtherCurrentTags) != 1 || pg.OtherCurrentTags[0] != "16.1" || len(pg.AffectedWorkloads) != 2 {
		t.Errorf("unexpected merge result: %+v", pg)
	}
}

func TestImageRepo(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io/a/b:1.2":                          "ghcr.io/a/b",
		"docker.io/envoyproxy/envoy:v1@sha256:abc": "docker.io/envoyproxy/envoy",
		"localhost:5000/foo":                       "localhost:5000/foo",
		"busybox":                                  "busybox",
	} {
		if got := ImageRepo(in); got != want {
			t.Errorf("ImageRepo(%q) = %q, want %q", in, got, want)
		}
	}
}
