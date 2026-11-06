#!/usr/bin/env bash
# Fail when internal development documents are tracked in this repository. They live outside it; the
# public tree must never contain them (see .gitignore). Run from the repository root.
set -euo pipefail

# CLAUDE.md and .claude/ at any depth; the rest at the repository root. Case-insensitive.
pattern='(^|/)CLAUDE\.md$|(^|/)\.claude/|^DECISIONS\.md$|^roadmap\.md$|^docs/(work-orders|handover|plans)/|^docs/STATUS\.md$|^docs/open-source-checklist\.md$'

found=$(git ls-files | grep -Ei -- "$pattern" || true)
if [ -n "$found" ]; then
  echo "::error title=Internal development documents are committed::Remove these files from the tree (keep them outside the repository):"
  # Annotate each file, then list them.
  while IFS= read -r f; do echo "::error file=$f::internal development document"; done <<< "$found"
  echo "$found" >&2
  exit 1
fi
echo "No internal development documents are tracked."
