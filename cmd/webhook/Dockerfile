# syntax=docker/dockerfile:1.7
# resource-optimizer mutating admission webhook (WO-07). Build context: repository root.
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY api/ api/
COPY cmd/webhook/ cmd/webhook/
COPY internal/webhook/ internal/webhook/
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/webhook ./cmd/webhook

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/webhook /webhook
USER 65532:65532
ENTRYPOINT ["/webhook"]
