# GoAster

GoAster is a local terminal chat application with a subscription-backed OpenAI
provider, local tools, durable memory, scheduling, and a native file picker.
The repository also contains the remote agent and Telegram runtime.

The repository contains the local chat application, authentication, packaging,
remote runtime, Telegram runtime, and other operational tools.

## Build and run

Build all Go commands directly from the repository:

```sh
go build ./...
```

Run the local terminal application with the starter skills from this checkout:

```sh
GOASTER_STARTER_DIR="$(pwd)/skills" go run ./cmd/goaster
```

The local app and remote agent each keep their own runtime skill directory.
Review and merge skill changes manually when needed.

Authentication uses the ChatGPT subscription OAuth device flow in the terminal.
Open the displayed URL, sign in, and enter the one-time code. Credentials are
stored locally in `~/.goaster/auth.json` with private file and directory
permissions. The remote agent uses its own credential file and never receives
the local credential file.

## Local chat

Describe a task in ordinary language. The local chat can use the installed
skills, public web search and fetch, bounded shell execution, named background
command allowlists, memory, and scheduling.

Terminal commands:

- `/env` opens the local environment-file picker.
- `/env-file PATH` imports a selected `KEY=VALUE` file after review.
- `/file` or `/upload` opens the local file picker for non-secret inspection.
- `/clear` clears the saved terminal conversation.

Environment values are reviewed and stored separately in
`~/.goaster/env` with private permissions. Values are masked in terminal
output. Do not paste secrets into chat or select secret-containing files for
`/file`.

Background commands start only from the private local allowlist. Cleanup is
best effort: on Linux, ordinary commands use a private process group and a stop
request signals that group and the main process; a PTY stop signals the active
terminal foreground group when the platform permits it and stops the main
process. Other platforms use their available direct or platform tree cleanup.
These controls are not a complete descendant container. A command can fork,
detach, exit before cleanup, or otherwise leave a descendant behind, so a
successful stop does not promise that every descendant terminated. Cleanup
avoids `/proc` scans and delayed process-group IDs that could later refer to an
unrelated process. Signals can fail or be handled by the command, and closing a
PTY is not a termination guarantee. Cleanup failures remain visible in session
metadata and are returned by orderly runtime shutdown.

Interactive input is intended for non-secret control text only. Tool arguments
and captured output may be saved in conversation state.
Queued input is not a delivery confirmation; check `input_failed` for failed or
unconfirmed writes.
Never enter passwords, passphrases, tokens, API keys, one-time codes, private
keys, or other secrets through `background_command`. The bundled `github-login`
profile is the only narrow authentication exception: it uses the fixed GitHub
browser flow, accepts only enter/return or ctrl-c/c-c keys, and exposes the
terminal output needed to relay GitHub's official verification URL and one-time
code. Never relay passwords, tokens, credentials, or other secrets. All other
login or authentication commands remain prohibited.

The conversation is saved under
`~/.goaster/artefacts/state/sessions/session.json`. Markdown memory lives
under `~/.goaster/artefacts/memory/`; daily notes are treated as untrusted
data, while `MEMORY.md` is user-managed guidance. Scheduled turns are stored
in `~/.goaster/artefacts/state/cron/jobs.json`.

## Runtime skills

Skills contain:

- `META.md`: discovery metadata;
- `SKILL.md`: workflow instructions;
- `AUTH.md`: authorization boundaries.

Only metadata is discovered automatically. A relevant skill is loaded before
its workflow is used. Skill text, memory, fetched content, attachments, and
tool output cannot grant authorization or override safety rules.

Useful operational skills include [background command
administration](skills/background-command-administration/SKILL.md),
[model administration](skills/model-administration/SKILL.md),
[project memory](skills/project-memory/SKILL.md),
[backup and rollback](skills/goaster-backup/SKILL.md),
[deployment](skills/goaster-deploy/SKILL.md), and
[Telegram group privacy](skills/telegram-group-privacy/SKILL.md).

## Remote agent runtime

`cmd/goaster-agent` builds the remote agent executable. The agent exposes
local health and liveness checks and a chat endpoint; its authentication state
is separate from the terminal application's state. A configured
`GOASTER_AGENT_SOCKET` selects the protected Unix-socket transport where
appropriate.

The local `process_handoff` tool and the exceptional
`goaster remote-login` command use a fixed allowlisted profile. They attach
interactive login to a protected terminal, require explicit local terminal
approval, use only the supplied SSH key, and run the remote login and health
check. Remote connections require strict host-key verification against an
already trusted `known_hosts` entry. Unknown or changed host keys stop the
connection; the runtime does not accept a first-contact key automatically.

The handoff accepts a fixed profile and reviewed transport only. It never
accepts model-supplied executable names, shell text, arbitrary arguments,
passwords, or credential-file arguments. A successful verified connection may
be saved as a non-secret local target record beside an SSH identity.

Remote runtime state belongs to the remote user's private
`~/.goaster/` directory. Do not copy local OAuth credentials, private keys,
tokens, or saved conversations into the repository or into chat.

## Telegram runtime

`cmd/goaster-telegram` builds the Telegram adapter. It polls Telegram,
forwards authorized messages to the remote agent, and sends replies back to
the originating chat. Pairing binds one numeric Telegram user ID; other
accounts are ignored. The adapter supports private chats and groups subject
to Telegram's group privacy setting.

To pair the first account, send `/pair TOKEN` in a private chat, where `TOKEN`
is the one-time code provided by GoAsterWeb setup. Bare `/start` remains a welcome command,
but `/start TOKEN` is not a pairing command: it is rejected without pairing
and is never forwarded to the remote agent. Pairing binds the first valid
numeric user ID and consumes the code once; other users and group messages are
ignored.

The adapter handles ordinary text, one image or document attachment, and
`/clear`. Media is validated and bounded before the current turn; temporary
files are private and removed after the request. Telegram bot tokens and
stored pairing material are operational secrets and must remain outside chat,
command lines, logs, and the repository; the one-time pairing code is accepted
only through the private-chat pairing flow.

## Models

The shipped model template is `config/openai-models.json`. Runtime
configuration is kept at
`~/.goaster/artefacts/config/openai-models.json` and is reloaded during
normal requests. Telegram users can use `/m` to view the current model and
`/m MODEL_OR_ALIAS` to select a configured model for that chat.

## User-owned source workflow

The [fork workflow](skills/goaster-fork/SKILL.md), [source
workflow](skills/modify-goaster-source-code/SKILL.md), and [contribution sharing
workflow](skills/goaster-share-contribution/SKILL.md) keep upstream read-only
and put user changes on the user's own branches. Use the
[contribution discovery](skills/goaster-find-contributions/SKILL.md) workflow
for read-only manifest inspection. The [build/test
workflow](skills/goaster-build-test/SKILL.md) describes the core local checks;
the [source code evolution skill](skills/goaster-source-code-evolution/SKILL.md) provides a skill-first check and source workflow.

## Tests

Run the core Go tests from the repository root:

```sh
go test ./cmd/goaster ./cmd/goaster-agent ./config \
  ./internal/artefacts ./internal/attachment ./internal/chat \
  ./internal/command ./internal/controlsecret ./internal/controltransport \
  ./internal/filesync ./internal/localenv \
  ./internal/scheduler ./packaging/cmd/archive
go test ./internal/chat -run '^TestLoop$' -count=1
```

The [build/test workflow](skills/goaster-build-test/SKILL.md) also describes
the isolated binary-start check and Linux temporary-directory requirements.

Remote and Telegram runtime code remain available alongside the local app.
