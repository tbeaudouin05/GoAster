# GoAster fork

Prepare and verify a user's public fork of `tbeaudouin05/GoAster` and its two
standard branches. Stop after the fork and branches are verified. Do not
commit, push, reset, deploy, create a pull request, or change application
files.

Use the canonical [github-auth skill](../github-auth/SKILL.md) and its
[authorization rules](../github-auth/AUTH.md) before any GitHub access. That
skill owns generic host selection, authentication, secret handling,
read-only discovery, and GitHub write safety. This skill adds the GoAster
fork-specific procedure below; do not create a second authentication flow.

## Boundaries

- Never write to the upstream repository's contents, refs, settings, or
  access. GitHub's upstream forks endpoint requests creation of a separate
  fork; it does not modify upstream contents. Use it only for the explicitly
  approved fork. Every branch write must target that fork's exact
  `owner/name`.
- Forks of public repositories are public. Tell the user before asking for
  approval. Do not offer a private fork; if private visibility is required,
  stop without creating one.
- Never overwrite or delete a fork or branch, force-push, use
  force-with-lease, or reset. Never blindly retry an uncertain write.
- The fixed branches are `goaster-customizations` for persistent user changes
  and `goaster-contributions` for contribution manifests. Do not ask the user
  to choose alternate branch names.
- Do not use a local remote as proof of GitHub identity. A local `upstream`
  remote may be fetch-only; never push to it.

## Read-only preflight

1. Follow `github-auth` to verify the host and authenticated login before API
   requests and after any authentication change. Confirm the exact upstream
   repository through GitHub, not a local remote. Require
   `tbeaudouin05/GoAster`, public visibility, `fork: false`, and a non-empty
   default branch. Read that branch's exact ref and commit SHA; verify the
   returned full ref name and that it points to a commit. Record the branch
   and SHA. If the repository or ref is missing, renamed, empty, inaccessible,
   or ambiguous, stop and report the blocker. A 404 is not proof that a
   repository or ref is absent.

   For repository identity, use a bounded GitHub API query such as:

   ```sh
   gh api --hostname github.com repos/tbeaudouin05/GoAster \
     --jq '{full_name,owner:.owner.login,visibility,fork,default_branch}'
   ```

2. Tell the user that the fork will be public. Ask for the exact personal
   account, fork name, and local checkout path if not already supplied. The
   requested account and fork owner must equal the verified authenticated
   login. Validate the account and fork name as single GitHub path components.
   Keep the branch names fixed as listed above.

3. Inspect the exact `owner/name` destination before writing. A 404 alone does
   not prove the fork is absent because it can hide an inaccessible private
   repository. Confirm absence using an authenticated account interface with
   sufficient access to see the account's repositories. If absence cannot be
   established, stop. Treat transport, permission, rate-limit, and malformed
   response errors as blockers.

   If the fork exists, verify its exact owner/name, public visibility,
   `fork: true`, and parent or source identity `tbeaudouin05/GoAster`. Require
   a non-empty default branch and read its exact ref and commit SHA. If any
   identity or ref is unverifiable, stop without changing it.

4. If the fork already exists, inspect both exact standard branch refs. For
   each missing branch, propose the existing fork's default-branch commit as
   its base. For each existing branch, record its current SHA and ask the user
   explicitly whether to reuse that exact branch as-is. Do not offer another
   name. Do not overwrite an existing branch or report completion without an
   explicit reuse choice for every existing branch. A reused branch need not
   equal the default-branch commit.

   If the fork is absent, do not query branches in it. Propose the verified
   upstream default branch and commit as the base for both standard branches.
   Explain that the branch refs can be inspected only after the fork is
   created and ready. An uncertain destination or missing upstream ref is a
   blocker, not a reason to guess.

Use exact-ref lookups. Check the returned full `refs/heads/...` name and commit
SHA rather than accepting a prefix match. URL-encode path components when
needed, quote values, and never pass untrusted text through `eval`.

For either fixed branch, an exact lookup can use:

```sh
gh api --hostname github.com \
  "repos/$ACCOUNT/$FORK_NAME/git/ref/heads/$BRANCH" \
  --jq '{ref,sha:.object.sha,type:.object.type}'
```

Set `BRANCH` only to `goaster-customizations` or `goaster-contributions`.

## Approval

Before any creation, present one exact plan containing:

