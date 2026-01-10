.PHONY: build test lint clean docker-build docker-push deploy run dry-run tidy

# Variables
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BINARY_NAME := nova-scanner
IMAGE_NAME ?= ghcr.io/olohmann/nova-scanner
GO := go

# Build flags
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)"

# Disable CGO for pure Go builds (avoids macOS clang warnings)
export CGO_ENABLED=0

# Default target
all: tidy lint test build

# Download and tidy dependencies
tidy:
	$(GO) mod tidy

# Build the binary
build: tidy
	$(GO) build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/scanner

# Run tests (without race detector since CGO is disabled)
test:
	$(GO) test -v -coverprofile=coverage.out ./...

# Run tests with race detector (requires CGO)
test-race:
	CGO_ENABLED=1 $(GO) test -v -race -coverprofile=coverage.out ./...

# Run linter
lint:
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run ./...

# Clean build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out

# Build Docker image
docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE_NAME):$(VERSION) -t $(IMAGE_NAME):latest .

# Push Docker image
docker-push: docker-build
	docker push $(IMAGE_NAME):$(VERSION)
	docker push $(IMAGE_NAME):latest

# Deploy to Kubernetes
deploy:
	kubectl apply -f deploy/namespace.yaml
	kubectl apply -f deploy/rbac.yaml
	kubectl apply -f deploy/configmap.yaml
	kubectl apply -f deploy/externalsecret.yaml
	kubectl apply -f deploy/cronjob.yaml

# Run locally (requires config file)
run: build
	./bin/$(BINARY_NAME) --config=config.yaml

# Run in dry-run mode
dry-run: build
	DRY_RUN=true ./bin/$(BINARY_NAME) --config=config.yaml

# Create a test job from the cronjob
test-job:
	kubectl create job --from=cronjob/nova-scanner nova-scanner-test-$(shell date +%s) -n nova-scanner

# View logs from the latest job
logs:
	kubectl logs -n nova-scanner -l job-name --tail=100

# Help
help:
	@echo "Available targets:"
	@echo "  all          - Run tidy, lint, test, and build (default)"
	@echo "  tidy         - Download and tidy Go module dependencies"
	@echo "  build        - Build the binary"
	@echo "  test         - Run tests with coverage"
	@echo "  lint         - Run golangci-lint"
	@echo "  clean        - Remove build artifacts"
	@echo "  docker-build - Build Docker image"
	@echo "  docker-push  - Push Docker image to registry"
	@echo "  deploy       - Deploy to Kubernetes"
	@echo "  run          - Run locally with config.yaml"
	@echo "  dry-run      - Run locally in dry-run mode"
	@echo "  test-job     - Create a test job from the CronJob"
	@echo "  logs         - View logs from the latest job"
	@echo ""
	@echo "Variables:"
	@echo "  VERSION      - Version tag (default: git describe or 'dev')"
	@echo "  IMAGE_NAME   - Docker image name (default: ghcr.io/olohmann/nova-scanner)"
