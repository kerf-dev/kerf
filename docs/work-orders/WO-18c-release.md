# WO-18c Multi-arch images and release

- **Phase / wave:** 5 / 1 (parallel with WO-18a and WO-18b)
- **Owns:** `cmd/*/Dockerfile`, `web/Dockerfile`, `web/nginx*` (only what the image needs to run unprivileged), `make/release.mk` (new), `docs/release.md` (new).
- **Does not own:** `deploy/charts/**` (WO-18b and WO-18d) and the per-component `make/<component>.mk` dev targets. Don't change how `make deploy-*` builds for k3d, except to make it work with the new Dockerfiles.
- **Decisions:** D-028 (multi-arch, ECR, OCI chart).

## Deliverables
1. **Every Dockerfile cross-compiles:** `FROM --platform=$BUILDPLATFORM golang:…`, `ARG TARGETOS TARGETARCH`, `GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0`.
   - The runtime images stay distroless nonroot.
   - The web image runs as non-root on an unprivileged port (8080), with a read-only root filesystem and writable paths only for temp, cache and the pid file. Document which paths those are for WO-18b.
   - nginx moves to 8080 (`web/nginx/default.conf.template`). WO-18b adds a `containerPort` value to the web chart; until both merge, test the web image with `--set` overrides or `docker run`. Don't edit the chart.
2. **`make release`** with `REGISTRY=` (e.g. `<acct>.dkr.ecr.<region>.amazonaws.com/resource-optimizer`), `VERSION=` (semver; defaults to `git describe`) and `PLATFORMS=linux/amd64,linux/arm64`.
   - Builds and pushes all 5 images with `docker buildx` (create a builder if missing).
   - Writes `bin/release/images.txt` with `name tag digest` per image.
   - Generates an SBOM per image (`buildx --sbom=true` or syft) and adds provenance (`--provenance=mode=max`).
   - `make release-chart` packages the chart(s) with version and appVersion `VERSION` and runs `helm push` to `oci://$(REGISTRY)/charts`. Until WO-18d lands, it packages the 5 component charts; make the chart list a variable.
   - `make release-dry` builds both architectures without pushing.
3. **Local registry dry run:** `make release REGISTRY=localhost:5005/ro-release VERSION=0.1.0-rc.0` works against the k3d registry. buildx needs a builder that can reach `localhost:5005`: document the `--driver-opt network=host` or registry-mirror setup.
4. **`docs/release.md`:**
   - the ECR login commands (`aws ecr get-login-password …`)
   - the repositories to create (5 images + charts)
   - lifecycle-policy and immutable-tag recommendations
   - how to verify the result (`docker buildx imagetools inspect`)

## Verification
```bash
make ci
make release-dry                                              # both platforms build
make release REGISTRY=localhost:5005/ro-release VERSION=0.1.0-rc.0
docker buildx imagetools inspect localhost:5005/ro-release/hub:0.1.0-rc.0   # lists linux/amd64 and linux/arm64
helm show chart oci://localhost:5005/ro-release/charts/agent --version 0.1.0-rc.0 --plain-http
docker run --rm --platform linux/amd64 localhost:5005/ro-release/hub:0.1.0-rc.0 --help   # amd64 runs under emulation
```
Then on the shared fleet, with no e2e running: run `make deploy-agent` and `make deploy-webhook` once with the new Dockerfiles. Both spokes stay *Automation ready*. Don't deploy the hub, web or pricing; the orchestrator does that after merging.

## Rules
Branch `wo-18c-release`; push the branch only. Write `docs/handover/WO-18c-handover.md`.
