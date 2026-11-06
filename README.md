# GoAster

GoAster is a local terminal chat application with a subscription-backed OpenAI
provider, local tools, durable memory, scheduling, and a native file picker.
The repository also contains the remote agent and Telegram runtime.

The repository contains the local chat application, authentication, packaging,
remote runtime, Telegram runtime, and other operational tools.

## Install and launch

Use the packaged entrypoint for the target platform:

- macOS: launch `GoAster.app`.
- Windows: launch `GoAster.cmd`.
- Linux: launch `GoAster.desktop`.

The entrypoint opens a terminal and starts the package's GoAster binary.
On first launch, starter skills are copied from the package's `skills/` tree
to `~/.goaster/artefacts/skills/`, and a missing
`~/.goaster/artefacts/memory/MEMORY.md` is initialized. Existing user files
are preserved. Installation is local and does not require network access.

The local app and remote agent each keep their own runtime skill directory.
Updating a package does not overwrite an existing user-edited runtime skill.
Review and merge skill changes manually when needed.

Authentication uses the ChatGPT subscription OAuth device flow in the terminal.
Open the displayed URL, sign in, and enter the one-time code. Credentials are
stored locally in `~/.goaster/auth.json` with private file and directory
permissions. The remote agent uses its own credential file and never receives
the local credential file.

## Local chat

Describe a task in ordinary language. The local chat can use the installed
skills, public web search and fetch, bounded shell execution, named background
commands, memory, and scheduling.

Terminal commands:

- `/env` opens the local environment-file picker.
- `/env-file PATH` imports a selected `KEY=VALUE` file after review.
- `/file` or `/upload` opens the local file picker for non-secret inspection.
- `/clear` clears the saved terminal conversation.

Environment values are reviewed and stored separately in
`~/.goaster/env` with private permissions. Values are masked in terminal
output. Do not paste secrets into chat or select secret-containing files for
`/file`.

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

The adapter handles ordinary text, one image or document attachment, and
`/clear`. Media is validated and bounded before the current turn; temporary
files are private and removed after the request. Telegram bot tokens and
pairing material are operational secrets and must remain outside chat,
command lines, logs, and the repository.

## Models

The shipped model template is `config/openai-models.json`. Runtime
configuration is kept at
`~/.goaster/artefacts/config/openai-models.json` and is reloaded during
normal requests. Telegram users can use `/m` to view the current model and
`/m MODEL_OR_ALIAS` to select a configured model for that chat.

## Build packages

Packaging requires Git, Go 1.22+, Python 3, and a POSIX-compatible shell:

```sh
./packaging/build.sh all
./packaging/validate.sh dist
./packaging/test.sh
```

The build creates macOS, Windows, and Linux packages under `dist/`.
Package validation checks launcher wiring, tracked regular assets, starter
skill contents, and archive structure. For source development, point the app
at this checkout's skills explicitly:

```sh
GOASTER_STARTER_DIR="$(pwd)/skills" go run ./cmd/goaster
```

The package contains only the maintained starter skills listed by
`packaging/assets.sh`. The packaging tests also check local Markdown links
and reject stale or unexpected starter assets.

## User-owned source workflow

The [fork workflow](skills/goaster-fork/SKILL.md), [source
workflow](skills/goaster-source/SKILL.md), and [contribution sharing
workflow](skills/goaster-share-contribution/SKILL.md) keep upstream read-only
and put user changes on the user's own branches. Use the
[contribution discovery](skills/goaster-find-contributions/SKILL.md) workflow
for read-only manifest inspection. The [build/test
workflow](skills/goaster-build-test/SKILL.md) describes the core local checks;
the [coordinator](skills/goaster-coordinator/SKILL.md) sequences local build,
backup, deployment, and verification stages.

## Tests

Run the core Go tests and packaging checks from the repository root:

```sh
gofmt -w <changed-go-files>
go test ./...
./packaging/test-skill-assets.sh
./packaging/validate.sh dist
```

Remote and Telegram runtime code remain available alongside the local app.
