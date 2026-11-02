# GoAster fork

This is the first stage of the user-owned GoAster workflow. It prepares a
personal fork and the two standard branches, then hands exact identity data to
the source stage. `goaster-customizations` is the persistent branch for user
changes; `goaster-contributions` is reserved for contribution manifests. Use
the canonical [github-cli skill](../github-cli/SKILL.md) and the detailed
[github-fork-starter skill](../github-fork-starter/SKILL.md); do not invent a
second GitHub authentication flow.

## Fork and branch procedure

1. Verify `github.com`, the authenticated login, and the fixed upstream
   `tbeaudouin05/GoAster` through read-only GitHub queries. Require the exact
   public, non-fork repository, its non-empty default branch, and the exact
   default-branch commit SHA.
2. Tell the user that a fork of a public repository is public. Ask for the
   exact personal fork name and local checkout path. The fork owner must equal
   the verified authenticated login. The branch names are fixed: use
   `goaster-customizations` and `goaster-contributions`; do not ask the user
   to choose an arbitrary dedicated branch.
3. Inspect the exact fork and both standard branches before any branch write.
   A missing or ambiguous response is a blocker, not permission to guess. An
   existing branch may be reused only when the user explicitly chooses reuse
   as-is; never overwrite it or substitute a differently named branch.
4. Present one approval containing the account, public `owner/name`, upstream
   default branch and SHA, both exact branch refs and their current or proposed
   commit SHAs, checkout path, and each precise fork or branch write. Create
   only an absent fork or absent standard branch in the user's fork. Recheck
   the login and target immediately before each write.
5. Verify the resulting fork and both exact branches by full ref name and
   commit SHA. Record whether each branch was newly created or explicitly
   reused.

For a local checkout, set `origin` to the user's fork, check out
`goaster-customizations`, and keep any `upstream` remote fetch-only. Check both
fetch and push destinations before source work.
Do not push upstream and do not use a local remote as proof of GitHub identity.

## Handoff

Return a non-secret handoff containing:

```text
github_host=github.com
account=<verified login>
fork=<owner/name>
branch=goaster-customizations
branch_sha=<verified customization branch commit SHA>
manifest_branch=goaster-contributions
manifest_branch_sha=<verified manifest branch commit SHA>
upstream=tbeaudouin05/GoAster
upstream_default_branch=<verified branch>
upstream_default_sha=<verified SHA>
checkout_path=<approved local path>
```

The next stage may customize only the approved fork checkout on
`goaster-customizations`; `goaster-contributions` is reserved for manifests.
Any change to the account, fork, either branch, base SHA, checkout path, or
GitHub host invalidates the approval and requires a fresh read-only preflight
and approval.
