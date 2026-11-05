# syntax=docker/dockerfile:1.7
# Kerf mutating admission webhook (WO-07). Build context: repository root.
# Multi-arch (D-028, WO-18c): the Go stage runs on the build host and cross-compiles for the target
# platform, so `docker buildx build --platform linux/amd64,linux/arm64` needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY api/ api/
COPY cmd/webhook/ cmd/webhook/
COPY internal/webhook/ internal/webhook/
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/webhook ./cmd/webhook

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/webhook /webhook
USER 65532:65532
ENTRYPOINT ["/webhook"]
