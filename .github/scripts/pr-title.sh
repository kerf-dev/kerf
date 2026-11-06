#!/usr/bin/env bash
# Checks that a pull request title follows Conventional Commits, so that the squash commit on main (the title plus
# " (#N)") becomes a line in the release notes (cliff.toml, tools/release/notes.sh).
#
#   .github/scripts/pr-title.sh "feat(hub): add a savings export"   exit 0 when valid, 1 (with the reason) when not
#   .github/scripts/pr-title.sh --self-test                         run the table of valid and invalid titles
#
# Format: type(scope)!: summary    (scope and ! are optional)
#   types   feat fix perf docs ci build refactor test chore revert
#   scopes  hub agent webhook pricing web chart api e2e site release deps security
#   summary starts with a lowercase letter, has no trailing period; the whole title is at most 72 characters.
#           Exception: a dependency bump (`chore(deps): bump ...`, `ci(deps): bump ...`, what Dependabot writes
#           for a grouped update) may be longer, since its title is generated.
set -euo pipefail

TYPES="feat fix perf docs ci build refactor test chore revert"
SCOPES="hub agent webhook pricing web chart api e2e site release deps security"
MAX_LENGTH=72

# validate TITLE: prints the reason to stderr and returns 1 when the title is not valid.
validate() {
  local title=$1 type_re scope_re
  type_re=${TYPES// /|}
  scope_re=${SCOPES// /|}
  # Regexes are kept in variables: bash parses parentheses in an unquoted [[ =~ ]] pattern as syntax.
  local re_title="^($type_re)(\\(([^()]*)\\))?(!)?: (.*)\$"
  local re_shape="^[a-z]+(\\([^()]*\\))?!?: "
  local re_word="^([a-z]+)[(!:]"

  if [[ $title == *$'\n'* ]]; then
    echo "the title must be one line" >&2; return 1
  fi
  if [[ ! $title =~ $re_title ]]; then
    # Say what is wrong with the part before the colon.
    if [[ ! $title =~ $re_shape ]]; then
      echo "the title must start with 'type: ' or 'type(scope): ' followed by a summary" >&2
    elif [[ $title =~ $re_word ]] && [[ " $TYPES " != *" ${BASH_REMATCH[1]} "* ]]; then
      echo "unknown type '${BASH_REMATCH[1]}': use one of $TYPES" >&2
    else
      echo "the title does not match 'type(scope)!: summary'" >&2
    fi
    return 1
  fi
  local scope=${BASH_REMATCH[3]:-} has_scope=${BASH_REMATCH[2]:+1} summary=${BASH_REMATCH[5]}
  if [ -n "$has_scope" ] && [[ ! $scope =~ ^($scope_re)$ ]]; then
    echo "unknown scope '$scope': use one of $SCOPES, or leave the scope out" >&2; return 1
  fi
  if [[ ! $summary =~ ^[a-z] ]]; then
    echo "the summary must start with a lowercase letter (got '${summary:0:20}')" >&2; return 1
  fi
  if [[ $summary == *. ]]; then
    echo "the summary must not end with a period" >&2; return 1
  fi
  if [[ $summary == *" " ]]; then
    echo "the title must not end with a space" >&2; return 1
  fi
  if [ "${#title}" -gt "$MAX_LENGTH" ] && ! { [ "$scope" = deps ] && [[ $summary == bump\ * ]]; }; then
    echo "the title is ${#title} characters; the limit is $MAX_LENGTH. Shorten the summary and put details in the description" >&2
    return 1
  fi
}

usage() {
  cat >&2 <<'MSG'

Pull request titles follow Conventional Commits, because the squash commit on main uses the title and
the release notes are grouped by its type:

    type(scope)!: summary          scope and ! are optional

    types    feat fix perf docs ci build refactor test chore revert
    scopes   hub agent webhook pricing web chart api e2e site release deps security
    summary  starts with a lowercase letter, no trailing period; the whole title is at most 72 characters

    examples
      feat(hub): add a savings export to the workloads page
      fix(agent): roll back when the resized pod never becomes ready
      docs: explain the maintenance windows
      feat(api)!: rename the policy field targetRef to workloadRef

See CONTRIBUTING.md ("Pull request titles").
MSG
}

self_test() {
  local fail=0 title
  local -a valid=(
    "feat: add a savings export"
    "fix(hub): handle an empty metrics response"
    "perf(hub): batch the usage queries"
    "docs(site): explain the maintenance windows"
    "ci(release): group release notes by change type"
    "build: pin the base images by digest"
    "refactor(agent): split the resize planner"
    "test(e2e): cover the rollback of a failed resize"
    "chore: tidy the Makefile help output"
    "revert: feat(hub): add a savings export"
    "feat(api)!: rename targetRef to workloadRef"
    "fix!: drop the deprecated /v0 endpoints"
    "fix(security): reject unsigned agent tokens"
    "chore(deps): bump github.com/jackc/pgx/v5 from 5.7.1 to 5.7.2"
    "chore(deps): bump the go-minor-and-patch group with 3 updates"
    "chore(deps): bump the go-minor-and-patch group across 1 directory with 6 updates"
    "chore(deps): bump the docker-minor-and-patch group across 5 directories with 2 updates"
    "ci(deps): bump the actions-minor-and-patch group with 2 updates"
    "chore(deps): bump the k8s group with 4 updates"
    "feat(web): a title of exactly seventy-two characters, as long as allowed"
  )
  local -a invalid=(
    "Fix the sign-in bug"
    "added a feature"
    "feat add a feature"
    "feat:no space after the colon"
    "feature: add a savings export"
    "Feat: add a savings export"
    "feat(): add a savings export"
    "feat(billing): add a savings export"
    "feat(hub) add a savings export"
    "fix(hub): Handle an empty metrics response"
    "fix(hub): handle an empty metrics response."
    "fix: "
    "fix:"
    "docs(site): $(printf 'x%.0s' {1..70})"
    "feat(web): a title of seventy-three characters, one more than allowed!!!!"
    "chore(hub): bump the go-minor-and-patch group across 1 directory with 6 updates"
    "Revert \"feat(hub): add a savings export\""
    "WIP: feat(hub): add a savings export"
  )
  for title in "${valid[@]}"; do
    if ! validate "$title" 2>/dev/null; then echo "FAIL (should be valid):   $title"; fail=1; fi
  done
  for title in "${invalid[@]}"; do
    if validate "$title" 2>/dev/null; then echo "FAIL (should be invalid): $title"; fail=1; fi
  done
  if [ "$fail" -ne 0 ]; then return 1; fi
  echo "self-test OK: ${#valid[@]} valid and ${#invalid[@]} invalid titles"
}

case "${1:-}" in
  --self-test) self_test ;;
  -h|--help|"") echo "usage: pr-title.sh [--self-test | TITLE]" >&2; exit 2 ;;
  *)
    title=$1
    if reason=$(validate "$title" 2>&1); then
      echo "ok: $title"
    else
      echo "::error title=Pull request title::$reason" >&2
      echo "Invalid pull request title: $title" >&2
      echo "  -> $reason" >&2
      usage
      exit 1
    fi ;;
esac
