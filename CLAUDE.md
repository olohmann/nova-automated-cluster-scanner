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

### Chart Release Process

The Helm chart version is automatically updated when creating a release via the **Prepare Release** workflow.

For chart-only changes (templates, values):
1. Make changes to `charts/nova-scanner/`
2. Commit and push to main branch
3. The `release-chart.yaml` workflow automatically publishes if chart version changed

The chart is published to gh-pages branch and available via Helm repo.

### Using the Published Chart

```bash
# Add the Helm repository
helm repo add nova-scanner https://olohmann.github.io/nova-automated-cluster-scanner

# Update repos
helm repo update

# Install the chart
helm install nova-scanner nova-scanner/nova-scanner --namespace nova-scanner --create-namespace
```

## Release Process

Releases are created using the **Prepare Release** workflow which ensures the Helm chart version is updated before the release tag is created.

### Creating a Release (Recommended)

Use the GitHub Actions workflow:

1. Go to **Actions** → **Prepare Release** → **Run workflow**
2. Enter the version (e.g., `0.2.0` or `0.2.0-rc1`)
3. Check "Mark as pre-release" if applicable
4. Click **Run workflow**

The workflow will:
1. Update `Chart.yaml` with the new version
2. Commit the changes
3. Create and push the git tag
4. Create the GitHub release

This triggers:
- **Build and Push** workflow: builds container image and binaries
- **Release Chart** workflow: publishes Helm chart to gh-pages

### Creating a Release (CLI)

```bash
# Run the prepare-release workflow via CLI
gh workflow run prepare-release.yaml -f version=0.2.0

# Or for a pre-release
gh workflow run prepare-release.yaml -f version=0.2.0-rc1 -f prerelease=true
```

### Manual Release (Not Recommended)

If you need to create a release manually:

1. Update `charts/nova-scanner/Chart.yaml`:
   - `version: X.Y.Z`
   - `appVersion: "vX.Y.Z"`
2. Commit: `git commit -m "chore: release vX.Y.Z"`
3. Tag: `git tag -a vX.Y.Z -m "Release vX.Y.Z"`
4. Push: `git push && git push --tags`
5. Create release: `gh release create vX.Y.Z --generate-notes`

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
