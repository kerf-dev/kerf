# Pending GitHub Actions workflows

These workflows belong in `.github/workflows/`. They are parked here because the Git credential on the
build machine lacks GitHub's `workflow` scope, and GitHub rejects any push that adds or changes a workflow file
without it. To activate them, with a token that has the `workflow` scope:

    git mv ci/github-workflows/*.yaml .github/workflows/ && git commit -m "Activate CI workflows" && git push
