"""Helpers shared by the reference generators: Markdown escaping and removal of internal labels."""
from __future__ import annotations

import re
import sys

REPO = "https://github.com/kerf-dev/kerf"

# Contract sources (the OpenAPI file, the CRD Go comments, chart values) may still carry internal planning labels.
# They must not reach the public site, so every generated string goes through scrub().
_LABEL = r"(?:WO|D)-\d+[a-z]?"
_ONLY_LABELS = re.compile(rf"\s*\({_LABEL}(?:[,;/ ]+(?:and )?{_LABEL})*\)")
_LEADING_LABEL = re.compile(rf"\(\s*{_LABEL}(?:[,;/ ]+{_LABEL})*,\s*")
_LABEL_LIST_AFTER = re.compile(rf",\s*{_LABEL}\b")
_BARE_LABEL = re.compile(rf"\s*\b{_LABEL}\b")

warnings: list[str] = []


def scrub(text: str | None, where: str = "") -> str:
    """Remove internal planning labels (a letter prefix, a dash and a number, in parentheses or bare) from text."""
    if not text:
        return ""
    out = _ONLY_LABELS.sub("", text)
    out = _LEADING_LABEL.sub("(", out)
    out = _LABEL_LIST_AFTER.sub("", out)
    if re.search(_LABEL, out):
        warnings.append(f"{where or 'text'}: internal label left in source, removed: {re.findall(_LABEL, out)}")
        out = _BARE_LABEL.sub("", out)
    return out


def report_warnings() -> None:
    for w in dict.fromkeys(warnings):
        print(f"warning: {w}", file=sys.stderr)


def cell(text: str | None) -> str:
    """Make text safe for one Markdown table cell."""
    if not text:
        return ""
    text = text.strip().replace("|", "\\|")
    # Paragraph breaks and list items become line breaks; plain wrapped lines are joined.
    lines = [ln.strip() for ln in text.splitlines()]
    out: list[str] = []
    for ln in lines:
        if not ln:
            if out and out[-1] != "<br>":
                out.append("<br>")
        elif ln.startswith(("- ", "* ")) and out:
            out.append("<br>" + ln)
        else:
            out.append(ln)
    return " ".join(out).replace(" <br> ", "<br>").replace("<br> ", "<br>").strip()


def code(text: object) -> str:
    s = str(text)
    return f"`{s}`" if "`" not in s else s


def anchor_id(text: str) -> str:
    return re.sub(r"[^a-z0-9._-]+", "-", text.lower()).strip("-")


def header(source: str, command: str = "make docs-generate") -> str:
    return (
        f"<!-- Generated from {source} by tools/docs. Do not edit: change the source and run `{command}`. -->\n"
    )
