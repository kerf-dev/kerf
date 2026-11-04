# syntax=docker/dockerfile:1
# Web UI (WO-14): static SPA served by nginx, /v1/ proxied to the hub with the `ui` token.
# Build context: web/  (docker build -t resource-optimizer/web:dev web/)

FROM node:22-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
RUN npm run build

FROM nginx:1.29-alpine
# Pick up resolvers from /etc/resolv.conf for the runtime `resolver` directive.
ENV NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1 \
    HUB_UPSTREAM=hub.resource-optimizer.svc.cluster.local:8080 \
    HUB_TOKEN_FILE=/etc/resource-optimizer/hub-token/token
RUN rm -f /etc/nginx/conf.d/default.conf
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY nginx/security-headers.conf /etc/nginx/snippets/security-headers.conf
COPY --chmod=0755 nginx/05-hub-token.envsh /docker-entrypoint.d/05-hub-token.envsh
COPY --from=build /src/dist /usr/share/nginx/html
EXPOSE 80
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1/healthz >/dev/null || exit 1
