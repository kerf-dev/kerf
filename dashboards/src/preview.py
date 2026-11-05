#!/usr/bin/env python3
"""Import the generated dashboards into Grafana through its HTTP API, as previews (WO-05).

Grafana provisions dashboards/ from the main checkout; to try a branch before it is merged, this
script imports every dashboard with "-preview" appended to its UID, " (preview)" to its title, tag
"kerf-preview" and links rewritten to the preview UIDs, into folder
"kerf (preview)". Provisioned dashboards are never touched.

  preview.py            import / update the previews
  preview.py --delete   delete the previews and their folder

Environment: GRAFANA_URL (default http://localhost:13000), GRAFANA_USER/GRAFANA_PASSWORD (admin/admin).
"""
import base64
import json
import os
import pathlib
import sys
import urllib.error
import urllib.request

sys.dont_write_bytecode = True
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import build  # noqa: E402

GRAFANA = os.environ.get("GRAFANA_URL", "http://localhost:13000").rstrip("/")
AUTH = "Basic " + base64.b64encode((os.environ.get("GRAFANA_USER", "admin") + ":"
                                    + os.environ.get("GRAFANA_PASSWORD", "admin")).encode()).decode()
FOLDER_UID = "kerf-preview"
FOLDER_TITLE = "kerf (preview)"
SUFFIX = "-preview"
TAG = build.TAG + "-preview"


def http(method, path, body=None):
    req = urllib.request.Request(GRAFANA + path, method=method,
                                 data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Authorization": AUTH, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.load(e)
        except ValueError:
            return e.code, {}


def previewed(dash):
    text = json.dumps(dash)
    for uid in build.UID.values():
        text = text.replace('"/d/' + uid + '/', '"/d/' + uid + SUFFIX + '/')
        text = text.replace('"uid": "' + uid + '"', '"uid": "' + uid + SUFFIX + '"')
    d = json.loads(text)
    d["title"] += " (preview)"
    d["tags"] = [TAG]
    for link in d.get("links", []):
        if link.get("type") == "dashboards":
            link["tags"] = [TAG]
    return d


def main():
    if "--delete" in sys.argv:
        for uid in build.UID.values():
            code, _ = http("DELETE", "/api/dashboards/uid/" + uid + SUFFIX)
            print("delete", uid + SUFFIX, code)
        code, _ = http("DELETE", "/api/folders/" + FOLDER_UID)
        print("delete folder", FOLDER_UID, code)
        return 0
    code, _ = http("GET", "/api/folders/" + FOLDER_UID)
    if code == 404:
        code, body = http("POST", "/api/folders", {"uid": FOLDER_UID, "title": FOLDER_TITLE})
        if code not in (200, 201):
            print("cannot create folder:", code, body, file=sys.stderr)
            return 1
    rc = 0
    for name, fn in build.DASHBOARDS.items():
        d = previewed(fn())
        code, body = http("POST", "/api/dashboards/db",
                          {"dashboard": d, "folderUid": FOLDER_UID, "overwrite": True,
                           "message": "WO-05 preview import"})
        if code != 200:
            rc = 1
            print("FAILED", name, code, body, file=sys.stderr)
        else:
            print(f"imported {name:<20} {GRAFANA}{body.get('url')}")
    return rc


if __name__ == "__main__":
    sys.exit(main())
