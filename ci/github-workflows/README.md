# GitHub Actions workflows (not active yet)

| Workflow | Runs on | Does |
|---|---|---|
| `ci.yaml` | every pull request and push to `main`; called by `release.yaml` | Go build, vet, lint, test, generated files; OpenAPI lint; web UI checks and build; chart lint and packaging; both image platforms built (not pushed) |
| `release.yaml` | a pushed tag `vX.Y.Z` or `vX.Y.Z-rc.N` | CI, then images and charts to `ghcr.io/<owner>/resource-optimizer`, cosign signatures, a GitHub Release with notes (`docs/release.md`, "Releasing on GitHub") |
| `e2e.yaml` | nightly, on demand, and on pull requests that change the e2e suite | three k3d clusters, the full end-to-end suite |

They belong in `.github/workflows/`. They are parked here because the Git credential on the build machine
lacks GitHub's `workflow` scope, and GitHub rejects any push that adds or changes a workflow file without it.
To activate them, from a checkout of `main` with a credential that has the `workflow` scope:

```bash
mkdir -p .github/workflows && git mv ci/github-workflows/*.yaml .github/workflows/ && git commit -m "Activate GitHub Actions workflows" && git push
```

`.github/dependabot.yml` and `.github/release.yml` (release-note categories) are already active.
