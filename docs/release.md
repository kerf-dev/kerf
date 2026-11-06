# Releasing kerf

Official releases are built by GitHub Actions and published to GitHub Container Registry
([Releasing on GitHub](#releasing-on-github)). The rest of this page is the same build run by hand with
`make release`, for example to put a release into your own ECR.

A release publishes five multi-arch container images (`linux/amd64` and `linux/arm64`) and the
Helm charts as OCI artifacts. Both go to one registry prefix, for example
`123456789012.dkr.ecr.eu-west-1.amazonaws.com/kerf`. The targets are in `make/release.mk`.

| Artifact | Reference |
|---|---|
| Images | `$REGISTRY/{hub,pricing,agent,webhook,web}:$VERSION` |
| Charts | `oci://$REGISTRY/charts/kerf` (the one chart to install, `docs/install/eks.md`) and the component charts `oci://$REGISTRY/charts/{hub,pricing,agent,webhook,web}`, version `$VERSION` |

Every image is an OCI index with one manifest per platform. Each index also holds an SBOM (SPDX) and
a SLSA provenance attestation (`mode=max`). Charts are packaged with `version` and `appVersion` set to `$VERSION`.
The umbrella chart's Artifact Hub annotations are set at packaging too: every image in `artifacthub.io/images`
becomes `$REGISTRY/<component>:$VERSION`, and `artifacthub.io/prerelease` is `"true"` for a `-suffix` version
(`-rc.N`), `"false"` otherwise. There is nothing to edit by hand.


## Releasing on GitHub

The release workflow (`.github/workflows/release.yaml`) runs when
a tag `vX.Y.Z` or `vX.Y.Z-rc.N` is pushed.

| Step | What happens |
|---|---|
| Checks | `CHANGELOG.md` has a `## [X.Y.Z]` section (final versions only); the whole `ci` workflow passes on the tagged commit; the commit is on `main` |
| Build and push | `make release REGISTRY=ghcr.io/<owner>/kerf VERSION=X.Y.Z RELEASE_CHART_DEFAULT_REGISTRY=1`: the five images for both platforms with SBOM and provenance, and the six charts |
| Chart defaults | `RELEASE_CHART_DEFAULT_REGISTRY=1` makes each packaged chart default to this release's GHCR images, so `helm install` needs no registry setting. Explicit values still win (the EKS guide's ECR values) |
| Sign | `cosign sign` (keyless, the workflow's GitHub OIDC identity) for every image and chart digest |
| GitHub Release | Title `kerf X.Y.Z`. Notes (`tools/release/notes.sh`): the CHANGELOG section, install steps, every artifact with its digest, the `cosign verify` command, then GitHub's list of merged pull requests by label (`.github/release.yml`). Attached: the chart packages, `images.txt`, `charts.txt`, `SHA256SUMS`. A `-rc.N` version is a pre-release and never "latest" |

To release:

```bash
# 1. In CHANGELOG.md, rename "## [Unreleased]" to "## [0.2.0] - 2026-11-20" and add a new empty Unreleased section.
git commit -am "Release 0.2.0" && git push origin main
# 2. Tag the commit on main and push the tag.
git tag -a v0.2.0 -m "kerf 0.2.0" && git push origin v0.2.0
```

For a release candidate, skip step 1 (its notes use the `[0.2.0]` or `[Unreleased]` section) and tag `v0.2.0-rc.1`.
Preview the notes locally with `tools/release/notes.sh 0.2.0` (after a `make release`, it lists the digests too).

GHCR facts to know:

- Images: `ghcr.io/<owner>/kerf/{hub,pricing,agent,webhook,web}:X.Y.Z`. Charts:
  `oci://ghcr.io/<owner>/kerf/charts/{kerf,hub,pricing,agent,webhook,web}`.
- A new package is created private. Once the repository is public, set each of the 11 packages to public
  once (package settings, "Change visibility"); later releases keep it.
- GHCR tags can be overwritten, unlike the ECR setup below. The workflow never re-releases a tag; to fix a
  release, publish a new patch or `-rc.N` version.
- Every image and chart is signed. Users verify with the `cosign verify` command in the release notes.

## Prerequisites

- Docker with buildx 0.13 or later, Helm 3.13 or later (`--plain-http`), the AWS CLI v2, `jq`.
- A clean, committed tree. `make release` refuses uncommitted changes unless you pass `RELEASE_ALLOW_DIRTY=1`, which is for test releases only.
- **No QEMU is needed to build.** The Go images cross-compile (`FROM --platform=$BUILDPLATFORM`,
  `GOOS=$TARGETOS GOARCH=$TARGETARCH`). The web image builds its JS on the build platform, and its runtime stage has no `RUN`.
  You only need emulation to *run* the other architecture's image locally.

## 1. One-time ECR setup

```bash
export AWS_REGION=eu-west-1
export ACCOUNT=123456789012
export ECR=$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com
export REGISTRY=$ECR/kerf

for repo in hub pricing agent webhook web \
            charts/hub charts/pricing charts/agent charts/webhook charts/web charts/kerf; do
  aws ecr create-repository --region "$AWS_REGION" \
    --repository-name "kerf/$repo" \
    --image-tag-mutability IMMUTABLE \
    --image-scanning-configuration scanOnPush=true \
    --encryption-configuration encryptionType=KMS
done
```

ECR does not create repositories on push, so every image and chart needs its own repository:
`kerf/<image>` and `kerf/charts/<chart>`. The `charts/kerf`
repository holds the one chart you install (`docs/install/eks.md`).

### Immutable tags

Use `IMMUTABLE` tags, as above. A version is then published exactly once:

- Re-running `make release` with an existing `VERSION` fails at the push.
- To fix a bad release, publish a new patch or `-rc.N` version. Never overwrite a tag.
- Deploy by tag, or pin the digest from `bin/release/images.txt`.

The SBOM and provenance manifests sit inside each tag's index, so they do not need tags of their own.

### Lifecycle policy

Never expire released semver tags. Expire only release candidates, untagged dev builds, and untagged leftovers:

```bash
cat > /tmp/ro-lifecycle.json <<'EOF'
{
  "rules": [
    { "rulePriority": 1, "description": "keep the 10 newest release candidates",
      "selection": { "tagStatus": "tagged", "tagPatternList": ["*-rc.*"],
                     "countType": "imageCountMoreThan", "countNumber": 10 },
      "action": { "type": "expire" } },
    { "rulePriority": 2, "description": "untagged builds (0.0.0-g<sha> test releases)",
      "selection": { "tagStatus": "tagged", "tagPatternList": ["0.0.0-g*"],
                     "countType": "sinceImagePushed", "countUnit": "days", "countNumber": 14 },
      "action": { "type": "expire" } },
    { "rulePriority": 3, "description": "untagged images older than 30 days",
      "selection": { "tagStatus": "untagged",
                     "countType": "sinceImagePushed", "countUnit": "days", "countNumber": 30 },
      "action": { "type": "expire" } }
  ]
}
EOF
for repo in hub pricing agent webhook web; do
  aws ecr start-lifecycle-policy-preview --region "$AWS_REGION" \
    --repository-name "kerf/$repo" --lifecycle-policy-text file:///tmp/ro-lifecycle.json
done
```

The per-platform manifests and attestations of a multi-arch image show up in ECR as untagged images.
Before you apply rule 3, run the preview above and check that it lists none of the digests a kept tag
references (`docker buildx imagetools inspect --raw`). Then apply the policy with
`aws ecr put-lifecycle-policy` and the same arguments.

### Pull access from EKS

- **Same account:** the node IAM role needs `AmazonEC2ContainerRegistryReadOnly`.
- **Other accounts:** add a repository policy that grants `ecr:BatchGetImage`, `ecr:GetDownloadUrlForLayer` and
  `ecr:BatchCheckLayerAvailability` to their node roles.
- **Argo CD:** to pull the OCI charts it needs an ECR credential, for example a registry secret refreshed by a CronJob, or the ECR helper of the External Secrets Operator.
- **Mixed node groups:** Graviton (arm64) and x86 nodes each pull their own platform from the same tag.

## 2. Log in

```bash
aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$ECR"
aws ecr get-login-password --region "$AWS_REGION" | helm registry login --username AWS --password-stdin "$ECR"
```

Both tokens are valid for 12 hours.

## 3. Release

```bash
git tag v0.1.0 && git push origin v0.1.0        # optional: VERSION defaults to the nearest v* tag
make release REGISTRY=$REGISTRY VERSION=0.1.0   # images + charts
```

| Variable | Default | Meaning |
|---|---|---|
| `REGISTRY` | (required) | `host[/path]`, no scheme, no trailing slash |
| `VERSION` | `git describe` of the nearest `v*` tag without the `v`, else `0.0.0-g<sha>` | semver; `+build` metadata is not allowed (not a valid image tag) |
| `PLATFORMS` | `linux/amd64,linux/arm64` | buildx platform list |
| `RELEASE_IMAGES` | `hub pricing agent webhook web` | images to build |
| `RELEASE_CHARTS` | `kerf hub pricing agent webhook web` | charts under `deploy/charts/` |
| `RELEASE_BUILDER` | `ro-release` | buildx builder, created if missing |
| `RELEASE_INSECURE_REGISTRIES` | `localhost:5005 127.0.0.1:5005` | registries the builder reaches over plain HTTP |

The targets:

- **`make release`** runs `release-images` and then `release-chart`.
- **`make release-images`** builds and pushes the images and writes `bin/release/images.txt`.
  Each line is `<repository> <tag> <index digest>`.
- **`make release-chart`** packages the charts into `bin/release/charts/`, runs `helm push` for each one,
  and writes `bin/release/charts.txt`.
- **`make release-dry`** builds every image for every platform and packages the charts. It needs no registry and pushes nothing.

Example `bin/release/images.txt`:

```
123456789012.dkr.ecr.eu-west-1.amazonaws.com/kerf/hub 0.1.0 sha256:…
```

### The buildx builder

Multi-platform output, SBOMs and provenance need a BuildKit builder that can export an OCI index.
`make release-builder` creates `ro-release` with:

- the `docker-container` driver;
- `--driver-opt network=host`, so a registry on the build host's `localhost` is reachable (the k3d registry on `:5005`);
- a `buildkitd.toml` that marks `RELEASE_INSECURE_REGISTRIES` as plain HTTP.

That config is fixed when the builder is created. After you change the variable, run
`make release-builder-rm`, and the next release recreates the builder.

ECR needs no extra configuration, because buildx uses the `docker login` credentials. On Docker Desktop
with the containerd image store, `RELEASE_BUILDER=desktop-linux` also works.

## 4. Verify

```bash
V=0.1.0
docker buildx imagetools inspect $REGISTRY/hub:$V                    # linux/amd64 + linux/arm64 (+ attestation manifests)
docker buildx imagetools inspect $REGISTRY/hub:$V --format '{{ json .SBOM.SPDX.name }}'
docker buildx imagetools inspect $REGISTRY/hub:$V --format '{{ json .Provenance.SLSA.buildDefinition.externalParameters }}'
helm show chart oci://$REGISTRY/charts/agent --version $V
docker run --rm --platform linux/amd64 $REGISTRY/hub:$V --help      # each platform runs
docker run --rm --platform linux/arm64 $REGISTRY/hub:$V --help      # (emulated on the other arch)
aws ecr describe-image-scan-findings --repository-name kerf/hub --image-id imageTag=$V
```

The digest in `imagetools inspect` (`Digest:`) must match the one in `bin/release/images.txt`.

**Docker Desktop with the containerd image store:** when a tag is already present locally for one platform,
`docker run --platform <other>` fails with `failed to extract layer … content digest … not found`.
Run `docker pull --platform <other> <image>` first.

### Local dry run against the k3d registry

```bash
make release-dry
make release REGISTRY=localhost:5005/ro-release VERSION=0.1.0-rc.0
docker buildx imagetools inspect localhost:5005/ro-release/hub:0.1.0-rc.0
helm show chart oci://localhost:5005/ro-release/charts/agent --version 0.1.0-rc.0 --plain-http
```

Add `RELEASE_ALLOW_DIRTY=1` to release uncommitted changes. The k3d registry accepts re-pushes of the same tag, so local runs can repeat.

## Image runtime contract

| Image | User | Ports | Writable paths needed with `readOnlyRootFilesystem: true` |
|---|---|---|---|
| hub | 65532 (distroless nonroot) | 8080 | none from the image (the chart mounts config and secrets read-only) |
| pricing | 65532 | 9090 | none |
| agent | 65532 | metrics (chart value), 8081 probes | none |
| webhook | 65532 | 9443 TLS, 8080 metrics, 8081 probes | none; the chart mounts the cert dir read-only |
| web (nginx) | 101:101 | **8080** | **`/tmp`** (emptyDir): rendered `conf.d/default.conf`, `nginx.pid`, `*_temp` buffers |

Every chart sets `readOnlyRootFilesystem: true`; the web chart mounts emptyDirs on nginx's writable paths.

The web image needs no Linux capabilities: drop `ALL` and add nothing. Its health endpoint is `GET /healthz` on 8080.
To run it locally:

```bash
docker run --read-only --tmpfs /tmp --cap-drop ALL -p 8080:8080 $REGISTRY/web:$V
```
