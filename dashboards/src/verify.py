#!/usr/bin/env python3
"""Verify the generated dashboards.

Static checks (no network):
  * every dashboard file is valid JSON and matches what build.py generates;
  * UIDs are unique, every panel has a title and a description and uses the ${datasource} variable;
  * every kerf_* metric is listed in docs/contracts/metrics.md (or uses an allowed operational prefix) and
    every label matched on a contract kerf_* metric is one of its contract labels.

Live checks (--live): resolve the template variables like Grafana does, then run every panel query and
annotation through Grafana's /api/ds/query (the same path a panel uses) for each cluster value. A query
error fails the run; queries without data are reported (and fail with --strict).

Environment: GRAFANA_URL (default http://localhost:13000), GRAFANA_USER/GRAFANA_PASSWORD (admin/admin),
DATASOURCE_UID (victoriametrics). Standard library only.
"""
import argparse
import base64
import json
import os
import pathlib
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent
DASH_DIR = HERE.parent
ROOT = DASH_DIR.parent
CONTRACT = ROOT / "docs" / "contracts" / "metrics.md"
sys.dont_write_bytecode = True
sys.path.insert(0, str(HERE))
import build  # noqa: E402

OPS_PREFIXES = ("kerf_hub_", "kerf_recommender_", "kerf_pricing_", "kerf_agent_", "kerf_webhook_", "kerf_health_")
SCRAPE_LABELS = {"cluster", "__name__"}
WL_LABELS = {"cluster", "namespace", "workload_kind", "workload"}
PRICING_LABELS = {"cluster", "node", "instance_type", "capacity_type", "region", "zone", "provider"}
CONTRACT_LABELS = {
    "kerf_node_hourly_cost_usd": PRICING_LABELS,
    "kerf_cpu_core_hourly_cost_usd": PRICING_LABELS,
    "kerf_memory_gib_hourly_cost_usd": PRICING_LABELS,
    "kerf_recommendation_cpu_cores": WL_LABELS | {"container", "type", "source"},
    "kerf_recommendation_memory_bytes": WL_LABELS | {"container", "type", "source"},
    "kerf_recommendation_monthly_savings_usd": WL_LABELS | {"container", "resource"},
    "kerf_recommendation_confidence": WL_LABELS,
    "kerf_recommendation_data_coverage_ratio": WL_LABELS,
    "kerf_recommendation_risk_flag": WL_LABELS | {"flag"},
    "kerf_recommendation_generated_timestamp_seconds": WL_LABELS,
    "kerf_recommendation_average_replicas": WL_LABELS,
    "kerf_workload_mode": WL_LABELS | {"resource", "mode", "source"},
    "kerf_workload_phase": WL_LABELS | {"phase"},
    "kerf_actions_total": WL_LABELS | {"type", "strategy", "resource", "reason"},
}

GRAFANA = os.environ.get("GRAFANA_URL", "http://localhost:13000").rstrip("/")
AUTH = "Basic " + base64.b64encode((os.environ.get("GRAFANA_USER", "admin") + ":"
                                    + os.environ.get("GRAFANA_PASSWORD", "admin")).encode()).decode()
DS_UID = os.environ.get("DATASOURCE_UID", "victoriametrics")
VM_URL = os.environ.get("VM_URL", "http://localhost:18428").rstrip("/")
VIA = "grafana"


# ------------------------------------------------------------------------------------------ static

def panels_of(dash):
    for p in dash["panels"]:
        yield p
        for sub in p.get("panels", []):
            yield sub


def exprs_of(dash):
    for p in panels_of(dash):
        for t in p.get("targets", []):
            yield p, t
    for a in dash.get("annotations", {}).get("list", []):
        if a.get("expr"):
            yield {"title": "annotation: " + a["name"], "type": "annotation"}, a


