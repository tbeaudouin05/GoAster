---
name: github-fork-starter
description: Safely guide creation of a user-approved fork of tbeaudouin05/GoAster with the standard goaster-customizations and goaster-contributions branches without changing upstream contents. Use an existing authenticated gh session or the standard foreground GitHub CLI web login when needed.
---

# GitHub fork starter

Use the canonical [github-cli skill](../github-cli/SKILL.md) for authentication,
host checks, read-only discovery, and safe GitHub actions. This workflow is the
fork-specific procedure built on that shared contract.

Prepare a user's fork of `tbeaudouin05/GoAster` for future development when
asked in ordinary language. Verify the standard `goaster-customizations` and
`goaster-contributions` branches. End after both branches are verified; do not
commit, push, reset, deploy, or change application files.

## Boundaries and authentication

- Never write to the upstream repository's contents, refs, settings, or
  access. All later GoAster work belongs in the user's own approved fork.
  GitHub's `POST /repos/tbeaudouin05/GoAster/forks` requests creation
  of a separate fork; it does not modify upstream contents. This specific
  request is allowed only for the explicitly approved fork. All branch writes
  must target that fork's exact `owner/name`.
- Never overwrite or delete a fork or branch, force push, use
  force-with-lease, or reset. Never blindly retry an uncertain write.
- Use GitHub's web interface or an already authenticated `gh` client. This
  procedure targets a personal account, so the fork owner must be the
  authenticated login. Use `github.com` explicitly; do not infer a host,
  account, fork name, or branch from local Git configuration.
- Keep using a working `gh` session that is already authenticated to
  `github.com`.
- If login is needed, run the standard foreground web flow:

  ```sh
  gh auth login --hostname github.com --web
  ```

  If `gh` displays an official GitHub URL and one-time code, relay only that
  URL and code to the user. The user completes authentication in the browser;
  do not enter the code on the user's behalf or ask them to send it back.
- Before any GitHub API requests, and again after any login, verify the session
  with `gh auth status --hostname github.com` and
  `gh api --hostname github.com user --jq '.login'`. Require the verified login
  to match the user's requested account; if it does not, stop and have the user
  switch credentials locally. Report only the verified login and host. If
  authentication or a suitable interface is not available, stop rather than
  guessing.
- Never ask the user to paste a password or token into chat; never read
  credential files or put secrets in command arguments, URLs, logs, or
  reports.

## Read-only preflight

1. Verify the fixed upstream through GitHub, not a local remote:

   ```sh
   gh api --hostname github.com repos/tbeaudouin05/GoAster --jq '{full_name,owner: .owner.login,visibility,fork,default_branch}'
   ```

   Require the exact upstream identity, public visibility, `fork: false`, and
   a non-empty default branch. Read that branch's exact ref and commit SHA;
   require a commit and record both values. If the repository or ref is
   inaccessible, missing, renamed, empty, or otherwise unverifiable, report a
   blocker and stop. A 404 can also conceal an inaccessible repository: do
   not assert that it exists or is absent, change upstream, or substitute
   another target based on an assumption.

2. Tell the user **forks of public repositories are public** before asking for
   confirmation. Do not offer a private fork; if private visibility is
   required, stop without creating one. Ask for the exact personal account
   and fork name if not already supplied. The account must match the
   authenticated login. Preserve the user's values and validate the account
   and fork name as single GitHub path components. Branch names are fixed:
   `goaster-customizations` for persistent user changes and
   `goaster-contributions` for contribution manifests. Do not ask the user to
   choose an arbitrary branch. Do not treat a general “go ahead” as approval
   for unspecified values.

3. Inspect `repos/$ACCOUNT/$FORK_NAME` before writing. A 404 alone does not
   prove absence: it can hide an inaccessible private repository. Confirm
   absence through an authenticated account interface with sufficient access
   to see the account's repositories; if that cannot be established, stop.
   Treat transport, permission, rate-limit, and malformed-response errors as
   blockers. If the repository exists, verify its exact owner/name, public
   visibility, `fork: true`, and parent or source identity
   `tbeaudouin05/GoAster`. Require a default branch and its exact commit ref;
   otherwise stop without changing it.

