---
name: github-fork-starter
description: Safely guide creation of a user-approved fork of tbeaudouin05/GoAster and a dedicated branch without changing upstream contents. Use an existing authenticated gh session; if login is needed, use GoAster's allowlisted secure GitHub handoff in the paired private Telegram chat.
---

# GitHub fork starter

Prepare a user's fork of `tbeaudouin05/GoAster` for future development when
asked in ordinary language. End after a dedicated branch is verified; do not
commit, push, reset, deploy, or change application files.

## Boundaries and authentication

- Never write to the upstream repository's contents, refs, settings, or
  access. GitHub's `POST /repos/tbeaudouin05/GoAster/forks` requests creation
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
  `github.com`. If its authenticated login is not the account the user wants,
  stop.
- If login is needed in Telegram, first have the paired user send the exact
  `/github-login` command in the paired **private** chat. This is the explicit,
  short-lived authorization for one handoff. Then ask GoAster to continue the
  GitHub workflow so its fixed `auth_handoff` tool can request provider
  `github`; the tool accepts no command, token, password, device code, or
  arbitrary provider. The agent sends GitHub's short-lived verification URL and
  code directly to that private chat through an out-of-band Telegram event.
  The code never enters model context or history, tool output, command
  arguments, logs, or reports. Never send a token.
- The handoff supports cancellation and expiry. Send `/github-login cancel` in
  the same paired private chat to cancel an active handoff. Stop if the user
  cancels or the code expires; do not refresh, reissue, or retry without a new
  `/github-login` authorization. After completion, the agent stores the
  provider credential through `gh`'s GitHub credential path and verifies the
  account with `gh api --hostname github.com user --jq '.login'` before
  reporting success. Report only the host and verified login.
- The agent must be configured with the non-secret public OAuth client ID in
  `GOASTER_GITHUB_CLIENT_ID`; no GitHub client secret is used for this device
  flow. The requested OAuth scopes are `repo read:org gist`, matching the
  minimum scope set documented for `gh auth login --with-token`. If the
  provider is not configured, stop and report that blocker.
- To obtain the client ID, create an OAuth App in GitHub under **Settings →
  Developer settings → OAuth Apps → New OAuth App**, enable **Device Flow** in
  the app settings, and copy its public **Client ID** into
  `GOASTER_GITHUB_CLIENT_ID`. Device Flow needs no GitHub client secret.
- The agent and Telegram adapter must share the deployment-only
  `GOASTER_GITHUB_AUTH_CONTROL_SECRET` through a root-controlled service
  `EnvironmentFile`. The agent requires that secret on every loopback `/chat`
  and GitHub control request; an arbitrary `chat_id` is never sufficient. Do
  not put the secret in command arguments, chat, logs, or reports.
- Generate `GOASTER_GITHUB_AUTH_CONTROL_SECRET` in the operator's secure local
  terminal with `openssl rand -hex 32` (or an equivalent cryptographically
  secure random generator). Keep it unique to this deployment and configure
  the exact same value in both services' root-controlled `EnvironmentFile`;
  never reuse it across deployments or commit it.
- Rollback removes the newly-installed account by its verified login before
  restoring a prior snapshot, including inactive `gh` accounts. If removal or
  restoration is uncertain, stop with the documented manual reconciliation
  state and do not report the handoff as successful.
- GoAster's existing `remote_login` is an OpenAI-specific login handoff and
  remains unchanged. Do not emulate either handoff with shell background jobs,
  captured `gh auth login` output, `tmux`, `expect`, pipes, or another
  workaround.
- Never ask the user to paste a password, token, or device code into chat;
  never read credential files or put secrets in command arguments, URLs,
  logs, or reports.

This is not a credential sandbox. The root-run GoAster shell is intentionally
unrestricted and can access locally stored `gh` credentials. Do not claim that
the installed GitHub token is inaccessible to the agent or model; only the
device instructions and credentials are kept out of normal model messages,
conversation history, and adapter progress output.

