#!/usr/bin/env python3
"""Generate the Kerf Grafana dashboards.

The JSON files in dashboards/ are build output: edit this file, then run `make dashboards`.
`make dashboards-verify` fails when the JSON is out of date.

Every query is plain PromQL (D-003) over the series in docs/contracts/metrics.md.
Conventions used throughout:
  * Hub series carry scrape labels (instance, kubernetes_pod). During a hub rollout old and new
    pods export the same series for a few minutes, so every hub gauge is first reduced with
    `max by (<identity labels>)` before it is summed or joined.
  * Modes and phases are encoded as numbers (value mappings turn them back into text) so that
    every query of a table returns the same label set and Grafana's merge transformation can
    join them into one row per container.
Standard library only (Python 3.9+).
"""
import json
import pathlib
import sys

OUT_DIR = pathlib.Path(__file__).resolve().parent.parent
DS = {"type": "prometheus", "uid": "${datasource}"}
TAG = "kerf"
SCHEMA_VERSION = 41

UID = {
    "overview": "ro-overview",
    "opportunities": "ro-opportunities",
    "workload": "ro-workload",
    "automation": "ro-automation",
}

WL = "cluster, namespace, workload_kind, workload"
WLC = WL + ", container"
POD = "cluster, namespace, pod"
PODC = POD + ", container"

FLAGS = ["throttled", "oom-killed", "restarting", "hpa-coupled", "sidecar", "short-lived", "low-confidence"]
MODES = [("off", 0, "text"), ("audit", 1, "blue"), ("auto-patch", 2, "green")]
PHASES = [
    ("Pending", 0, "text"),
    ("Applying", 1, "yellow"),
    ("Applied", 2, "green"),
    ("Blocked", 3, "orange"),
    ("RolledBack", 4, "red"),
    ("Released", 5, "purple"),
]
EVENT_TYPES = [
    ("applied", "green"),
    ("skipped", "text"),
    ("blocked", "orange"),
    ("fallback", "yellow"),
    ("rolled_back", "red"),
    ("released", "purple"),
    ("failed", "dark-red"),
]

CORES = "suffix: cores"
USD = "currencyUSD"


# --------------------------------------------------------------------------------------------
# PromQL helpers
# --------------------------------------------------------------------------------------------

def v(metric, *matchers):
    """Instant vector selector: v("m", 'a="1"', None, 'b=~"x"') -> m{a="1", b=~"x"}."""
    return metric + "{" + ", ".join(m for m in matchers if m) + "}"


def dedup(metric, by, *matchers):
    return "max by (" + by + ") (" + v(metric, *matchers) + ")"


def zero_filled(values, label, expr_for):
    """One series per value (0 when absent), labelled label=value."""
    parts = []
    for val in values:
        parts.append('label_replace((' + expr_for(val) + ') or vector(0), "' + label + '", "' + val + '", "", "")')
    return "\n or ".join(parts)


def encoded(metric, label, choices, sel, by=WL):
    """Numeric encoding of a one-hot gauge (kerf_workload_mode / kerf_workload_phase): value = index."""
    parts = []
    for name, idx, _ in sorted(choices, key=lambda c: -c[1]):
        parts.append("(" + dedup(metric, by, *sel, label + '="' + name + '"') + " == 1) * " + str(idx))
    return "(\n  " + "\n  or ".join(parts) + "\n)"


def per_container(expr, base):
    """Copy a per-workload value onto every container row of `base` (per WLC, value ignored)."""
    return "(" + expr + ")\n+ on (" + WL + ") group_right ()\n(0 * " + base + ")"


def rec_containers(sel):
    """One series (value 1) per container that has a recommendation."""
    return ("group by (" + WLC + ") (" + v("kerf_recommendation_cpu_cores", *sel, 'source="recommended"')
            + " or " + v("kerf_recommendation_memory_bytes", *sel, 'source="recommended"') + ")")


def risk_flags(wl_sel, base, keys):
    """Attach a label `rf` = "throttled, oom-killed" (or "none") to each series of `base`.

    PromQL cannot concatenate labels across series, so each flag is first attached as its own label
    (f_<flag>) and the labels are then folded into one with label_join/label_replace.
    """
    expr = base
    labels = []
    for flag in FLAGS:
        lab = "f_" + flag.replace("-", "_")
        labels.append(lab)
        present = ('label_replace(' + dedup("kerf_recommendation_risk_flag", WL, *wl_sel, 'flag="' + flag + '"')
                   + ', "' + lab + '", "' + flag + '", "", "")')
        absent = "0 * " + dedup("kerf_recommendation_confidence", WL, *wl_sel) + " + 1"
        expr = ("(" + expr + ")\n* on (" + WL + ") group_left (" + lab + ")\n(" + present
                + " or on (" + WL + ") (" + absent + "))")
    expr = 'label_replace(' + expr + ', "rf", "$1", "' + labels[0] + '", "(.*)")'
    for lab in labels[1:]:
        expr = 'label_join(' + expr + ', "rf", "|", "rf", "' + lab + '")'
        expr = 'label_replace(' + expr + ', "rf", "$1, $2", "rf", "(.+)[|](.+)")'
        expr = 'label_replace(' + expr + ', "rf", "$1", "rf", "[|]?(.*?)[|]?")'
    expr = 'label_replace(' + expr + ', "rf", "none", "rf", "")'
    return "max by (" + keys + ", rf) (\n" + expr + "\n)"


# --------------------------------------------------------------------------------------------
# Panel helpers
# --------------------------------------------------------------------------------------------

def target(expr, legend="", ref="A", instant=False, fmt="time_series", interval=None, hide=False):
    t = {
        "datasource": DS,
        "editorMode": "code",
        "expr": expr,
        "legendFormat": legend or "__auto",
        "range": not instant,
        "instant": instant,
        "format": fmt,
        "refId": ref,
    }
    if interval:
        t["interval"] = interval
    if hide:
        t["hide"] = True
    return t


def targets(*specs):
    """targets(("expr", "legend"), ...) or dict kwargs -> list with refIds A, B, ..."""
    out = []
    for i, spec in enumerate(specs):
        ref = chr(ord("A") + i)
        if isinstance(spec, dict):
            out.append(target(ref=ref, **spec))
        else:
            out.append(target(spec[0], spec[1] if len(spec) > 1 else "", ref=ref))
    return out


def thresholds(*steps):
    return {"mode": "absolute", "steps": [{"color": c, "value": val} for c, val in steps]}


TH_NEUTRAL = thresholds(("text", None))
TH_SAVINGS = thresholds(("red", None), ("green", 0))
TH_CONFIDENCE = thresholds(("red", None), ("orange", 0.5), ("green", 0.7))
TH_COVERAGE = thresholds(("red", None), ("orange", 0.5), ("green", 0.9))


def value_map(choices):
    return [{
        "type": "value",
        "options": {str(idx): {"text": name, "color": color, "index": i}
                    for i, (name, idx, color) in enumerate(choices)},
    }]


def range_map(lo, hi, text, color=None):
    r = {"type": "range", "options": {"from": lo, "to": hi, "result": {"text": text}}}
    if color:
        r["options"]["result"]["color"] = color
    return r


def link(title, uid, slug, params, tooltip=""):
    q = "&".join(["${__url_time_range}", "${datasource:queryparam}"] + params)
    return {"title": title, "url": "/d/" + uid + "/" + slug + "?" + q, "targetBlank": False, "tooltip": tooltip}


def fixed(color):
    return {"mode": "fixed", "fixedColor": color}


def override_name(name, props, regex=False):
    return {"matcher": {"id": "byRegexp" if regex else "byName", "options": name},
            "properties": [{"id": k, "value": val} for k, val in props]}


def base_panel(kind, title, desc, tgts, unit="short", decimals=None, th=TH_NEUTRAL, mappings=None,
               color=None, overrides=None, links=None, no_value=None, min_=None, max_=None, interval=None,
               transformations=None):
    defaults = {"unit": unit, "thresholds": th, "mappings": mappings or []}
    if decimals is not None:
        defaults["decimals"] = decimals
    if color:
        defaults["color"] = color
    if no_value is not None:
        defaults["noValue"] = no_value
    if min_ is not None:
        defaults["min"] = min_
    if max_ is not None:
        defaults["max"] = max_
    p = {
        "type": kind,
        "title": title,
        "description": desc,
        "datasource": DS,
        "targets": tgts,
        "fieldConfig": {"defaults": defaults, "overrides": overrides or []},
        "options": {},
    }
    if links:
        p["links"] = links
    if interval:
        p["interval"] = interval
    if transformations:
        p["transformations"] = transformations
    return p


