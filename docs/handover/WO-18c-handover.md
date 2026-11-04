# WO-18c handover: multi-arch images and release

Branch `wo-18c-release`, cut from main 0fa222b. D-028. Release guide: `docs/release.md`.

## What changed

| File | Change |
|---|---|
| `cmd/{hub,pricing,agent,webhook}/Dockerfile` | `FROM --platform=$BUILDPLATFORM golang:1.27-alpine`, `ARG TARGETOS TARGETARCH`, `GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0`. The runtime stays `distroless/static-debian12:nonroot` (65532). |
| `web/Dockerfile` | Node stage on `$BUILDPLATFORM`. The runtime stage is `nginx:1.29-alpine` with no `RUN`, `USER 101:101`, `EXPOSE 8080`, and the HEALTHCHECK on 8080. |
| `web/nginx/nginx.conf` (new) | Unprivileged main config: no `user` directive, `pid /tmp/nginx.pid`, every `*_temp_path` under `/tmp`, includes `/tmp/conf.d/*.conf`. |
| `web/nginx/default.conf.template` | `listen 8080`. The template now installs at `/etc/nginx/templates/conf.d/` and renders to `/tmp/conf.d/default.conf` (`NGINX_ENVSUBST_OUTPUT_DIR=/tmp`). |
| `make/release.mk` (new) | `release`, `release-images`, `release-chart`, `release-dry`, `release-builder`, `release-builder-rm`. |
| `docs/release.md` (new) | ECR repositories, login, immutable tags, lifecycle policy, verification, local dry run, image runtime contract. |

The release images need no QEMU to build: the Go stages cross-compile, and the web runtime stage has no `RUN`.

**The fleet host is Intel.** `uname -m` on the build host reports x86_64, and Docker Desktop's
server is linux/amd64. So far every k3d image was amd64-only, which would not run on Graviton nodes.
That is the reverse of the assumption in the brief, and multi-arch fixes either case. Because of this,
the emulation check also runs **arm64** under QEMU.

## For WO-18b (web chart): the web image's runtime contract

- `containerPort: 8080`. The chart's Service can keep port 80 with `targetPort: http`.
- Pod and container securityContext: `runAsNonRoot: true`, `runAsUser: 101`, `runAsGroup: 101`, `readOnlyRootFilesystem: true`,
  `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]` and **no `add`**. Drop today's
  `CHOWN, SETUID, SETGID, NET_BIND_SERVICE, DAC_OVERRIDE`.
- One writable volume: `emptyDir` at **`/tmp`**. It holds `/tmp/conf.d/default.conf` (rendered at start), `/tmp/nginx.pid`, and
  `/tmp/{client,proxy,fastcgi,uwsgi,scgi}_temp`. Nothing else is written. Logs go to stdout/stderr, and `/var/cache/nginx` is unused.
  A `sizeLimit` of 64Mi is plenty, because request bodies are capped at 1m.
- Probes: `GET /healthz` on `http` (8080). They are unchanged apart from the port.
- The environment variables are the same: `HUB_UPSTREAM`, `HUB_TOKEN_FILE`, `ANONYMOUS_READ`.

> **Merge-order caveat:** the chart on main still has `containerPort: 80` and probes on `http`. Until WO-18b's port
> change is merged, `make deploy-web` with this image fails its probes, because nginx listens on 8080. Merge WO-18b's web
> chart change together with this branch or before it. Before that, don't run `make deploy-web` from this branch.
> The Service's `targetPort: http` follows the containerPort, so no other change is needed.
