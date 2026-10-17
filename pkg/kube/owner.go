// Package kube provides the Kubernetes API access the scanner needs beyond Nova: mapping
// workloads to Helm releases.
package kube

import (
	"context"
	"fmt"
	"sync"

	"github.com/olohmann/nova-automated-cluster-scanner/pkg/logging"
	"github.com/olohmann/nova-automated-cluster-scanner/pkg/nova"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	annotationReleaseName      = "meta.helm.sh/release-name"
	annotationReleaseNamespace = "meta.helm.sh/release-namespace"
)

// NewDynamicClient builds a dynamic client from the kubeconfig (or the in-cluster config
// if kubeconfig is empty).
func NewDynamicClient(kubeconfig, kubeContext string) (dynamic.Interface, error) {
	var cfg *rest.Config
	var err error
	if kubeconfig == "" {
		cfg, err = rest.InClusterConfig()
	} else {
		rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
		overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to build kubernetes client config: %w", err)
	}
	return dynamic.NewForConfig(cfg)
}

// workloadGVRs maps the workload kinds reported by Nova to their API resources.
var workloadGVRs = map[string]schema.GroupVersionResource{
	"Deployment":            {Group: "apps", Version: "v1", Resource: "deployments"},
	"StatefulSet":           {Group: "apps", Version: "v1", Resource: "statefulsets"},
	"DaemonSet":             {Group: "apps", Version: "v1", Resource: "daemonsets"},
	"ReplicaSet":            {Group: "apps", Version: "v1", Resource: "replicasets"},
	"Job":                   {Group: "batch", Version: "v1", Resource: "jobs"},
	"CronJob":               {Group: "batch", Version: "v1", Resource: "cronjobs"},
	"ReplicationController": {Version: "v1", Resource: "replicationcontrollers"},
	"Pod":                   {Version: "v1", Resource: "pods"},
}

type ownerResult struct {
	ref nova.HelmRef
	ok  bool
}

// OwnerResolver maps workloads to the Helm release that manages them using the
// meta.helm.sh/release-* annotations Helm puts on every object it installs.
type OwnerResolver struct {
	client dynamic.Interface
	logger *logging.Logger

	mu       sync.Mutex
	cache    map[string]ownerResult
	warnOnce sync.Once
}

// NewOwnerResolver creates an OwnerResolver.
func NewOwnerResolver(client dynamic.Interface, logger *logging.Logger) *OwnerResolver {
	return &OwnerResolver{
		client: client,
		logger: logger.WithComponent("kube"),
		cache:  map[string]ownerResult{},
	}
}

// HelmRelease implements nova.WorkloadOwnerResolver.
func (o *OwnerResolver) HelmRelease(ctx context.Context, w nova.WorkloadOutput) (nova.HelmRef, bool) {
	gvr, known := workloadGVRs[w.Kind]
	if !known || w.Name == "" {
		return nova.HelmRef{}, false
	}
	key := w.Kind + "/" + w.Namespace + "/" + w.Name

	o.mu.Lock()
	if r, ok := o.cache[key]; ok {
		o.mu.Unlock()
		return r.ref, r.ok
	}
	o.mu.Unlock()

	obj, err := o.client.Resource(gvr).Namespace(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
	var res ownerResult
	switch {
	case err != nil:
		// An unreadable workload is treated as not helm-managed, so its container is still reported.
		o.warnOnce.Do(func() {
			o.logger.Warn().Err(err).Str("workload", key).Msg("Cannot read workload to determine its Helm release; containers are not deduplicated against Helm releases (check RBAC)")
		})
	default:
		ann := obj.GetAnnotations()
		if name := ann[annotationReleaseName]; name != "" {
			ns := ann[annotationReleaseNamespace]
			if ns == "" {
				ns = w.Namespace
			}
			res = ownerResult{ref: nova.HelmRef{Release: name, Namespace: ns}, ok: true}
		}
	}

	o.mu.Lock()
	o.cache[key] = res
	o.mu.Unlock()
	return res.ref, res.ok
}
