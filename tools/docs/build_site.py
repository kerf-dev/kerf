#!/usr/bin/env python3
"""Build the documentation site with a version switch.

    python tools/docs/build_site.py --out _site/docs [--max-minors 3] [--dev-only]

Versions published:
  dev      the docs/ of this checkout (main), noindex
  X.Y      one per minor release: the docs/ of the newest tag vX.Y.* (a release candidate counts until the final
           release exists), the newest three minors
  latest   an alias of the newest stable minor; while there is no final release, the newest release candidate

Every version is built with the mkdocs.yml of this checkout (so one theme for all), with docs_dir pointing at that
tag's docs/. `mike` assembles them and writes versions.json (the version switch). It runs against a scratch git
repository, never the checkout. Tags that have no docs/README.md are skipped.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SEMVER = re.compile(r"^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$")


@dataclass(frozen=True)
class Tag:
    name: str
    major: int
    minor: int
    patch: int
    pre: tuple

    @property
    def stable(self) -> bool:
        return not self.pre

    @property
    def minor_id(self) -> str:
        return f"{self.major}.{self.minor}"

    def key(self) -> tuple:
        # SemVer precedence: a pre-release sorts before its release; numeric identifiers sort before text.
        pre = tuple((0, int(p), "") if p.isdigit() else (1, 0, p) for p in self.pre)
        return (self.major, self.minor, self.patch, self.stable, pre)


def parse_tag(name: str) -> Tag | None:
    m = SEMVER.match(name)
    if not m:
        return None
    pre = tuple(m.group(4).split(".")) if m.group(4) else ()
    return Tag(name, int(m.group(1)), int(m.group(2)), int(m.group(3)), pre)


def pick_versions(tags: list[Tag], max_minors: int) -> tuple[list[Tag], Tag | None]:
    """The newest tag of each minor (newest minors first), and the tag the `latest` alias points at."""
    newest: dict[str, Tag] = {}
    for t in tags:
        cur = newest.get(t.minor_id)
        if cur is None or t.key() > cur.key():
            newest[t.minor_id] = t
    chosen = sorted(newest.values(), key=lambda t: (t.major, t.minor), reverse=True)[:max_minors]
    stable = [t for t in chosen if t.stable]
    latest = stable[0] if stable else (chosen[0] if chosen else None)
    return chosen, latest


def run(cmd: list[str], **kw) -> str:
    kw.setdefault("check", True)
    res = subprocess.run(cmd, text=True, capture_output=kw.pop("capture", False), **kw)
    return res.stdout if res.stdout else ""


def git(*args: str, cwd: Path = ROOT) -> str:
    return subprocess.run(["git", *args], cwd=cwd, check=True, text=True, capture_output=True).stdout


def release_tags() -> list[Tag]:
    out = []
    for name in git("tag", "--list", "v*").split():
        t = parse_tag(name)
        if t and subprocess.run(["git", "cat-file", "-e", f"{name}:docs/README.md"], cwd=ROOT,
                                capture_output=True).returncode == 0:
            out.append(t)
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--out", type=Path, required=True, help="where the docs go (published at /docs/)")
    ap.add_argument("--max-minors", type=int, default=3)
    ap.add_argument("--dev-only", action="store_true", help="only build dev (no tags needed)")
    ap.add_argument("--mkdocs-config", type=Path, default=ROOT / "mkdocs.yml")
    args = ap.parse_args()

    chosen, latest = ([], None) if args.dev_only else pick_versions(release_tags(), args.max_minors)
    print(f"versions: dev, {', '.join(f'{t.minor_id} ({t.name})' for t in chosen) or 'no release yet'}; latest: "
          f"{latest.name if latest else 'none'}")

    out: Path = args.out.resolve()
    if out.exists():
        shutil.rmtree(out)
    out.mkdir(parents=True)

    with tempfile.TemporaryDirectory(prefix="kerf-docs-") as tmp_s:
        tmp = Path(tmp_s)
        repo = tmp / "mike"  # scratch repository that mike commits the versions to
        repo.mkdir()
        for cmd in (["init", "-q", "-b", "scratch"], ["config", "user.name", "docs build"],
                    ["config", "user.email", "docs@getkerf.dev"], ["config", "commit.gpgsign", "false"],
                    ["commit", "-q", "--allow-empty", "-m", "scratch"]):  # mike records the current commit in its own
            git(*cmd, cwd=repo)

        def mike(*a: str, env: dict[str, str] | None = None) -> None:
            cmd = [str(Path(sys.executable).with_name("mike")), *a, "-F", str(args.mkdocs_config.resolve()), "-b", "docs-build"]
            # mike runs the `mkdocs` of the same environment: put it first on PATH.
            path = str(Path(sys.executable).parent) + os.pathsep + os.environ.get("PATH", "")
            subprocess.run(cmd, cwd=repo, check=True, env={**os.environ, "PATH": path, **(env or {})})

        # Oldest first, so a later deploy never rewrites an earlier alias.
        for t in reversed(chosen):
            src = tmp / "src" / t.name
            git("worktree", "add", "--detach", "-q", str(src), t.name)
            try:
                aliases = ["latest"] if latest and t.name == latest.name else []
                mike("deploy", "--alias-type", "redirect", "--update-aliases", "--title", t.name.lstrip("v"),
                     t.minor_id, *aliases,
                     env={"KERF_DOCS_DIR": str(src / "docs"), "KERF_DOCS_REF": t.name})
            finally:
                git("worktree", "remove", "--force", str(src))
        mike("deploy", "--alias-type", "redirect", "--title", "dev (main)", "dev",
             env={"KERF_DOCS_REF": "main", "KERF_DOCS_STRICT": "true", "KERF_DOCS_NOINDEX": "true"})
        mike("set-default", "latest" if latest else "dev")
        archive = tmp / "docs.tar"
        run(["git", "archive", "--format=tar", "-o", str(archive), "docs-build"], cwd=repo)
        run(["tar", "-xf", str(archive), "-C", str(out)])

    # No Jekyll processing on GitHub Pages; the site's own .nojekyll lives at the root.
    versions = json.loads((out / "versions.json").read_text())
    print("published:", ", ".join(f"{v['version']}" + (f" ({','.join(v['aliases'])})" if v["aliases"] else "") for v in versions))
    return 0


if __name__ == "__main__":
    sys.exit(main())
