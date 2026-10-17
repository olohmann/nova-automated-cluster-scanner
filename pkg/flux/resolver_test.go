package flux

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/olohmann/nova-automated-cluster-scanner/pkg/config"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/nova"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func obj(apiVersion, kind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"namespace": ns, "name": name},
		"spec":     spec,
	}}
}

func helmReleaseObj(ns, name string, spec map[string]any) *unstructured.Unstructured {
	return obj("helm.toolkit.fluxcd.io/v2", "HelmRelease", ns, name, spec)
}

const grafanaIndex = `
entries:
  alloy:
    - version: 1.13.1
    - version: 1.13.0
    - version: 2.0.0-rc.1
  lgtm-distributed:
    - version: 3.0.1
    - version: 3.0.2
      deprecated: true
`

func newTestResolver(t *testing.T, cfg *config.Config, objs ...runtime.Object) *Resolver {
	t.Helper()
	gvrToList := map[schema.GroupVersionResource]string{}
	for _, gvr := range helmReleaseGVRs {
		gvrToList[gvr] = "HelmReleaseList"
	}
	for kind, gvrs := range sourceGVRs {
		for _, gvr := range gvrs {
			gvrToList[gvr] = kind + "List"
		}
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToList, objs...)
	r := New(client, cfg, logging.NewLogger("error"))
	r.fetchIndex = func(_ context.Context, url string) ([]byte, error) {
		if url != "https://grafana.github.io/helm-charts/index.yaml" {
			return nil, fmt.Errorf("unexpected index url %s", url)
		}
		return []byte(grafanaIndex), nil
	}
	r.listOCITags = func(_ context.Context, repo string) ([]string, error) {
		switch repo {
		case "docker.io/envoyproxy/gateway-helm":
			return []string{"v1.9.2", "v1.9.3", "v1.10.0-rc.1", "0.0.0-latest"}, nil
		case "ghcr.io/m0nsterrr/helm-charts/homepage":
			return []string{"4.14.0", "4.14.1_abc"}, nil
		}
		return nil, fmt.Errorf("unexpected repository %s", repo)
	}
	return r
}

func release(name, ns, chart string) nova.ReleaseOutput {
	return nova.ReleaseOutput{ReleaseName: name, Namespace: ns, ChartName: chart}
}

func TestResolve_HelmRepository(t *testing.T) {
	r := newTestResolver(t, &config.Config{},
		helmReleaseObj("alloy", "alloy", map[string]any{"chart": map[string]any{"spec": map[string]any{
			"chart": "alloy", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "grafana", "namespace": "flux-system"},
		}}}),
		obj("source.toolkit.fluxcd.io/v1", "HelmRepository", "flux-system", "grafana", map[string]any{"url": "https://grafana.github.io/helm-charts"}),
		helmReleaseObj("lgtm", "lgtm", map[string]any{"chart": map[string]any{"spec": map[string]any{
			"chart": "lgtm-distributed", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "grafana", "namespace": "flux-system"},
		}}}),
	)

	res, found := r.Resolve(context.Background(), release("alloy", "alloy", "alloy"))
	if !found || res.Latest != "1.13.1" || res.Source != SourceHelmRepository {
		t.Fatalf("alloy: %+v found=%v, want 1.13.1 (prereleases ignored)", res, found)
	}
	res, _ = r.Resolve(context.Background(), release("lgtm", "lgtm", "lgtm-distributed"))
	if res.Latest != "3.0.1" {
		t.Errorf("lgtm: %+v, want deprecated chart versions skipped", res)
	}
}

