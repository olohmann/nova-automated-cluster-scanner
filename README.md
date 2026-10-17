# Nova Automated Cluster Scanner

Automated Kubernetes cluster scanner that detects outdated Helm charts and container images using [Fairwinds Nova](https://nova.docs.fairwinds.com/), creates GitHub issues for tracking, and exposes Prometheus metrics.

## Features

- **Helm Chart Scanning**: Detects outdated Helm releases. Latest versions come from the
  Flux `HelmRepository`/`OCIRepository` a release is installed from (falling back to
  ArtifactHub for releases Flux does not manage); releases that cannot be checked are
  reported instead of silently ignored
- **GitHub Issue Creation**: Automatically creates issues with update checklists (Flux-aware)
- **Issue Deduplication**: One issue per component — exact duplicates are skipped, a newer
  version retitles the existing issue (keeping its labels and discussion) and closes older
  ones as superseded, and issues closed as *not planned* are not re-opened. Issues are
  closed automatically once the component is no longer outdated
- **Prometheus Metrics**: Exposes metrics for monitoring and alerting
- **Severity Filtering**: Filter by minor, major, or critical version changes
- **Dry-run Mode**: Test without creating actual GitHub issues

## Quick Start

### Prerequisites

- Go 1.22+
- Kubernetes cluster access
- GitHub token with `repo` scope
- Pushgateway (optional, for metrics)

### Build

```bash
# Build the binary
make build

# Build Docker image
make docker-build
```

### Run Locally

```bash
# Create a config file
cat > config.yaml <<EOF
scanHelm: true
scanContainers: false
minSeverity: minor
logLevel: info
dryRun: true
EOF

# Set environment variables
export GITHUB_TOKEN="ghp_your_token"
export GITHUB_OWNER="your-username"
export GITHUB_REPO="your-repo"

# Run in dry-run mode
make dry-run
```

### Deploy to Kubernetes

1. **Configure ExternalSecrets**

   Update `deploy/externalsecret.yaml` to match your secret store:

   ```yaml
   spec:
     secretStoreRef:
       name: your-cluster-secret-store  # Update this
       kind: ClusterSecretStore
     data:
       - secretKey: GITHUB_TOKEN
         remoteRef:
           key: path/to/your/github-token  # Update this
   ```

2. **Customize Configuration**

   Edit `deploy/configmap.yaml` to adjust scanning options.

3. **Deploy**

   ```bash
   # Using kubectl
   make deploy

   # Using Flux
   kubectl apply -f deploy/kustomization.yaml
   ```

4. **Test the CronJob**

   ```bash
   # Create a manual test run
   make test-job

   # View logs
   make logs
   ```

## CI/CD Setup

The GitHub Actions workflow builds and pushes the container image to GitHub Container Registry (ghcr.io).

### Creating Releases

Use the **Prepare Release** workflow to create releases. This ensures the Helm chart version and deploy manifests are updated before the release tag is created.

**Via GitHub UI:**
1. Go to **Actions** → **Prepare Release** → **Run workflow**
2. Enter the version (e.g., `0.2.0`)
3. Click **Run workflow**

**Via CLI:**
```bash
# Create a release
gh workflow run prepare-release.yaml -f version=0.2.0

# Create a pre-release
gh workflow run prepare-release.yaml -f version=0.2.0-rc1 -f prerelease=true
```

**Release Flow:**
```
Step 1: gh workflow run prepare-release.yaml -f version=X.Y.Z
        ↓
    prepare-release.yaml
    ├── Updates Chart.yaml (version + appVersion)
    ├── Updates deploy/cronjob.yaml (image tag)
    ├── Commits: "chore: release vX.Y.Z"
    ├── Creates tag vX.Y.Z
    └── Creates GitHub release

Step 2: Manually trigger build-push workflow (GitHub UI or CLI)
        ↓
    build-push.yaml (on release event)
    ├── Builds container image → ghcr.io
    └── Builds binaries → release assets

Step 3: Helm chart auto-publishes (triggered by push to main)
        ↓
    release-chart.yaml
    └── Publishes Helm chart to gh-pages
```

**Important:** Due to GitHub Actions limitations, workflows using `GITHUB_TOKEN` to create releases don't trigger other workflows. After running prepare-release, manually trigger build-push:

```bash
# After prepare-release completes, re-run build-push for the release
gh run list --workflow=build-push.yaml --limit=1  # Find the run ID
gh run rerun <run-id>

# Or via GitHub UI: Actions → Build and Push → Re-run (select the release run)
```

**Produced artifacts:**
- Container image: `ghcr.io/olohmann/nova-automated-cluster-scanner:vX.Y.Z`
- Helm chart: `nova-scanner-X.Y.Z.tgz`
- Binaries: linux/darwin/windows (amd64/arm64)

## Configuration

Configuration can be provided via YAML file and/or environment variables.

### YAML Configuration

```yaml
# Kubernetes
kubeconfig: ""       # Path to kubeconfig (empty for in-cluster)
context: ""          # Kubernetes context to use

# Scanning
scanHelm: true       # Enable Helm chart scanning
scanContainers: false # Enable container image scanning
ignoreReleases: []   # Helm releases to ignore
ignoreCharts: []     # Chart names to ignore
ignoreImages:        # Container images to ignore (see "Ignoring images" below)
  - "*/pause"
ignoreVersionPatterns:  # Blacklist patterns for target versions
  - "-develop"          # Skip versions like 9.2.0-develop.18
  - "-rc"               # Skip release candidates
  - "-alpha"            # Skip alpha releases
  - "-beta"             # Skip beta releases

# Severity: minor, major, critical (applies to Helm charts and container images)
minSeverity: minor

# Container update target: any (newest tag, may cross major versions), minor (same major),
# patch (same minor). imageUpdateScope overrides it per image (glob -> scope).
containerUpdateScope: any
imageUpdateScope: {}   # e.g. {"*/postgres": "minor"}
ignoreWorkloadKinds: [Node]   # skip static control-plane pods (not updatable via GitOps)
novaTimeoutSeconds: 120       # registry lookups; nova's default of 10s silently drops slow registries

# Helm chart sources
fluxResolver: true    # latest version from Flux HelmRepository/OCIRepository (needs RBAC, see deploy/rbac.yaml)
helmOwnerLookup: true # map containers to their Helm release via meta.helm.sh annotations
chartSources: {}      # for charts Flux does not manage: {chart: {type: helm|oci, url: ...}}

# Close open issues whose component is no longer outdated
autoCloseResolved: true

# GitHub
githubToken: ""      # GitHub token (prefer env var)
githubOwner: ""      # Repository owner
githubRepo: ""       # Repository name
dryRun: false        # Don't create actual issues

# Metrics
pushgatewayUrl: ""   # Pushgateway URL (empty to disable)
jobName: "nova-scanner"

# Logging
logLevel: info       # debug, info, warn, error

# Nova
pollArtifactHub: true
namespaces: []       # Namespaces to scan (empty = all)
desiredVersions: {}  # Override target versions (chart name -> version)
```

### How versions are determined

- **Helm**: Nova lists the installed releases. The latest version is then taken from the
  chart's real source: the Flux `HelmRelease` is mapped to its `HelmRepository`
  (`index.yaml`) or OCI repository, and the newest *stable* version is used.
  `chartSources` pins sources for charts Flux does not manage. Without either, Nova's
  ArtifactHub name matching is used (shown as `Version Source` in the issue) - this guess can
  pick an unrelated chart with the same name. Charts installed from a `GitRepository`, or
  whose source cannot be read, are logged as *unresolved* (metric
  `nova_unresolved_helm_releases`) and their issues are left untouched.
- **Containers**: Nova proposes the newest tag; the scanner picks the update target according
  to `containerUpdateScope`/`imageUpdateScope`, falls back to the same-major/same-minor tag if
  the newest one is blacklisted, drops proposals that are not actually newer (e.g.
  `-nbxyz24` -> `-nbxyz9`) or that would change the image variant (`1.2-alpine` -> `1.3`), and
  applies `minSeverity`. Images that Nova cannot check (registry errors, timeouts, no
  credentials) are logged, counted in `nova_errored_container_images` and never auto-closed.
  Private registries need credentials: see `registryAuth.existingSecret` in the Helm chart
  (a `dockerconfigjson` secret exposed via `DOCKER_CONFIG`).
- **Helm vs. container issues**: a container is only skipped if *all* its workloads belong to a
  Helm release that is itself reported as outdated. Otherwise the issue notes the owning release.
- **Limitations of Nova**: images pinned by digest only, tagged `latest`, or with non-semver
  tags are not checked.

### Ignoring images

Nova reports container images by **name without tag** (e.g. `ghcr.io/fluxcd/helm-controller`)
and the versions separately. Each `ignoreImages` pattern is matched against both
`<name>` and `<name>:<proposed tag>`:

| Pattern | Effect |
|---------|--------|
| `ghcr.io/fluxcd/*` | every image under `ghcr.io/fluxcd/` |
| `ghcr.io/fluxcd/*:*` | same (the tag part matches any proposed tag) |
| `*/busybox` | `busybox` from any registry/org, including bare `busybox` |
| `grafana/rollout-operator` | also matches `docker.io/grafana/rollout-operator` |
| `registry.k8s.io/sig-storage/csi-provisioner:v6*` | only proposed **v6.x** updates; v5.x updates are still reported |

`*` matches any sequence of characters, including `/` and `:`.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | GitHub personal access token |
| `GITHUB_OWNER` | GitHub repository owner |
| `GITHUB_REPO` | GitHub repository name |
| `KUBECONFIG` | Path to kubeconfig file |
| `KUBE_CONTEXT` | Kubernetes context |
| `PUSHGATEWAY_URL` | Prometheus Pushgateway URL |
| `JOB_NAME` | Pushgateway job name |
| `LOG_LEVEL` | Log level (debug, info, warn, error) |
| `DRY_RUN` | Enable dry-run mode (true/false) |
| `SCAN_HELM` | Enable Helm scanning (true/false) |
| `SCAN_CONTAINERS` | Enable container scanning (true/false) |
| `MIN_SEVERITY` | Minimum severity (minor, major, critical) |
| `AUTO_CLOSE_RESOLVED` | Close issues of components that are no longer outdated (true/false) |
| `DOCKER_CONFIG` | Directory with `config.json` registry credentials (read by Nova and the OCI chart lookup) |

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `nova_outdated_helm_charts_total` | Gauge | Count of outdated Helm releases |
| `nova_outdated_containers_total` | Gauge | Count of outdated container images |
| `nova_helm_chart_version_info` | GaugeVec | Helm chart version details |
| `nova_container_version_info` | GaugeVec | Container version details |
| `nova_scan_duration_seconds` | Histogram | Scan duration |
| `nova_scan_last_success_timestamp` | Gauge | Last successful scan timestamp |
| `nova_issues_created_total` | Counter | GitHub issues created |
| `nova_scan_errors_total` | Counter | Scan errors |
| `nova_issues_closed_total` | Counter | GitHub issues closed because the component is no longer outdated |
| `nova_unresolved_helm_releases` | Gauge | Helm releases whose latest version could not be determined |
| `nova_errored_container_images` | Gauge | Container images Nova could not check |

## GitHub Issues

Issues are created with the following format:

**Helm Chart Updates:**
- **Title**: `[Nova] Update Helm chart: <name> (<current> → <latest>)`
- **Labels**: `nova-scan`, `claude-code`, `helm-update`

**Container Image Updates:**
- **Title**: `[Nova] Update container image: <name> (<current> → <latest>)`
- **Labels**: `nova-scan`, `claude-code`, `container-update`

**Body** includes:
- Version information table
- Update checklist (Flux-aware)
- HelmRelease update snippet (Helm) / Affected workloads (Container)
- Useful commands

## Grafana Dashboard

Import `deploy/grafana-dashboard.json` into Grafana to visualize:

- Current outdated component counts
- Trend over time
- Scan duration histogram
- Issues created rate
- Detailed version table

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                   nova-scanner (Go)                     │
├─────────────────────────────────────────────────────────┤
│  cmd/scanner/main.go         - entrypoint, config       │
│  pkg/nova/scanner.go         - Nova module integration  │
│  pkg/flux, pkg/kube          - Flux sources, Helm owners│
│  pkg/version                 - Version comparison       │
│  pkg/github/issues.go        - GitHub issue creation    │
│  pkg/metrics/prometheus.go   - Prometheus metrics       │
│  pkg/logging/logger.go       - Structured logging       │
└─────────────────────────────────────────────────────────┘
         │                │                │
         ▼                ▼                ▼
    ┌─────────┐    ┌───────────┐    ┌────────────┐
    │  Nova   │    │  GitHub   │    │ Pushgateway│
    │ (scan)  │    │   API     │    │ (metrics)  │
    └─────────┘    └───────────┘    └────────────┘
```

## Development

```bash
# Run tests
make test

# Run linter
make lint

# Build and run
make build && ./bin/nova-scanner --config=config.yaml
```

## License

MIT
