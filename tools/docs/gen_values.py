"""Chart values reference.

The umbrella chart: deploy/charts/kerf/values.schema.json (types, enums, patterns, rules) merged with the commented
defaults in values.yaml. The component charts: the commented defaults of deploy/charts/<name>/values.yaml.
Output: docs/reference/values/*.md.
"""
from __future__ import annotations

import json
import re
from pathlib import Path

import yaml

from common import anchor_id, cell, header, scrub

COMPONENTS = {
    "hub": "API, recommender and readiness checks",
    "web": "web UI",
    "pricing": "pricing service",
    "agent": "per-cluster agent",
    "webhook": "mutating webhook",
}

_KEY = re.compile(r"^(\s*)([A-Za-z0-9_.\-/\"']+):(?:\s+(.*))?$")
# A commented-out setting such as "caBundle: {existingConfigMap: x}" is an example, not prose.
_EXAMPLE = re.compile(r"^[a-z][A-Za-z0-9_.-]*:\s*(\{.*\}|\[.*\]|true|false|\"\"|-?[0-9]+)?\s*(#.*)?$")
_RULER = re.compile(r"^[\s#\-=~*_]{4,}$")


def parse_comments(text: str) -> dict[str, str]:
    """Map the dotted path of every mapping key to the comment written directly above it (or after it, on the
    same line). A blank line ends a comment block, so section banners do not leak into the next key."""
    docs: dict[str, str] = {}
    stack: list[tuple[int, str]] = []
    block: list[str] = []
    for raw in text.splitlines():
        line = raw.rstrip()
        stripped = line.strip()
        if not stripped:
            block = []
            continue
        if stripped.startswith("#"):
            body = stripped[1:]
            if _RULER.match(stripped):
                block = []
            else:
                block.append(body[1:] if body.startswith(" ") else body)
            continue
        if stripped.startswith("- "):
            block = []
            continue
        m = _KEY.match(line)
        if not m:
            block = []
            continue
        indent, key, rest = len(m.group(1)), m.group(2).strip("\"'"), m.group(3) or ""
        while stack and stack[-1][0] >= indent:
            stack.pop()
        path = ".".join([k for _, k in stack] + [key])
        inline = ""
        if " #" in rest:
            inline = rest.split(" #", 1)[1].strip()
        desc_lines = list(block)
        if inline:
            desc_lines.append(inline)
        if desc_lines:
            docs[path] = "\n".join(desc_lines)
        stack.append((indent, key))
        block = []
    return docs


def fmt_default(v: object) -> str:
    if v is None:
        return "`null`"
    if isinstance(v, str):
        return '`""`' if v == "" else f"`{v}`"
    if isinstance(v, dict) and v:
        return "see the keys below"
    if isinstance(v, (dict, list)):
        if not v:
            return "`{}`" if isinstance(v, dict) else "`[]`"
        s = json.dumps(v, separators=(", ", ": "))
        return f"`{s}`" if len(s) <= 80 else "see values.yaml"
    return f"`{json.dumps(v)}`"


def type_name(v: object) -> str:
    return {bool: "boolean", int: "integer", float: "number", str: "string", list: "array", dict: "object"}.get(
        type(v), "object")


def comment_cell(text: str, where: str) -> str:
    """Comment lines to one table cell: wrapped prose is joined, indented example lines stay on their own line."""
    text = scrub(text, where)
    out: list[str] = []
    for ln in text.splitlines():
        if (re.match(r"^\s{2,}\S", ln) and not ln.lstrip().startswith(("- ", "* "))) or _EXAMPLE.match(ln):
            out.append("<br>`" + ln.strip().replace("|", "\\|") + "`<br>")
        elif ln.strip():
            out.append(ln.strip().replace("|", "\\|"))
        else:
            out.append("<br>")
    return " ".join(out).replace(" <br>", "<br>").replace("<br> ", "<br>").replace("<br><br>", "<br>").strip("<br>").strip()


def flatten(values: object, prefix: str = "") -> list[tuple[str, object]]:
    """Dotted paths of every mapping key, each with its default. Descends into non-empty mappings only."""
    out: list[tuple[str, object]] = []
    if isinstance(values, dict):
        for k, v in values.items():
            p = f"{prefix}{k}"
            out.append((p, v))
            if isinstance(v, dict) and v:
                out.extend(flatten(v, p + "."))
    return out


def schema_index(schema: dict) -> dict[str, dict]:
    """Dotted path -> schema node, for the umbrella schema (refs resolved one level)."""
    defs = schema.get("definitions", {})
    index: dict[str, dict] = {}

    def walk(node: dict, prefix: str) -> None:
        node = defs.get(node["$ref"].rsplit("/", 1)[-1], node) if "$ref" in node else node
        for name, sub in node.get("properties", {}).items():
            index[f"{prefix}{name}"] = sub
            walk(sub, f"{prefix}{name}.")

    walk(schema, "")
    return index


