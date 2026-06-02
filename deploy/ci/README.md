# CI workflow (parked)

`github-actions-ci.yml` is the GitHub Actions pipeline for MX Sentinel (build / vet /
test / golangci-lint / schema-lint). It lives here rather than under
`.github/workflows/` because it was pushed with an OAuth token that lacks the `workflow`
scope, and GitHub refuses to create or update workflow files without it.

## To activate it

Pick one:

1. **Grant the scope, then move it** (CLI):
   ```bash
   gh auth refresh -h github.com -u zezekim -s workflow
   git mv deploy/ci/github-actions-ci.yml .github/workflows/ci.yml
   git commit -m "ci: enable GitHub Actions workflow"
   git push
   ```

2. **Add it via the GitHub web UI** — create `.github/workflows/ci.yml` and paste the
   contents of this file (the web editor has the necessary scope).

Once it's under `.github/workflows/`, it runs on push to `main` and on pull requests.