For `gh`, check `gh auth status --hostname github.com` and
`gh api --hostname github.com user --jq '.login'`. Report only the login and
host. If the login is not the account the user wants, stop and have the user
switch credentials locally. If authentication or a suitable interface is not
available, stop rather than guessing.

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
   required, stop without creating one. Ask for the exact personal account,
   fork name, and dedicated branch if not already supplied. The account must
   match the authenticated login. Preserve the user's values, validate the
   account and fork name as single GitHub path components, and validate the
   branch with `git check-ref-format --branch` when available. Require its
   output to equal the supplied branch, since it can expand checkout
   shorthand. Do not treat a general “go ahead” as approval for unspecified
   values.

3. Inspect `repos/$ACCOUNT/$FORK_NAME` before writing. A 404 alone does not
   prove absence: it can hide an inaccessible private repository. Confirm
   absence through an authenticated account interface with sufficient access
   to see the account's repositories; if that cannot be established, stop.
   Treat transport, permission, rate-limit, and malformed-response errors as
   blockers. If the repository exists, verify its exact owner/name, public
   visibility, `fork: true`, and parent or source identity
   `tbeaudouin05/GoAster`. Require a default branch and its exact commit ref;
   otherwise stop without changing it.

4. If the fork exists, inspect the exact dedicated branch in it. If it is
   missing, use the fork's default branch and commit as the proposed base. If
   it exists, show its current commit and ask the user to explicitly choose
   either to reuse it as-is or to supply another branch name. Never overwrite
   it; do not report the workflow complete or reuse it without that choice.
   A reused branch need not equal the default-branch commit. Recheck the new
   name if one is chosen. Treat an ambiguous ref lookup as a blocker.

   If the fork is absent, **do not query any branch in it**. Propose the
   upstream default branch and commit recorded in step 1 as the base. Explain
   that the fork's branch can be inspected only after creation and readiness.
   A missing upstream ref or an uncertain candidate-fork state is a blocker,
   not a reason to guess a base.

Use exact-ref lookups and check the returned full `refs/heads/...` name and
commit SHA, not a prefix match. URL-encode path components when needed, and
quote values; never pass untrusted text through `eval`.

## Approval

Before any creation, present the exact upstream, authenticated account,
fork `owner/name`, **public** visibility, dedicated branch, and proposed
default branch and base SHA. State whether the fork exists, whether the branch
has been inspected, and the intended writes: create the separate fork only if
absent, then create the branch in that fork only if missing. For an existing
branch, describe reuse as-is with its observed SHA and make no branch write.
Obtain explicit approval of that exact plan; reuse also requires the user's
explicit choice. If any approved identity, visibility, branch, default branch,
base SHA, or reuse SHA changes, stop and obtain a new choice or approval.
Immediately before each creation request, recheck that the active GitHub login
is still the approved account; if it changed or cannot be verified, stop.

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

## Create or reuse the branch

Once the fork is verified and ready, inspect the exact target branch again
immediately before any branch write. This is the first target-branch lookup in
an initially absent fork. If it now exists, show its SHA and ask the user to
choose reuse as-is or a different name; do not create or overwrite it. If the
ref is confirmed missing, create it at the approved `BASE_SHA` in the fork:

```sh
gh api --hostname github.com --method POST "repos/$ACCOUNT/$FORK_NAME/git/refs" \
  --raw-field "ref=refs/heads/$BRANCH" \
  --raw-field "sha=$BASE_SHA"
```

If creation fails or times out, inspect the exact ref; never overwrite or
blindly retry it. If a ref exists, show its SHA and ask for an explicit choice
to reuse it or select another branch. If its state is uncertain, stop and
report that state. Do not claim an uncertain write succeeded.

## Verify and hand off

Re-read the upstream identity, the fork's owner/name, public visibility,
parent/source and default branch, and the exact dedicated ref. A newly
created branch must resolve to the approved base SHA; a reused branch must
resolve to the SHA the user explicitly chose to reuse. If the reused ref
changed, ask for a new choice. If verification fails, report the observed
state rather than claiming completion. Report the verified account, fork URL,
public visibility, default branch, dedicated branch, and commit IDs. Later
commits or pushes require a separate request and a fresh destination check;
this workflow does not modify upstream contents.
