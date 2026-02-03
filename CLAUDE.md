# Nova Scanner Developer Guide

## Project Overview

Nova Automated Cluster Scanner - detects outdated Helm charts and container images in Kubernetes clusters using Fairwinds Nova CLI.

## Build Commands

- `make build` - Build the binary
- `make test` - Run all tests
- `make lint` - Run linter
- `make docker-build` - Build Docker image
- `make dry-run` - Run scanner in dry-run mode

## Test Commands

- `go test -v ./...` - Run all tests verbosely
- `go test -v -run=TestName` - Run a specific test by name
- `go test -race ./...` - Run tests with race detector

## Code Style

- Use `goimports` for formatting
- Follow standard Go formatting conventions
- Group imports: standard library first, then third-party
- Use PascalCase for exported types/methods, camelCase for variables
- Add comments for public API and complex logic

## Helm Chart Development

The Helm chart is located in `charts/nova-scanner/`.

### Local Development

```bash
# Lint the chart
helm lint charts/nova-scanner

# Template the chart (dry-run)
helm template nova-scanner charts/nova-scanner --namespace nova-scanner

# Template with custom values
helm template nova-scanner charts/nova-scanner -f my-values.yaml

# Install locally for testing
helm install nova-scanner charts/nova-scanner --namespace nova-scanner --create-namespace --dry-run

# Upgrade existing release
helm upgrade nova-scanner charts/nova-scanner --namespace nova-scanner
```

### Using the Published Chart

```bash
# Add the Helm repository
helm repo add nova-scanner https://olohmann.github.io/nova-automated-cluster-scanner

# Update repos
helm repo update

# Install the chart
helm install nova-scanner nova-scanner/nova-scanner --namespace nova-scanner --create-namespace
```

## CI/CD Workflows

### CI Workflow (`ci.yaml`)

Runs on every push to `main` and on pull requests. Jobs:
- **lint** - Run golangci-lint
- **test** - Run tests with race detector
- **build** - Build binary and Docker image (no push)
- **helm-lint** - Lint the Helm chart

### CD Workflow (`cd.yaml`)

Triggered by pushing a `v*` tag (e.g., `v1.2.3`). All jobs run in a single workflow:
1. **prepare** - Extract and validate version from tag
2. **test** - Run tests
3. **docker** - Build and push to GHCR (tags: `vX.Y.Z`, `<sha>`, `latest` if not prerelease)
4. **binaries** - Build for 5 platforms (linux/darwin/windows × amd64/arm64)
5. **release** - Create GitHub Release with binaries
6. **helm** - Publish chart to gh-pages

## Release Process

Use the `/release` skill in Claude Code to create releases.

### Creating a Release

```bash
# In Claude Code, run:
/release 0.5.0

# For pre-releases:
/release 0.5.0-rc1
```

The skill will:
1. Validate the version format
2. Check for clean git state
3. Update `charts/nova-scanner/Chart.yaml` (version + appVersion)
4. Update `deploy/cronjob.yaml` (image tag)
5. Show diff for review
6. Commit: `release: v0.5.0`
7. Create annotated tag: `v0.5.0`
8. Push commit and tag (triggers CD workflow)

### Release Flow

```
/release 0.5.0
     │
     ▼
┌─────────────────────┐
│ Claude Skill        │
│ • Update Chart.yaml │
│ • Update cronjob    │
│ • Commit & Tag      │
│ • Push              │
└─────────────────────┘
     │
     ▼ (tag triggers cd.yaml)
┌─────────────────────┐
│ CD Workflow         │
│ • Test              │
│ • Docker → GHCR     │
│ • Binaries → Release│
│ • Helm → gh-pages   │
└─────────────────────┘
```

### Monitoring a Release

```bash
# Watch the CD workflow
gh run list --workflow=cd.yaml --limit=1
gh run watch
```

### Verifying a Release

```bash
# Check GitHub release exists with binaries
gh release view v0.5.0

# Check Docker image is available
docker pull ghcr.io/olohmann/nova-automated-cluster-scanner:v0.5.0

# Check Helm chart is available
helm repo update nova-scanner
helm search repo nova-scanner --versions | grep 0.5.0
```

### Prerelease Support

Versions with a hyphen (e.g., `1.2.3-rc1`, `1.2.3-beta`) are treated as prereleases:
- GitHub Release marked as prerelease
- Docker image does NOT get `latest` tag

### Prerequisites

- `yq` v4+ installed locally (`brew install yq`)
- gh-pages branch exists with Helm repo index
- Repository has `packages:write` permission for GHCR

## Dependencies

- Go 1.22+
- Nova CLI (bundled in container image)
- Helm 3 (for chart development)

## Project Structure

```
├── cmd/scanner/          # Main entrypoint
├── pkg/
│   ├── config/           # Configuration handling
│   ├── github/           # GitHub issue creation
│   ├── logging/          # Structured logging
│   ├── metrics/          # Prometheus metrics
│   └── nova/             # Nova CLI integration
├── charts/nova-scanner/  # Helm chart
├── deploy/               # Raw Kubernetes manifests
└── .github/workflows/    # CI/CD workflows
```
