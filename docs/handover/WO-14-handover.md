# WO-14 hand-over (web UI)

Branch `wo-14-web`, commit d2f5f03, pushed. Owns only web/, deploy/charts/web/, make/web.mk.
Deployed: release `web` in ro-hub/resource-optimizer, image k3d-ro-registry:5005/resource-optimizer/web:wo14-2,
Service NodePort 30090 → http://localhost:19090. It talks to the live hub through nginx with the dev `ui` token.

## Verification (all pass)
npm ci · lint (eslint + prettier) · typecheck · vitest 34/34 (incl. mock responses validated against hub.yaml) ·
build · gen:api:check · playwright e2e 8/8 · docker build + run + curl `<div id="root">` OK.
Proxy checked against an echo upstream: `%2F` reaches the hub undecoded, the browser's Authorization header is
replaced by the mounted token, cookies are dropped, and POST returns 403.

## Screenshots
wo-14-screenshots/ in this folder: {overview,opportunities,workload,workload-rolled-back,clusters,activity}-{light,dark}.png
and {overview,opportunities,workload}-tablet.png. Regenerate with `make web-screenshots`.

## Gaps / things for other tracks
1. Hub Service is `hub` (WO-04 chart: fullname = release name), not `hub-api` as in the WO-14 brief.
   The chart default is `hub.upstream: hub.resource-optimizer.svc.cluster.local:8080`. It has to be an FQDN
   because nginx's runtime resolver ignores search domains.
2. WO-04 savings trend disagrees with its summary: synth-a trend last point $154.55 vs summary $6,318;
   synth-b trend is all 0 vs summary $3,360.
3. WO-04 usage series are empty for synth workloads (usage/request/limit []), so the detail page shows its
   "no usage samples" state. Probably the synthetic series labels vs. the hub query (WO-02/WO-04).
4. API has no facets endpoint. The Namespace filter options come from one 500-row page. Suggested addition:
   GET /v1/workloads/facets (namespaces, kinds, counts).
5. API has no confidence breakdown, only `confidence` + `dataCoverage`. The UI shows coverage, samples and the
   risk flags that lower it. A `confidenceFactors` field would let "why" show the real formula.
6. Tiltfile (WO-01) does not pick up web: it is not in COMPONENTS, and the Dockerfile lookup expects
   cmd|build/<name>/Dockerfile with repo-root context. web needs ('web', ['ro-hub'], ['web']) and
   `docker build web/` (context web/).
7. NodePort 30090 is marked "reserved: pricing (WO-03)" in dev/k3d/ro-hub.yaml and the WO-01 handover. Pricing
   currently runs as ClusterIP, so the port is free and web uses it as the brief says. The docs should be updated.
8. The embedded Claude browser pane can't register service workers. The mock mode falls back to patching
   fetch (and remembers that choice), so `make web-dev` works there too.
