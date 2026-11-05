# syntax=docker/dockerfile:1.7
# Build context: repository root (see dev/scripts/tilt-deploy.sh and make/pricing.mk).
# Multi-arch (D-028, WO-18c): the Go stage runs on the build host and cross-compiles for the target
# platform, so `docker buildx build --platform linux/amd64,linux/arm64` needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pricing /pricing
# Default price file for the static provider; the chart can mount another one over it.
COPY dev/prices.yaml /etc/resource-optimizer/prices.yaml
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/pricing"]
