#!/usr/bin/env python3
"""Generate (or check) the reference pages under docs/reference/.

    python tools/docs/generate.py           write the pages
    python tools/docs/generate.py --check   fail when a committed page differs from what the sources produce

The sources are api/crds, api/openapi/hub.yaml, deploy/charts/*/values.yaml and values.schema.json.
Files in docs/reference/ that start with the generated marker but are no longer produced are removed (or reported by --check).
"""
from __future__ import annotations

import argparse
import difflib
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import common  # noqa: E402
import gen_api  # noqa: E402
import gen_crds  # noqa: E402
import gen_values  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
MARKER = "<!-- Generated from "


def build(root: Path) -> dict[str, str]:
    pages: dict[str, str] = {}
    for mod in (gen_crds, gen_values, gen_api):
        pages.update(mod.generate(root))
    return pages


def stale_files(root: Path, pages: dict[str, str]) -> list[str]:
    out = []
    for f in sorted((root / "docs/reference").rglob("*.md")):
        rel = f.relative_to(root).as_posix()
        if rel not in pages and MARKER in f.read_text():
            out.append(rel)
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--check", action="store_true", help="do not write; exit 1 when pages are stale")
    ap.add_argument("--root", type=Path, default=ROOT)
    args = ap.parse_args()
    root: Path = args.root
    pages = build(root)
    common.report_warnings()
    problems = []
    for rel, text in pages.items():
        path = root / rel
        current = path.read_text() if path.exists() else None
        if current == text:
            continue
        if args.check:
            diff = "".join(difflib.unified_diff((current or "").splitlines(True), text.splitlines(True),
                                                f"a/{rel}", f"b/{rel}", n=1))
            problems.append(f"{rel}: {'missing' if current is None else 'out of date'}\n{diff[:1500]}")
        else:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
            print(f"wrote {rel}")
    for rel in stale_files(root, pages):
        if args.check:
            problems.append(f"{rel}: no longer generated, delete it")
        else:
            (root / rel).unlink()
            print(f"removed {rel}")
    if problems:
        print("\n".join(problems), file=sys.stderr)
        print("\nThe reference pages are stale: run `make docs-generate` and commit the result.", file=sys.stderr)
        return 1
    if args.check:
        print(f"{len(pages)} reference pages are up to date.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
