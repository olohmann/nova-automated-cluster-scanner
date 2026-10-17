package kube

import (
	"context"
	"testing"

	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/nova"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func deployment(ns, name string, annotations map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"namespace": ns, "name": name, "annotations": annotations},
	}}
}

func TestOwnerResolver(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(),
		deployment("lgtm", "mimir", map[string]any{
			"meta.helm.sh/release-name": "lgtm", "meta.helm.sh/release-namespace": "lgtm",
		}),
		deployment("apps", "plain", nil),
	)
	o := NewOwnerResolver(client, logging.NewLogger("error"))
	ctx := context.Background()

	ref, ok := o.HelmRelease(ctx, nova.WorkloadOutput{Kind: "Deployment", Namespace: "lgtm", Name: "mimir"})
	if !ok || ref.Key() != "lgtm/lgtm" {
		t.Errorf("got %+v, %v; want lgtm/lgtm", ref, ok)
	}
	if _, ok := o.HelmRelease(ctx, nova.WorkloadOutput{Kind: "Deployment", Namespace: "apps", Name: "plain"}); ok {
		t.Error("workload without helm annotations must not be owned")
	}
	if _, ok := o.HelmRelease(ctx, nova.WorkloadOutput{Kind: "Deployment", Namespace: "apps", Name: "missing"}); ok {
		t.Error("unreadable workload must not be owned")
	}
	if _, ok := o.HelmRelease(ctx, nova.WorkloadOutput{Kind: "Node", Name: "n1"}); ok {
		t.Error("unsupported kinds must not be owned")
	}
}
