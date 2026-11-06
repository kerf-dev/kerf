#!/usr/bin/env bash
# Release notes for a GitHub Release. Used by .github/workflows/release.yaml and `make release-notes`.
#
#   tools/release/notes.sh --check VERSION       fail when a final VERSION (no -suffix) has no CHANGELOG.md section
#   tools/release/notes.sh VERSION [REGISTRY]    print the notes (markdown) on stdout
#   tools/release/notes.sh --changelog VERSION   print the changes of VERSION as Keep a Changelog sections
#                                                (Added, Changed, Fixed, Security), to curate into CHANGELOG.md
#   tools/release/notes.sh --previous VERSION    print the tag the changes are counted from (empty: none)
#
# The notes are, in this order:
#   - the pre-release banner for a -suffix version;
#   - "Highlights": the CHANGELOG.md section `## [VERSION]` (a pre-release such as 0.2.0-rc.1 falls back to
#     `## [0.2.0]`, then to `## [Unreleased]`), the hand-written summary;
#   - "What's changed": the Conventional Commits since the previous release, grouped by type (git-cliff, cliff.toml);
#   - how to install (the Helm chart, and the command-line tool's binaries), and the published images and charts with their digests (bin/release/images.txt and
#     charts.txt, written by `make release`);
#   - the link to the full changelog (the compare view).
#
# Commit range: a final release (0.2.0) compares against the previous final tag, so it lists everything since the
# last final release; a pre-release (0.2.0-rc.2) compares against the previous tag of any kind. Without an earlier
# tag in that class (the first final release) there is no commit list and no compare link.
#
# git-cliff: $GIT_CLIFF, else `git-cliff` on the PATH, else the official container image (pinned by digest).
# With GITHUB_TOKEN set (the release workflow does) each line names its author and links its pull request;
# without a token the lines end with the short commit hash.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
CHANGELOG=$ROOT/CHANGELOG.md
RELEASE_DIR=${RELEASE_DIR:-$ROOT/bin/release}
# git-cliff 2.14.2, the official image for local runs without an installed git-cliff (index digest, multi-arch).
CLIFF_IMAGE=${CLIFF_IMAGE:-orhunp/git-cliff@sha256:1e696a381a1b0366bb6ccbb6bd4e50e73473fda88eab3a757b95e0d0d66c9531}

mode=notes
case "${1:-}" in
  --check)     mode=check;     shift ;;
  --changelog) mode=changelog; shift ;;
  --previous)  mode=previous;  shift ;;
