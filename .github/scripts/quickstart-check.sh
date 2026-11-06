#!/usr/bin/env bash
# Checks the answer of GET /v1/workloads for the quickstart's demo namespace (hack/quickstart.sh, docs/quickstart.md):
# every demo workload has a recommendation and most of them are priced. Usage: quickstart-check.sh workloads.json
set -euo pipefail
file=${1:?usage: quickstart-check.sh workloads.json}
jq -e '
  (.items | length) as $total
  | [.items[] | select(.recommendationID != null)] as $rec
  | [$rec[] | select((.monthlySavingsUSD.total // 0) > 0)] as $priced
  | ([$priced[] | .monthlySavingsUSD.total] | add // 0) as $sum
  | {total: $total, withRecommendation: ($rec | length), withPositiveSavings: ($priced | length), monthlySavingsUSD: $sum}
  | . as $r
  | if $r.total >= 5 and $r.withRecommendation == $r.total and $r.withPositiveSavings >= 3 and $r.monthlySavingsUSD > 0
    then (. | tostring | "ok: " + .) | halt_error(0)
    else error("quickstart check failed: " + ($r | tostring)) end
' "$file" 2>&1 | sed 's/^jq: error (at [^)]*): //' || { echo "quickstart-check: failed" >&2; exit 1; }
