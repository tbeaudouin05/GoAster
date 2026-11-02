# Authorization

Use read-only GitHub discovery first; if the fixed upstream cannot be verified, stop and report the blocker. Disclose before approval that forks of public repositories are public. Before any creation, obtain explicit approval for the exact authenticated account, public fork, branch, default branch, base commit, and intended actions. An existing branch may be reused only by the user's explicit choice and must never be overwritten.

GitHub's upstream forks endpoint requests creation of a separate approved fork and does not modify upstream contents. Approval does not authorize changes to upstream contents, refs, settings, or access, nor commits, pushes, deletes, overwrites, force pushes, or resets. Branch creation must target only the approved fork.

## GitHub authentication

- Keep using a working `gh` session that is already authenticated to `github.com`. Check it with `gh auth status --hostname github.com` and verify the account with `gh api --hostname github.com user --jq '.login'`. Report only the host and login.
- If login is needed in Telegram, have the paired user send `/github-login`
  in the paired private chat, then ask GoAster to continue the workflow. The
  fixed, provider-allowlisted `auth_handoff` accepts only `github` and no
  command, token, password, device code, or arbitrary provider. GitHub's
  short-lived verification URL and code are delivered directly to that private
  chat through an out-of-band event; they never enter model context or history,
  tool output, command arguments, logs, or reports. Never send a token.
- Send `/github-login cancel` in the same chat to cancel. Stop on cancellation
  or expiry; do not refresh, reissue, or retry without a new `/github-login`
  authorization. The handoff requires the non-secret public
  `GOASTER_GITHUB_CLIENT_ID` configuration and uses no GitHub client secret.
  The device request uses the `repo read:org gist` scope set expected by
  `gh auth login --with-token`.
  After completion, verify the `gh` session and account as above, and continue
  only if the login is the account the user intends to use.
- The agent's `/chat`, `/auth/github/authorize`, and `/auth/github/cancel`
  loopback routes accept only the shared `GOASTER_GITHUB_AUTH_CONTROL_SECRET`
  header injected into both root-run services by the deployment. A `chat_id` by
  itself is not authorization. Keep the secret in a root-controlled service
  `EnvironmentFile` with restrictive permissions; never put it in chat,
  command arguments, logs, or reports.
- Rollback removes the newly-installed GitHub account by its verified login
  before restoring a prior credential snapshot, including when that account is
  inactive in `gh`. If `gh` cannot confirm that removal or the prior snapshot
  cannot be restored, the handoff fails closed and reports a manual
  reconciliation state; it must not report authentication success.
- GoAster's existing `remote_login` is an OpenAI-specific login handoff and
  remains unchanged. Do not emulate either handoff with shell background jobs,
  captured `gh auth login` output, `tmux`, `expect`, pipes, or another
  workaround. Do not ask the user to paste a password, token, or device code.
  Never read credential files or put secrets in command arguments, URLs, logs,
  or reports.

This handoff is not a credential sandbox. GoAster intentionally runs an
unrestricted root shell, so the agent/model can access locally stored `gh`
credentials through that shell. Do not claim that the GitHub token is
inaccessible to the agent or model; the handoff only keeps device instructions
and credentials out of ordinary model messages, conversation history, and
adapter progress output.