def stat(title, desc, tgts, text_mode="auto", color_mode="value", graph="none", orientation="auto",
         justify="auto", calcs="lastNotNull", **kw):
    kw.setdefault("color", {"mode": "thresholds"})
    p = base_panel("stat", title, desc, tgts, **kw)
    p["options"] = {
        "reduceOptions": {"calcs": [calcs], "fields": "", "values": False},
        "orientation": orientation,
        "textMode": text_mode,
        "colorMode": color_mode,
        "graphMode": graph,
        "justifyMode": justify,
        "wideLayout": True,
        "showPercentChange": False,
    }
    return p


def timeseries(title, desc, tgts, stack=False, bars=False, fill=10, legend_calcs=None, legend_mode="list",
               line_width=1, gradient="opacity", th_style="off", legend_place="bottom", **kw):
    kw.setdefault("color", {"mode": "palette-classic"})
    p = base_panel("timeseries", title, desc, tgts, **kw)
    p["fieldConfig"]["defaults"]["custom"] = {
        "drawStyle": "bars" if bars else "line",
        "lineInterpolation": "smooth" if not bars else "linear",
        "lineWidth": line_width,
        "fillOpacity": 80 if bars else fill,
        "gradientMode": "none" if bars else gradient,
        "showPoints": "never",
        "spanNulls": False,
        "stacking": {"mode": "normal" if stack else "none", "group": "A"},
        "axisBorderShow": False,
        "axisPlacement": "auto",
        "barAlignment": 0,
        "thresholdsStyle": {"mode": th_style},
    }
    p["options"] = {
        "legend": {"showLegend": True, "displayMode": legend_mode, "placement": legend_place,
                   "calcs": legend_calcs or []},
        "tooltip": {"mode": "multi", "sort": "desc"},
    }
    return p


def bargauge(title, desc, tgts, orientation="horizontal", display="gradient", **kw):
    kw.setdefault("color", {"mode": "thresholds"})
    for t in tgts:
        t["instant"], t["range"] = True, False
    p = base_panel("bargauge", title, desc, tgts, **kw)
    p["options"] = {
        "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
        "orientation": orientation,
        "displayMode": display,
        "valueMode": "color",
        "namePlacement": "left" if orientation == "horizontal" else "auto",
        "showUnfilled": True,
        "sizing": "auto",
        "minVizWidth": 8,
        "minVizHeight": 16,
        "maxVizHeight": 300,
    }
    return p


def table(title, desc, tgts, sort_by=None, columns=None, hidden=(), footer=False, **kw):
    """columns: list of (field name, display name, extra properties) in display order."""
    overrides = list(kw.pop("overrides", []))
    order = {}
    for i, col in enumerate(columns or []):
        name, display, props = col[0], col[1], (col[2] if len(col) > 2 else [])
        order[name] = i
        overrides.append(override_name(name, [("displayName", display)] + props))
    p = base_panel("table", title, desc, tgts, overrides=overrides, **kw)
    p["fieldConfig"]["defaults"]["custom"] = {"align": "auto", "cellOptions": {"type": "auto"},
                                              "inspect": False, "filterable": True}
    p["options"] = {
        "showHeader": True,
        "cellHeight": "sm",
        "footer": {"show": footer, "reducer": ["sum"], "fields": "", "countRows": False},
        "sortBy": [{"displayName": sort_by[0], "desc": sort_by[1]}] if sort_by else [],
    }
    excl = {h: True for h in hidden}
    excl.update({"Time": True})
    p.setdefault("transformations", [])
    p["transformations"] = p["transformations"] + [{
        "id": "organize",
        "options": {"excludeByName": excl, "indexByName": order, "renameByName": {}},
    }]
    return p


def text_panel(title, desc, content):
    p = {"type": "text", "title": title, "description": desc,
         "options": {"mode": "markdown", "content": content, "code": {"language": "plaintext",
                                                                      "showLineNumbers": False,
                                                                      "showMiniMap": False}}}
    return p


def cell(kind, **extra):
    d = {"type": kind}
    d.update(extra)
    return ("custom.cellOptions", d)


MERGE = [{"id": "merge", "options": {}}]


# --------------------------------------------------------------------------------------------
# Dashboard skeleton
# --------------------------------------------------------------------------------------------

class Grid:
    def __init__(self):
        self.panels = []
        self.x = 0
        self.y = 0
        self.row_h = 0
        self.next_id = 1

    def _place(self, panel, w, h):
        if self.x + w > 24:
            self.newline()
        panel["id"] = self.next_id
        self.next_id += 1
        panel["gridPos"] = {"x": self.x, "y": self.y, "w": w, "h": h}
        self.x += w
        self.row_h = max(self.row_h, h)
        self.panels.append(panel)

    def newline(self):
        self.y += self.row_h
        self.x = 0
        self.row_h = 0

    def add(self, w, h, panel):
        self._place(panel, w, h)

    def row(self, title, collapsed=False):
        self.newline()
        self._place({"type": "row", "title": title, "collapsed": collapsed, "panels": []}, 24, 1)
        self.newline()


def ds_var():
    return {
        "name": "datasource", "label": "Data source", "type": "datasource", "query": "prometheus",
        "current": {"selected": True, "text": "VictoriaMetrics", "value": "victoriametrics"},
        "regex": "", "hide": 0, "refresh": 1, "includeAll": False, "multi": False, "options": [],
        "description": "Prometheus-compatible data source with the hub's VictoriaMetrics (or Prometheus).",
    }


def query_var(name, label, query, multi=True, include_all=True, description="", hide=0):
    var = {
        "name": name, "label": label, "type": "query", "datasource": DS,
        "definition": query,
        "query": {"qryType": 1, "query": query, "refId": "PrometheusVariableQueryEditor-VariableQuery"},
        "refresh": 2, "sort": 1, "regex": "", "hide": hide,
        "multi": multi, "includeAll": include_all, "options": [],
        "description": description,
    }
    if include_all:
        var["allValue"] = ".*"
        var["current"] = {"selected": True, "text": ["All"], "value": ["$__all"]} if multi else \
            {"selected": True, "text": "All", "value": "$__all"}
    else:
        var["current"] = {}
    return var


def custom_var(name, label, options, default, description=""):
    """options: list of (text, value)."""
    opts = [{"text": t, "value": val, "selected": val == default} for t, val in options]
    cur = next(o for o in opts if o["selected"])
    return {
        "name": name, "label": label, "type": "custom", "hide": 0,
        "query": ",".join(t + " : " + val if t != val else val for t, val in options),
        "options": opts, "current": {"selected": True, "text": cur["text"], "value": cur["value"]},
        "multi": False, "includeAll": False, "description": description,
    }


def dashboard(key, title, description, grid, variables, time_from="now-24h", refresh="1m", annotations=()):
    return {
        "uid": UID[key],
        "title": title,
        "description": description,
        "tags": [TAG],
        "editable": True,
        "graphTooltip": 1,
        "timezone": "browser",
        "fiscalYearStartMonth": 0,
        "liveNow": False,
        "weekStart": "",
        "refresh": refresh,
        "schemaVersion": SCHEMA_VERSION,
        "version": 1,
        "time": {"from": time_from, "to": "now"},
        "timepicker": {},
        "links": [{
            "title": "kerf", "type": "dashboards", "tags": [TAG], "asDropdown": False,
            "includeVars": True, "keepTime": True, "targetBlank": False, "icon": "external link",
            "tooltip": "", "url": "",
        }],
        "annotations": {"list": [{
            "builtIn": 1, "datasource": {"type": "grafana", "uid": "-- Grafana --"}, "enable": True,
            "hide": True, "iconColor": "rgba(0, 211, 255, 1)", "name": "Annotations & Alerts",
            "type": "dashboard",
        }] + list(annotations)},
        "templating": {"list": [ds_var()] + variables},
        "panels": grid.panels,
    }


# --------------------------------------------------------------------------------------------
# Shared selectors and expressions
# --------------------------------------------------------------------------------------------

F_C = 'cluster=~"$cluster"'
F_N = 'namespace=~"$namespace"'
F_W = 'workload=~"$workload"'
F_CT = 'container=~"$container"'
REAL_CONTAINER = ['container!=""', 'container!="POD"']

D_C = 'cluster="$cluster"'
D_N = 'namespace="$namespace"'
D_K = 'workload_kind="$kind"'
D_W = 'workload="$workload"'
D_WL = [D_C, D_N, D_K, D_W]
D_WLC = D_WL + [F_CT]

