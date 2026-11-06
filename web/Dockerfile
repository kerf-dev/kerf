# syntax=docker/dockerfile:1.7
# Web UI: static SPA served by nginx, /v1/ proxied to the hub (the browser's session cookie only).
# Build context: web/  (docker build -t kerf/web:dev web/)
#
# Multi-arch: the Node stage runs on the build host (its output is plain JS/CSS), and the
# runtime stage has no RUN step, so `--platform linux/amd64,linux/arm64` needs no emulation.
#
# Runtime contract (for the chart):
#   - runs as uid/gid 101 (runAsNonRoot works), listens on 8080, needs no capabilities;
#   - readOnlyRootFilesystem: true with one writable emptyDir at /tmp (rendered config, pid file, temp
#     buffers; see nginx/nginx.conf);
#   - liveness/readiness: GET /healthz on 8080.

FROM --platform=$BUILDPLATFORM node:25-alpine@sha256:bdf2cca6fe3dabd014ea60163eca3f0f7015fbd5c7ee1b0e9ccb4ced6eb02ef4 AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
# The version shown on Admin → Maintenance (make release passes --build-arg VERSION).
ARG VERSION=dev
RUN KERF_VERSION=${VERSION} npm run build

FROM nginx:1.30-alpine-slim@sha256:f65ed258206d1eb6c4eb6141111715a2cf94ac0a4ec9675fc2da186f7aadb163
# Pick up resolvers from /etc/resolv.conf for the runtime `resolver` directive. Templates render into
# /tmp/conf.d (writable), which nginx.conf includes; the stock /etc/nginx/conf.d is not read.
ENV NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1 \
    NGINX_ENVSUBST_OUTPUT_DIR=/tmp \
    HUB_UPSTREAM=hub.kerf.svc.cluster.local:8080 \
    TRUSTED_PROXIES=""
COPY nginx/nginx.conf /etc/nginx/nginx.conf
COPY nginx/default.conf.template /etc/nginx/templates/conf.d/default.conf.template
COPY nginx/security-headers.conf /etc/nginx/snippets/security-headers.conf
COPY --chmod=0755 nginx/05-real-ip.envsh /docker-entrypoint.d/05-real-ip.envsh
COPY --from=build /src/dist /usr/share/nginx/html
USER 101:101
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
