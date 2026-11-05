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
- Architecture decisions are recorded in [DECISIONS.md](DECISIONS.md). Read the relevant ones before
  changing behaviour they cover. The [documentation index](docs/README.md) shows where everything lives.

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

[dev/README.md](dev/README.md) explains the local environment; [docs/STATUS.md](docs/STATUS.md) lists the
local URLs. The dev credentials in `*values-dev.yaml`, `values-ro-spoke-*.yaml` and `dev/` are for the
local k3d clusters only.

## Checks

```bash
make ci            # Go build, vet, lint, tests, generated files, OpenAPI lint (what CI runs)
make web-verify    # web UI: API types, lint, typecheck, unit and browser tests, build
make charts-lint   # every Helm chart: lint, render, kubeconform
make e2e           # the end-to-end suite against the local clusters (about 25 minutes)
```

Pull requests must pass CI. Add or update tests with your change (table-driven tests for Go).
Run `make generate` after changing `api/` and commit the result.

## Pull requests

- One topic per pull request, with a description of what changes and how you tested it. The pull request
  template has a short checklist.
- Label it (`bug`, `enhancement`, `documentation`, `breaking-change`) so the release notes sort it.
- User-visible changes get a line under `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md).
- Go: `log/slog` for logging, controller-runtime for Kubernetes code.
- Writing docs or UI text? [docs/branding/brand.md](docs/branding/brand.md) has the voice and the words we use.

If a pull request waits for review longer than a week, a friendly ping on it is welcome.

## Community

Be kind and constructive; this project follows the [code of conduct](CODE_OF_CONDUCT.md).

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE).