VAR_CLUSTER_Q = "label_values(kerf_workload_mode, cluster)"
VAR_NAMESPACE_Q = 'label_values(kerf_workload_mode{cluster=~"$cluster"}, namespace)'


def savings(sel):
    """Monthly savings per container and resource, de-duplicated across hub pods."""
    return dedup("kerf_recommendation_monthly_savings_usd", WLC + ", resource", *sel)


def rec_value(metric, sel, typ, source):
    return dedup(metric, WLC, *sel, 'type="' + typ + '"', 'source="' + source + '"')


def fleet_total(metric, sel, source):
    """Sum over workloads of (per-container request x average replicas)."""
    return ("sum (\n  " + rec_value(metric, sel, "request", source) + "\n  * on (" + WL + ") group_left ()\n  "
            + dedup("kerf_recommendation_average_replicas", WL, *sel) + "\n)")


def running_requests(resource, sel):
    """Requests of containers that are running now (cAdvisor drops series of terminated ones)."""
    return ("sum (\n  " + dedup("kube_pod_container_resource_requests", PODC, *sel, 'resource="' + resource + '"')
            + "\n  and on (" + PODC + ")\n  " + dedup("container_memory_working_set_bytes", PODC, *sel, *REAL_CONTAINER)
            + "\n)")


# --------------------------------------------------------------------------------------------
# Overview
# --------------------------------------------------------------------------------------------

