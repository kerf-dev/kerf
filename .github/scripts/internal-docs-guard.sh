#!/usr/bin/env bash
# Fail when internal development documents are tracked in this repository, or when tracked files carry
# internal tracking labels (WO-12 and the like). They live outside it; the public tree must never contain
# them (see .gitignore). Run from the repository root.
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

# Internal tracking labels (WO-<number>, with an optional letter suffix) in any tracked text file, except
# this directory, which has to spell the pattern out. Describe the change in words instead of citing a label.
labels=$(git grep -InE '(^|[^A-Za-z0-9_])WO-[0-9]+' -- . ':(exclude).github/scripts/' || true)
if [ -n "$labels" ]; then
  echo "::error title=Internal tracking labels in tracked files::Replace each WO-<number> with a plain description:"
  while IFS= read -r l; do
    f=${l%%:*}; rest=${l#*:}; n=${rest%%:*}
    echo "::error file=$f,line=$n::internal tracking label"
  done <<< "$labels"
  echo "$labels" >&2
  exit 1
fi
echo "No internal development documents or tracking labels are tracked."
