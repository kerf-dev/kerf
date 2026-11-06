<!-- Thanks for contributing to Kerf. Keep one topic per pull request; small ones get reviewed faster. -->
<!-- The pull request title becomes the commit and the release-notes line: `type(scope): summary`, e.g. `fix(hub): handle an empty metrics response`. See CONTRIBUTING.md. -->

## What and why

<!-- What changes for users or operators, and the problem it solves. Link the issue: "Fixes #123". -->

## How it was tested

<!-- Commands you ran and what you checked, e.g. `make ci`, `make web-verify`, `make e2e`, a screenshot of the UI. -->

## Checklist

- [ ] `make ci` passes (and `make web-verify` for web UI changes, `make charts-lint` for chart changes)
- [ ] Tests added or updated
- [ ] User-visible change: a line under `## [Unreleased]` in `CHANGELOG.md`
- [ ] Contract change (CRDs, hub API, metrics, database migrations): described above, with `make generate` run
- [ ] Docs updated where behaviour changed
