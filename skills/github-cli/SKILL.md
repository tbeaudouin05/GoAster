# Canonical GitHub CLI workflow

Use this skill before any workflow that authenticates to GitHub or changes a
repository. It is the shared safety and authentication contract for
`goaster-fork` and later user-owned source work.

## Authentication

Use the GitHub CLI on the user's host. If authentication is needed, use only
the first-install, private allowlisted command named `github-login` through
`background_command`:

```json
{"action":"start","name":"github-login"}
```

That name expands to this fixed command and no model-supplied arguments:

```sh
gh auth login --hostname github.com --git-protocol https --web
```

The profile uses a PTY, accepts only `enter`/`return` or `ctrl-c`/`c-c`, and
discards its output. Use `send-keys` only for `enter` to continue the browser
flow or `ctrl-c` to cancel; never use `input`, paste, enter a password, token,
device code, or any other secret. Tell the user to complete verification in the
browser that GitHub CLI opens. Do not ask the user to paste credentials or
device information into chat. After `start`, send one named `enter` key to the
returned session if GitHub CLI is waiting to open the browser; then leave the
session running while the user completes verification.

After the user finishes, poll the returned session with `status` without
requesting `output`. Verify only non-secret state with:

```sh
gh auth status --hostname github.com
gh api --hostname github.com user --jq '.login'
```

Stop a still-running or canceled session and verify its terminal state before
continuing. If cleanup or verification is uncertain, report that state and
stop. If `github-login` is absent from the existing user configuration, do not
overwrite or merge that file; ask the owner to complete login locally or
explicitly configure the reviewed profile.

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
