# syntax=docker/dockerfile:1.7
# Web UI (WO-14): static SPA served by nginx, /v1/ proxied to the hub with the `ui` token.
# Build context: web/  (docker build -t resource-optimizer/web:dev web/)
#
# Multi-arch (D-028, WO-18c): the Node stage runs on the build host (its output is plain JS/CSS), and the
# runtime stage has no RUN step, so `--platform linux/amd64,linux/arm64` needs no emulation.
#
# Runtime contract (for the chart, WO-18b):
#   - runs as uid/gid 101 (runAsNonRoot works), listens on 8080, needs no capabilities;
#   - readOnlyRootFilesystem: true with one writable emptyDir at /tmp (rendered config, pid file, temp
#     buffers; see nginx/nginx.conf);
#   - liveness/readiness: GET /healthz on 8080.

FROM --platform=$BUILDPLATFORM node:22-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

FROM nginx:1.29-alpine
# Pick up resolvers from /etc/resolv.conf for the runtime `resolver` directive. Templates render into
# /tmp/conf.d (writable), which nginx.conf includes; the stock /etc/nginx/conf.d is not read.
ENV NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1 \
    NGINX_ENVSUBST_OUTPUT_DIR=/tmp \
    HUB_UPSTREAM=hub.resource-optimizer.svc.cluster.local:8080 \
    HUB_TOKEN_FILE=/etc/resource-optimizer/hub-token/token
COPY nginx/nginx.conf /etc/nginx/nginx.conf
COPY nginx/default.conf.template /etc/nginx/templates/conf.d/default.conf.template
COPY nginx/security-headers.conf /etc/nginx/snippets/security-headers.conf
COPY --chmod=0755 nginx/05-hub-token.envsh /docker-entrypoint.d/05-hub-token.envsh
COPY --from=build /src/dist /usr/share/nginx/html
USER 101:101
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