def static_checks():
    errors = []
    contract_text = CONTRACT.read_text()
    contract_names = set(re.findall(r"`(kerf_[a-z0-9_]+)`", contract_text))
    missing = [m for m in CONTRACT_LABELS if m not in contract_names]
    if missing:
        errors.append("verify.py lists metrics that are not in the contract: " + ", ".join(missing))

    generated = build.render()
    uids = {}
    for name, text in generated.items():
        path = DASH_DIR / name
        try:
            on_disk = json.loads(path.read_text())
        except (OSError, ValueError) as e:
            errors.append(f"{name}: {e}")
            continue
        if path.read_text() != text:
            errors.append(f"{name}: out of date, run `make dashboards`")
        dash = on_disk
        if dash["uid"] in uids:
            errors.append(f"{name}: duplicate uid {dash['uid']} (also in {uids[dash['uid']]})")
        uids[dash["uid"]] = name
        for p in panels_of(dash):
            if p["type"] == "row":
                continue
            if not p.get("title"):
                errors.append(f"{name}: panel {p.get('id')} has no title")
            if not p.get("description"):
                errors.append(f"{name}: panel '{p.get('title')}' has no description")
            if p["type"] != "text" and p.get("datasource", {}).get("uid") != "${datasource}":
                errors.append(f"{name}: panel '{p.get('title')}' does not use ${{datasource}}")
        for p, t in exprs_of(dash):
            expr = t["expr"]
            for metric in set(re.findall(r"\b(kerf_[a-z0-9_]+)", expr)):
                base = re.sub(r"_(bucket|sum|count)$", "", metric)
                if metric not in contract_names and not base.startswith(OPS_PREFIXES):
                    errors.append(f"{name}: '{p['title']}' uses {metric}, not in docs/contracts/metrics.md")
            for metric, body in re.findall(r"\b(kerf_[a-z0-9_]+)\{([^}]*)\}", expr):
                allowed = CONTRACT_LABELS.get(metric)
                if allowed is None:
                    continue
                for label in re.findall(r"([a-zA-Z_][a-zA-Z0-9_]*)\s*(?:=~|!~|!=|=)", body):
                    if label not in allowed and label not in SCRAPE_LABELS:
                        errors.append(f"{name}: '{p['title']}' matches label {label} on {metric}")
    return errors, len(generated)


# ------------------------------------------------------------------------------------------ live