esac
VERSION=${1:?usage: notes.sh [--check | --changelog | --previous] VERSION [REGISTRY]}
VERSION=${VERSION#v}
REPO=${GITHUB_REPOSITORY:-kerf-dev/kerf}   # owner/name, set by GitHub Actions
OWNER=$(echo "${REPO%%/*}" | tr '[:upper:]' '[:lower:]')
REGISTRY=${2:-ghcr.io/$OWNER/kerf}
BASE=${VERSION%%-*}   # 0.2.0-rc.1 -> 0.2.0
TAG=v$VERSION

# section NAME: the lines after `## [NAME]` up to the next `## [`, without leading/trailing blank lines.
section() {
  [ -f "$CHANGELOG" ] || return 0
  awk -v want="$1" '
    /^## \[/ { if (on) exit; on = (index($0, "## [" want "]") == 1); next }
    on { print }
  ' "$CHANGELOG" | sed -e '/./,$!d' | awk 'NF {for (; n > 0; n--) print ""; print; next} {n++}'
}

# semver_cmp A B (versions without the leading v): prints -1, 0 or 1 by SemVer precedence (build metadata ignored).
semver_cmp() {
  local a=${1%%+*} b=${2%%+*} i x y
  local acore=${a%%-*} bcore=${b%%-*} apre="" bpre=""
  [ "$a" = "$acore" ] || apre=${a#*-}
  [ "$b" = "$bcore" ] || bpre=${b#*-}
  local -a ac bc ap bp
  IFS=. read -r -a ac <<< "$acore"
  IFS=. read -r -a bc <<< "$bcore"
  for i in 0 1 2; do
    if [ $((10#${ac[i]:-0})) -lt $((10#${bc[i]:-0})) ]; then echo -1; return; fi
    if [ $((10#${ac[i]:-0})) -gt $((10#${bc[i]:-0})) ]; then echo 1; return; fi
  done
  # Equal cores: a version without a pre-release part is the greater.
  if [ -z "$apre" ] && [ -z "$bpre" ]; then echo 0; return; fi
  if [ -z "$apre" ]; then echo 1; return; fi
  if [ -z "$bpre" ]; then echo -1; return; fi
  IFS=. read -r -a ap <<< "$apre"
  IFS=. read -r -a bp <<< "$bpre"
  local n=${#ap[@]}
  [ "${#bp[@]}" -le "$n" ] || n=${#bp[@]}
  for ((i = 0; i < n; i++)); do
    x=${ap[i]}; y=${bp[i]}
    if [[ $x =~ ^[0-9]+$ ]] && [[ $y =~ ^[0-9]+$ ]]; then
      if [ $((10#$x)) -lt $((10#$y)) ]; then echo -1; return; fi
      if [ $((10#$x)) -gt $((10#$y)) ]; then echo 1; return; fi
    elif [[ $x =~ ^[0-9]+$ ]]; then echo -1; return   # numeric identifiers sort before the others
    elif [[ $y =~ ^[0-9]+$ ]]; then echo 1; return
    elif [[ $x < $y ]]; then echo -1; return
    elif [[ $x > $y ]]; then echo 1; return
    fi
  done
  # The longer pre-release part is the greater: rc.1 < rc.1.1.
  if [ "${#ap[@]}" -lt "${#bp[@]}" ]; then echo -1; elif [ "${#ap[@]}" -gt "${#bp[@]}" ]; then echo 1; else echo 0; fi
}

# previous_tag: the tag the changes of $VERSION are counted from (see the header); prints nothing when there is none.
previous_tag() {
  local best="" t v
  while read -r t; do
    [[ $t =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || continue
    v=${t#v}
    [ "$v" != "$VERSION" ] || continue
    if [ "$VERSION" = "$BASE" ]; then case $v in *-*) continue ;; esac; fi   # a final release: final tags only
    [ "$(semver_cmp "$v" "$VERSION")" = -1 ] || continue
    if [ -z "$best" ] || [ "$(semver_cmp "$v" "${best#v}")" = 1 ]; then best=$t; fi
  done < <(git -C "$ROOT" tag --list 'v[0-9]*')
  printf '%s' "$best"
}

# cliff ARGS...: git-cliff from $GIT_CLIFF or the PATH, else the pinned container image (the repository, and for a
# worktree its common git directory, are mounted at their own paths so that git finds them).
cliff() {
  if [ -n "${GIT_CLIFF:-}" ]; then "$GIT_CLIFF" "$@"
  elif command -v git-cliff >/dev/null 2>&1; then git-cliff "$@"
  elif command -v docker >/dev/null 2>&1; then
    local common
    common=$(cd "$ROOT" && git rev-parse --path-format=absolute --git-common-dir)
    docker run --rm -e GITHUB_TOKEN -v "$ROOT:$ROOT:ro" -v "$common:$common:ro" -w "$ROOT" "$CLIFF_IMAGE" "$@"
  else
    echo "git-cliff is needed for the commit list: install it (https://git-cliff.org/docs/installation), set GIT_CLIFF, or install Docker" >&2
    return 1
  fi
}

# changes: the git-cliff groups (markdown, `### Group` headings) of the commits from PREV to this version;
# nothing without a PREV, or without a conventional commit in the range.
changes() {
  [ -n "$PREV" ] || return 0
  local -a args=(--config "$ROOT/cliff.toml")
  local err out
  if [ "$VERSION" = "$BASE" ]; then
    args+=(--tag-pattern '^v[0-9]+\.[0-9]+\.[0-9]+$')   # an -rc tag in the range is not a release of its own
  else
    args+=(--tag-pattern '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$')
  fi
  if git -C "$ROOT" rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
    args+=("$PREV..$TAG")
    # Author and pull request links need the GitHub API, which only knows tags that were pushed.
    [ -n "${GITHUB_TOKEN:-}" ] || args+=(--offline)
  else
    args+=(--tag "$TAG" "$PREV..HEAD" --offline)   # not tagged yet: the notes preview, `make changelog`
  fi
  err=$(mktemp)
  if ! out=$(cd "$ROOT" && cliff "${args[@]}" 2>"$err"); then cat "$err" >&2; rm -f "$err"; return 1; fi
  rm -f "$err"
  # Normalise the blank lines: one before and after every heading, none inside a list.
  printf '%s\n' "$out" | awk '
    /^### / { if (printed) print ""; print; print ""; printed = 1; next }
    NF      { print; printed = 1 }
  '
}

# keep_a_changelog: the changes() groups as Keep a Changelog sections.
keep_a_changelog() {
  awk '
    /^### / {
      h = substr($0, 5)
      cur = (h == "Features") ? "Added" : (h == "Bug fixes") ? "Fixed" : (h == "Security") ? "Security" : (h == "New contributors") ? "" : "Changed"
      pre = (h == "Breaking changes") ? "**Breaking:** " : ""
      next
    }
    /^- / && cur != "" { body[cur] = body[cur] "- " pre substr($0, 3) "\n" }
    END {
      n = split("Added Changed Fixed Security", order, " ")
      for (i = 1; i <= n; i++) if (body[order[i]] != "") {
        if (shown) print ""
        print "### " order[i]; print ""; printf "%s", body[order[i]]; shown = 1
      }
    }
  '
}

PREV=$(previous_tag)

case $mode in
  previous)
    echo "$PREV"
    exit 0 ;;
  check)
    if [ "$VERSION" = "$BASE" ] && [ -z "$(section "$VERSION")" ]; then
      echo "CHANGELOG.md has no '## [$VERSION]' section: move the Unreleased notes under it before tagging v$VERSION" >&2
      exit 1
    fi
    exit 0 ;;
  changelog)
    out=$(changes) || exit 1
    if [ -n "$out" ]; then printf '%s\n' "$out" | keep_a_changelog
    else echo "no conventional commits since ${PREV:-the start}: nothing to list" >&2; fi
    exit 0 ;;
esac

notes=$(section "$VERSION")
[ -n "$notes" ] || notes=$(section "$BASE")
[ -n "$notes" ] || notes=$(section Unreleased)
changed=$(changes)

CHART=oci://$REGISTRY/charts/kerf

{
  if [ "$VERSION" != "$BASE" ]; then
    echo "> **Pre-release.** For testing; use a final version in production."
    echo
  fi
  if [ -n "$notes" ]; then
    echo "## Highlights"
    echo
    echo "$notes"
    echo
  fi
  if [ -n "$changed" ]; then
    echo "## What's changed"
    echo
    echo "$changed"
    echo
  fi
  cat <<MD
## Install

One Helm chart installs everything (\`global.mode: all\`), the hub cluster (\`hub\`) or a workload cluster (\`spoke\`).
The images below are its defaults.

\`\`\`bash
helm install kerf $CHART \\
  --version $VERSION --namespace kerf --create-namespace \\
  -f values.yaml
\`\`\`

- Quick start and values: [deploy/charts/kerf](https://github.com/$REPO/tree/v$VERSION/deploy/charts/kerf)
- Production install on Amazon EKS: [docs/install/eks.md](https://github.com/$REPO/blob/v$VERSION/docs/install/eks.md)
- Upgrading: read the notes above first; CRDs are upgraded by the chart.

**Command-line tool.** \`kerf report\` shows what a cluster over-requests from its existing Prometheus, without
installing anything in the cluster ([docs/cli.md](https://github.com/$REPO/blob/v$VERSION/docs/cli.md)). Binaries for
Linux and macOS (amd64, arm64), signed with cosign, are attached to this release:

\`\`\`bash
os=\$(uname -s | tr A-Z a-z); arch=\$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/)
bin=kerf_${VERSION}_\${os}_\${arch}
curl -fsSLO https://github.com/$REPO/releases/download/$TAG/\$bin
curl -fsSLO https://github.com/$REPO/releases/download/$TAG/\$bin.sigstore.json
cosign verify-blob --bundle \$bin.sigstore.json \\
  --certificate-identity-regexp '^https://github.com/$REPO/.github/workflows/release.yaml@' \\
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \$bin
install -m 0755 \$bin /usr/local/bin/kerf
kerf report --prometheus-url https://prometheus.example.com --cluster-label cluster=prod-eu-1
\`\`\`

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

Verify a signature (needs cosign **v3** or newer: v2 reports "no signatures found" for these signatures):

\`\`\`bash
cosign verify $REGISTRY/hub:$VERSION \\
  --certificate-identity-regexp '^https://github.com/$REPO/.github/workflows/release.yaml@' \\
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
\`\`\`
MD
  if [ -n "$PREV" ]; then
    echo
    echo "**Full changelog:** https://github.com/$REPO/compare/$PREV...$TAG"
  fi
}
