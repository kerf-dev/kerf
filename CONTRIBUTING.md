# Contributing to Kerf

Thanks for helping. Bug reports, docs fixes, tests, UI polish and features are all welcome, and so is a first
pull request: you don't need to know Kubernetes internals to improve Kerf.

## Ways to help

- **Try it and tell us.** Install it in `audit` mode, compare its recommendations with what you would have
  set, and open an issue or a [discussion](https://github.com/kerf-dev/kerf/discussions) about what you find.
- **Pick a first issue.** Issues labelled
  [`good first issue`](https://github.com/kerf-dev/kerf/labels/good%20first%20issue) are small and come with
  pointers to the code. [`help wanted`](https://github.com/kerf-dev/kerf/labels/help%20wanted) issues are
  bigger. Comment on the issue to say you're working on it.
- **Fix the docs.** If a step in the [install guide](docs/install/eks.md) didn't work for you, a pull request
  that fixes it is one of the most useful contributions.
- **Report a bug** with the [bug report form](https://github.com/kerf-dev/kerf/issues/new/choose). Security
  problems go through [SECURITY.md](SECURITY.md), not a public issue.

## Before you start

- **Bugs and small fixes:** open a pull request, or an issue first if you are unsure it is a bug.
- **Features and API changes:** open an issue to agree on the design first. The CRDs (`api/v1alpha1`),
  the hub API (`api/openapi/hub.yaml`), the metric names (`docs/contracts/`) and the database schema
  (`db/migrations/`) are contracts between components; changes to them need a short design note.
- [docs/architecture.md](docs/architecture.md) explains how the components fit together and why. Read it before
  changing behaviour it covers. The [documentation index](docs/README.md) shows where everything lives.

## Run it locally

**The web UI in one minute.** Needs Node.js 22 or later. The UI runs against a mock API with a sample fleet,
so no cluster is needed:

```bash
make web-dev       # http://localhost:5173
```

**The Go code.** Needs Go (the version in `go.mod`):

```bash
make test          # unit tests
make ci            # build, vet, lint, tests, generated files, OpenAPI lint (what CI runs)
```

**The whole system.** Needs Docker, `kubectl`, Helm 4 and k3d 5. This creates three local k3d clusters (a hub
and two workload clusters) with metrics, Argo CD and sample workloads, then deploys Kerf into them:

```bash
make dev-up        # about 3 minutes the first time
make deploy-hub deploy-pricing deploy-agent deploy-webhook deploy-web
make dev-status    # URLs and how to sign in
```

[dev/README.md](dev/README.md) explains the local environment and lists the local URLs. The dev credentials
in `*values-dev.yaml`, `values-ro-spoke-*.yaml` and `dev/` are for the local k3d clusters only.

## Checks

```bash
make ci            # Go build, vet, lint, tests, generated files, OpenAPI lint (what CI runs)
make web-verify    # web UI: API types, lint, typecheck, unit and browser tests, build
make charts-lint   # every Helm chart: lint, render, kubeconform
make e2e           # the end-to-end suite against the local clusters (about 25 minutes)
```

Pull requests must pass CI. Add or update tests with your change (table-driven tests for Go).

CI runs only what a change needs: a `changes` job (`.github/scripts/changes.sh`) works out which areas the
pull request touches (Go, web, OpenAPI, charts, images), and the other jobs finish in seconds, with success,
when their area is untouched. The five required checks therefore always report, so a docs-only pull request
is green within a minute. Pull requests build the images for `linux/amd64`; `main` and release tags build
`linux/amd64` and `linux/arm64`. A new push to a pull request cancels the run in progress. CI also fails
if internal development notes are committed.
Run `make generate` after changing `api/` and commit the result.
CodeQL runs as its own workflow (`.github/workflows/codeql.yaml`) and scans only the languages a pull request changes
(Go, JavaScript/TypeScript, Python, GitHub Actions; `.github/scripts/codeql-languages.sh` decides). Pushes to `main` and
a weekly run scan all of them. It is not a required check.

CI also fails if a tracked file contains an internal tracking label (`WO-` or `D-0` followed by digits) or is larger than 5 MB: describe the change in words, and keep build outputs out of the tree.

## Pull request titles

Every pull request is squash-merged, and the commit on `main` keeps the pull request title. The release notes
are built from those titles, grouped by type under Features, Bug fixes, Documentation and so on, so the title is
what users read. A check (`pr title (conventional commits)`) fails until it follows
[Conventional Commits](https://www.conventionalcommits.org/):

```
type(scope)!: summary
```

- **type** is one of `feat` (a new capability), `fix` (a bug fix), `perf`, `docs`, `ci`, `build`, `refactor`,
  `test`, `chore` or `revert`.
- **scope** is optional, and when given one of `hub`, `agent`, `webhook`, `pricing`, `web`, `chart`, `api`, `e2e`,
  `site`, `release`, `deps` or `security`. `security` and `deps` get their own groups in the notes, whatever the type.
- **`!`** after the type or scope marks a breaking change (a changed CRD, API, metric or chart value). It is listed
  first, under Breaking changes. A `BREAKING CHANGE:` footer in the description does the same.
- **summary** starts with a lowercase letter, has no trailing period, and describes the change for a user. The whole
  title is at most 72 characters.

Examples:

- `feat(hub): add a savings export to the workloads page`
- `fix(agent): roll back when the resized pod never becomes ready`
- `docs: explain the maintenance windows`
- `feat(api)!: rename the policy field targetRef to workloadRef`

Fix a title by editing it on the pull request; the check runs again. Try one locally with
`.github/scripts/pr-title.sh "fix(hub): handle an empty metrics response"`. Dependabot's titles
(`chore(deps): bump ...`) already fit.

## Pull requests

- One topic per pull request, with a description of what changes and how you tested it. The pull request
  template has a short checklist.
- Give it a [Conventional Commit title](#pull-request-titles): the release notes are grouped by it. A check
  fails the pull request until the title fits.
- User-visible changes get a line under `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md).
- Go: `log/slog` for logging, controller-runtime for Kubernetes code.
- Writing docs or UI text? [docs/branding/brand.md](docs/branding/brand.md) has the voice and the words we use.

If a pull request waits for review longer than a week, a friendly ping on it is welcome.

## Community

Be kind and constructive; this project follows the [code of conduct](CODE_OF_CONDUCT.md).

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE).
