package nova

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olohmann/nova-automated-cluster-scanner/pkg/config"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/version"
)

// Chart version sources reported in ReleaseOutput.Source.
const (
	SourceArtifactHub = "artifacthub"
	SourceDesired     = "desired-versions"
)

// Scanner wraps Nova CLI functionality.
type Scanner struct {
	config *config.Config
	logger *logging.Logger

	chartResolver ChartResolver
	ownerResolver WorkloadOwnerResolver

	// run executes the nova binary; replaced in tests.
	run func(ctx context.Context, args []string) ([]byte, error)
}

// ChartResolution is the authoritative latest version of a chart's source.
type ChartResolution struct {
	// Latest is the newest stable chart version ("" if it could not be determined).
	Latest string
	// Source describes where Latest came from (e.g. "flux:helmrepository").
	Source string
	// Unresolvable explains why the source cannot be checked (e.g. a GitRepository).
	Unresolvable string
}

// ChartResolver looks up the latest chart version for a release from its real source,
// instead of Nova's name-based ArtifactHub guess. found is false if the resolver knows
// nothing about the release.
type ChartResolver interface {
	Resolve(ctx context.Context, release ReleaseOutput) (res ChartResolution, found bool)
}

// HelmRef identifies a Helm release.
type HelmRef struct {
	Release   string
	Namespace string
}

// Key returns "namespace/release".
func (r HelmRef) Key() string { return r.Namespace + "/" + r.Release }

// WorkloadOwnerResolver maps a workload to the Helm release that manages it.
type WorkloadOwnerResolver interface {
	HelmRelease(ctx context.Context, w WorkloadOutput) (ref HelmRef, ok bool)
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

	// Source is where Latest was determined (set by the scanner).
	Source string `json:"-"`
	// Unresolved is set when the latest version cannot be determined reliably.
	Unresolved       bool   `json:"-"`
	UnresolvedReason string `json:"-"`
}

// Key returns "namespace/release".
func (r ReleaseOutput) Key() string {
	return HelmRef{Release: r.ReleaseName, Namespace: r.Namespace}.Key()
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
	LatestMinorTag    string           `json:"latest_minor_version"`
	LatestPatchTag    string           `json:"latest_patch_version"`
	IsOld             bool             `json:"outdated"`
	AffectedWorkloads []WorkloadOutput `json:"affectedWorkloads"`

	// OverallLatestTag is Nova's newest tag; LatestTag is replaced by the update target
	// selected by the scanner (see containerUpdateScope).
	OverallLatestTag string `json:"-"`
	// OtherCurrentTags lists further tags of the same image running in the cluster.
	OtherCurrentTags []string `json:"-"`
	// HelmReleases lists the Helm releases that own the affected workloads.
	HelmReleases []HelmRef `json:"-"`
}

// WorkloadOutput represents a Kubernetes workload.
type WorkloadOutput struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Container string `json:"container"`
}

// ErroredImage is an image Nova could not check (registry error, timeout, ...).
type ErroredImage struct {
	Image string `json:"Image"`
	Err   string `json:"Err"`
}

// NovaOutput represents the full Nova JSON output.
type NovaOutput struct {
	HelmReleases []ReleaseOutput   `json:"helm_releases"`
	Helm         []ReleaseOutput   `json:"helm"`
	Containers   []ContainerOutput `json:"container_images"`
	ErrImages    []ErroredImage    `json:"err_images"`
}

// HelmScanResult contains the results of a Helm scan.
type HelmScanResult struct {
	AllReleases []ReleaseOutput
	Outdated    []ReleaseOutput
	// Unresolved releases could not be checked (no reliable latest version).
	Unresolved []ReleaseOutput
	Duration   time.Duration
}

// OutdatedNamespaces returns a set of namespaces that have outdated Helm releases.
func (r *HelmScanResult) OutdatedNamespaces() map[string]bool {
	namespaces := make(map[string]bool)
	for _, release := range r.Outdated {
		namespaces[release.Namespace] = true
	}
	return namespaces
}

