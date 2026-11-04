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

## `make release` notes

- `VERSION` defaults to the nearest `v*` tag without the `v`. With no such tag it is `0.0.0-g<sha>[-dirty]`, which is still semver, so Helm accepts it.
  `release-check` rejects non-semver versions, `+build` metadata (not allowed in image tags), and a dirty tree (unless `RELEASE_ALLOW_DIRTY=1`).
- **Builder:** `ro-release`, with the docker-container driver, `network=host`, and `bin/release/buildkitd.toml` marking
  `localhost:5005`/`127.0.0.1:5005` as plain HTTP. It is created on first use and survives across runs, along with its build cache.
  `make release-builder-rm` resets it.
- **Charts:** `RELEASE_CHARTS` (the five component charts today; set it to `resource-optimizer` after WO-18d). `helm package --dependency-update`
  runs when a `Chart.yaml` has `dependencies:`. `--plain-http` is added automatically for `localhost:*`/`127.0.0.1:*` registries.
- **Outputs:** `bin/release/images.txt` (`<repository> <tag> <index digest>`), `bin/release/charts.txt`, `bin/release/<img>.metadata.json`, and
  `bin/release/charts/*.tgz`. All are ignored by git (`/bin/`).
- Images carry OCI labels (version, revision, source) and index annotations. Each platform has an SPDX SBOM and a `mode=max` provenance attestation.
- `release-dry` uses `--output type=cacheonly`, needs no registry, and packages the charts. On this loaded host, a cold-cache run took several minutes per Go image.
  Later runs reuse the builder cache.

## Verification (2026-11-04)

| Step | Result |
|---|---|
| `make ci` | rc=0: build, vet, golangci-lint `0 issues.`, 28 test packages ok, verify-generate, redocly `Your API description is valid` |
| `make release-dry` | `release-dry OK: hub pricing agent webhook web built for linux/amd64,linux/arm64`; 5 charts packaged |
| `make release REGISTRY=localhost:5005/ro-release VERSION=0.1.0-rc.0` | rc=0, 5 images and 5 charts pushed, `images.txt`/`charts.txt` written (below) |
| `docker buildx imagetools inspect localhost:5005/ro-release/hub:0.1.0-rc.0` | OCI index: `linux/amd64`, `linux/arm64` and 2 attestation manifests. Same for pricing, agent, webhook and web |
| SBOM / provenance (`--format '{{ json .SBOM }}'` / `.Provenance`) | SPDX-2.3 per platform (63 packages for hub); SLSA provenance with revision `cfd2d2d` |
| `helm show chart oci://localhost:5005/ro-release/charts/agent --version 0.1.0-rc.0 --plain-http` | `name: agent`, `version: 0.1.0-rc.0`, `appVersion: 0.1.0-rc.0` |
| `docker run --rm --platform linux/amd64 …/hub:0.1.0-rc.0 --help` | `Usage of /hub:` rc=0. pricing rc=0; agent and webhook print usage, rc=1 (`ContinueOnError` returns ErrHelp) |
| the same with `--platform linux/arm64` (QEMU on this x86 host) | hub and pricing rc=0; agent and webhook print usage. Needs `docker pull --platform linux/arm64` first (containerd-store quirk, in `docs/release.md`) |
| web on amd64 and arm64, `--read-only --tmpfs /tmp --cap-drop ALL` | `uname -m` x86_64 / aarch64, `id -u` 101, `/healthz` ok, `/` 200 |
| `make deploy-agent AGENT_TAG=wo18c-cfd2d2d`, `make deploy-webhook WEBHOOK_TAG=wo18c-cfd2d2d` | Run after confirming no `e2e-` namespaces on either spoke. Both rc=0; agent revs 33 (spoke-1) / 26 (spoke-2), webhook revs 7 / 6, pods Ready, logs `version: wo18c-cfd2d2d`, 0 ERROR lines |
| Readiness after the deploy | `GET /v1/clusters/ro-spoke-{1,2}/readiness`: both `AutomationReady`, checkedAt 17:51Z (after the deploy), blocking 0 |

```
localhost:5005/ro-release/hub 0.1.0-rc.0 sha256:e4faebf8bfd6c4c9f4e4292dddf48b1d085534645c8e7fed3b50ce466410e4a7
localhost:5005/ro-release/pricing 0.1.0-rc.0 sha256:0eb6d259802499a36a32f6eb392afb436f8731501c42871ea950f006ec5937af
localhost:5005/ro-release/agent 0.1.0-rc.0 sha256:ea3c32f70ca96ad8316af7bbcb9a882d0e718d5e76f30353a3557702f31b7e16
localhost:5005/ro-release/webhook 0.1.0-rc.0 sha256:a66a024bd7c523f4daa31a4f79d754a4ae95ebf26e804dbd4c9ddb919ffe4e28
localhost:5005/ro-release/web 0.1.0-rc.0 sha256:b98f346010011da471472972b9677fd70a9145e3d451824df69cfbf511d2888a
```

The web image was not deployed (per the work-order), so `make deploy-web` from this branch is untested on k3d. See the merge-order caveat above.

## Fleet state left behind

- On both spokes, agent and webhook now run `wo18c-cfd2d2d`: main 0fa222b code built with the new Dockerfiles.
  - **This replaced another track's agent deploy.** Agent rev 32 / 25, tag `dev-20261104-192852`, was deployed at 19:33 IST, probably by WO-18a or WO-18b.
    Its owner should redeploy it if they still need it.
- Hub, web and pricing were not touched. The hub pod had restarted 5 times during the afternoon's host load (load average above 100). This is unrelated to this track.
- The `ro-release` buildx builder container (`buildx_buildkit_ro-release0`) stays up for later releases. `make release-builder-rm` removes it.
- `localhost:5005/ro-release/*` holds the 0.1.0-rc.0 test release.

## Follow-ups

- WO-18b: the web chart changes listed above (port 8080, non-root, `/tmp` emptyDir, no added capabilities).
- WO-18d: `RELEASE_CHARTS=resource-optimizer` (or change the default) once the umbrella chart exists.
  The chart's default `image.repository` still points at `localhost:5005/...` and `image.tag` at `dev`. A release
  install has to set them to `$REGISTRY/<name>` and `$VERSION`, or the umbrella chart should default the tag to `.Chart.AppVersion`.
- CI: a workflow (in `ci/github-workflows/`) that runs `make release` on `v*` tags with OIDC → ECR. None was added here (not owned).
- No root `.dockerignore`: BuildKit only transfers the paths the `COPY` lines reference, so this is harmless, but the hub's `COPY internal/` takes the whole tree.
