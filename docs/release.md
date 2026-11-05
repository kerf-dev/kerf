# Releasing resource-optimizer to ECR

A release publishes five multi-arch container images (`linux/amd64` and `linux/arm64`, D-028) and the
Helm charts as OCI artifacts. Both go to one registry prefix, for example
`123456789012.dkr.ecr.eu-west-1.amazonaws.com/resource-optimizer`. The targets are in `make/release.mk` (WO-18c).

| Artifact | Reference |
|---|---|
| Images | `$REGISTRY/{hub,pricing,agent,webhook,web}:$VERSION` |
| Charts | `oci://$REGISTRY/charts/resource-optimizer` (the one chart to install, `docs/install/eks.md`) and the component charts `oci://$REGISTRY/charts/{hub,pricing,agent,webhook,web}`, version `$VERSION` |

Every image is an OCI index with one manifest per platform. Each index also holds an SBOM (SPDX) and
a SLSA provenance attestation (`mode=max`). Charts are packaged with `version` and `appVersion` set to `$VERSION`.

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
export REGISTRY=$ECR/resource-optimizer

for repo in hub pricing agent webhook web \
            charts/hub charts/pricing charts/agent charts/webhook charts/web charts/resource-optimizer; do
  aws ecr create-repository --region "$AWS_REGION" \
    --repository-name "resource-optimizer/$repo" \
    --image-tag-mutability IMMUTABLE \
    --image-scanning-configuration scanOnPush=true \
    --encryption-configuration encryptionType=KMS
done
```

ECR does not create repositories on push, so every image and chart needs its own repository:
`resource-optimizer/<image>` and `resource-optimizer/charts/<chart>`. The `charts/resource-optimizer`
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
    --repository-name "resource-optimizer/$repo" --lifecycle-policy-text file:///tmp/ro-lifecycle.json
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
| `RELEASE_CHARTS` | `resource-optimizer hub pricing agent webhook web` | charts under `deploy/charts/` |
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
123456789012.dkr.ecr.eu-west-1.amazonaws.com/resource-optimizer/hub 0.1.0 sha256:…
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
aws ecr describe-image-scan-findings --repository-name resource-optimizer/hub --image-id imageTag=$V
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

The agent, webhook and pricing charts already set `readOnlyRootFilesystem: true`. For the hub and web
charts, WO-18b sets it.

The web image needs no Linux capabilities: drop `ALL` and add nothing. Its health endpoint is `GET /healthz` on 8080.
To run it locally:

```bash
docker run --read-only --tmpfs /tmp --cap-drop ALL -p 8080:8080 $REGISTRY/web:$V
```