func TestResolve_OCIChartRefAndOCIHelmRepository(t *testing.T) {
	r := newTestResolver(t, &config.Config{},
		helmReleaseObj("envoy-gateway-system", "envoy-gateway", map[string]any{
			"chartRef": map[string]any{"kind": "OCIRepository", "name": "envoy-gateway-oci", "namespace": "flux-system"},
		}),
		obj("source.toolkit.fluxcd.io/v1", "OCIRepository", "flux-system", "envoy-gateway-oci", map[string]any{"url": "oci://docker.io/envoyproxy/gateway-helm"}),
		helmReleaseObj("homepage", "homepage", map[string]any{"chart": map[string]any{"spec": map[string]any{
			"chart": "homepage", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "m0", "namespace": "flux-system"},
		}}}),
		obj("source.toolkit.fluxcd.io/v1", "HelmRepository", "flux-system", "m0", map[string]any{"url": "oci://ghcr.io/m0nsterrr/helm-charts", "type": "oci"}),
	)

	res, found := r.Resolve(context.Background(), release("envoy-gateway", "envoy-gateway-system", "gateway-helm"))
	if !found || res.Latest != "v1.9.3" || res.Source != SourceOCIRepository {
		t.Errorf("envoy: %+v, want v1.9.3 from the OCI source instead of ArtifactHub's date-based version", res)
	}
	res, _ = r.Resolve(context.Background(), release("homepage", "homepage", "homepage"))
	if res.Latest != "4.14.1+abc" {
		t.Errorf("homepage: %+v", res)
	}
}

func TestResolve_GitSourceIsUnresolvable(t *testing.T) {
	r := newTestResolver(t, &config.Config{},
		helmReleaseObj("synology-csi", "synology-csi", map[string]any{"chart": map[string]any{"spec": map[string]any{
			"chart": "charts/synology-csi", "sourceRef": map[string]any{"kind": "GitRepository", "name": "x", "namespace": "flux-system"},
		}}}),
	)
	res, found := r.Resolve(context.Background(), release("synology-csi", "synology-csi", "synology-csi"))
	if !found || res.Unresolvable == "" || !strings.Contains(res.Unresolvable, "GitRepository") {
		t.Errorf("got %+v found=%v, want unresolvable git source", res, found)
	}
}

func TestResolve_TargetNamespaceReleaseName(t *testing.T) {
	r := newTestResolver(t, &config.Config{},
		helmReleaseObj("flux-system", "alloy", map[string]any{
			"targetNamespace": "monitoring",
			"chart": map[string]any{"spec": map[string]any{
				"chart": "alloy", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "grafana", "namespace": "flux-system"},
			}}}),
		obj("source.toolkit.fluxcd.io/v1", "HelmRepository", "flux-system", "grafana", map[string]any{"url": "https://grafana.github.io/helm-charts/"}),
	)
	if _, found := r.Resolve(context.Background(), release("monitoring-alloy", "monitoring", "alloy")); !found {
		t.Error("Flux names the release <targetNamespace>-<name> when targetNamespace is set")
	}
}

func TestResolve_UnknownReleaseFallsThroughAndChartSourcesWin(t *testing.T) {
	cfg := &config.Config{ChartSources: map[string]config.ChartSource{
		"alloy": {Type: "helm", URL: "https://grafana.github.io/helm-charts"},
	}}
	r := newTestResolver(t, cfg)

	if _, found := r.Resolve(context.Background(), release("manual", "default", "manual")); found {
		t.Error("releases unknown to Flux must fall back to Nova")
	}
	res, found := r.Resolve(context.Background(), release("whatever", "default", "alloy"))
	if !found || res.Latest != "1.13.1" || res.Source != SourceConfig {
		t.Errorf("chartSources override: %+v found=%v", res, found)
	}
}

func TestResolve_IndexErrorIsReportedNotSilent(t *testing.T) {
	r := newTestResolver(t, &config.Config{ChartSources: map[string]config.ChartSource{
		"x": {Type: "helm", URL: "https://private.example.com"},
	}})
	res, found := r.Resolve(context.Background(), release("x", "default", "x"))
	if !found || res.Unresolvable == "" {
		t.Errorf("got %+v, want unresolvable with the fetch error", res)
	}
}
