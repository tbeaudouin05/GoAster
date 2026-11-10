# GitHub authentication

Before checking authentication or logging in, check that `/usr/bin/gh` exists
and is executable. If it does not, install GitHub CLI at exactly
`/usr/bin/gh` before proceeding with login.

Check authentication on the user's host:

```sh
gh auth status --hostname github.com
gh api --hostname github.com user --jq '.login'
```

Confirm the login matches the account the user named. Do not infer the account
from a Git remote, environment variable, or repository name.

## Log in

If authentication is needed, start the private, allowlisted `github-login`
profile using `background_command`:

```json
{"action":"start","name":"github-login"}
```

The profile runs this fixed command in an interactive terminal (PTY), without
model-supplied arguments:

```sh
gh auth login --hostname github.com --git-protocol https --web
```

Use the session ID returned by `start` for subsequent `background_command`
calls. If GitHub CLI is waiting to open the browser, send Enter to that
session:

```json
{"action":"send-keys","id":"<session-id>","keys":"enter"}
```

To cancel, send `ctrl-c` to the same session:

```json
{"action":"send-keys","id":"<session-id>","keys":"ctrl-c"}
```

Do not use `input`, paste, or enter secrets. Read the terminal output and share
GitHub's official verification URL and one-time code with the user, who
completes verification in their browser. Leave the session running while they
do so.

Afterward, check the session with `status` and read `output` only as needed.
Verify the host and account using the commands above. Stop any still-running
or canceled session and verify its terminal state.
