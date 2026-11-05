# Going public: checklist (WO-26)

What is done in the repository, and what is left before and after the repository's visibility becomes
public. Audit of 2026-11-05 on `main`: no secrets, keys or tokens in tracked files or in history; dev
credentials exist only in clearly marked local-k3d files.

## Done (WO-26)

- [x] `LICENSE` (Apache-2.0), `NOTICE` (Geist fonts, base images), `SECURITY.md`, `CONTRIBUTING.md`,
  `CODE_OF_CONDUCT.md`, issue and pull-request templates, `.github/dependabot.yml`
- [x] README written for outside users; `CHANGELOG.md` (Keep a Changelog)
- [x] Release pipeline: images and charts to GHCR, cosign signatures, GitHub Release with notes
  (`ci/github-workflows/release.yaml`, docs/release.md "Releasing on GitHub")
- [x] CI for pull requests: Go, OpenAPI, web UI, charts, both image platforms
- [x] Released charts default to the GHCR images of their release
- [x] Version in every binary's startup log (`-X main.version`)
- [x] The 12-digit AWS account ID in the synthetic data replaced with the placeholder `123456789012`
- [x] `ghcr.io/oz/…` and `github.com/oz/…` in the UI mocks replaced; personal machine paths removed from docs

## Before making it public (owner decisions)

1. **Permission to publish.** The code was written for an employer: confirm it may be open-sourced, and
   who the copyright holder is (`NOTICE` says "The resource-optimizer Authors"). Confirm Apache-2.0.
2. **CRD API group.** `resource-optimizer.oz.io` uses a domain the project does not own, and `oz` is the
   company abbreviation. Renaming it later breaks every installed policy, so do it before the first public
   release. It touches about 115 places in 53 files (Go types, CRDs, charts, agent, webhook, web, e2e, docs)
   and the `resource-optimizer.oz.io/*` labels and annotations. Pick a domain you control.
3. **History.** The account ID above was in history since the first synthetic-data commit. If it is a real
   account, rewrite history before publishing (`git filter-repo --replace-text`, then force-push every
   branch and tag); otherwise nothing to do. Two old commits also contain a personal home-directory path.
4. **Branches.** 43 `wo-*` work branches are on GitHub and would become public. 42 are merged into `main`;
   `wo-12b-jvm-format` is not (check it, then delete it or merge it). Delete the merged ones:
   ```bash
   git fetch --prune origin
   git branch -r --merged origin/main | grep 'origin/wo-' | sed 's|origin/||' | xargs -n 20 git push origin --delete
   ```
5. **Activate the workflows** with a credential that has the `workflow` scope (`ci/github-workflows/README.md`),
   then cut `v0.1.0-rc.1` to test the pipeline end to end while the repository is still private.
6. **Optional tidy-up:**
   - the agent-orchestration files (`CLAUDE.md`, `.claude/state/`, `docs/work-orders/`, `docs/handover/`,
     `docs/STATUS.md`): keep them as design history, or move them under `docs/history/`;
   - rename the dev values `deploy/charts/agent/values-ro-spoke-{1,2}.yaml` to `values-dev-ro-spoke-{1,2}.yaml`;
   - check the mock workload names in `web/src/mocks/` and `tools/synth/` (payments, ledger, aml-screening…)
     and the screenshots in `docs/screenshots/`: they should not mirror real internal service names.

## Right after making it public

- Settings → Code security: enable **private vulnerability reporting** (SECURITY.md relies on it),
  Dependabot alerts and security updates, secret scanning and push protection.
- Settings → Branches: protect `main` (pull request required, the `ci` checks required, no force-push).
- Packages: set the 11 GHCR packages (`resource-optimizer/{hub,pricing,agent,webhook,web}` and
  `resource-optimizer/charts/*`) to **public**; link them to the repository if GitHub did not.
- Release `v0.1.0`: move the `Unreleased` notes in `CHANGELOG.md` under `## [0.1.0] - <date>`, push, tag.
- Optional: list the chart on [Artifact Hub](https://artifacthub.io/) (it reads OCI charts from GHCR) and
  add topics (kubernetes, finops, rightsizing, eks) to the repository.
