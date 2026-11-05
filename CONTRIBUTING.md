# Contributing to Kerf

Thanks for helping. Bug reports, fixes, docs and features are all welcome.

## Before you start

- **Bugs and small fixes:** open a pull request, or an issue first if you are unsure it is a bug.
- **Features and API changes:** open an issue to agree on the design first. The CRDs (`api/v1alpha1`),
  the hub API (`api/openapi/hub.yaml`), the metric names (`docs/contracts/`) and the database schema
  (`db/migrations/`) are contracts between components; changes to them need a short design note.
- Architecture decisions are recorded in [DECISIONS.md](DECISIONS.md). Read the relevant ones before
  changing behaviour they cover.
- Security problems: see [SECURITY.md](SECURITY.md), not a public issue.

## Development setup

You need Docker, Go (the version in `go.mod`), Node.js 22 or later, `kubectl`, Helm 4 and k3d 5.

```bash
make dev-up        # three local k3d clusters (hub + two workload clusters) with metrics and sample workloads
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

- One topic per pull request, with a description of what changes and how you tested it.
- Label it (`bug`, `enhancement`, `documentation`, `breaking-change`) so the release notes sort it.
- User-visible changes get a line under `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md).
- Go: `log/slog` for logging, controller-runtime for Kubernetes code.

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE).
