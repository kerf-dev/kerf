# Documentation site tooling

The documentation at [getkerf.dev/docs](https://getkerf.dev/docs/) is built from [`docs/`](../../docs) with
[MkDocs Material](https://squidfunk.github.io/mkdocs-material/). There is one source: the Markdown files keep working
on GitHub. Nothing here needs a service: it builds with Python in CI.

| Path | What it is |
|---|---|
| [`mkdocs.yml`](../../mkdocs.yml) | Site configuration: theme, search, navigation, version switch |
| [`docs-site/overrides/`](../../docs-site/overrides) | The theme: colours and type of getkerf.dev (`assets/kerf/kerf.css`), the logo, the head of every page |
| `hooks.py` | MkDocs hooks: links that leave `docs/` point at GitHub, missing navigation entries are dropped, unlisted pages are added under "More", fonts and logos are copied from `site/` |
| `generate.py` | Writes the reference pages in `docs/reference/` (`--check` fails when they are stale) |
| `gen_values.py`, `gen_crds.py`, `gen_api.py` | The three generators: chart values, custom resources, hub API |
| `build_site.py` | Builds every version with `mike` and assembles `_site/docs/` |
| `check-links.mjs` | Checks every link of the assembled site and docs, offline |
| `requirements.in`, `requirements.txt` | Python packages, pinned with hashes |

## Everyday tasks

```bash
make docs-serve        # live preview on http://127.0.0.1:8000
make docs-build        # build the dev docs into _docs-site/; warnings fail the build
make docs-generate     # regenerate docs/reference/ after changing a chart value, a CRD field or the API
make docs-verify       # what CI runs: reference pages up to date, tests, strict build
make docs-site         # every version, as the Pages workflow builds them, into _site/docs
```

`make docs-*` creates `.venv-docs` and installs the pinned packages. To change a package, edit `requirements.in` and run
`make docs-lock`.

## Adding a page

1. Create the Markdown file in `docs/` at a path that will not change, and add a line to
   [`docs/README.md`](../../docs/README.md). Existing paths never move: other pages and external links point at them.
2. Add it to `nav` in `mkdocs.yml`. A page that is not listed still appears, under "More", so it is never lost.
3. Link to other pages with relative `.md` links. A link to a file outside `docs/` (for example
   `../deploy/charts/kerf/README.md`) is rewritten to GitHub at the version being built.
4. Run `make docs-build`: a broken link, anchor or navigation entry fails it.

Write for GitHub and the site at once: no front matter and no admonition syntax in hand-written pages. The first
heading is the page title.

## Reference pages

`docs/reference/` holds generated pages. Do not edit them: change the source and run `make docs-generate`.

| Pages | Source |
|---|---|
| `values/` | `deploy/charts/kerf/values.schema.json` and `values.yaml`, and `deploy/charts/<component>/values.yaml`; the comment above a key becomes its description |
| `crds.md` | `api/crds`, which `make generate` builds from the Go types in `api/v1alpha1` (field descriptions are the Go comments) |
| `api/` | `api/openapi/hub.yaml` |

CI runs `generate.py --check` and fails when a committed page differs from what its source produces.

Contract sources may still mention internal planning labels. The generators remove them from the published text
(`common.scrub`) and print a warning when they find one that the usual patterns did not cover.

## Versions

`build_site.py` publishes `dev` (the checkout, `main`) and one version per minor release: the docs of the newest tag
`vX.Y.*` of each of the last three minors. `latest` is the newest stable release, or the newest release candidate while
there is no final release. Every version is built with this checkout's `mkdocs.yml` and theme, with `docs_dir` set to
the tag's `docs/`; navigation entries for pages that an older release does not have are dropped. `mike` assembles
the versions in a scratch repository and writes `versions.json`, which drives the version switch.

## Publishing

[`.github/workflows/pages.yaml`](../../.github/workflows/pages.yaml) builds the site and the docs together and
deploys from `main` only. It runs on changes to `site/`, `docs/` and this tooling, after each release, and on pull
requests (build and checks only). The `docs` job of the `ci` workflow runs `make docs-verify` on changes to the
docs, the API contracts and the charts.
