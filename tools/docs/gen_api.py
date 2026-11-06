"""Hub API reference: api/openapi/hub.yaml -> docs/reference/api/*.md (one page per tag, plus the schemas)."""
from __future__ import annotations

from pathlib import Path

import yaml

from common import anchor_id, cell, header, scrub

METHODS = ("get", "put", "post", "delete", "patch")

TAG_TITLES = {
    "agent": "Agent",
    "ui": "Web UI",
    "audit": "Audit",
    "admin": "Admin",
    "auth": "Authentication",
    "internal": "Internal",
}
# Pages in nav order. An operation lands on the page of its first tag.
TAG_ORDER = ["agent", "ui", "audit", "admin", "auth", "internal"]


class Spec:
    def __init__(self, doc: dict):
        self.doc = doc
        self.schemas = doc["components"]["schemas"]

    def resolve(self, node: dict) -> dict:
        while "$ref" in node:
            node = self.lookup(node["$ref"])
        return node

    def lookup(self, ref: str) -> dict:
        cur: object = self.doc
        for part in ref.lstrip("#/").split("/"):
            cur = cur[part]  # type: ignore[index]
        return cur  # type: ignore[return-value]

    def type_of(self, node: dict, link: str) -> str:
        if "$ref" in node:
            name = node["$ref"].rsplit("/", 1)[-1]
            target = self.lookup(node["$ref"])
            # Named scalars and enums read better inline; objects link to the schema page.
            if target.get("type") == "object" or "allOf" in target:
                return f"[{name}]({link}#{anchor_id(name)})"
            return self.type_of(target, link) if "enum" not in target else f"{name} (enum)"
        if "allOf" in node and len(node["allOf"]) == 1:
            return self.type_of(node["allOf"][0], link)
        t = node.get("type", "object")
        if isinstance(t, list):
            t = " or ".join(x for x in t)
        if t == "array":
            return f"array of {self.type_of(node.get('items', {}), link)}"
        if t == "object" and isinstance(node.get("additionalProperties"), dict):
            return f"map of {self.type_of(node['additionalProperties'], link)}"
        if node.get("format"):
            return f"{t} ({node['format']})"
        return str(t)

    def notes(self, node: dict) -> str:
        parts = []
        target = self.resolve(node)
        for src in (node, target):
            if "enum" in src:
                parts.append("one of " + ", ".join(f"`{v}`" for v in src["enum"]))
                break
        if "pattern" in target:
            parts.append(f"pattern `{target['pattern']}`")
        for key, label in (("minimum", "min"), ("maximum", "max"), ("minLength", "min length"), ("maxLength", "max length"),
                           ("minItems", "min items"), ("maxItems", "max items")):
            if key in target:
                parts.append(f"{label} {target[key]}")
        if "default" in target:
            parts.append(f"default `{target['default']}`")
        if target.get("writeOnly"):
            parts.append("write-only")
        if "examples" in target:
            parts.append("example " + ", ".join(f"`{e}`" for e in target["examples"][:2]))
        return "; ".join(parts)


def prop_rows(spec: Spec, schema: dict, link: str, where: str, prefix: str = "", depth: int = 0,
              seen: tuple = ()) -> list[tuple[str, str, str, str]]:
    """Rows of a schema's properties. Inline objects are expanded (up to three levels); named object schemas link."""
    node = spec.resolve(schema)
    if "allOf" in node:
        merged: dict = {"properties": {}, "required": []}
        for part in node["allOf"]:
            p = spec.resolve(part)
            merged["properties"].update(p.get("properties", {}))
            merged["required"] += p.get("required", [])
        merged["properties"].update(node.get("properties", {}))
        merged["required"] += node.get("required", [])
        node = merged
    required = set(node.get("required", []))
    rows = []
    for name, sub in node.get("properties", {}).items():
        resolved = spec.resolve(sub)
        typ = spec.type_of(sub, link)
        desc = scrub(sub.get("description") or resolved.get("description"), where)
        notes = spec.notes(sub)
        req = ", required" if name in required else ""
        rows.append((f"{prefix}{name}", typ + req, cell(notes), cell(desc)))
        inline_object = "$ref" not in sub and resolved.get("type") == "object" and resolved.get("properties")
        if inline_object and depth < 2:
            rows += prop_rows(spec, sub, link, where, f"{prefix}{name}.", depth + 1, seen)
        item = sub.get("items") if resolved.get("type") == "array" else None
        if item and "$ref" not in item and item.get("type") == "object" and depth < 2:
            rows += prop_rows(spec, item, link, where, f"{prefix}{name}[].", depth + 1, seen)
    return rows


def render_table(rows: list[tuple[str, str, str, str]]) -> list[str]:
    out = ["| Field | Type | Constraints | Description |", "|---|---|---|---|"]
    for name, typ, notes, desc in rows:
        out.append(f"| `{name}` | {cell(typ)} | {notes} | {desc} |")
    return out + [""]


def roles(spec: Spec, op: dict) -> str:
    """Who may call the operation: token roles and, for the session cookie, the lowest user role."""
    sec = op.get("security", spec.doc.get("security", []))
    parts = []
    for req in sec:
        for scheme, scopes in req.items():
            names = ", ".join(f"`{x}`" for x in scopes) if scopes else "any"
            if scheme == "bearerAuth":
                parts.append(f"bearer token with role {names}")
            elif scheme == "sessionAuth":
                parts.append(f"session of a user with role {names} or higher" if scopes else "a user session")
            else:
                parts.append(f"{scheme}: {names}")
    return " or ".join(parts) if parts else "no authentication"


def summary(op: dict, where: str) -> str:
    return scrub(op.get("summary", op.get("operationId", "")), where)


