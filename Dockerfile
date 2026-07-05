# syntax=docker/dockerfile:1.7
# ── build stage ──────────────────────────────────────────────────────────────
# --platform=$BUILDPLATFORM: always build on the host arch (fast), cross-compile
# to TARGETOS/TARGETARCH in the final go build invocation.
FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

# OCI image metadata — consumed by GHCR and `docker inspect`.
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILDDATE

LABEL org.opencontainers.image.title="go-daemon-config-extraction" \
      org.opencontainers.image.source="https://github.com/arindraaribudi/config-extractor-daemon" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${BUILDDATE}"

# ca-certificates: provides the system bundle we embed into the binary below.
# Without it the binary cannot verify TLS against GCP/AWS APIs at runtime.
RUN apk add --no-cache ca-certificates

WORKDIR /src

# ── layer-cache hygiene ──────────────────────────────────────────────────────
# 1. Copy only go.mod/go.sum first → this layer caches independently of source.
# 2. go mod download populates /go/pkg/mod; cache-mount it so subsequent
#    builds don't re-download the world.
# 3. THEN copy the rest of the source. Code changes do not invalidate the
#    dependency-download layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# ── generate CA bundle + compile in one layer ────────────────────────────────
# The cert must exist before `go build` because internal/infrastructure/tls/tls.go
# uses `//go:embed certs/ca-certificates.crt`. The source-controlled .gitkeep
# placeholder is overwritten here with the real system bundle.
# A separate --mount for go-build cache keeps incremental rebuilds fast.
# GOCACHE is set to /go-build-cache; mounted RW so cache survives between
# `docker buildx build` invocations (CI uses `cache-to: type=gha` on top).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/go-build-cache \
    mkdir -p internal/infrastructure/tls/certs && \
    cp /etc/ssl/certs/ca-certificates.crt \
       internal/infrastructure/tls/certs/ca-certificates.crt && \
    GOCACHE=/go-build-cache \
    CGO_ENABLED=0 \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.revision=${REVISION}" \
      -o /out/config-extractor \
      ./cmd/config-extractor

# ── final stage ──────────────────────────────────────────────────────────────
# scratch: no shell, no libc, no busybox → minimal attack surface.
# Binary is fully static (CGO_ENABLED=0) so no dynamic linker is required.
FROM scratch

# Runtime CA bundle: required because `tls.CertPool()` calls
# x509.SystemCertPool() first. The embedded copy inside the binary is the
# secondary fallback for environments that lack /etc/ssl/certs.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --chmod=0755 --from=builder /out/config-extractor /config-extractor

ENTRYPOINT ["/config-extractor"]