def overview():
    sel = [F_C, F_N]
    g = Grid()
    opp = link("Opportunities", UID["opportunities"], "opportunities",
               ["${cluster:queryparam}", "${namespace:queryparam}"], "Ranked table of every recommendation")
    sav = savings(sel)

    g.row("Estimated monthly savings")
    g.add(5, 5, stat(
        "Net savings / month",
        "Estimated USD per month if every recommendation were applied: savings from decreases minus the cost "
        "of increases (kerf_recommendation_monthly_savings_usd, summed over containers and resources). "
        "Estimated, not realized.",
        targets(("sum(" + sav + ")", "Net")), unit=USD, decimals=0, th=TH_SAVINGS, graph="area", links=[opp]))
    g.add(5, 5, stat(
        "From decreases",
        "Sum of positive savings only: what over-provisioned containers would free up per month.",
        targets(("sum(" + sav + " > 0)", "Decreases")), unit=USD, decimals=0, no_value="0",
        th=thresholds(("green", None)), graph="area", links=[opp]))
    g.add(5, 5, stat(
        "Cost of increases",
        "Sum of negative savings: what under-provisioned containers (throttled, OOM-killed, …) would cost "
        "per month once raised. Shown as a negative amount.",
        targets(("sum(" + sav + " < 0)", "Increases")), unit=USD, decimals=0, no_value="0",
        th=thresholds(("orange", None)), graph="area", links=[opp]))
    g.add(5, 5, stat(
        "Node cost / month",
        "Current price of all priced nodes in the selected clusters × 730 h (kerf_node_hourly_cost_usd from the "
        "pricing service). Not filtered by namespace.",
        targets(("sum(" + dedup("kerf_node_hourly_cost_usd", "cluster, node", F_C) + ") * 730", "Nodes")),
        unit=USD, decimals=0, graph="area"))
    g.add(4, 5, stat(
        "Share of node cost",
        "Net monthly savings as a share of the monthly node cost of the selected clusters. Indicative: the "
        "namespace filter narrows the savings but not the node cost.",
        targets(("sum(" + sav + ") / (sum(" + dedup("kerf_node_hourly_cost_usd", "cluster, node", F_C) + ") * 730)",
                 "Share")),
        unit="percentunit", decimals=1, th=thresholds(("text", None), ("green", 0.05))))

    g.add(8, 8, bargauge(
        "Savings by cluster",
        "Net estimated monthly savings per cluster. Click through to the ranked opportunities.",
        targets(("sort_desc(sum by (cluster) (" + sav + "))", "{{cluster}}")),
        unit=USD, decimals=0, th=TH_SAVINGS, links=[opp], display="basic"))
    g.add(10, 8, bargauge(
        "Top namespaces by savings",
        "The 15 namespaces with the highest net estimated monthly savings across the selected clusters.",
        targets(("sort_desc(topk(15, sum by (cluster, namespace) (" + sav + ")))", "{{namespace}} · {{cluster}}")),
        unit=USD, decimals=0, th=TH_SAVINGS, links=[opp], display="basic"))
    g.add(6, 8, stat(
        "Savings by resource",
        "Net estimated monthly savings split into CPU and memory.",
        targets({"expr": "sum by (resource) (" + sav + ")", "legend": "{{resource}}", "instant": True}),
        unit=USD, decimals=0, th=TH_SAVINGS, links=[opp], text_mode="value_and_name", orientation="horizontal"))
    g.add(24, 7, timeseries(
        "Net savings over time",
        "History of the net estimated monthly savings per cluster, as recomputed by every recommender run. "
        "A drop means recommendations were applied or usage grew.",
        targets(("sum by (cluster) (" + sav + ")", "{{cluster}}")),
        unit=USD, decimals=0, legend_calcs=["lastNotNull", "max"], legend_mode="table"))

    g.row("Realized savings (from applied changes)")
    rl = "cluster, namespace, workload_kind, workload, resource"
    realized = dedup("kerf_hub_realized_savings_usd", rl, *sel)
    released = lambda res: "sum(" + dedup("kerf_hub_realized_released_requests", rl, *sel, 'resource="' + res + '"') + ")"
    g.add(6, 6, stat(
        "Realized savings (since the first change)",
        "What the changes Kerf applied actually saved: requests released by applied changes × replicas that ran × "
        "node rates of the time, from the first change on (kerf_hub_realized_savings_usd, computed hourly by the "
        "hub). Realized, not estimated: never add it to the estimated panels above. Method: docs/realized-savings.md.",
        targets(("sum(" + realized + ")", "Realized")), unit=USD, decimals=2, th=TH_SAVINGS, graph="area",
        no_value="0"))
    g.add(4, 6, stat(
        "CPU released now",
        "CPU requests released by applied changes in the last computed hour, × replicas.",
        targets((released("cpu"), "CPU")), unit=CORES, decimals=2, no_value="0"))
    g.add(4, 6, stat(
        "Memory released now",
        "Memory requests released by applied changes in the last computed hour, × replicas.",
        targets((released("memory"), "Memory")), unit="bytes", decimals=1, no_value="0"))
    g.add(10, 6, timeseries(
        "Realized savings over time",
        "Cumulative realized savings per cluster (USD since the first applied change). The slope is the saving "
        "per hour; a flat line means nothing is released (rolled back, or handed back to Git).",
        targets(("sum by (cluster) (" + realized + ")", "{{cluster}}")),
        unit=USD, decimals=2, legend_calcs=["lastNotNull"], legend_mode="table"))

    g.row("Capacity: requested vs used vs recommended")
    cpu_req = running_requests("cpu", sel)
    cpu_used = ("sum(rate(" + v("container_cpu_usage_seconds_total", *sel, *REAL_CONTAINER) + "[$__rate_interval]))")
    cpu_rec = fleet_total("kerf_recommendation_cpu_cores", sel, "recommended")
    cpu_cur = fleet_total("kerf_recommendation_cpu_cores", sel, "current")
    mem_req = running_requests("memory", sel)
    mem_used = "sum(" + dedup("container_memory_working_set_bytes", PODC, *sel, *REAL_CONTAINER) + ")"
    mem_rec = fleet_total("kerf_recommendation_memory_bytes", sel, "recommended")
    mem_cur = fleet_total("kerf_recommendation_memory_bytes", sel, "current")
    cap_overrides = [
        override_name("Requested", [("color", fixed("orange")), ("custom.lineWidth", 2)]),
        override_name("Used", [("color", fixed("blue")), ("custom.fillOpacity", 25)]),
        override_name("Recommended", [("color", fixed("green")), ("custom.lineWidth", 2),
                                      ("custom.lineStyle", {"fill": "dash", "dash": [10, 10]})]),
    ]
    cap_desc = ("Requested = requests of running containers (kube-state-metrics). Used = {used}. "
                "Recommended = recommended request × average replicas per workload (hub). "
                "The gap between Requested and Recommended is the rightsizing opportunity.")
    g.add(12, 9, timeseries(
        "CPU: requested vs used vs recommended",
        cap_desc.format(used="cAdvisor CPU usage rate"),
        targets((cpu_req, "Requested"), (cpu_used, "Used"), (cpu_rec, "Recommended")),
        unit=CORES, decimals=2, overrides=cap_overrides, legend_calcs=["lastNotNull", "mean"], legend_mode="table",
        min_=0))
    g.add(12, 9, timeseries(
        "Memory: requested vs used vs recommended",
        cap_desc.format(used="cAdvisor working set"),
        targets((mem_req, "Requested"), (mem_used, "Used"), (mem_rec, "Recommended")),
        unit="bytes", decimals=1, overrides=cap_overrides, legend_calcs=["lastNotNull", "mean"],
        legend_mode="table", min_=0))
    util_th = thresholds(("orange", None), ("green", 0.5), ("orange", 0.9))
    g.add(6, 4, stat(
        "CPU used / requested",
        "Share of the requested CPU that running containers actually use right now. Low = over-provisioned.",
        targets(("(" + cpu_used + ") / (" + cpu_req + ")", "CPU")), unit="percentunit", decimals=0, th=util_th,
        graph="area"))
    g.add(6, 4, stat(
        "CPU change if applied",
        "Recommended vs current CPU requests of the workloads the hub covers (both × average replicas). "
        "Negative = the fleet needs fewer cores.",
        targets(("(" + cpu_rec + ") / (" + cpu_cur + ") - 1", "CPU")), unit="percentunit", decimals=0,
        th=thresholds(("green", None), ("text", 0)), graph="area"))
    g.add(6, 4, stat(
        "Memory used / requested",
        "Share of the requested memory that running containers use right now (working set).",
        targets(("(" + mem_used + ") / (" + mem_req + ")", "Memory")), unit="percentunit", decimals=0, th=util_th,
        graph="area"))
    g.add(6, 4, stat(
        "Memory change if applied",
        "Recommended vs current memory requests of the workloads the hub covers (both × average replicas).",
        targets(("(" + mem_rec + ") / (" + mem_cur + ") - 1", "Memory")), unit="percentunit", decimals=0,
        th=thresholds(("green", None), ("text", 0)), graph="area"))

    g.row("Workloads: modes, risk flags, phases")
    tracked = "count(" + dedup("kerf_workload_mode", WL, *sel, 'resource="cpu"') + " == 1)"
    g.add(4, 4, stat(
        "Workloads",
        "Workloads known to the hub (every workload reported by an agent or seen in metrics).",
        targets((tracked, "Workloads")), decimals=0, no_value="0"))
    g.add(4, 4, stat(
        "Recommended",
        "Workloads that have a current recommendation (needs usage data).",
        targets(("count(" + dedup("kerf_recommendation_confidence", WL, *sel) + ")", "Recommended")),
        decimals=0, no_value="0"))
    g.add(4, 4, stat(
        "Confidence (avg)",
        "Mean recommendation confidence (0–1). Below 0.5 the hub flags low-confidence; the agent's default "
        "guardrail needs 0.7 to apply.",
        targets(("avg(" + dedup("kerf_recommendation_confidence", WL, *sel) + ")", "Confidence")),
        unit="percentunit", decimals=0, th=TH_CONFIDENCE, min_=0, max_=1))
    g.add(4, 4, stat(
        "Coverage (avg)",
        "Mean fraction of the recommendation window that has usage samples.",
        targets(("avg(" + dedup("kerf_recommendation_data_coverage_ratio", WL, *sel) + ")", "Coverage")),
        unit="percentunit", decimals=0, th=TH_COVERAGE, min_=0, max_=1))
    g.add(4, 4, stat(
        "Flagged",
        "Workloads with at least one risk flag (throttled, oom-killed, restarting, hpa-coupled, sidecar, "
        "short-lived, low-confidence).",
        targets(("count(count by (" + WL + ") (" + dedup("kerf_recommendation_risk_flag", WL + ", flag", *sel) + "))",
                 "Flagged")), decimals=0, no_value="0", th=thresholds(("text", None), ("orange", 1))))
    g.add(4, 4, stat(
        "In auto-patch",
        "Workloads with auto-patch as the effective mode for CPU or memory.",
        targets(("count(" + dedup("kerf_workload_mode", WL, *sel, 'mode="auto-patch"') + " == 1)", "auto-patch")),
        decimals=0, no_value="0", th=thresholds(("text", None), ("green", 1))))

    mode_expr = lambda res: zero_filled([m for m, _, _ in MODES], "mode", lambda m: (
        "count(" + dedup("kerf_workload_mode", WL, *sel, 'resource="' + res + '"', 'mode="' + m + '"') + " == 1)"))
    mode_panel = base_panel(
        "barchart", "Workloads per mode",
        "Effective mode per resource (D-005: global default audit < cluster default < policy < annotation).",
        [target(mode_expr("cpu"), "{{mode}}", "A", instant=True, fmt="table"),
         target(mode_expr("memory"), "{{mode}}", "B", instant=True, fmt="table")],
        decimals=0, color={"mode": "palette-classic"},
        overrides=[override_name("Value #A", [("displayName", "CPU"), ("color", fixed("blue"))]),
                   override_name("Value #B", [("displayName", "Memory"), ("color", fixed("purple"))])],
        transformations=MERGE + [{"id": "organize", "options": {"excludeByName": {"Time": True},
                                                                "indexByName": {"mode": 0}}}])
    mode_panel["options"] = {"xField": "mode", "orientation": "horizontal", "barWidth": 0.8, "groupWidth": 0.7,
                             "showValue": "auto", "stacking": "none", "xTickLabelRotation": 0,
                             "legend": {"showLegend": True, "displayMode": "list", "placement": "bottom", "calcs": []},
                             "tooltip": {"mode": "multi", "sort": "none"}}
    mode_panel["fieldConfig"]["defaults"]["custom"] = {"fillOpacity": 80, "lineWidth": 0, "gradientMode": "none",
                                                       "axisPlacement": "auto", "axisBorderShow": False}
    g.add(8, 8, mode_panel)
    g.add(8, 8, bargauge(
        "Workloads per risk flag",
        "How many workloads carry each risk flag. throttled / oom-killed: never decreased; hpa-coupled: audit "
        "only (D-011); low-confidence: confidence < 0.5.",
        targets({"expr": zero_filled(FLAGS, "flag", lambda f: (
            "count(" + dedup("kerf_recommendation_risk_flag", WL, *sel, 'flag="' + f + '"') + ")")),
            "legend": "{{flag}}", "instant": True}),
        decimals=0, th=thresholds(("text", None), ("orange", 1)), links=[opp]))
    g.add(8, 8, bargauge(
        "Workloads per phase",
        "Current RightsizingRecommendation phase reported by the agents. Pending = nothing applied yet.",
        targets({"expr": zero_filled([p for p, _, _ in PHASES], "phase", lambda ph: (
            "count(" + dedup("kerf_workload_phase", WL, *sel, 'phase="' + ph + '"') + " == 1)")),
            "legend": "{{phase}}", "instant": True}),
        decimals=0, color={"mode": "fixed", "fixedColor": "blue"}))

    g.row("Data freshness")
    g.add(12, 4, stat(
        "Last recommender run",
        "Time since the recommender last succeeded for each cluster. Production runs every 24 h (orange after "
        "26 h, red after 48 h); the dev environment runs every 5 min.",
        targets({"expr": "time() - max by (cluster) (" + v("kerf_recommender_last_success_timestamp_seconds", F_C) + ")",
                 "legend": "{{cluster}}", "instant": True}),
        unit="s", decimals=0, th=thresholds(("green", None), ("orange", 26 * 3600), ("red", 48 * 3600)),
        text_mode="value_and_name"))
    g.add(12, 4, stat(
        "Pricing source",
        "Whether savings for each cluster use live node prices from the pricing service or the hub's default "
        "rates (kerf_recommender_pricing_fallback = 1).",
        targets({"expr": "max by (cluster) (" + v("kerf_recommender_pricing_fallback", F_C) + ")", "legend": "{{cluster}}",
                 "instant": True}),
        mappings=[{"type": "value", "options": {"0": {"text": "Live prices", "color": "green", "index": 0},
                                                "1": {"text": "Default rates", "color": "orange", "index": 1}}}],
        text_mode="value_and_name"))

    return dashboard(
        "overview", "Rightsizing / Overview",
        "Estimated savings, capacity requested vs used vs recommended, and workload modes / risk flags.",
        g, [
            query_var("cluster", "Cluster", VAR_CLUSTER_Q),
            query_var("namespace", "Namespace", VAR_NAMESPACE_Q),
        ])


# --------------------------------------------------------------------------------------------
# Opportunities
# --------------------------------------------------------------------------------------------