def render_operation(spec: Spec, path: str, method: str, op: dict, common: list, link: str) -> list[str]:
    where = f"api/openapi/hub.yaml {method.upper()} {path}"
    op_id = op.get("operationId", anchor_id(f"{method}-{path}"))
    lines = [f"### {summary(op, where)} {{ #{op_id} }}", "", f"`{method.upper()} {path}`", ""]
    if op.get("deprecated"):
        lines += ["!!! warning \"Deprecated\"", "", ""]
    lines += [f"Accepts: {roles(spec, op)}.", ""]
    desc = scrub(op.get("description"), where)
    if desc:
        lines += [desc.strip(), ""]
    params = [spec.resolve(p) for p in (common + op.get("parameters", []))]
    if params:
        lines += ["**Parameters**", "", "| Name | In | Type | Description |", "|---|---|---|---|"]
        for p in params:
            sch = p.get("schema", {})
            notes = spec.notes(sch)
            d = scrub(p.get("description"), where)
            req = ", required" if p.get("required") else ""
            lines.append(f"| `{p['name']}` | {p['in']} | {cell(spec.type_of(sch, link) + req)} | "
                         f"{cell(' '.join(x for x in (d, notes) if x))} |")
        lines.append("")
    body = op.get("requestBody")
    if body:
        body = spec.resolve(body)
        for ctype, media in body.get("content", {}).items():
            sch = media.get("schema", {})
            lines += [f"**Request body** (`{ctype}`{', required' if body.get('required') else ''}): {spec.type_of(sch, link)}", ""]
            if "$ref" not in sch:
                rows = prop_rows(spec, sch, link, where)
                if rows:
                    lines += render_table(rows)
    lines += ["**Responses**", "", "| Status | Description | Body |", "|---|---|---|"]
    for status, resp in op.get("responses", {}).items():
        resp = spec.resolve(resp)
        body_type = ""
        for ctype, media in resp.get("content", {}).items():
            body_type = spec.type_of(media.get("schema", {}), link) if media.get("schema") else ctype
            break
        lines.append(f"| `{status}` | {cell(scrub(resp.get('description'), where))} | {cell(body_type)} |")
    return lines + [""]


def generate(root: Path) -> dict[str, str]:
    doc = yaml.safe_load((root / "api/openapi/hub.yaml").read_text())
    spec = Spec(doc)
    info = doc["info"]
    where = "api/openapi/hub.yaml"
    tag_desc = {t["name"]: scrub(t.get("description"), where) for t in doc.get("tags", [])}
    by_tag: dict[str, list[tuple[str, str, dict, list]]] = {t: [] for t in TAG_ORDER}
    for path, item in doc["paths"].items():
        common = item.get("parameters", [])
        for method in METHODS:
            if method in item:
                op = item[method]
                tag = (op.get("tags") or ["internal"])[0]
                by_tag.setdefault(tag, []).append((path, method, op, common))
    where_ = where
    pages: dict[str, str] = {}
    schemas_link = "schemas.md"
    overview = [

        header("api/openapi/hub.yaml"),
        "# Hub API",
        "",
        scrub(info.get("description"), where).strip().replace("\n    ", "\n"),
        "",
        "## Endpoints by area",
        "",
        "| Area | Endpoints | What it covers |",
        "|---|---|---|",
    ]
    for tag in TAG_ORDER:
        ops = by_tag.get(tag, [])
        if ops:
            overview.append(f"| [{TAG_TITLES[tag]}]({tag}.md) | {len(ops)} | {cell(tag_desc.get(tag, ''))} |")
    overview += ["", f"The request and response objects are described on the [schemas](schemas.md) page. "
                 f"The machine-readable contract is [api/openapi/hub.yaml]({'https://github.com/kerf-dev/kerf/blob/main/api/openapi/hub.yaml'}) "
                 f"(OpenAPI {doc['openapi']})."]
    pages["docs/reference/api/README.md"] = "\n".join(overview).rstrip() + "\n"
    for tag in TAG_ORDER:
        ops = by_tag.get(tag, [])
        if not ops:
            continue
        lines = [header("api/openapi/hub.yaml"),
                 f"# {TAG_TITLES[tag]} endpoints", "", tag_desc.get(tag, ""), "",
                 "| Endpoint | Summary |", "|---|---|"]
        for path, method, op, _ in ops:
            lines.append(f"| [`{method.upper()} {path}`](#{op.get('operationId', anchor_id(method + '-' + path))}) | "
                         f"{cell(summary(op, f'{where_} {method.upper()} {path}'))} |")
        lines.append("")
        for path, method, op, common in ops:
            lines += render_operation(spec, path, method, op, common, schemas_link)
        pages[f"docs/reference/api/{tag}.md"] = "\n".join(lines).rstrip() + "\n"
    lines = [header("api/openapi/hub.yaml"), "# Schemas", "",
             "Objects and enumerations used by the [hub API](README.md).", ""]
    for name, schema in spec.schemas.items():
        sw = f"{where} schema {name}"
        node = spec.resolve(schema)
        lines += [f"## {name} {{ #{anchor_id(name)} }}", ""]
        d = scrub(node.get("description"), sw)
        if d:
            lines += [d.strip(), ""]
        if node.get("properties") or "allOf" in node:
            lines += render_table(prop_rows(spec, schema, "schemas.md", sw))
        else:
            notes = spec.notes(schema)
            lines += [f"Type: {spec.type_of(schema, 'schemas.md')}" + (f". {notes}." if notes else "."), ""]
    pages["docs/reference/api/schemas.md"] = "\n".join(lines).rstrip() + "\n"
    return pages
