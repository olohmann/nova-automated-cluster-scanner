// Package flux resolves the latest version of Helm charts from the sources Flux installs
// them from (HelmRepository, OCIRepository), instead of guessing via ArtifactHub.
package flux

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/config"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/nova"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/version"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Sources reported in nova.ChartResolution.Source.
const (
	SourceHelmRepository = "flux:helmrepository"
	SourceOCIRepository  = "flux:ocirepository"
	SourceGit            = "flux:gitrepository"
	SourceConfig         = "config:chartSources"
)

var (
	helmReleaseGVRs = []schema.GroupVersionResource{
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},
		{Group: "helm.toolkit.fluxcd.io", Version: "v2beta2", Resource: "helmreleases"},
		{Group: "helm.toolkit.fluxcd.io", Version: "v2beta1", Resource: "helmreleases"},
	}
	sourceGVRs = map[string][]schema.GroupVersionResource{
		"HelmRepository": {
			{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"},
			{Group: "source.toolkit.fluxcd.io", Version: "v1beta2", Resource: "helmrepositories"},
		},
		"OCIRepository": {
			{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "ocirepositories"},
			{Group: "source.toolkit.fluxcd.io", Version: "v1beta2", Resource: "ocirepositories"},
		},
	}
)

// Resolver implements nova.ChartResolver.
type Resolver struct {
	client        dynamic.Interface
	logger        *logging.Logger
	chartSources  map[string]config.ChartSource
	httpClient    *http.Client
	listOCITags   func(ctx context.Context, repository string) ([]string, error)
	fetchIndex    func(ctx context.Context, url string) ([]byte, error)
	loadOnce      sync.Once
	helmReleases  []helmRelease
	mu            sync.Mutex
	indexCache    map[string]map[string][]string // repo URL -> chart -> versions
	indexErrCache map[string]error
	tagCache      map[string][]string
	tagErrCache   map[string]error
}

type helmRelease struct {
	name, namespace string // HelmRelease object
	releaseName     string
	releaseNS       string
	chart           string
	sourceKind      string
	sourceName      string
	sourceNS        string
	chartRefKind    string
}

// New creates a Resolver. client may be nil if only chartSources are used.
func New(client dynamic.Interface, cfg *config.Config, logger *logging.Logger) *Resolver {
	r := &Resolver{
		client:        client,
		logger:        logger.WithComponent("flux"),
		chartSources:  cfg.ChartSources,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		indexCache:    map[string]map[string][]string{},
		indexErrCache: map[string]error{},
		tagCache:      map[string][]string{},
		tagErrCache:   map[string]error{},
	}
	r.listOCITags = func(ctx context.Context, repository string) ([]string, error) {
		repo, err := name.NewRepository(repository)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		return remote.List(repo, remote.WithAuthFromKeychain(authn.DefaultKeychain), remote.WithContext(ctx))
	}
	r.fetchIndex = r.httpGet
	return r
}

func (r *Resolver) httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// Resolve implements nova.ChartResolver.
func (r *Resolver) Resolve(ctx context.Context, rel nova.ReleaseOutput) (nova.ChartResolution, bool) {
	if src, ok := r.chartSources[rel.ChartName]; ok {
		return r.lookup(ctx, src.Type, src.URL, rel.ChartName, SourceConfig), true
	}
	if r.client == nil {
		return nova.ChartResolution{}, false
	}
	hr, ok := r.findHelmRelease(ctx, rel)
	if !ok {
		return nova.ChartResolution{}, false
	}

	switch {
	case hr.chartRefKind != "":
		if hr.chartRefKind != "OCIRepository" {
			return nova.ChartResolution{Source: SourceOCIRepository, Unresolvable: "chartRef of kind " + hr.chartRefKind + " is not supported"}, true
		}
		url, _, err := r.sourceURL(ctx, "OCIRepository", hr.sourceName, hr.sourceNS)
		if err != nil {
			return nova.ChartResolution{Source: SourceOCIRepository, Unresolvable: err.Error()}, true
		}
		return r.lookup(ctx, "oci", url, "", SourceOCIRepository), true
	case hr.sourceKind == "HelmRepository":
		url, repoType, err := r.sourceURL(ctx, "HelmRepository", hr.sourceName, hr.sourceNS)
		if err != nil {
			return nova.ChartResolution{Source: SourceHelmRepository, Unresolvable: err.Error()}, true
		}
		if repoType == "oci" || strings.HasPrefix(url, "oci://") {
			return r.lookup(ctx, "oci", strings.TrimSuffix(url, "/")+"/"+hr.chart, "", SourceHelmRepository), true
		}
		return r.lookup(ctx, "helm", url, hr.chart, SourceHelmRepository), true
	default:
		return nova.ChartResolution{
			Source:       SourceGit,
			Unresolvable: fmt.Sprintf("chart is installed from a %s, which has no version index to compare against", hr.sourceKind),
		}, true
	}
}

// lookup finds the newest stable version in a "helm" repository index or an "oci" repository.
func (r *Resolver) lookup(ctx context.Context, typ, url, chart, source string) nova.ChartResolution {
	var versions []string
	var err error
	if typ == "oci" {
		versions, err = r.ociTags(ctx, strings.TrimPrefix(url, "oci://"))
	} else {
		versions, err = r.helmVersions(ctx, url, chart)
	}
	if err != nil {
		return nova.ChartResolution{Source: source, Unresolvable: err.Error()}
	}
	return nova.ChartResolution{Source: source, Latest: version.Latest(versions)}
}

