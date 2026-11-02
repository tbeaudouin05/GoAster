# Canonical GitHub CLI workflow

Use this skill before any workflow that authenticates to GitHub or changes a
repository. It is the shared safety and authentication contract for
`github-fork-starter`, `goaster-fork`, and later user-owned source work.

## Authentication

Use the GitHub CLI on the user's host. If authentication is needed, ask the
user to run this in their own interactive terminal and complete the browser or
device flow there:

```sh
gh auth login --hostname github.com
```

Do not run an interactive login through a captured command tool. Do not ask the
user to paste a password, token, or device code. An existing local credential
helper is acceptable.

Before discovery, check only the host and login, not the credential itself:

```sh
gh auth status --hostname github.com
gh api --hostname github.com user --jq '.login'
```

If the login is not the account the user named, stop and ask the user to switch
credentials locally. Never infer an account from a local Git remote, shell
environment, or a repository name.

## Read-only discovery first

Use `gh api --hostname github.com` with bounded field selection for discovery.
Verify the exact owner and repository, visibility, fork or parent identity,
default branch, exact ref, and commit SHA before proposing an action. A 404 or
ambiguous response is not proof that a repository or ref is absent; report the
uncertainty and stop.

Keep upstream inspection read-only. A local `upstream` remote may fetch from
the approved source, but no command may push to it or change its contents,
refs, settings, or access.

## Safe GitHub actions

Before a write, state the exact:

- authenticated account and host;
- repository owner/name and visibility;
- branch or ref and its current SHA;
- operation, including whether it creates a fork or branch; and
- result expected after verification.

Obtain approval for that exact plan. Validate repository owners and names as
single path components and validate branch names with Git's ref checker when
available. Quote values and never pass untrusted text through `eval`.

Submit at most one request for a creation whose result could be uncertain. If a
request fails, times out, or returns malformed data, inspect the exact target
read-only before retrying. Never blindly repeat a fork, branch, commit, push,
delete, or overwrite request. Verify the resulting owner/name, ref, SHA, and
visibility before reporting success.

For local work, `origin` must be the user's approved fork or repository. Check
fetch and push destinations before a push, and never push to upstream. A push,
commit, release, issue, settings change, or pull request is a distinct action;
general approval to work on a fork does not authorize it. Do not create a pull
request unless the user explicitly requests that exact action.

Report only non-secret state: host, account, repository, branch, SHA, and the
verified result. Preserve the user's existing local changes and stop when an
approved identity, ref, SHA, path, or host changes.
