# Copy a recommendation as a patch

Many teams keep their workloads in Git and leave Kerf in `audit`: they want the recommendation and make the
change themselves through a pull request. Kerf does not open pull requests against your repositories, but the web
UI hands over the exact change, ready to paste.

**Copy as…** is on the workload page (next to the savings) and on the Opportunities bulk bar for the selected
workloads. Every role can use it, viewers included: it only reads the values the page already shows. Nothing is
sent anywhere; the text is built in your browser.

| Format | What you get | Typical use |
|---|---|---|
| **Strategic merge patch** | A YAML document with `apiVersion`, `kind`, `metadata` and the containers to change | `kubectl patch --patch-file`, Kustomize `patchesStrategicMerge`, Flux `patches` |
| **kubectl patch** | One `kubectl patch … --type strategic -p '{…}'` command per workload | A quick change, or a script |
| **Kustomize patch** | A `patches:` list: each entry has a `target` (group, version, kind, name) and the patch | Paste into a `kustomization.yaml` |
| **Helm values** | A `resources:` block per container, keyed by the container name | Merge into your values file |

**Preview…** in the menu shows the output of each format before you copy it, and is where you land when the
browser refuses clipboard access (plain `http` pages other than `localhost`).

## What goes into the patch

- Only the resources that have a recommendation. A container without a recommended CPU request, for example,
  gets no `cpu` request in the patch; a workload with nothing to change is skipped (and counted in the toast).
- Each container is matched by name. Init containers and native sidecars go under `initContainers`.
- A limit that the recommendation removes (`removeLimits`) becomes `null`: in a merge patch that deletes the
  field, and in a Helm values file it removes the chart's default for it.
- Deployments, StatefulSets and DaemonSets patch `spec.template.spec`; CronJobs patch
  `spec.jobTemplate.spec.template.spec`.

## Bulk copy

Select workloads in Opportunities and use **Copy as…** in the bar at the bottom.

- Strategic merge patch and Helm values: one document per workload, joined with `---`.
- kubectl patch: one command per workload.
- Kustomize: a single `patches:` list with an entry per workload, so the result is one valid fragment.

Each document starts with a comment naming the kind, namespace, workload and cluster, so a selection across
clusters stays readable.

## Things to know

- **Kustomize targets match on kind and name, not namespace.** Overlays usually set `namespace:` themselves, and a
  base without a namespace would not match a namespaced target. If two workloads in different namespaces share a
  name, add `namespace:` to the `target` by hand. The namespace is in the comment above each entry.
- **Helm values are keyed by container name**, because every chart lays out its values differently. Move each
  `resources:` block to where your chart reads it (many charts use a top-level `resources:` for a single
  container).
- **Server-side apply cannot remove a limit.** `kubectl apply --server-side` reads `limits: {cpu: null}` as a limit
  of 0, so use `kubectl patch` or Kustomize for patches that remove a limit.
- Requests and limits come from the latest recommendation. Copy again after the next recommendation run to pick up
  new values, and keep `auto-patch` off for workloads you manage this way (or Kerf and your pipeline will fight
  over the same fields).

## Checking the patches against a cluster

`web/scripts/patch-dry-run.sh` applies every format to sample Deployments, StatefulSets, DaemonSets and CronJobs
with `--dry-run=server`: a strategic merge patch through `kubectl apply --server-side` and `kubectl patch
--patch-file`, the `kubectl patch` command itself, the Kustomize output through `kubectl kustomize`, and Helm
values merged into the samples. It creates a throw-away namespace (`kerf-dryrun-…`), uses replica-less samples and deletes
the namespace afterwards.

```bash
cd web && KERF_DRYRUN_CONTEXT=<kube context> npm run test:patch-dry-run
```