// OutdatedReleases returns the "namespace/release" keys of the outdated releases.
func (r *HelmScanResult) OutdatedReleases() map[string]bool {
	keys := make(map[string]bool, len(r.Outdated))
	for _, release := range r.Outdated {
		keys[release.Key()] = true
	}
	return keys
}

// ContainerScanResult contains the results of a container scan.
type ContainerScanResult struct {
	AllContainers []ContainerOutput
	Outdated      []ContainerOutput
	Skipped       []ContainerOutput // Containers skipped because their Helm release is itself reported as outdated
	Errored       []ErroredImage    // Images Nova could not check; their state is unknown
	Duration      time.Duration
}

// NewScanner creates a new Scanner instance.
func NewScanner(cfg *config.Config, logger *logging.Logger) (*Scanner, error) {
	s := &Scanner{
		config: cfg,
		logger: logger.WithComponent("nova"),
	}
	s.run = s.execNova
	return s, nil
}

// SetChartResolver sets the resolver that overrides Nova's ArtifactHub guess for Helm releases.
func (s *Scanner) SetChartResolver(r ChartResolver) { s.chartResolver = r }

// SetWorkloadOwnerResolver sets the resolver used to map containers to Helm releases.
func (s *Scanner) SetWorkloadOwnerResolver(r WorkloadOwnerResolver) { s.ownerResolver = r }

func (s *Scanner) execNova(ctx context.Context, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "nova", args...)
	s.logger.Debug().Strs("args", args).Msg("Executing nova command")
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			s.logger.Error().
				Str("stderr", string(exitErr.Stderr)).
				Strs("args", args).
				Err(err).
				Msg("Nova command failed")
		}
		return nil, fmt.Errorf("nova command failed: %w", err)
	}
	return output, nil
}

// commonArgs returns the kubeconfig/context arguments shared by all nova invocations.
func (s *Scanner) commonArgs() []string {
	var args []string
	if kubeconfig := getKubeconfig(s.config.Kubeconfig); kubeconfig != "" {
		args = append(args, "--kubeconfig", kubeconfig)
	}
	if s.config.Context != "" {
		args = append(args, "--context", s.config.Context)
	}
	return args
}

// namespaces returns the namespaces to scan; "" means cluster-wide.
func (s *Scanner) namespaces() []string {
	if len(s.config.Namespaces) == 0 {
		return []string{""}
	}
	return s.config.Namespaces
}

// parseNovaOutput parses Nova's JSON. Nova prints a bare array for helm-only scans, an
// object for container scans, and plain text ("No releases found") when empty.
func parseNovaOutput(output []byte) (NovaOutput, error) {
	var out NovaOutput
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 || bytes.HasPrefix(trimmed, []byte("No ")) {
		return out, nil
	}
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &out.HelmReleases); err != nil {
			return out, fmt.Errorf("failed to parse nova output: %w", err)
		}
		return out, nil
	}
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return out, fmt.Errorf("failed to parse nova output: %w", err)
	}
	out.HelmReleases = append(out.HelmReleases, out.Helm...)
	return out, nil
}

