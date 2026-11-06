#!/usr/bin/env bash
# Which CodeQL languages does this change touch? Prints `<language>=true|false` lines (to $GITHUB_OUTPUT when
# set) for the languages the codeql workflow scans: go, javascript-typescript, python, actions.
#
# The change is:
#   pull_request   the merge commit GitHub checks out, against its first parent (the base branch tip),
#                  i.e. exactly what the pull request adds (checkout with fetch-depth >= 2);
#   anything else  (push to main, the weekly schedule, workflow_dispatch): everything.
# When the range cannot be determined (shallow history, unexpected merge shape) everything is "true":
# scanning too much is safe, scanning too little is not. A change to the workflow or to this script scans
# everything too.
#
# Local use: EVENT_NAME=pull_request CHANGES_BASE=<rev> CHANGES_HEAD=<rev> .github/scripts/codeql-languages.sh
set -euo pipefail

event=${EVENT_NAME:-local}
head=${CHANGES_HEAD:-HEAD}
base=${CHANGES_BASE:-}

if [ -z "$base" ] && [ "$event" = pull_request ]; then
  # The merge commit has two parents; the first is the base branch.
  if [ "$(git rev-list --parents -n1 "$head" | wc -w)" -eq 3 ]; then base=$head^1; fi
fi
# Only pull requests are narrowed down; a local run without a base also scans everything.
if [ "$event" != pull_request ] && [ "$event" != local ]; then base=; fi

# Everything below is a git pathspec (space-separated; globbing is off so the patterns reach git intact);
# ":(glob)" lets ** cross directories. `common` triggers every language.
set -f
common='.github/workflows/codeql.yaml .github/scripts/codeql-languages.sh'
pathspecs() {
  case "$1" in
    go) echo "$common :(glob)**/*.go go.mod go.sum" ;;
    javascript-typescript) echo "$common :(glob)web/** :(exclude,glob)web/**/*.md :(glob)**/*.js :(glob)**/*.mjs :(glob)**/*.ts :(glob)**/*.tsx" ;;
    python) echo "$common :(glob)**/*.py :(glob)tools/docs/requirements*" ;;
    actions) echo "$common .github/workflows .github/actions" ;;
  esac
}

languages=(go javascript-typescript python actions)
out=${GITHUB_OUTPUT:-/dev/stdout}
summary=${GITHUB_STEP_SUMMARY:-/dev/null}

if [ -z "$base" ]; then
  reason="no comparable base for a '$event' event: scanning everything"
  files=
else
  reason="$event: $(git rev-parse --short "$base")..$(git rev-parse --short "$head")"
  files=$(git diff --name-only --no-renames "$base" "$head")
fi

{
  echo "### Which languages CodeQL scans"
  echo "$reason, $(printf '%s' "$files" | grep -c . || true) file(s) changed"
  echo
  echo "| language | scanned |"
  echo "|---|---|"
} >> "$summary"

for language in "${languages[@]}"; do
  # shellcheck disable=SC2046  # the pathspecs are deliberately split into words
  if [ -z "$base" ] || ! git diff --quiet --no-renames "$base" "$head" -- $(pathspecs "$language"); then run=true; else run=false; fi
  echo "$language=$run" >> "$out"
  echo "| $language | $run |" >> "$summary"
  echo "$language=$run" >&2
done
echo "$reason" >&2
if [ -n "$files" ]; then printf '%s\n' "$files" | head -40 | sed 's/^/  /' >&2; fi