def opportunity_columns(sel_c, sel_wl, filt):
    """Per-container queries for the opportunities tables; every one returns labels WLC."""
    total = "sum by (" + WLC + ") (" + savings(sel_c) + ")"
    keep = "\nand on (" + WLC + ") (" + total + " " + filt + ")" if filt else ""
    base = rec_containers(sel_c)
    q = []

    def add(expr, ref):
        q.append(target("(" + expr + ")" + keep, "", ref, instant=True, fmt="table"))

    add(total, "A")
    add(rec_value("kerf_recommendation_cpu_cores", sel_c, "request", "current"), "D")
    add(rec_value("kerf_recommendation_cpu_cores", sel_c, "request", "recommended"), "E")
    add(rec_value("kerf_recommendation_memory_bytes", sel_c, "request", "current"), "F")
    add(rec_value("kerf_recommendation_memory_bytes", sel_c, "request", "recommended"), "G")
    add(per_container(dedup("kerf_recommendation_confidence", WL, *sel_wl), base), "H")
    add(per_container(encoded("kerf_workload_mode", "mode", MODES, sel_wl + ['resource="cpu"']), base), "I")
    add(per_container(encoded("kerf_workload_mode", "mode", MODES, sel_wl + ['resource="memory"']), base), "J")
    add(per_container(encoded("kerf_workload_phase", "phase", PHASES, sel_wl), base), "K")
    add(risk_flags(sel_wl, base, WLC), "L")
    return q


def drill_link_fields():
    return [{
        "title": "Open workload drill-down",
        "url": ("/d/" + UID["workload"] + "/workload?${datasource:queryparam}"
                "&var-cluster=${__data.fields.cluster}&var-namespace=${__data.fields.namespace}"
                "&var-kind=${__data.fields.workload_kind}&var-workload=${__data.fields.workload}"
                "&var-container=${__data.fields.container}"),
    }]


def opportunity_table_columns():
    mode_map = value_map(MODES)
    phase_map = value_map(PHASES)
    return [
        ("cluster", "Cluster", [("custom.width", 90)]),
        ("namespace", "Namespace", [("custom.width", 115)]),
        ("workload_kind", "Kind", [("custom.width", 100)]),
        ("workload", "Workload", [("links", drill_link_fields()), ("custom.minWidth", 170)]),
        ("container", "Container", [("custom.minWidth", 100)]),
        ("Value #D", "CPU req", [("unit", CORES), ("custom.width", 90)]),
        ("Value #E", "CPU rec", [("unit", CORES), ("custom.width", 90), ("color", fixed("green")),
                                 cell("color-text")]),
        ("Value #F", "Mem req", [("unit", "bytes"), ("decimals", 0), ("custom.width", 85)]),
        ("Value #G", "Mem rec", [("unit", "bytes"), ("decimals", 0), ("custom.width", 85),
                                 ("color", fixed("green")), cell("color-text")]),
        ("Value #A", "$/month", [("unit", USD), ("decimals", 2), ("thresholds", TH_SAVINGS),
                                 cell("color-background", mode="gradient"), ("custom.width", 95)]),
        ("Value #H", "Confidence", [("unit", "percentunit"), ("decimals", 0), ("min", 0), ("max", 1),
                                    ("thresholds", TH_CONFIDENCE),
                                    cell("gauge", mode="basic", valueDisplayMode="text"), ("custom.width", 110)]),
        ("rf", "Risk flags", [("custom.minWidth", 120)]),
        ("Value #I", "CPU mode", [("mappings", mode_map), cell("color-text"), ("custom.width", 85)]),
        ("Value #J", "Mem mode", [("mappings", mode_map), cell("color-text"), ("custom.width", 85)]),
        ("Value #K", "Phase", [("mappings", phase_map), cell("color-text"), ("custom.width", 85)]),
    ]


def opportunities():
    sel_wl = [F_C, F_N, F_W]
    sel_c = sel_wl + [F_CT]
    g = Grid()
    sav = savings(sel_c)
    kept = "sum by (" + WLC + ") (" + sav + ") $direction"

    g.add(6, 4, stat(
        "Net savings in view",
        "Net estimated monthly savings of the rows in the table below (filters and the Show variable apply).",
        targets(("sum(" + kept + ")", "Net")), unit=USD, decimals=0, th=TH_SAVINGS, no_value="0"))
    g.add(6, 4, stat(
        "Containers in view",
        "Number of container rows in the table below.",
        targets(("count(" + kept + ")", "Rows")), decimals=0, no_value="0"))
    g.add(6, 4, stat(
        "Decreases",
        "Containers whose recommendation lowers cost (positive savings).",
        targets(("count(sum by (" + WLC + ") (" + sav + ") > 0)", "Decreases")), decimals=0, no_value="0",
        th=thresholds(("green", None))))
    g.add(6, 4, stat(
        "Increases",
        "Containers whose recommendation raises cost (negative savings): usually throttled or OOM-killed.",
        targets(("count(sum by (" + WLC + ") (" + sav + ") < 0)", "Increases")), decimals=0, no_value="0",
        th=thresholds(("orange", None))))

    g.add(24, 20, table(
        "Opportunities by monthly savings",
        "One row per container with a recommendation, ranked by estimated monthly savings (negative = the "
        "recommendation raises the request). Requests are per pod; savings include the average replica count. "
        "Click a workload to open its drill-down. Columns are filterable; Mode/Phase come from agent reports.",
        opportunity_columns(sel_c, sel_wl, "$direction"),
        sort_by=("$/month", True), columns=opportunity_table_columns(), hidden=("Value #L",),
        transformations=MERGE, footer=False))

    g.add(12, 9, bargauge(
        "Top 10 workloads",
        "Workloads with the highest net estimated monthly savings (all containers).",
        targets(("sort_desc(topk(10, sum by (" + WL + ") (" + sav + ")))", "{{workload}} · {{namespace}} · {{cluster}}")),
        unit=USD, decimals=0, th=TH_SAVINGS))
    g.add(12, 9, bargauge(
        "Largest increases",
        "Workloads whose recommendations raise cost the most: under-provisioned today.",
        targets(("sort(bottomk(10, sum by (" + WL + ") (" + sav + ") < 0))", "{{workload}} · {{namespace}} · {{cluster}}")),
        unit=USD, decimals=0, th=thresholds(("orange", None)), no_value="None"))

    return dashboard(
        "opportunities", "Rightsizing / Opportunities",
        "Every container recommendation ranked by estimated monthly savings, with current vs recommended "
        "requests, confidence, risk flags and modes.",
        g, [
            query_var("cluster", "Cluster", VAR_CLUSTER_Q),
            query_var("namespace", "Namespace", VAR_NAMESPACE_Q),
            query_var("workload", "Workload",
                      'label_values(kerf_workload_mode{cluster=~"$cluster", namespace=~"$namespace"}, workload)'),
            query_var("container", "Container",
                      'label_values(kerf_recommendation_memory_bytes{cluster=~"$cluster", namespace=~"$namespace", '
                      'workload=~"$workload"}, container)'),
            custom_var("direction", "Show", [("All", "> -Inf"), ("Savings", "> 0"), ("Increases", "< 0")], "> -Inf",
                       "Filter rows by the sign of the estimated savings."),
        ], time_from="now-6h")


# --------------------------------------------------------------------------------------------
# Workload drill-down
# --------------------------------------------------------------------------------------------

K_NS = [D_C, D_N]


def owner(metric, window, *matchers):
    """KSM owner series seen at any time in the window, so short-lived pods (CronJobs) still join."""
    return "max_over_time(" + v(metric, *matchers) + "[" + window + "])"


def workload_pods(window="$__rate_interval"):
    """1 per pod of the selected workload ($kind/$workload), following the contract's owner joins."""
    deploy = ("max by (" + POD + ") (\n    label_replace(" + owner("kube_pod_owner", window, *K_NS, 'owner_kind="ReplicaSet"')
              + ', "replicaset", "$1", "owner_name", "(.*)")\n    * on (cluster, namespace, replicaset) group_left ()\n    '
              + "max by (cluster, namespace, replicaset) (" + owner("kube_replicaset_owner", window, *K_NS,
                                                                   'owner_kind="$kind"', 'owner_name="$workload"')
              + ")\n  )")
    cron = ("max by (" + POD + ") (\n    label_replace(" + owner("kube_pod_owner", window, *K_NS, 'owner_kind="Job"')
            + ', "job_name", "$1", "owner_name", "(.*)")\n    * on (cluster, namespace, job_name) group_left ()\n    '
            + "max by (cluster, namespace, job_name) (" + owner("kube_job_owner", window, *K_NS,
                                                             'owner_kind="$kind"', 'owner_name="$workload"')
            + ")\n  )")
    direct = "max by (" + POD + ") (" + owner("kube_pod_owner", window, *K_NS, 'owner_kind="$kind"',
                                              'owner_name="$workload"') + ")"
    return "(\n  " + deploy + "\n  or " + cron + "\n  or " + direct + "\n)"