4. If the fork exists, inspect both exact standard branches in it. For each
   missing branch, use the fork's default branch and commit as the proposed
   base. For each existing branch, show its current commit and ask the user
   explicitly whether to reuse that exact branch as-is. Do not offer an
   alternate branch name. Never overwrite an existing branch or report the
   workflow complete without an explicit reuse choice for every existing
   branch. A reused branch need not equal the default-branch commit. Treat an
   ambiguous ref lookup as a blocker.

   If the fork is absent, **do not query any branch in it**. Propose the
   upstream default branch and commit recorded in step 1 as the base for both
   standard branches. Explain that the branches can be inspected only after
   creation and readiness.
   A missing upstream ref or an uncertain candidate-fork state is a blocker,
   not a reason to guess a base.

Use exact-ref lookups and check the returned full `refs/heads/...` name and
commit SHA, not a prefix match. URL-encode path components when needed, and
quote values; never pass untrusted text through `eval`.

## Approval

Before any creation, present the exact upstream, authenticated account,
fork `owner/name`, **public** visibility, both standard branch refs, and
proposed default branch and base SHA. State whether the fork exists and,
separately for each branch, whether it has been inspected, its current or
proposed SHA, and the intended write. Create the separate fork only if absent,
then create each standard branch in that fork only if missing. For every
existing branch, describe reuse as-is with its observed SHA and make no branch
write. Obtain explicit approval of that exact plan; reuse also requires the
user's explicit choice. If any approved identity, visibility, branch, default
branch, base SHA, or reuse SHA changes, stop and obtain a new choice or
approval. Immediately before each creation request, recheck that the active
GitHub login is still the approved account; if it changed or cannot be
verified, stop.

## Create and verify the fork

For an absent fork, recheck the exact destination before creation. If it has
appeared or its state is uncertain, do not submit the request; inspect it and
obtain a new approval for any changed plan. Otherwise use the GitHub web flow
or the direct API, which does not change local remotes:

```sh
gh api --hostname github.com --method POST repos/tbeaudouin05/GoAster/forks --raw-field "name=$FORK_NAME"
```

Submit at most one fork-creation request. Creation is asynchronous: poll
read-only for the exact destination for at most 60 seconds with bounded
individual requests. Query its default ref only after the repository is
visible. If the request failed or timed out, inspect the destination within
that same bound; never repeat the POST automatically.
Continue only after the fork is demonstrably ready and its exact owner/name,
public visibility, fork status, parent or source, default branch, and default
ref commit are verified. If it remains absent, incomplete, inaccessible,
different, or otherwise uncertain, stop and report the observed state; do not
delete or repair it automatically.

For a newly created fork, its default branch and commit must equal the
upstream branch and SHA approved in preflight. If either differs, stop and
leave the fork unchanged. Only if a fresh upstream preflight confirms that
both values now match the fork may a new plan use that upstream base, with
fresh approval before branch creation. Do not bypass this check by treating the
newly created, mismatching fork as a pre-existing fork. For a fork that
already existed at preflight, do not recreate it. In either case, re-read its
identity, default branch, and commit immediately before branch creation; if
they differ from the approved plan, stop and repeat the appropriate preflight
and approval, including the upstream-match check for a newly created fork.

## Create or reuse the standard branches

Once the fork is verified and ready, inspect each exact standard branch again
immediately before any branch write. This is the first target-branch lookup in
an initially absent fork. If a branch now exists, show its SHA and ask the user
to choose reuse as-is; do not offer a different name and do not create or
overwrite it. If the ref is confirmed missing, create that standard branch at
the approved `BASE_SHA` in the fork. Set `BRANCH` only to
`goaster-customizations` or `goaster-contributions`, for an approved missing
ref:

```sh
gh api --hostname github.com --method POST "repos/$ACCOUNT/$FORK_NAME/git/refs" \
  --raw-field "ref=refs/heads/$BRANCH" \
  --raw-field "sha=$BASE_SHA"
```

If creation fails or times out, inspect the exact ref; never overwrite or
blindly retry it. If a ref exists, show its SHA and ask for an explicit choice
to reuse that exact standard branch. If its state is uncertain, stop and report
that state. Do not claim an uncertain write succeeded. Recheck the login and
fork identity immediately before each branch creation.

## Verify and hand off

Re-read the upstream identity, the fork's owner/name, public visibility,
parent/source and default branch, and both exact standard refs. Each newly
created branch must resolve to the approved base SHA; each reused branch must
resolve to the SHA the user explicitly chose to reuse. If a reused ref
changed, ask for a new choice. If verification fails, report the observed
state rather than claiming completion. Report the verified account, fork URL,
public visibility, default branch, both standard branch names, and their
commit IDs. Later commits or pushes require a separate request and a fresh
destination check; this workflow does not modify upstream contents.