- the `github.com` host and verified authenticated account;
- the public fork `owner/name`, whether it already exists, and its verified
  parent/source and default branch;
- the upstream default branch and exact SHA used as the new-fork base, or the
  existing fork default branch and SHA used as branch bases;
- both standard branch refs, whether each was inspected, its current or
  proposed SHA, and the precise intended write or reuse-as-is action; and
- the exact local checkout path and any requested local setup.

Obtain explicit approval for that plan. An existing branch requires the
user's explicit choice to reuse that exact ref at its observed SHA. General
approval or a “go ahead” with unspecified values is not enough. If the
approved account, fork, visibility, branch, default branch, base SHA, reuse
SHA, checkout path, or intended action changes, stop and obtain a new choice
or approval. Immediately before every creation, recheck that the active login
and target still match the approved plan.

## Create and verify the fork

If the fork is absent, recheck the exact destination immediately before
creation. If it has appeared or its state is uncertain, do not submit a
creation request; inspect it and obtain approval for any changed plan.
Otherwise request creation of the separate fork once. Creation is
asynchronous: poll read-only for the exact destination for at most 60 seconds
using bounded requests. Inspect the destination within that same bound if the
request fails or times out. Never automatically repeat the creation request.
For the approved, validated fork name, the API request is:

```sh
gh api --hostname github.com --method POST \
  repos/tbeaudouin05/GoAster/forks \
  --raw-field "name=$FORK_NAME"
```

Continue only after the fork is demonstrably ready and its exact owner/name,
public visibility, fork status, parent/source, default branch, and default-ref
commit are verified. Query its default ref only after the repository is
visible. If it remains absent, incomplete, inaccessible, different, or
uncertain, report the observed state and stop; do not delete or repair it.

For a newly created fork, its default branch and commit must equal the upstream
branch and SHA approved in preflight. If either differs, stop without creating
branches. A fresh upstream preflight that confirms the new values requires a
new plan and approval before branch creation; do not treat a mismatching new
fork as pre-existing. For a fork that existed at preflight, reread its
identity, default branch, and commit immediately before branch creation. If
any value changed, repeat the appropriate preflight and obtain new approval.

## Create or reuse the standard branches

After verifying the fork, inspect each exact standard ref again immediately
before any branch write. For a newly created fork, this is the first lookup of
those refs. If a branch now exists, show its SHA and ask the user to choose
reuse as-is; do not create or overwrite it. If the ref is confirmed missing,
create only that standard branch at its approved base SHA in the approved
fork. Recheck the authenticated login and fork identity immediately before
each branch creation. For the confirmed-missing approved ref, create it at the
approved base SHA with:

```sh
gh api --hostname github.com --method POST \
  "repos/$ACCOUNT/$FORK_NAME/git/refs" \
  --raw-field "ref=refs/heads/$BRANCH" \
  --raw-field "sha=$BASE_SHA"
```

If a branch creation fails or times out, inspect the exact ref read-only and
never blindly retry. If it exists, show its SHA and ask for an explicit choice
to reuse that exact ref. If its state is uncertain, stop and report the
observed state. Do not claim an uncertain write succeeded.

## Local checkout and handoff

If local setup is part of the approved plan, use only the approved checkout
path and preserve existing user changes. Set `origin` to the user's verified
fork, check out `goaster-customizations`, and keep any `upstream` remote
fetch-only. Check fetch and push destinations before source work. Do not push
upstream.

Re-read the upstream identity, fork owner/name, public visibility,
parent/source, default branch and commit, and both exact standard refs. Each
new branch must resolve to its approved base SHA. Each reused branch must
resolve to the SHA the user explicitly chose; if it changed, ask for a new
choice. If verification fails, report the observed state instead of claiming
completion.

Return a non-secret handoff with the verified account, fork URL and
visibility, default branch, both branch names and SHAs, whether each branch
was created or explicitly reused, upstream branch and SHA, and approved
checkout path. For example:

```text
github_host=github.com
account=<verified login>
fork=<owner/name>
visibility=public
branch=goaster-customizations
branch_sha=<verified customization branch commit SHA>
branch_status=<created|reused_as_is>
manifest_branch=goaster-contributions
manifest_branch_sha=<verified manifest branch commit SHA>
manifest_branch_status=<created|reused_as_is>
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