def of_workload(per_pod_container, agg="max", window="$__rate_interval"):
    return (agg + " by (container) (\n  " + per_pod_container + "\n  * on (" + POD + ") group_left ()\n  "
            + workload_pods(window) + "\n)")


def cpu_pod(rate_window):
    return ("sum by (" + PODC + ") (rate(" + v("container_cpu_usage_seconds_total", *K_NS, F_CT, *REAL_CONTAINER)
            + "[" + rate_window + "]))")


def mem_pod(inner=None):
    sel = v("container_memory_working_set_bytes", *K_NS, F_CT, *REAL_CONTAINER)
    if inner:
        sel = "max_over_time(" + sel + "[" + inner + "])"
    return "max by (" + PODC + ") (" + sel + ")"


def ksm_value(metric, resource):
    return of_workload(dedup(metric, PODC, *K_NS, F_CT, 'resource="' + resource + '"'))


def percentile_lines(usage_5m):
    out = []
    for name, fn in [("P50", "quantile_over_time(0.5, "), ("P95", "quantile_over_time(0.95, "),
                     ("P99", "quantile_over_time(0.99, "), ("max", "max_over_time(")]:
        out.append({"expr": fn + "(\n" + usage_5m + "\n)[$window:5m] @ end())",
                    "legend": "{{container}} " + name + " ($window)", "interval": "30m"})
    return out


def usage_panel(title, desc, unit, usage, usage_avg, usage_5m, req, lim, metric):
    tgts = targets(
        {"expr": usage, "legend": "{{container}} usage (max of pods)"},
        {"expr": usage_avg, "legend": "{{container}} usage (avg of pods)"},
        {"expr": req, "legend": "{{container}} current request"},
        {"expr": lim, "legend": "{{container}} current limit"},
        {"expr": rec_value(metric, D_WLC, "request", "recommended"), "legend": "{{container}} recommended request"},
        {"expr": rec_value(metric, D_WLC, "limit", "recommended"), "legend": "{{container}} recommended limit"},
        *percentile_lines(usage_5m))
    dash = {"fill": "dash", "dash": [10, 10]}
    dot = {"fill": "dot", "dash": [0, 10]}
    overrides = [
        override_name(".*usage \\(max of pods\\)", [("color", fixed("blue")), ("custom.fillOpacity", 20)], True),
        override_name(".*usage \\(avg of pods\\)", [("color", fixed("light-blue")), ("custom.fillOpacity", 0)], True),
        override_name(".*current request", [("color", fixed("orange")), ("custom.lineWidth", 2),
                                            ("custom.fillOpacity", 0)], True),
        override_name(".*current limit", [("color", fixed("red")), ("custom.lineWidth", 2), ("custom.fillOpacity", 0),
                                          ("custom.lineStyle", dash)], True),
        override_name(".*recommended request", [("color", fixed("green")), ("custom.lineWidth", 2),
                                                ("custom.fillOpacity", 0)], True),
        override_name(".*recommended limit", [("color", fixed("dark-green")), ("custom.lineWidth", 2),
                                              ("custom.fillOpacity", 0), ("custom.lineStyle", dash)], True),
        override_name(".* (P50|P95|P99|max) \\(.*", [("color", fixed("purple")), ("custom.fillOpacity", 0),
                                                     ("custom.lineStyle", dot), ("custom.lineInterpolation", "stepAfter")],
                      True),
        override_name(".* P50 \\(.*", [("color", fixed("super-light-purple"))], True),
        override_name(".* P95 \\(.*", [("color", fixed("light-purple"))], True),
        override_name(".* P99 \\(.*", [("color", fixed("purple"))], True),
        override_name(".* max \\(.*", [("color", fixed("dark-purple"))], True),
    ]
    return timeseries(title, desc, tgts, unit=unit, decimals=3 if unit == CORES else 1, overrides=overrides,
                      legend_mode="table", legend_calcs=["lastNotNull", "max"], legend_place="right", min_=0)


def drill_container_table():
    mapping = [
        ("A", "kerf_recommendation_cpu_cores", "request", "current"),
        ("B", "kerf_recommendation_cpu_cores", "request", "recommended"),
        ("C", "kerf_recommendation_cpu_cores", "limit", "current"),
        ("D", "kerf_recommendation_cpu_cores", "limit", "recommended"),
        ("E", "kerf_recommendation_memory_bytes", "request", "current"),
        ("F", "kerf_recommendation_memory_bytes", "request", "recommended"),
        ("G", "kerf_recommendation_memory_bytes", "limit", "current"),
        ("H", "kerf_recommendation_memory_bytes", "limit", "recommended"),
    ]
    tg = [target(rec_value(m, D_WLC, t, s), "", ref, instant=True, fmt="table") for ref, m, t, s in mapping]
    tg.append(target("sum by (" + WLC + ") (" + savings(D_WLC) + ")", "", "I", instant=True, fmt="table"))
    green = [("color", fixed("green")), cell("color-text")]
    cols = [("container", "Container")]
    for ref, m, t, s in mapping:
        unit = CORES if "cpu" in m else "bytes"
        label = ("CPU " if "cpu" in m else "Mem ") + ("req" if t == "request" else "lim") + (" rec" if s == "recommended" else "")
        props = [("unit", unit), ("decimals", 3 if unit == CORES else 0)]
        if t == "limit":
            props.append(("noValue", "none"))
        if s == "recommended":
            props += green
        cols.append(("Value #" + ref, label, props))
    cols.append(("Value #I", "$/mo", [("unit", USD), ("decimals", 2), ("thresholds", TH_SAVINGS), cell("color-text")]))
    return table(
        "Containers: current vs recommended",
        "Per-pod requests and limits now (as reported by the hub) and what the hub recommends. 'none' = no limit "
        "set, or the recommendation removes it. $/mo = estimated monthly savings of that container.",
        tg, columns=cols, hidden=("cluster", "namespace", "workload_kind", "workload"),
        transformations=MERGE)