// ScanHelm scans for outdated Helm releases using Nova CLI.
func (s *Scanner) ScanHelm(ctx context.Context) (*HelmScanResult, error) {
	s.logger.ScanStart("helm")
	start := time.Now()

	var releases []ReleaseOutput
	seen := map[string]bool{}
	for _, ns := range s.namespaces() {
		args := []string{"find", "--format", "json", "--helm", "--include-all"}
		if s.config.PollArtifactHub {
			args = append(args, "--poll-artifacthub")
		}
		if ns != "" {
			args = append(args, "--namespace", ns)
		}
		if len(s.config.DesiredVersions) > 0 {
			args = append(args, "--desired-versions", desiredVersionsArg(s.config.DesiredVersions))
		}
		args = append(args, s.commonArgs()...)

		output, err := s.run(ctx, args)
		if err != nil {
			s.logger.ScanError("helm", err)
			return nil, err
		}
		parsed, err := parseNovaOutput(output)
		if err != nil {
			s.logger.ScanError("helm", err)
			return nil, err
		}
		for _, r := range parsed.HelmReleases {
			if seen[r.Key()] {
				continue
			}
			seen[r.Key()] = true
			releases = append(releases, r)
		}
	}

	var filtered, outdated, unresolved []ReleaseOutput
	for _, release := range releases {
		if s.shouldIgnoreRelease(release) {
			continue
		}
		release = s.resolveRelease(ctx, release)
		filtered = append(filtered, release)

		if release.Unresolved {
			unresolved = append(unresolved, release)
			s.logger.Warn().
				Str("release", release.ReleaseName).
				Str("namespace", release.Namespace).
				Str("chart", release.ChartName).
				Str("installed", release.Installed.Version).
				Str("reason", release.UnresolvedReason).
				Msg("Cannot determine latest chart version; release is not checked")
			continue
		}
		if !release.IsOld {
			continue
		}
		if s.config.ShouldIgnoreChartVersion(release.ChartName, release.Latest.Version) {
			s.logger.Debug().
				Str("release", release.ReleaseName).
				Str("chart", release.ChartName).
				Str("latestVersion", release.Latest.Version).
				Msg("Skipping release: latest version matches blacklist pattern")
			continue
		}
		if !s.meetsMinSeverity(release.Installed.Version, release.Latest.Version) {
			continue
		}
		outdated = append(outdated, release)
		s.logger.OutdatedFound("helm", release.ReleaseName, release.Namespace, release.Installed.Version, release.Latest.Version)
	}

	duration := time.Since(start)
	s.logger.ScanEnd("helm", duration, len(filtered), len(outdated))
	if len(unresolved) > 0 {
		s.logger.Warn().Int("unresolved", len(unresolved)).Msg("Some Helm releases could not be checked")
	}

	return &HelmScanResult{
		AllReleases: filtered,
		Outdated:    outdated,
		Unresolved:  unresolved,
		Duration:    duration,
	}, nil
}

