# syntax=docker/dockerfile:1.7
# Build context: repository root (see dev/scripts/tilt-deploy.sh and make/pricing.mk).
# Multi-arch: the Go stage runs on the build host and cross-compiles for the target
# platform, so `docker buildx build --platform linux/amd64,linux/arm64` needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/pricing/ cmd/pricing/
COPY api/v1alpha1/ api/v1alpha1/
COPY internal/hub/hubapi/ internal/hub/hubapi/
COPY internal/hub/datasource/ internal/hub/datasource/
COPY internal/pricing/ internal/pricing/
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/pricing ./cmd/pricing

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/pricing /pricing
# Default price file for the static provider; the chart can mount another one over it.
COPY dev/prices.yaml /etc/kerf/prices.yaml
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/pricing"]
