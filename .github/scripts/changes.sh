#!/usr/bin/env bash
# Which parts of the repository does this change touch? Prints `<area>=true|false` lines (to $GITHUB_OUTPUT
# when set) for the areas the ci workflow gates on: go, openapi, web, charts, images, docs.
#
# The change is:
#   pull_request   the merge commit GitHub checks out, against its first parent (the base branch tip),
#                  i.e. exactly what the pull request adds (checkout with fetch-depth >= 2);
#   push           the commits since the previous tip of the branch ($BEFORE_SHA);
#   anything else  (workflow_call from a release tag, workflow_dispatch): everything.
# When the range cannot be determined (new branch, force push, shallow history) everything is "true":
# running too much is safe, running too little is not.
#
# Local use: CHANGES_BASE=<rev> CHANGES_HEAD=<rev> .github/scripts/changes.sh
set -euo pipefail

event=${EVENT_NAME:-local}
head=${CHANGES_HEAD:-HEAD}
base=${CHANGES_BASE:-}

if [ -z "$base" ]; then
  case "$event" in
    pull_request)
      # The merge commit has two parents; the first is the base branch.
      if [ "$(git rev-list --parents -n1 "$head" | wc -w)" -eq 3 ]; then base=$head^1; fi ;;
    push)
      before=${BEFORE_SHA:-}
      case "$before" in ''|0000000000000000000000000000000000000000) ;; *)
        if git cat-file -e "$before^{commit}" 2>/dev/null ||
           git fetch --no-tags --depth=1 --quiet origin "$before" 2>/dev/null; then base=$before; fi ;;
      esac ;;
  esac
fi

# Everything below is a git pathspec (space-separated; globbing is off so the patterns reach git intact);
# ":(glob)" lets ** cross directories. `common` triggers every area: the pipeline itself and the build plumbing.
set -f
common='.github/workflows/ci.yaml .github/scripts Makefile make .golangci.yml'
go="$common :(glob)**/*.go go.mod go.sum api cmd internal tools db deploy dev dashboards docs/contracts test testdata"
openapi="$common api/openapi"
web="$common web api/openapi"
charts="$common deploy api/crds dashboards internal/hub/install tools/release go.mod go.sum"
images="$common cmd internal web api db deploy go.mod go.sum tools/release"
docs="$common docs docs-site mkdocs.yml site tools/docs api deploy/charts"
# The Go tests read db/, deploy/, dev/, dashboards/ and docs/contracts/, and `make verify-generate` checks
# dashboards/; the charts job runs internal/hub/install's tests; the images copy api/ and db/migrations,
# and `make release-dry` packages the charts. The docs build reads docs/ and publishes it with site/; its
# reference pages are generated from api/ (CRDs, the OpenAPI contract) and the chart values and schema.

areas=(go openapi web charts images docs)
out=${GITHUB_OUTPUT:-/dev/stdout}
summary=${GITHUB_STEP_SUMMARY:-/dev/null}

if [ -z "$base" ]; then
  reason="no comparable base for a '$event' event: running everything"
  files=
else
  reason="$event: $(git rev-parse --short "$base")..$(git rev-parse --short "$head")"
  files=$(git diff --name-only --no-renames "$base" "$head")
fi

{
  echo "### Which areas need CI"
  echo "$reason, $(printf '%s' "$files" | grep -c . || true) file(s) changed"
  echo
  echo "| area | runs |"
  echo "|---|---|"
} >> "$summary"

for area in "${areas[@]}"; do
  # shellcheck disable=SC2086  # the pathspecs are deliberately split into words
  if [ -z "$base" ] || ! git diff --quiet --no-renames "$base" "$head" -- ${!area}; then run=true; else run=false; fi
  echo "$area=$run" >> "$out"
  echo "| $area | $run |" >> "$summary"
  echo "$area=$run" >&2
done
echo "$reason" >&2
if [ -n "$files" ]; then printf '%s\n' "$files" | head -40 | sed 's/^/  /' >&2; fi