def workload():
    g = Grid()
    wl = D_WL
    g.row("Recommendation")
    g.add(6, 4, stat(
        "Monthly savings",
        "Estimated monthly savings of the selected containers (CPU + memory); negative = increase.",
        targets(("sum(" + savings(D_WLC) + ")", "Savings")), unit=USD, decimals=2, th=TH_SAVINGS, no_value="–"))
    g.add(6, 4, stat(
        "Confidence",
        "Recommendation confidence 0–1 = 0.6 × coverage + 0.2 × samples + 0.2 × stability. < 0.5 is flagged "
        "low-confidence; the agent applies at ≥ 0.7 by default.",
        targets(("max(" + v("kerf_recommendation_confidence", *wl) + ")", "Confidence")),
        unit="percentunit", decimals=0, th=TH_CONFIDENCE, min_=0, max_=1, no_value="–"))
    g.add(6, 4, stat(
        "Data coverage",
        "Fraction of the recommendation window that has usage samples.",
        targets(("max(" + v("kerf_recommendation_data_coverage_ratio", *wl) + ")", "Coverage")),
        unit="percentunit", decimals=0, th=TH_COVERAGE, min_=0, max_=1, no_value="–"))
    g.add(4, 4, stat(
        "Replicas",
        "Average number of running pods the hub used to compute savings.",
        targets(("max(" + v("kerf_recommendation_average_replicas", *wl) + ")", "Replicas")), decimals=1,
        no_value="–"))
    g.add(6, 4, stat(
        "Recommendation age",
        "Time since the current recommendation was generated.",
        targets(("time() - max(" + v("kerf_recommendation_generated_timestamp_seconds", *wl) + ")", "Age")),
        unit="s", decimals=0, th=thresholds(("green", None), ("orange", 26 * 3600), ("red", 48 * 3600)),
        no_value="–"))
    for res, title in [("cpu", "CPU mode"), ("memory", "Memory mode")]:
        g.add(4, 4, stat(
            title,
            "Effective " + res + " mode (off / audit / auto-patch) as reported by the agent.",
            targets((encoded("kerf_workload_mode", "mode", MODES, wl + ['resource="' + res + '"']), "Mode")),
            mappings=value_map(MODES), no_value="unknown", color={"mode": "fixed", "fixedColor": "text"}))
    g.add(4, 4, stat(
        "Phase",
        "Phase of the workload's RightsizingRecommendation: Pending → Applying → Applied, or Blocked / "
        "RolledBack / Released.",
        targets((encoded("kerf_workload_phase", "phase", PHASES, wl), "Phase")),
        mappings=value_map(PHASES), no_value="unknown", color={"mode": "fixed", "fixedColor": "text"}))

    g.add(8, 4, stat(
        "Risk flags",
        "Risk flags on the current recommendation. throttled / oom-killed: never decreased; restarting; "
        "hpa-coupled: audit only; sidecar; short-lived; low-confidence.",
        targets(("max by (rf) (" + risk_flags(wl, dedup("kerf_recommendation_confidence", WL, *wl), WL) + ")",
                 "{{rf}}")),
        text_mode="name", color_mode="value", no_value="–",
        color={"mode": "fixed", "fixedColor": "orange"}))
    g.add(24, 6, drill_container_table())

    g.row("CPU")
    g.add(24, 12, usage_panel(
        "CPU usage vs request, limit and recommendation",
        "CPU usage of the workload's pods (rate of container_cpu_usage_seconds_total; the hub uses the max "
        "across pods per step), current request/limit from kube-state-metrics, the hub's recommended values, "
        "and P50/P95/P99/max of the max-of-pods usage over the last $window (dotted, 5m resolution). "
        "Production uses P95 over 14d + 15 % headroom.",
        CORES,
        of_workload(cpu_pod("$__rate_interval")), of_workload(cpu_pod("$__rate_interval"), "avg"),
        of_workload(cpu_pod("5m"), window="5m"),
        ksm_value("kube_pod_container_resource_requests", "cpu"),
        ksm_value("kube_pod_container_resource_limits", "cpu"),
        "kerf_recommendation_cpu_cores"))

    g.row("Memory")
    g.add(24, 12, usage_panel(
        "Memory usage vs request, limit and recommendation",
        "Working-set memory of the workload's pods (peak within each step, so short-lived pods show up), current request/limit from kube-state-metrics, the hub's "
        "recommended values, and P50/P95/P99/max of the max-of-pods working set over the last $window. "
        "Production uses max(P99, window peak) + 15 % buffer.",
        "bytes",
        of_workload(mem_pod("$__rate_interval")), of_workload(mem_pod("$__rate_interval"), "avg"),
        of_workload(mem_pod("5m"), window="5m"),
        ksm_value("kube_pod_container_resource_requests", "memory"),
        ksm_value("kube_pod_container_resource_limits", "memory"),
        "kerf_recommendation_memory_bytes"))

    g.row("Pressure and events")
    throttle = of_workload(
        "sum by (" + PODC + ") (rate(" + v("container_cpu_cfs_throttled_periods_total", *K_NS, F_CT, *REAL_CONTAINER)
        + "[$__rate_interval]))\n  / clamp_min(sum by (" + PODC + ") (rate("
        + v("container_cpu_cfs_periods_total", *K_NS, F_CT, *REAL_CONTAINER) + "[$__rate_interval])), 1)")
    g.add(12, 8, timeseries(
        "CPU throttling ratio",
        "Share of CFS periods in which the container was throttled (max across pods). Only containers with a "
        "CPU limit are throttled. Sustained throttling sets the throttled flag and blocks CPU decreases.",
        targets((throttle, "{{container}}")), unit="percentunit", decimals=1, min_=0,
        th=thresholds(("transparent", None), ("orange", 0.05), ("red", 0.25)), th_style="dashed",
        no_value="No CFS data: no CPU limit set"))
    restarts = "sum by (" + PODC + ") (increase(" + v("kube_pod_container_status_restarts_total", *K_NS, F_CT) + "[$__interval]))"
    oom = ("(" + restarts + "\n  * on (" + PODC + ") group_left ()\n  ("
           + dedup("kube_pod_container_status_last_terminated_reason", PODC, *K_NS, F_CT, 'reason="OOMKilled"')
           + " == 1))")
    g.add(12, 8, timeseries(
        "Restarts and OOM kills",
        "Container restarts per interval across the workload's pods, and the subset whose last termination "
        "reason is OOMKilled (kube-state-metrics, authoritative for OOM kills).",
        targets((of_workload(restarts, "sum"), "{{container}} restarts"), (of_workload(oom, "sum"), "{{container}} OOM kills")),
        bars=True, decimals=0, min_=0, interval="5m",
        overrides=[override_name(".*OOM kills", [("color", fixed("red"))], True),
                   override_name(".*restarts", [("color", fixed("orange"))], True)]))
    g.add(24, 7, table(
        "Automation events for this workload",
        "Events the agent sent for this workload in the selected time range (kerf_actions_total, counted by the "
        "hub). Shown as annotations on the graphs above.",
        [target("sum by (type, reason, resource, strategy) (increase(" + v("kerf_actions_total", *wl)
                + "[$__range])) > 0", "", "A", instant=True, fmt="table")],
        columns=[("type", "Type", [("mappings", [{"type": "value", "options": {
                    t: {"color": c, "index": i} for i, (t, c) in enumerate(EVENT_TYPES)}}]), cell("color-text")]),
                 ("reason", "Reason"), ("resource", "Resource"), ("strategy", "Strategy"),
                 ("Value", "Events", [("decimals", 0)])],
        sort_by=("Events", True), no_value="No events in range"))

    annotation = {
        "name": "Automation events", "enable": True, "hide": False, "iconColor": "#FF9830",
        "datasource": DS, "expr": ("sum by (type, reason) (increase(" + v("kerf_actions_total", *wl) + "[2m])) > 0"),
        "step": "1m", "titleFormat": "{{type}}", "textFormat": "{{reason}}", "tagKeys": "type,reason",
        "useValueForTime": False, "refId": "Anno",
    }
    return dashboard(
        "workload", "Rightsizing / Workload drill-down",
        "Why a workload got its recommendation: usage percentiles over the window, current and recommended "
        "requests/limits, throttling, OOM kills and automation events.",
        g, [
            query_var("cluster", "Cluster", VAR_CLUSTER_Q, multi=False, include_all=False),
            query_var("namespace", "Namespace", 'label_values(kerf_workload_mode{cluster="$cluster"}, namespace)',
                      multi=False, include_all=False),
            query_var("workload", "Workload",
                      'label_values(kerf_workload_mode{cluster="$cluster", namespace="$namespace"}, workload)',
                      multi=False, include_all=False),
            query_var("kind", "Kind",
                      'label_values(kerf_workload_mode{cluster="$cluster", namespace="$namespace", '
                      'workload="$workload"}, workload_kind)',
                      multi=False, include_all=False, description="Workload kind (normally a single value)."),
            query_var("container", "Container",
                      'label_values(kerf_recommendation_memory_bytes{cluster="$cluster", namespace="$namespace", '
                      'workload_kind="$kind", workload="$workload"}, container)'),
            custom_var("window", "Percentile window", [("7d", "7d"), ("14d", "14d"), ("28d", "28d")], "14d",
                       "Window of the P50/P95/P99/max lines. Profiles: dev 7d, production 14d, high-availability 28d."),
        ], time_from="now-7d", annotations=[annotation])


# --------------------------------------------------------------------------------------------
# Automation
# --------------------------------------------------------------------------------------------

