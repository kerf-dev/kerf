# syntax=docker/dockerfile:1.7
# Hub image. Build from the repo root: docker build -f cmd/hub/Dockerfile .
# Multi-arch: the Go stage runs on the build host and cross-compiles for the target
# platform, so `docker buildx build --platform linux/amd64,linux/arm64` needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
ENV GOTOOLCHAIN=auto CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY api/ api/
COPY internal/ internal/
COPY cmd/hub/ cmd/hub/
COPY tools/synth/ tools/synth/
ARG VERSION=dev
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hub ./cmd/hub
# The demo history of the laptop quickstart (docs/quickstart.md) is generated in the cluster by this tool; it
# only runs when the chart's quickstart job asks for it.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/synth ./tools/synth

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/hub /hub
COPY --from=build /out/synth /synth
COPY db/migrations/ /migrations/
ENV HUB_MIGRATIONS_DIR=/migrations
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hub"]
