"""MkDocs hooks for the Kerf documentation site.

docs/ is written to work on GitHub, so its pages link to files outside docs/ (the chart README, the CRD Go types,
CONTRIBUTING.md). This module makes the same sources work as a site:

* links that leave docs/ (and links to a directory) become links to github.com/kerf-dev/kerf at the ref being built;
* navigation entries whose file does not exist in this version of docs/ are dropped (older releases have fewer pages);
* pages that no navigation entry lists are added under "More", so a new page is never lost;
* the site's fonts, favicon and logos are copied into the build (not duplicated in git).
"""
from __future__ import annotations

import logging
import os
import re
import shutil
from pathlib import Path, PurePosixPath

log = logging.getLogger("mkdocs.hooks.kerf")

REPO_URL = "https://github.com/kerf-dev/kerf"
_LINK = re.compile(r"(?<!\!)(\]\()([^)\s]+)((?:\s+\"[^\"]*\")?\))")
_FENCE = re.compile(r"^(\s*)(```|~~~)")
_SCHEME = re.compile(r"^[a-zA-Z][a-zA-Z0-9+.-]*:")


def git_ref() -> str:
    """The branch or tag the GitHub links point at: main, or the release being built (KERF_DOCS_REF)."""
    return os.environ.get("KERF_DOCS_REF", "main")


def rewrite_links(markdown: str, page_src_uri: str, docs_dir: Path, ref: str | None = None) -> str:
    """Point links that leave docs_dir, or name a directory, at GitHub. Code blocks are left alone."""
    ref = ref or git_ref()
    docs_dir = docs_dir.resolve()
    repo_root = docs_dir.parent
    page_dir = (docs_dir / PurePosixPath(page_src_uri).parent).resolve()

    def fix(m: re.Match[str]) -> str:
        target = m.group(2)
        if target.startswith(("#", "/", "<")) or _SCHEME.match(target):
            return m.group(0)
        path, sep, fragment = target.partition("#")
        path = path.split("?")[0]
        if not path:
            return m.group(0)
        resolved = (page_dir / path).resolve()
        try:
            rel = resolved.relative_to(repo_root)
        except ValueError:
            return m.group(0)
        inside_docs = docs_dir == resolved or docs_dir in resolved.parents
        if inside_docs and not resolved.is_dir():
            return m.group(0)
        if inside_docs and (resolved / "README.md").exists():
            return m.group(0)
        if not resolved.exists():
            return m.group(0)  # mkdocs reports it
        kind = "tree" if resolved.is_dir() else "blob"
        url = f"{REPO_URL}/{kind}/{ref}/{rel.as_posix()}"
        return f"{m.group(1)}{url}{sep}{fragment}{m.group(3)}"

    out, in_fence, fence = [], False, ""
    for line in markdown.split("\n"):
        f = _FENCE.match(line)
        if f and (not in_fence or f.group(2) == fence):
            in_fence, fence = not in_fence, f.group(2)
            out.append(line)
        elif in_fence:
            out.append(line)
        else:
            out.append(_LINK.sub(fix, line))
    return "\n".join(out)


def _nav_files(nav: list) -> set[str]:
    found: set[str] = set()
    for item in nav:
        if isinstance(item, str):
            found.add(item)
        elif isinstance(item, dict):
            for value in item.values():
                if isinstance(value, str):
                    found.add(value)
                elif isinstance(value, list):
                    found |= _nav_files(value)
    return found


def prune_nav(nav: list, exists) -> list:
    """Drop entries whose file is missing and sections left empty."""
    out = []
    for item in nav:
        if isinstance(item, str):
            if exists(item) or item.startswith(("http://", "https://")):
                out.append(item)
            else:
                log.info("navigation: %s is not in this version of the docs, skipped", item)
        elif isinstance(item, dict):
            for title, value in item.items():
                if isinstance(value, str):
                    if exists(value) or value.startswith(("http://", "https://")):
                        out.append(item)
                    else:
                        log.info("navigation: %s is not in this version of the docs, skipped", value)
                elif isinstance(value, list):
                    children = prune_nav(value, exists)
                    if children:
                        out.append({title: children})
    return out


def on_config(config):
    docs_dir = Path(config["docs_dir"])
    exists = lambda rel: (docs_dir / rel).is_file()  # noqa: E731
    nav = prune_nav(list(config["nav"] or []), exists)
    listed = _nav_files(nav)
    excluded = config.get("exclude_docs")
    more = []
    for f in sorted(docs_dir.rglob("*.md")):
        rel = f.relative_to(docs_dir).as_posix()
        if rel in listed or (excluded and excluded.match_file(rel)):
            continue
        more.append(rel)
    if more:
        log.info("navigation: pages not listed in mkdocs.yml, added under More: %s", ", ".join(more))
        nav.append({"More": more})
    config["nav"] = nav
    return config


def on_page_markdown(markdown, page, config, files):
    return rewrite_links(markdown, page.file.src_uri, Path(config["docs_dir"]))


def on_post_build(config):
    """Copy the site's fonts, favicon and logos into the build so the theme needs no copies in git."""
    root = Path(config["config_file_path"]).resolve().parent
    out = Path(config["site_dir"]) / "assets" / "kerf"
    out.mkdir(parents=True, exist_ok=True)
    sources = [root / "site/fonts/geist-latin-wght-normal.woff2", root / "site/fonts/geist-mono-latin-wght-normal.woff2",
               root / "site/favicon.svg", root / "docs/branding/kerf-mark-on-dark.svg",
               root / "docs/branding/kerf-mark-on-light.svg"]
    for src in sources:
        if not src.exists():
            raise FileNotFoundError(f"{src} is needed by the docs theme")
        target = out / ("fonts" if src.suffix == ".woff2" else "")
        target.mkdir(parents=True, exist_ok=True)
        shutil.copy(src, target / src.name)
