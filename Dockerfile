# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install git for fetching dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
ARG VERSION=dev
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /nova-scanner \
    ./cmd/scanner

# Download Nova CLI
# >= 3.12.2 is required: earlier versions propose bare tags for variant tags (e.g. 1.2.3-alpine -> 1.2.4)
ARG NOVA_VERSION=3.12.2
RUN wget -qO- "https://github.com/FairwindsOps/nova/releases/download/v${NOVA_VERSION}/nova_${NOVA_VERSION}_linux_${TARGETARCH}.tar.gz" | tar xz -C /usr/local/bin nova

# Runtime stage - use alpine for shell commands (Nova CLI needs it)
FROM alpine:3.19

# Install ca-certificates for HTTPS connections
RUN apk add --no-cache ca-certificates

# Copy binaries from builder
COPY --from=builder /nova-scanner /nova-scanner
COPY --from=builder /usr/local/bin/nova /usr/local/bin/nova

# Create non-root user
RUN addgroup -g 10001 scanner && adduser -D -u 10001 -G scanner scanner

# Run as non-root user
USER scanner

ENTRYPOINT ["/nova-scanner"]