def schema_info(node: dict | None) -> str:
    if not node:
        return ""
    parts = []
    if "enum" in node:
        parts.append("one of " + ", ".join(f"`{v}`" for v in node["enum"]))
    if "pattern" in node and node["pattern"]:
        parts.append(f"pattern `{node['pattern']}`")
    return "; ".join(parts)


def table(rows: list[tuple[str, str, str, str]]) -> list[str]:
    lines = ["| Key | Type | Default | Description |", "|---|---|---|---|"]
    for key, typ, default, desc in rows:
        lines.append(f"| `{key}` | {typ} | {default} | {desc} |")
    return lines


def rule_lines(schema: dict, where: str) -> list[str]:
    rules = []
    for item in schema.get("allOf", []):
        d = scrub(item.get("description"), where)
        if d:
            rules.append(f"- {d}")
    return rules


def umbrella(root: Path) -> str:
    base = root / "deploy/charts/kerf"
    where = "deploy/charts/kerf"
    schema = json.loads((base / "values.schema.json").read_text())
    text = (base / "values.yaml").read_text()
    values = yaml.safe_load(text) or {}
    comments = parse_comments(text)
    sidx = schema_index(schema)
    rows = []
    for path, default in flatten(values):
        # Component sections are the component charts' values: only list their top-level keys here.
        top = path.split(".")[0]
        if top in COMPONENTS and path.count(".") > 1:
            continue
        node = sidx.get(path)
        desc_parts = []
        if node and node.get("description"):
            desc_parts.append(scrub(node["description"], where))
        elif path in comments:
            desc_parts.append(comment_cell(comments[path], f"{where}:{path}"))
        info = schema_info(node)
        if info:
            desc_parts.append(info)
        typ = node.get("type") if node and isinstance(node.get("type"), str) else type_name(default)
        rows.append((path, typ, fmt_default(default), " ".join(desc_parts)))
    # Schema-only keys (set nowhere in values.yaml, e.g. global.mode)
    listed = {r[0] for r in rows}
    for path, node in sidx.items():
        if path not in listed and "." in path and path.split(".")[0] in ("global",):
            info = schema_info(node)
            d = scrub(node.get("description"), where)
            rows.append((path, str(node.get("type", "string")), "none (required)" if path == "global.mode" else "`\"\"`",
                         cell(" ".join(x for x in (d, info) if x))))
    rows.sort(key=lambda r: [p for p in r[0].split(".")])
    lines = [

        header("deploy/charts/kerf/values.schema.json and values.yaml"),
        "# Chart values",
        "",
        "One chart, `kerf`, installs every component. `global.mode` chooses what a release installs: `hub`, `spoke` or "
        "`all`. Each component's own settings sit under its name (`hub`, `web`, `pricing`, `agent`, `webhook`) and are the "
        "values of the component chart: see the pages for [hub](hub.md), [web](web.md), [pricing](pricing.md), "
        "[agent](agent.md) and [webhook](webhook.md). The chart's `values.schema.json` checks the values on "
        "`helm install` and `helm template`.",
        "",
        "## Umbrella values",
        "",
        scrub(schema.get("description"), where),
        "",
    ]
    lines += table(rows)
    rules = rule_lines(schema, where)
    if rules:
        lines += ["", "## Rules the schema enforces", "", *rules]
    return "\n".join(lines).rstrip() + "\n"


def component(root: Path, name: str, blurb: str) -> str:
    base = root / f"deploy/charts/{name}"
    where = f"deploy/charts/{name}"
    text = (base / "values.yaml").read_text()
    values = yaml.safe_load(text) or {}
    comments = parse_comments(text)
    rows = []
    for path, default in flatten(values):
        desc = comment_cell(comments[path], f"{where}:{path}") if path in comments else ""
        rows.append((path, type_name(default), fmt_default(default), desc))
    lines = [

        header(f"deploy/charts/{name}/values.yaml"),
        f"# `{name}` values ({blurb})",
        "",
        f"The values of the `{name}` component chart. In the `kerf` chart they sit under `{name}:`; "
        "a key listed here as `image.tag` is then `" + name + ".image.tag`. "
        "Nested keys appear after their parent, with the parent's default shown in full or abbreviated.",
        "",
    ]
    lines += table(rows)
    return "\n".join(lines).rstrip() + "\n"


def generate(root: Path) -> dict[str, str]:
    out = {"docs/reference/values/README.md": umbrella(root)}
    for name, blurb in COMPONENTS.items():
        out[f"docs/reference/values/{name}.md"] = component(root, name, blurb)
    return out