func (r *Resolver) ociTags(ctx context.Context, repository string) ([]string, error) {
	r.mu.Lock()
	tags, ok := r.tagCache[repository]
	err := r.tagErrCache[repository]
	r.mu.Unlock()
	if ok || err != nil {
		return tags, err
	}
	tags, err = r.listOCITags(ctx, repository)
	if err != nil {
		err = fmt.Errorf("failed to list tags of oci://%s: %w", repository, err)
	}
	r.mu.Lock()
	r.tagCache[repository], r.tagErrCache[repository] = tags, err
	r.mu.Unlock()
	return tags, err
}

func (r *Resolver) helmVersions(ctx context.Context, repoURL, chart string) ([]string, error) {
	r.mu.Lock()
	index, ok := r.indexCache[repoURL]
	err := r.indexErrCache[repoURL]
	r.mu.Unlock()
	if !ok && err == nil {
		index, err = r.loadIndex(ctx, repoURL)
		r.mu.Lock()
		r.indexCache[repoURL], r.indexErrCache[repoURL] = index, err
		r.mu.Unlock()
	}
	if err != nil {
		return nil, err
	}
	versions, ok := index[chart]
	if !ok {
		return nil, fmt.Errorf("chart %q not found in repository %s", chart, repoURL)
	}
	return versions, nil
}

func (r *Resolver) loadIndex(ctx context.Context, repoURL string) (map[string][]string, error) {
	data, err := r.fetchIndex(ctx, strings.TrimSuffix(repoURL, "/")+"/index.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch helm repository index: %w", err)
	}
	var idx struct {
		Entries map[string][]struct {
			Version    string `yaml:"version"`
			Deprecated bool   `yaml:"deprecated"`
		} `yaml:"entries"`
	}
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("failed to parse helm repository index of %s: %w", repoURL, err)
	}
	out := make(map[string][]string, len(idx.Entries))
	for chart, entries := range idx.Entries {
		for _, e := range entries {
			if !e.Deprecated {
				out[chart] = append(out[chart], e.Version)
			}
		}
	}
	return out, nil
}

// sourceURL returns the URL (and repository type) of a HelmRepository/OCIRepository.
func (r *Resolver) sourceURL(ctx context.Context, kind, name, namespace string) (url, repoType string, err error) {
	for _, gvr := range sourceGVRs[kind] {
		obj, getErr := r.client.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			err = getErr
			continue
		}
		url, _, _ = unstructured.NestedString(obj.Object, "spec", "url")
		repoType, _, _ = unstructured.NestedString(obj.Object, "spec", "type")
		if url == "" {
			return "", "", fmt.Errorf("%s %s/%s has no spec.url", kind, namespace, name)
		}
		return url, repoType, nil
	}
	return "", "", fmt.Errorf("cannot read %s %s/%s: %w", kind, namespace, name, err)
}

// findHelmRelease finds the Flux HelmRelease that produced the Helm release.
func (r *Resolver) findHelmRelease(ctx context.Context, rel nova.ReleaseOutput) (helmRelease, bool) {
	r.loadOnce.Do(func() { r.loadHelmReleases(ctx) })

	var byName []helmRelease
	for _, hr := range r.helmReleases {
		if hr.releaseName != rel.ReleaseName {
			continue
		}
		if hr.releaseNS == rel.Namespace {
			return hr, true
		}
		byName = append(byName, hr)
	}
	// Release namespaces can differ when storageNamespace is used; accept an unambiguous name match.
	if len(byName) == 1 {
		return byName[0], true
	}
	return helmRelease{}, false
}

func (r *Resolver) loadHelmReleases(ctx context.Context) {
	var lastErr error
	for _, gvr := range helmReleaseGVRs {
		list, err := r.client.Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			lastErr = err
			continue
		}
		for i := range list.Items {
			r.helmReleases = append(r.helmReleases, parseHelmRelease(&list.Items[i]))
		}
		r.logger.Debug().Int("helmreleases", len(r.helmReleases)).Str("version", gvr.Version).Msg("Loaded Flux HelmReleases")
		return
	}
	r.logger.Warn().Err(lastErr).Msg("Cannot list Flux HelmReleases; chart versions fall back to ArtifactHub (is Flux installed and RBAC granted?)")
}

func parseHelmRelease(obj *unstructured.Unstructured) helmRelease {
	str := func(path ...string) string {
		s, _, _ := unstructured.NestedString(obj.Object, path...)
		return s
	}
	hr := helmRelease{name: obj.GetName(), namespace: obj.GetNamespace()}

	target := str("spec", "targetNamespace")
	hr.releaseNS = target
	if hr.releaseNS == "" {
		hr.releaseNS = hr.namespace
	}
	hr.releaseName = str("spec", "releaseName")
	if hr.releaseName == "" {
		hr.releaseName = hr.name
		if target != "" {
			hr.releaseName = target + "-" + hr.name
		}
	}

	if kind := str("spec", "chartRef", "kind"); kind != "" {
		hr.chartRefKind = kind
		hr.sourceName = str("spec", "chartRef", "name")
		hr.sourceNS = str("spec", "chartRef", "namespace")
	} else {
		hr.chart = str("spec", "chart", "spec", "chart")
		hr.sourceKind = str("spec", "chart", "spec", "sourceRef", "kind")
		hr.sourceName = str("spec", "chart", "spec", "sourceRef", "name")
		hr.sourceNS = str("spec", "chart", "spec", "sourceRef", "namespace")
	}
	if hr.sourceNS == "" {
		hr.sourceNS = hr.namespace
	}
	return hr
}