func desiredVersionsArg(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

// resolveRelease replaces Nova's ArtifactHub-based latest version with the one from the
// release's real chart source when it is known, and re-validates IsOld.
func (s *Scanner) resolveRelease(ctx context.Context, r ReleaseOutput) ReleaseOutput {
	switch {
	case r.Overridden:
		r.Source = SourceDesired
	default:
		var res ChartResolution
		var found bool
		if s.chartResolver != nil {
			res, found = s.chartResolver.Resolve(ctx, r)
		}
		switch {
		case found && res.Unresolvable != "":
			r.Unresolved = true
			r.UnresolvedReason = res.Unresolvable
			r.Source = res.Source
			r.IsOld = false
			return r
		case found && res.Latest == "":
			r.Unresolved = true
			r.UnresolvedReason = "no stable chart version found in " + res.Source
			r.Source = res.Source
			r.IsOld = false
			return r
		case found:
			r.Latest = VersionInfo{Version: res.Latest}
			r.Source = res.Source
		case r.Latest.Version != "":
			r.Source = SourceArtifactHub
		default:
			r.Unresolved = true
			r.UnresolvedReason = "no matching chart found on ArtifactHub and the release is not managed by a known Flux source"
			r.IsOld = false
			return r
		}
	}
	// Re-derive IsOld: Nova's comparison cannot see build metadata/suffix ordering and
	// the resolver may have replaced the version altogether.
	r.IsOld = version.IsNewer(r.Installed.Version, r.Latest.Version)
	return r
}

// ScanContainers scans for outdated container images using Nova CLI.
//
// skipReleases contains the "namespace/release" keys of Helm releases that are reported
// as outdated themselves; containers owned only by such releases are skipped because
// updating the chart updates them.
func (s *Scanner) ScanContainers(ctx context.Context, skipReleases map[string]bool) (*ContainerScanResult, error) {
	s.logger.ScanStart("container")
	start := time.Now()

	var all []ContainerOutput
	var errored []ErroredImage
	for _, ns := range s.namespaces() {
		args := []string{
			"find", "--format", "json", "--containers",
			"--show-errored-containers",
			"--timeout", strconv.Itoa(s.config.NovaTimeoutSeconds),
		}
		if ns != "" {
			args = append(args, "--namespace", ns)
		}
		args = append(args, s.commonArgs()...)

		output, err := s.run(ctx, args)
		if err != nil {
			s.logger.ScanError("container", err)
			return nil, err
		}
		parsed, err := parseNovaOutput(output)
		if err != nil {
			s.logger.ScanError("container", err)
			return nil, err
		}
		all = append(all, parsed.Containers...)
		errored = append(errored, parsed.ErrImages...)
	}

	for _, e := range errored {
		s.logger.Warn().Str("image", e.Image).Str("error", e.Err).Msg("Nova could not check image; its update state is unknown")
	}

	var filtered []ContainerOutput
	for _, container := range mergeContainers(all) {
		container, keep := s.filterWorkloadKinds(container)
		if !keep || s.shouldIgnoreContainer(container) {
			continue
		}
		filtered = append(filtered, container)
	}

	var candidates []ContainerOutput
	for _, container := range filtered {
		if !container.IsOld {
			continue
		}
		container.OverallLatestTag = container.LatestTag
		target := s.containerTarget(container)
		if target == "" {
			s.logger.Debug().
				Str("image", container.Name).
				Str("current", container.CurrentTag).
				Str("latest", container.LatestTag).
				Msg("Skipping container: no newer, allowed tag in the configured update scope")
			continue
		}
		container.LatestTag = target
		if !s.meetsMinSeverity(container.CurrentTag, target) {
			continue
		}
		candidates = append(candidates, container)
	}

	var outdated, skipped []ContainerOutput
	for _, container := range groupByImage(candidates) {
		var allOwned bool
		container.HelmReleases, allOwned = s.helmOwners(ctx, container)
		if shouldSkipContainerForHelm(container.HelmReleases, allOwned, skipReleases) {
			skipped = append(skipped, container)
			s.logger.Debug().
				Str("image", container.Name).
				Str("reason", "owning Helm release is reported as outdated").
				Msg("Skipping container (will be updated with Helm chart)")
			continue
		}
		outdated = append(outdated, container)
		s.logger.OutdatedFound("container", container.Name, "", container.CurrentTag, container.LatestTag)
	}

	duration := time.Since(start)
	s.logger.ScanEnd("container", duration, len(filtered), len(outdated))
	if len(skipped) > 0 {
		s.logger.Info().Int("skipped", len(skipped)).Msg("Skipped containers owned by outdated Helm releases")
	}
	if len(errored) > 0 {
		s.logger.Warn().Int("errored", len(errored)).Msg("Some container images could not be checked")
	}

	return &ContainerScanResult{
		AllContainers: filtered,
		Outdated:      outdated,
		Skipped:       skipped,
		Errored:       errored,
		Duration:      duration,
	}, nil
}

// mergeContainers combines entries with identical image and current tag (possible when
// several namespaces are scanned separately).
func mergeContainers(in []ContainerOutput) []ContainerOutput {
	index := map[string]int{}
	var out []ContainerOutput
	for _, c := range in {
		key := c.Name + "\x00" + c.CurrentTag
		if i, ok := index[key]; ok {
			out[i].AffectedWorkloads = append(out[i].AffectedWorkloads, c.AffectedWorkloads...)
			continue
		}
		index[key] = len(out)
		out = append(out, c)
	}
	return out
}

// groupByImage collapses entries of the same image running with different tags into one
// entry (the oldest tag), so a single issue tracks the image instead of flapping between
// the variants.
func groupByImage(in []ContainerOutput) []ContainerOutput {
	index := map[string]int{}
	var out []ContainerOutput
	for _, c := range in {
		i, ok := index[c.Name]
		if !ok {
			index[c.Name] = len(out)
			out = append(out, c)
			continue
		}
		keep := out[i]
		if version.IsNewer(c.CurrentTag, keep.CurrentTag) { // c is older
			c.AffectedWorkloads = append(c.AffectedWorkloads, keep.AffectedWorkloads...)
			c.OtherCurrentTags = append(append(c.OtherCurrentTags, keep.OtherCurrentTags...), keep.CurrentTag)
			out[i] = c
		} else {
			keep.AffectedWorkloads = append(keep.AffectedWorkloads, c.AffectedWorkloads...)
			keep.OtherCurrentTags = append(append(keep.OtherCurrentTags, c.OtherCurrentTags...), c.CurrentTag)
			out[i] = keep
		}
	}
	return out
}

// filterWorkloadKinds drops workloads of ignored kinds (e.g. static control-plane pods
// owned by a Node). keep is false if nothing is left.
func (s *Scanner) filterWorkloadKinds(c ContainerOutput) (ContainerOutput, bool) {
	if len(s.config.IgnoreWorkloadKinds) == 0 || len(c.AffectedWorkloads) == 0 {
		return c, true
	}
	var kept []WorkloadOutput
	for _, w := range c.AffectedWorkloads {
		if !s.config.ShouldIgnoreWorkloadKind(w.Kind) {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		return c, false
	}
	c.AffectedWorkloads = kept
	return c, true
}

// scopeFor returns the update scope for an image ("any", "minor" or "patch").
func (s *Scanner) scopeFor(image string) string {
	patterns := make([]string, 0, len(s.config.ImageUpdateScope))
	for p := range s.config.ImageUpdateScope {
		patterns = append(patterns, p)
	}
	sort.Strings(patterns)
	for _, p := range patterns {
		if matchGlob(p, image) {
			return s.config.ImageUpdateScope[p]
		}
	}
	return s.config.ContainerUpdateScope
}

// containerTarget selects the tag to propose for an outdated container, honouring the
// update scope and the version blacklist. If Nova's newest tag is unusable (blacklisted,
// or not actually newer) the next best candidate within the scope is used. It returns ""
// if there is nothing to propose.
func (s *Scanner) containerTarget(c ContainerOutput) string {
	var candidates []string
	switch s.scopeFor(c.Name) {
	case "patch":
		candidates = []string{c.LatestPatchTag}
	case "minor":
		candidates = []string{c.LatestMinorTag, c.LatestPatchTag}
	default:
		candidates = []string{c.LatestTag, c.LatestMinorTag, c.LatestPatchTag}
	}
	for _, cand := range candidates {
		if cand == "" || cand == c.CurrentTag {
			continue
		}
		if s.config.ShouldIgnoreVersion(cand) {
			continue
		}
		if !version.IsNewer(c.CurrentTag, cand) || !version.SameVariant(c.CurrentTag, cand) {
			continue
		}
		return cand
	}
	return ""
}

// helmOwners resolves the Helm releases owning the container's workloads. allOwned is
// true only if every workload maps to a release.
func (s *Scanner) helmOwners(ctx context.Context, c ContainerOutput) (refs []HelmRef, allOwned bool) {
	if s.ownerResolver == nil || len(c.AffectedWorkloads) == 0 {
		return nil, false
	}
	allOwned = true
	seen := map[string]bool{}
	for _, w := range c.AffectedWorkloads {
		ref, ok := s.ownerResolver.HelmRelease(ctx, w)
		if !ok {
			allOwned = false
			continue
		}
		if !seen[ref.Key()] {
			seen[ref.Key()] = true
			refs = append(refs, ref)
		}
	}
	return refs, allOwned
}

// shouldSkipContainerForHelm returns true if every affected workload belongs to a Helm
// release that is itself reported as outdated. Workloads that cannot be mapped to a
// release keep the container reported.
func shouldSkipContainerForHelm(owners []HelmRef, allOwned bool, skipReleases map[string]bool) bool {
	if !allOwned || len(owners) == 0 {
		return false
	}
	for _, ref := range owners {
		if !skipReleases[ref.Key()] {
			return false
		}
	}
	return true
}

// meetsMinSeverity checks if the version difference meets the minimum severity threshold.
// Versions that cannot be parsed are always reported.
func (s *Scanner) meetsMinSeverity(currentVersion, latestVersion string) bool {
	sev := version.Severity(currentVersion, latestVersion)
	if sev == version.SeverityUnknown {
		return true
	}
	return sev >= s.config.SeverityLevel()
}
