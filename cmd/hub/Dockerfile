# syntax=docker/dockerfile:1
# Hub image (WO-04). Build from the repo root: docker build -f cmd/hub/Dockerfile .
FROM golang:1.27-alpine AS build
ENV GOTOOLCHAIN=auto CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY api/ api/
COPY internal/ internal/
COPY cmd/hub/ cmd/hub/
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w" -o /out/hub ./cmd/hub

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/hub /hub
COPY db/migrations/ /migrations/
ENV HUB_MIGRATIONS_DIR=/migrations
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/hub"]
