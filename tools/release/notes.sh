#!/usr/bin/env bash
# Release notes for a GitHub Release (WO-26). Used by .github/workflows/release.yaml and `make release-notes`.
#
#   tools/release/notes.sh --check VERSION     fail when a final VERSION (no -suffix) has no CHANGELOG.md section
#   tools/release/notes.sh VERSION [REGISTRY]  print the notes (markdown) on stdout
#
# The notes are: the CHANGELOG.md section `## [VERSION]` (a pre-release such as 0.2.0-rc.1 falls back to
# `## [0.2.0]`, then to `## [Unreleased]`), how to install, and the published images and charts with their
# digests (bin/release/images.txt and charts.txt, written by `make release`). GitHub appends the merged pull
# requests and the compare link (gh release create --generate-notes).
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
CHANGELOG=$ROOT/CHANGELOG.md
RELEASE_DIR=${RELEASE_DIR:-$ROOT/bin/release}

check=""
if [ "${1:-}" = --check ]; then check=1; shift; fi
VERSION=${1:?usage: notes.sh [--check] VERSION [REGISTRY]}
REPO=${GITHUB_REPOSITORY:-tomer1983/resource-optimizer}   # owner/name, set by GitHub Actions
OWNER=$(echo "${REPO%%/*}" | tr '[:upper:]' '[:lower:]')
REGISTRY=${2:-ghcr.io/$OWNER/resource-optimizer}
BASE=${VERSION%%-*}   # 0.2.0-rc.1 -> 0.2.0

# section NAME: the lines after `## [NAME]` up to the next `## [`, without leading/trailing blank lines.
section() {
  [ -f "$CHANGELOG" ] || return 0
  awk -v want="$1" '
    /^## \[/ { if (on) exit; on = (index($0, "## [" want "]") == 1); next }
    on { print }
  ' "$CHANGELOG" | sed -e '/./,$!d' | awk 'NF {for (; n > 0; n--) print ""; print; next} {n++}'
}

if [ -n "$check" ]; then
  if [ "$VERSION" = "$BASE" ] && [ -z "$(section "$VERSION")" ]; then
    echo "CHANGELOG.md has no '## [$VERSION]' section: move the Unreleased notes under it before tagging v$VERSION" >&2
    exit 1
  fi
  exit 0
fi

notes=$(section "$VERSION")
[ -n "$notes" ] || notes=$(section "$BASE")
[ -n "$notes" ] || notes=$(section Unreleased)

CHART=oci://$REGISTRY/charts/resource-optimizer

{
  if [ "$VERSION" != "$BASE" ]; then
    echo "> **Pre-release.** For testing; use a final version in production."
    echo
  fi
  if [ -n "$notes" ]; then
    echo "$notes"
    echo
  fi
  cat <<MD
## Install

One Helm chart installs everything (\`global.mode: all\`), the hub cluster (\`hub\`) or a workload cluster (\`spoke\`).
The images below are its defaults.

\`\`\`bash
helm install resource-optimizer $CHART \\
  --version $VERSION --namespace resource-optimizer --create-namespace \\
  -f values.yaml
\`\`\`

- Quick start and values: [deploy/charts/resource-optimizer](https://github.com/$REPO/tree/v$VERSION/deploy/charts/resource-optimizer)
- Production install on Amazon EKS: [docs/install/eks.md](https://github.com/$REPO/blob/v$VERSION/docs/install/eks.md)
- Upgrading: read the notes above first; CRDs are upgraded by the chart.

## Artifacts

Every image is multi-arch (\`linux/amd64\`, \`linux/arm64\`) with an SBOM and SLSA provenance, and is signed with
[cosign](https://docs.sigstore.dev/) (keyless, by this repository's release workflow).

| Artifact | Reference (version $VERSION) | Digest |
|---|---|---|
MD
  for kind in image chart; do
    f=$RELEASE_DIR/${kind}s.txt
    [ -f "$f" ] || continue
    while read -r ref _ digest; do
      echo "| $kind \`${ref##*/}\` | \`$ref\` | \`$digest\` |"
    done < "$f"
  done
  cat <<MD

Verify a signature:

\`\`\`bash
cosign verify $REGISTRY/hub:$VERSION \\
  --certificate-identity-regexp '^https://github.com/$REPO/.github/workflows/release.yaml@' \\
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
\`\`\`
MD
}