def http(method, path, body=None):
    req = urllib.request.Request(GRAFANA + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Authorization": AUTH, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.load(e)
        except ValueError:
            return e.code, {"message": str(e)}


def prom(path, params):
    """Prometheus HTTP API through Grafana's datasource proxy."""
    qs = urllib.parse.urlencode(params, doseq=True)
    if VIA == "vm":
        try:
            with urllib.request.urlopen(f"{VM_URL}/api/v1/{path}?{qs}", timeout=120) as r:
                code, body = r.status, json.load(r)
        except urllib.error.HTTPError as e:
            code, body = e.code, json.load(e)
    else:
        code, body = http("GET", f"/api/datasources/proxy/uid/{DS_UID}/api/v1/{path}?{qs}")
    if code != 200 or body.get("status") != "success":
        raise RuntimeError(f"{path} {params}: {body}")
    return body["data"]


def interpolate(text, values):
    def repl(m):
        name = m.group(1) or m.group(2)
        if name in values:
            return values[name]
        return m.group(0)
    return re.sub(r"\$\{(\w+)(?::\w+)?\}|\$(\w+)", repl, text)


def label_values(query, values):
    m = re.fullmatch(r"label_values\((.*),\s*(\w+)\)", query.strip(), re.S)
    if not m:
        raise ValueError("unsupported variable query " + query)
    selector, label = interpolate(m.group(1), values), m.group(2)
    return sorted(prom(f"label/{label}/values", {"match[]": selector, "start": "-86400"}))


def resolve(dash, cluster, pick):
    """Variable values for one run. pick: {var: value} forced values (e.g. workload for the drill-down)."""
    values = {"datasource": DS_UID}
    for var in dash["templating"]["list"]:
        name = var["name"]
        if var["type"] == "datasource":
            continue
        if var["type"] == "custom":
            values[name] = var["current"]["value"]
            continue
        if name == "cluster":
            values[name] = cluster
            continue
        if name in pick:
            values[name] = pick[name]
            continue
        if var.get("includeAll"):
            values[name] = var.get("allValue", ".*")
            continue
        opts = label_values(var["query"]["query"], values)
        values[name] = opts[0] if opts else ""
    return values


DURATION = re.compile(r"^now-(\d+)([mhd])$")
UNIT = {"s": 1, "m": 60, "h": 3600, "d": 86400}


def secs(text):
    m = re.fullmatch(r"(\d+)([smhd])", text)
    return int(m.group(1)) * UNIT[m.group(2)] if m else 0


def run_query_vm(dash, t, values):
    """Same query straight against VictoriaMetrics, with Grafana's macro expansion emulated."""
    m = DURATION.match(dash["time"]["from"])
    rng = int(m.group(1)) * UNIT[m.group(2)]
    step = max(rng // 1000, secs(t.get("interval", "")), 15)
    step = (step + 14) // 15 * 15
    macros = dict(values, __interval=f"{step}s", __rate_interval=f"{max(step + 30, 120)}s", __range=f"{rng}s")
    expr = interpolate(t["expr"], macros)
    now = int(time.time())
    try:
        if t.get("instant"):
            data = prom("query", {"query": expr})
        else:
            data = prom("query_range", {"query": expr, "start": now - rng, "end": now, "step": step})
    except RuntimeError as e:
        return "error", str(e)[:400]
    return ("ok", len(data["result"])) if data["result"] else ("empty", 0)


def interval_ms(dash, t):
    """What Grafana's frontend sends: range / max data points, at least the query's min interval."""
    m = DURATION.match(dash["time"]["from"])
    rng = int(m.group(1)) * UNIT[m.group(2)]
    return max(rng * 1000 // 1000, secs(t.get("interval", "")) * 1000, 15000)


def run_query(dash, t, values):
    if VIA == "vm":
        return run_query_vm(dash, t, values)
    q = {
        "refId": t.get("refId", "A"),
        "datasource": {"type": "prometheus", "uid": DS_UID},
        "expr": interpolate(t["expr"], values),
        "instant": t.get("instant", False),
        "range": t.get("range", not t.get("instant", False)),
        "format": t.get("format", "time_series"),
        "interval": interpolate(t.get("interval", "") or t.get("step", ""), values),
        "intervalMs": interval_ms(dash, t),
        "maxDataPoints": 1000,
        "legendFormat": t.get("legendFormat", ""),
    }
    body = {"queries": [q], "from": dash["time"]["from"], "to": dash["time"]["to"]}
    code, res = http("POST", "/api/ds/query", body)
    r = res.get("results", {}).get(q["refId"], {})
    if code != 200 or r.get("error"):
        return "error", r.get("error") or res.get("message") or f"HTTP {code}"
    rows = 0
    for frame in r.get("frames", []):
        vals = frame.get("data", {}).get("values", [])
        if len(vals) >= 2 and any(x is not None for x in vals[-1]):
            rows += 1
    return ("ok", rows) if rows else ("empty", 0)


def drill_targets(cluster):
    """Workloads to open in the drill-down for one cluster: the top-savings workload of each kind."""
    data = prom("query", {"query": (
        'topk by (workload_kind) (1, sum by (namespace, workload_kind, workload) '
        '(max by (namespace, workload_kind, workload, container, resource) '
        '(kerf_recommendation_monthly_savings_usd{cluster="' + cluster + '"})))')})
    out = []
    for s in sorted(data["result"], key=lambda s: s["metric"]["workload_kind"]):
        m = s["metric"]
        out.append({"namespace": m["namespace"], "kind": m["workload_kind"], "workload": m["workload"]})
    return out


def live_checks(clusters, strict, only):
    failures = 0
    empties = []
    for name, fn in build.DASHBOARDS.items():
        dash = fn()
        if only and dash["uid"] not in only:
            continue
        cl = clusters or label_values(build.VAR_CLUSTER_Q, {})
        for cluster in cl:
            picks = [{}]
            if dash["uid"] == build.UID["workload"]:
                picks = drill_targets(cluster) or [{}]
            for pick in picks:
                values = resolve(dash, cluster, pick)
                ctx = cluster + ("" if not pick else f" {values['namespace']}/{values['kind']}/{values['workload']}")
                ok = empty = 0
                for p, t in exprs_of(dash):
                    status, detail = run_query(dash, t, values)
                    if VIA == "grafana":
                        time.sleep(0.2)  # the dev Grafana runs with little memory headroom
                    label = f"{dash['uid']} [{ctx}] '{p['title']}' {t.get('refId', '')}".rstrip()
                    if status == "error":
                        failures += 1
                        print(f"ERROR  {label}: {detail}")
                    elif status == "empty":
                        empty += 1
                        empties.append(label)
                    else:
                        ok += 1
                print(f"{dash['uid']:<17} {ctx:<60} queries ok={ok} empty={empty}")
    if empties:
        print("\nqueries without data:")
        for e in empties:
            print("  " + e)
    if failures:
        print(f"\n{failures} query error(s)")
    return failures + (len(empties) if strict else 0)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--live", action="store_true", help="run every query through Grafana")
    ap.add_argument("--cluster", action="append", help="cluster(s) to cycle through (default: all)")
    ap.add_argument("--dashboard", action="append", help="limit to these dashboard UIDs")
    ap.add_argument("--strict", action="store_true", help="fail on queries without data")
    ap.add_argument("--via", choices=["grafana", "vm"], default="grafana",
                    help="run queries through Grafana's /api/ds/query (default) or straight against VictoriaMetrics")
    args = ap.parse_args()
    global VIA
    VIA = args.via

    errors, n = static_checks()
    for e in errors:
        print("ERROR  " + e)
    print(f"static: {n} dashboards, {len(errors)} problem(s)")
    rc = 1 if errors else 0
    if args.live:
        rc |= 1 if live_checks(args.cluster, args.strict, args.dashboard) else 0
    return rc


if __name__ == "__main__":
    sys.exit(main())
