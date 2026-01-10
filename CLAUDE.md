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

1. Make changes to `charts/nova-scanner/`
2. Update `version` in `Chart.yaml` (bump for chart changes)
3. Update `appVersion` in `Chart.yaml` (to match container image version)
4. Commit and push to main branch
5. The `release-chart.yaml` workflow automatically:
   - Packages the chart
   - Creates a GitHub release for the chart
   - Publishes to gh-pages branch for Helm repo

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

### Application Release

1. Make code changes
2. Run tests: `make test`
3. Commit and push to main
4. Create release: `gh release create v0.x.x --generate-notes`
5. Workflow builds and pushes container image to ghcr.io

### Chart Release

1. Update chart in `charts/nova-scanner/`
2. Bump `version` in `Chart.yaml`
3. Update `appVersion` to match latest release
4. Push to main - chart is automatically released

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
