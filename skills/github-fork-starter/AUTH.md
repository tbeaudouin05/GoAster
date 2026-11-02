# Authorization

Load and follow the canonical [github-cli skill](../github-cli/SKILL.md) and
its [authorization rules](../github-cli/AUTH.md) before using GitHub. This
workflow adds the fork-specific identity and branch approvals below; it does
not replace the generic GitHub safety rules.

Use read-only GitHub discovery first; if the fixed upstream cannot be verified, stop and report the blocker. Disclose before approval that forks of public repositories are public. Before any creation, obtain explicit approval for the exact authenticated account, public fork, the `goaster-customizations` and `goaster-contributions` refs, default branch, base commit, and intended actions. Do not ask the user to choose another branch name. An existing standard branch may be reused only by the user's explicit choice and must never be overwritten.

GitHub's upstream forks endpoint requests creation of a separate approved fork and does not modify upstream contents. Approval does not authorize changes to upstream contents, refs, settings, or access, nor commits, pushes, deletes, overwrites, force pushes, or resets. Branch creation must target only the approved fork.

## GitHub authentication

- Use an existing `gh` session when it is authenticated to `github.com`. Before
  GitHub API requests, and again after any login, verify it with
  `gh auth status --hostname github.com` and
  `gh api --hostname github.com user --jq '.login'`. Continue only when the
  verified login is the account the user intends to use. Report only the host
  and login.
- If login is needed, run this standard web flow as a foreground command:

  ```sh
  gh auth login --hostname github.com --web
  ```

  If `gh` displays an official GitHub URL and a one-time code, relay only that
  URL and code to the user. The user completes authentication in the browser;
  do not enter the code on the user's behalf or ask them to send it back. Let
  the command finish, then verify the session and account as above. If the
  account is not the one the user intends to use, stop and have the user switch
  credentials locally.
- Never ask the user to paste a password or token into chat. Never read
  credential files or put secrets in command arguments, URLs, logs, or reports.

GoAster's root-run unrestricted shell can access locally stored `gh`
credentials. Do not describe those credentials as inaccessible to the agent or
model; the web login flow does not create a credential sandbox. GoAster's
profile-based `process_handoff` remains the separate OpenAI interactive login
flow; only its fixed `openai` profile is available through the current chat
tool, and no command or executable string may be supplied by the model.
