# Reference

Exact names, fields and defaults. The pages marked *generated* are built from the sources in the repository, and CI
fails when a committed page no longer matches its source.

| Page | What it lists | Source |
|---|---|---|
| [Chart values](values/README.md) | `global.mode`, the umbrella values, rules the schema enforces; one page per component chart: [hub](values/hub.md), [web](values/web.md), [pricing](values/pricing.md), [agent](values/agent.md), [webhook](values/webhook.md) (*generated*) | `deploy/charts/*/values.yaml`, `values.schema.json` |
| [Custom resources](crds.md) | `RightsizingPolicy` and `RightsizingRecommendation`, field by field (*generated*) | `api/v1alpha1`, through `api/crds` |
| [Hub API](api/README.md) | Every endpoint, by area, with parameters, bodies and responses, and the [schemas](api/schemas.md) (*generated*) | `api/openapi/hub.yaml` |
| [Metrics](../contracts/metrics.md) | The `kerf_*` metrics and the PromQL the recommender runs | |
| [Events](../contracts/events.md) | The audit trail: what the agent decided and did | |
| [Admin area](../contracts/admin.md) | Users, roles, tokens, policies and settings | |
| [Readiness](../contracts/readiness.md) | Metrics sources and the per-cluster readiness checks | |

To regenerate the pages after you change a source, run `make docs-generate` and commit the result.
