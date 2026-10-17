package nova

import (
	"os"
	"strings"
)

// shouldIgnoreRelease reports whether a release matches ignoreReleases/ignoreCharts.
func (s *Scanner) shouldIgnoreRelease(release ReleaseOutput) bool {
	for _, ignore := range s.config.IgnoreReleases {
		if release.ReleaseName == ignore {
			return true
		}
	}
	for _, ignore := range s.config.IgnoreCharts {
		if release.ChartName == ignore {
			return true
		}
	}
	return false
}

// shouldIgnoreContainer reports whether a container matches any ignoreImages pattern.
//
// Nova reports the image name without its tag (e.g. "ghcr.io/fluxcd/helm-controller")
// and the versions separately. Patterns are therefore matched against both the bare
// name and "name:<latest tag>", so that:
//   - "ghcr.io/fluxcd/*" and "ghcr.io/fluxcd/*:*" both ignore every Flux image, and
//   - "registry.k8s.io/sig-storage/csi-provisioner:v6*" only ignores proposed v6 updates.
func (s *Scanner) shouldIgnoreContainer(container ContainerOutput) bool {
	candidates := []string{container.Name}
	if container.LatestTag != "" {
		candidates = append(candidates, container.Name+":"+container.LatestTag)
	}
	for _, pattern := range s.config.IgnoreImages {
		for _, c := range candidates {
			if matchGlob(pattern, c) {
				return true
			}
		}
	}
	return false
}

// matchGlob matches an image reference against a glob pattern where "*" matches any
// sequence of characters (including "/" and ":").
//
// Registry prefixes are optional:
//   - "*/name" also matches a bare "name" (e.g. "*/busybox" matches "busybox")
//   - a pattern not starting with "*" also matches after any "/" in s, so
//     "grafana/rollout-operator:*" matches "docker.io/grafana/rollout-operator:v0.28.0"
func matchGlob(pattern, s string) bool {
	if wildcardMatch(pattern, s) {
		return true
	}
	if strings.HasPrefix(pattern, "*/") && wildcardMatch(pattern[2:], s) {
		return true
	}
	if pattern != "" && pattern[0] != '*' {
		for i := 0; i < len(s); i++ {
			if s[i] == '/' && wildcardMatch(pattern, s[i+1:]) {
				return true
			}
		}
	}
	return false
}

// wildcardMatch reports whether s matches pattern in full, where "*" matches any
// (possibly empty) sequence of characters. All other characters match literally.
func wildcardMatch(pattern, s string) bool {
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case p < len(pattern) && pattern[p] == s[i]:
			p++
			i++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// expandTilde expands ~ to the user's home directory.
func expandTilde(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}

// isRunningInCluster returns true if the process is running inside a Kubernetes cluster.
// Detection is based on the presence of KUBERNETES_SERVICE_HOST environment variable,
// which is automatically set by Kubernetes for all pods.
func isRunningInCluster() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// KubeconfigPath determines the kubeconfig path to use.
// Returns empty string when running in-cluster (the client falls back to in-cluster config).
// Otherwise returns the configured path, KUBECONFIG env var, or default ~/.kube/config.
func KubeconfigPath(configuredPath string) string {
	return getKubeconfig(configuredPath)
}

func getKubeconfig(configuredPath string) string {
	if isRunningInCluster() {
		return ""
	}
	if configuredPath != "" {
		return expandTilde(configuredPath)
	}
	if envPath := os.Getenv("KUBECONFIG"); envPath != "" {
		return expandTilde(envPath)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/.kube/config"
	}
	return ""
}

// ImageRepo strips tag and digest from an image reference ("ghcr.io/a/b:1.2@sha256:..." -> "ghcr.io/a/b").
func ImageRepo(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
}
