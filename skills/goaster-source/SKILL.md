# GoAster source customization

Use this stage after `goaster-fork` and before build/test. It supports
arbitrary user customization in the approved fork while keeping the source
identity explicit and reproducible.

## Source preflight

1. Confirm the approved `github.com` account, fork owner/name, exact
   `goaster-customizations` branch, and checkout path. Verify `origin` points
   to that fork and any `upstream` remote is read-only. Do not infer identity
   from a directory name or use another branch for persistent changes.
2. Inspect the exact branch and current full SHA through GitHub and local Git.
   Record the source SHA used for this stage. If local files are dirty, show
   the status and preserve it; do not reset or stash without explicit approval.
3. Review the requested customization in the fork. Any Go code, Markdown
   skill, package asset, documentation, configuration, or test may be changed
   when the user requests it. Keep generated output and secrets outside the
   checkout unless the user explicitly needs a non-secret fixture.
4. If upstream comparison is useful, fetch or inspect it read-only. Do not
   auto-merge, rebase, or replace the user's fork. A request to synchronize
   from upstream is a separate reviewed change.

When skill assets are changed, keep every intended starter in the existing
`packaging/assets.sh` allowlist and retain exactly `META.md`, `SKILL.md`, and
`AUTH.md` for each starter. The package test is the source of truth for the
allowlist and local Markdown reference checks.

## Handoff

Before build/test, report:

```text
source_host=github.com
source_repo=<approved fork owner/name>
source_branch=goaster-customizations
source_sha=<exact full SHA>
source_path=<absolute checkout path>
working_tree=<clean or described user changes>
```

Do not claim the source is reproducible if the SHA, path, or fork identity is
unknown. A changed source SHA or checkout path invalidates build, backup, and
deployment approvals; return to the affected preflight stage.