def automation():
    sel = [F_C, F_N, F_W]
    g = Grid()
    acts = lambda *m: v("kerf_actions_total", *sel, *m)

    g.row("Actions")
    type_overrides = [override_name(t, [("color", fixed(c))]) for t, c in EVENT_TYPES]
    g.add(24, 4, stat(
        "Events in range",
        "Events stored by the hub in the selected time range, by type (kerf_actions_total; resets on hub restart, "
        "so always read through increase()). One increment per resource listed in an event.",
        targets({"expr": zero_filled([t for t, _ in EVENT_TYPES], "type", lambda t: (
            "sum(increase(" + acts('type="' + t + '"') + "[$__range]))")), "legend": "{{type}}", "instant": True}),
        decimals=0, text_mode="value_and_name", overrides=type_overrides,
        color={"mode": "fixed", "fixedColor": "text"}))
    g.add(16, 9, timeseries(
        "Actions over time by type",
        "Events per interval, stacked by type.",
        targets(("sum by (type) (increase(" + acts() + "[$__interval]))", "{{type}}")),
        bars=True, stack=True, decimals=0, min_=0, interval="5m", overrides=type_overrides,
        no_value="No events in range"))
    g.add(8, 9, timeseries(
        "Rollbacks by reason",
        "Health-watch rollbacks per interval: OOMKills, Restarts or Throttling after a change.",
        targets(("sum by (reason) (increase(" + acts('type="rolled_back"') + "[$__interval]))", "{{reason}}")),
        bars=True, stack=True, decimals=0, min_=0, interval="5m", color={"mode": "palette-classic"},
        no_value="No rollbacks in range"))
    g.add(8, 8, bargauge(
        "Blocked reasons",
        "Why guardrails stopped changes the mode would allow (LowConfidence, Cooldown, HPACoupled, …), in range.",
        targets({"expr": "sort_desc(sum by (reason) (increase(" + acts('type="blocked"') + "[$__range])) > 0)",
                 "legend": "{{reason}}", "instant": True}),
        decimals=0, th=thresholds(("orange", None)), no_value="Nothing blocked"))
    g.add(8, 8, bargauge(
        "Skipped reasons",
        "Deliberate no-ops in range: mode off/audit, change too small, or recommendation stale.",
        targets({"expr": "sort_desc(sum by (reason) (increase(" + acts('type="skipped"') + "[$__range])) > 0)",
                 "legend": "{{reason}}", "instant": True}),
        decimals=0, color={"mode": "fixed", "fixedColor": "blue"}, no_value="Nothing skipped"))
    g.add(8, 8, bargauge(
        "Fallbacks and failures",
        "In-place changes that fell back to rollout, and Kubernetes API errors, by reason, in range.",
        targets({"expr": "sort_desc(sum by (type, reason) (increase(" + acts('type=~"fallback|failed"')
                 + "[$__range])) > 0)", "legend": "{{type}} · {{reason}}", "instant": True}),
        decimals=0, th=thresholds(("red", None)), no_value="None"))

    per_type = [("applied", "A"), ("rolled_back", "B"), ("blocked", "C"), ("failed", "D"), ("skipped", "E")]
    tg = [target("sum by (" + WL + ") (increase(" + acts('type="' + t + '"') + "[$__range])) > 0", "", ref,
                 instant=True, fmt="table") for t, ref in per_type]
    cols = [("cluster", "Cluster"), ("namespace", "Namespace"), ("workload_kind", "Kind"),
            ("workload", "Workload", [("links", [{
                "title": "Open workload drill-down",
                "url": ("/d/" + UID["workload"] + "/workload?${__url_time_range}&${datasource:queryparam}"
                        "&var-cluster=${__data.fields.cluster}&var-namespace=${__data.fields.namespace}"
                        "&var-kind=${__data.fields.workload_kind}&var-workload=${__data.fields.workload}"),
            }])])]
    for t, ref in per_type:
        c = dict(EVENT_TYPES)[t]
        cols.append(("Value #" + ref, t.replace("_", " ").capitalize(),
                     [("decimals", 0), ("color", fixed(c)), cell("color-text"), ("noValue", "0")]))
    g.add(24, 9, table(
        "Workloads with events",
        "Event counts per workload in the selected range. Click a workload for its drill-down.",
        tg, columns=cols, sort_by=("Applied", True), transformations=MERGE, no_value="No events in range"))

    g.row("Modes and phases")
    phase_overrides = [override_name(p, [("color", fixed(c if c != "text" else "blue"))]) for p, _, c in PHASES]
    g.add(14, 8, timeseries(
        "Workloads by phase",
        "Number of workloads per RightsizingRecommendation phase over time.",
        targets(("count by (phase) (" + dedup("kerf_workload_phase", WL + ", phase", *sel) + " == 1)", "{{phase}}")),
        stack=True, fill=40, decimals=0, min_=0, overrides=phase_overrides))
    g.add(10, 8, table(
        "Modes and where they come from",
        "Workloads per effective mode and the level that set it (global-default < cluster-default < policy < "
        "annotation, D-005).",
        [target("count by (resource, mode, source) (" + dedup("kerf_workload_mode", WL + ", resource, mode, source", *sel)
                + " == 1)", "", "A", instant=True, fmt="table")],
        columns=[("resource", "Resource"), ("mode", "Mode", [("mappings", [{"type": "value", "options": {
                     m: {"color": c, "index": i} for i, (m, _, c) in enumerate(MODES)}}]), cell("color-text")]),
                 ("source", "Source"), ("Value", "Workloads", [("decimals", 0)])],
        sort_by=("Workloads", True)))

    g.row("Pipeline health")
    g.add(8, 5, stat(
        "Last recommender run",
        "Time since the recommender last succeeded per cluster (production: every 24 h; dev: every 5 min).",
        targets({"expr": "time() - max by (cluster) (" + v("kerf_recommender_last_success_timestamp_seconds", F_C) + ")",
                 "legend": "{{cluster}}", "instant": True}),
        unit="s", decimals=0, th=thresholds(("green", None), ("orange", 26 * 3600), ("red", 48 * 3600)),
        text_mode="value_and_name"))
    g.add(8, 5, stat(
        "Last agent pull",
        "Time since each cluster's agent last pulled recommendations successfully. 'never' = the agent has "
        "not reached the hub yet (check its token and the hub URL).",
        targets({"expr": "time() - max by (cluster) (" + v("kerf_agent_last_successful_pull_timestamp_seconds", F_C) + ")",
                 "legend": "{{cluster}}", "instant": True}),
        unit="s", decimals=0, th=thresholds(("green", None), ("orange", 900), ("red", 3600)),
        mappings=[range_map(1e8, None, "never", "red")], text_mode="value_and_name", no_value="no agent"))
    g.add(8, 5, stat(
        "Recommender runs in range",
        "Recommender runs by outcome in the selected range (hub-wide, not per cluster). 'partial' = some "
        "clusters failed.",
        targets({"expr": "sum by (status) (increase(kerf_recommender_runs_total[$__range]))", "legend": "{{status}}",
                 "instant": True}),
        decimals=0, text_mode="value_and_name", color={"mode": "fixed", "fixedColor": "text"},
        overrides=[override_name("succeeded", [("color", fixed("green"))]),
                   override_name("partial", [("color", fixed("orange"))]),
                   override_name("failed", [("color", fixed("red"))])]))
    g.add(8, 8, timeseries(
        "Agent → hub requests",
        "Requests from each agent to the hub by result (ok, not_modified, error).",
        targets(("sum by (cluster, result) (rate(" + v("kerf_agent_hub_requests_total", F_C) + "[$__rate_interval]))",
                 "{{cluster}} · {{result}}")),
        unit="reqps", decimals=2, min_=0, no_value="No agent reporting",
        overrides=[override_name(".*error", [("color", fixed("red"))], True)]))
    g.add(8, 8, timeseries(
        "Agent event buffer",
        "Events waiting in each agent's buffer for delivery to the hub, and events dropped because the buffer "
        "was full. Both should stay at 0.",
        targets(("max by (cluster) (" + v("kerf_agent_events_buffered", F_C) + ")", "{{cluster}} buffered"),
                ("sum by (cluster) (increase(" + v("kerf_agent_events_dropped_total", F_C) + "[$__interval]))",
                 "{{cluster}} dropped")),
        decimals=0, min_=0, overrides=[override_name(".*dropped", [("color", fixed("red"))], True)],
        no_value="No agent reporting"))
    g.add(8, 8, timeseries(
        "Recommender run duration",
        "Average duration of recommender runs completed in each interval (hub-wide).",
        targets(("sum(increase(kerf_recommender_run_duration_seconds_sum[$__interval])) / "
                 "sum(increase(kerf_recommender_run_duration_seconds_count[$__interval]))", "avg run")),
        unit="s", decimals=1, min_=0, interval="15m", bars=True, no_value="No runs in range"))

    return dashboard(
        "automation", "Rightsizing / Automation",
        "What the agents did: actions by type over time, rollbacks, blocked/skipped reasons, phases, and the "
        "health of the recommendation pipeline.",
        g, [
            query_var("cluster", "Cluster", VAR_CLUSTER_Q),
            query_var("namespace", "Namespace", VAR_NAMESPACE_Q),
            query_var("workload", "Workload",
                      'label_values(kerf_workload_mode{cluster=~"$cluster", namespace=~"$namespace"}, workload)'),
        ], time_from="now-7d")


DASHBOARDS = {
    "overview.json": overview,
    "opportunities.json": opportunities,
    "workload.json": workload,
    "automation.json": automation,
}


def render():
    return {name: json.dumps(fn(), indent=2, ensure_ascii=False) + "\n" for name, fn in DASHBOARDS.items()}


def main(argv):
    check = "--check" in argv
    stale = []
    for name, text in render().items():
        path = OUT_DIR / name
        if check:
            if not path.exists() or path.read_text() != text:
                stale.append(name)
        else:
            path.write_text(text)
            print("wrote", path.relative_to(OUT_DIR.parent))
    if stale:
        print("out of date (run `make dashboards`):", ", ".join(stale), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
