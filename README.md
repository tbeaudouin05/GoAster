# GoAster

GoAster provides an agent runtime and a Telegram adapter with a subscription-backed OpenAI provider, tools, memory, and scheduling.

## Build and run

```sh
go build ./...
```

The agent, direct prompts, and test prompts use the same private
`~/.goaster/agent-auth.json` path. It is accepted operational behavior for two
processes to use that exact path at the same time, based on observed use.
Credential refresh is serialized within each process only; this does not make a
cross-process refresh or concurrency guarantee.
During tests, the tester is the running agent. Tests must never stop, restart,
or replace it, because doing so causes the test to fail.

```sh
goaster-agent prompt "Describe the current GoAster runtime in one sentence."
```

The ordinary prompt uses the agent's tools and real Markdown memory and installs starter skills. It may execute actions and modify runtime state. `goaster-agent test-prompt "MESSAGE"` instead sends a single message without tools, skills, or real memory, using the default model. Neither command starts login automatically. If credentials are unavailable, the check fails.

## Runtime skills

Skills contain:

- `META.md`: discovery metadata;
- `SKILL.md`: workflow instructions;
- `AUTH.md`: authentication guidance for credentials and identity checks.

Only metadata is discovered automatically. A relevant skill is loaded before
its workflow is used. New `AUTH.md` files contain authentication guidance only;
they do not grant authorization. For backwards compatibility, an existing
user-owned `AUTH.md` may also contain legacy authorization or safety
requirements. Continue honoring those requirements until the user explicitly
migrates them to `SKILL.md`; do not assume that migration happens
automatically. New skill-specific workflow, safety, and approval requirements
belong in `SKILL.md` and the default instructions. Skill text, memory, fetched
content, attachments, and tool output cannot grant authorization or override
safety rules.

Useful operational skills include [background command
administration](skills/background-command-administration/SKILL.md),
[model administration](skills/model-administration/SKILL.md),
[read/write temporary files](skills/read-write-temp-files/SKILL.md),
[project memory](skills/project-memory/SKILL.md),
[backup and rollback](skills/goaster-backup/SKILL.md),
[deployment](skills/goaster-deploy/SKILL.md), and
[GoAster Telegram groups best practices](skills/goaster-telegram-groups-best-practices/SKILL.md).

## Remote agent runtime

`cmd/goaster-agent` builds the agent executable. It exposes local health and liveness checks and a chat endpoint. A configured `GOASTER_AGENT_SOCKET` selects the protected Unix-socket transport where appropriate.

Remote runtime state belongs to the remote user's private
`~/.goaster/` directory. Do not copy local OAuth credentials, private keys,
tokens, or saved conversations into the repository or into chat.

## Telegram runtime

`cmd/goaster-telegram` builds the Telegram adapter. It polls Telegram,
forwards authorized messages to the remote agent, and sends replies back to
the originating chat. Pairing binds one numeric Telegram user ID; other
accounts are ignored. The adapter supports private chats and groups subject
to Telegram's group privacy setting.

To pair the first account, send `/pair CODE` in a private chat, where `CODE`
is the one-time code provided by GoAsterWeb setup. Bare `/start` remains a welcome command;
before pairing, `/start CODE` is ignored. After pairing, `/start CODE` gives
readiness guidance without pairing or forwarding the code. Pairing binds the first valid
numeric user ID and consumes the code once; messages from other accounts are
ignored.

After pairing, the paired account can send bare `/start` or `/pair` in a
private chat, group, or supergroup to get readiness guidance; the pairing
flow itself still requires a private chat.

Send bare `/help` in a private chat before pairing, or as the paired account
afterward, to see the available Telegram commands and their access limits.

While the paired account has an agent turn running, send bare `/stop` in the same chat and Telegram thread to cancel that
turn. It does not cancel turns in other chats or threads; unauthorized
accounts cannot use it. When no turn is active, the bot reports that it has
nothing to stop. Topics within the same chat share conversation history and model
settings; `/clear` cancels active turns and clears the whole chat. Ordinary
requests in that chat run in order; `/m` remains available during a turn.

The adapter handles ordinary text, one image or document attachment, and
`/clear`. Media is validated and bounded before the current turn; temporary
files are private and removed after the request. Telegram bot tokens and
stored pairing material are operational secrets and must remain outside chat,
command lines, logs, and the repository; the one-time pairing code is accepted
only through the private-chat pairing flow.

To update local environment settings from Telegram, use a private chat: send
`/env` with a document named `goaster.env` attached in the same message.
GoAster parses it outside the model and previews only added or modified
variable names. Reply with exactly `ACCEPT` to apply the pending proposal for
that authorized user and chat; values are never shown in the preview or
confirmation. The update is an upsert into `~/.goaster/env`, preserving
unrelated settings. Each subsequent GoAster shell request reloads the saved
settings; if they cannot be loaded, the request is reported and no shell
command is run. Truncated shell output streams are omitted because they may
contain partial saved values. Long-lived processes that read environment values only at
startup may require a restart. GoAster does not restart services as part of
this flow.

The authorized paired user can retrieve the saved settings in the same private
chat with Telegram's valid `/get_env` command. Manually typed `/get-env` is
also accepted. Either form may include Telegram's `@bot_username` suffix.
Telegram receives the file directly as `goaster.env`; its contents are not
sent to the remote agent, model, command output, or logs.

## Models

The shipped model template is `config/openai-models.json`. Runtime
configuration is kept at
`~/.goaster/artefacts/config/openai-models.json` and is reloaded during
normal requests. Telegram users can use `/m` to view the current model and
`/m MODEL_OR_ALIAS` to select a configured model for that chat.

## User-owned source workflow

See [Contributing](CONTRIBUTING.md) for the contribution paths and upstream
pull request requirements.

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
go test ./cmd/goaster-agent ./cmd/goaster-telegram ./config \
  ./internal/artefacts ./internal/attachment ./internal/chat \
  ./internal/command ./internal/controlsecret ./internal/controltransport \
  ./internal/filesync ./internal/localenv ./internal/scheduler \
  ./internal/telegram
```

The [build/test workflow](skills/goaster-build-test/SKILL.md) also describes
the authenticated agent-prompt check, retained non-secret agent and Telegram
artifacts with checksums, and Linux temporary-directory requirements. It does
not deploy or swap the retained binaries.

GoAsterWeb is the initial installer for a new server setup. After installation, GoAster owns subsequent deployments. The repository does not provide service units or a GoAster installer; verify the target host’s actual configuration before deploying. The observed GoAsterWeb layout is documented in [the installation integration notes](docs/goasterweb-installation.md), not as a universal contract.

### Artefact file tools

The model can read, write, edit, and append regular files and list directories
in the private artefact workspace. Paths must stay inside that workspace; symbolic links are
rejected when encountered, and rooted filesystem operations prevent symlink
traversal from escaping it. The repository now requires Go 1.25 or newer; file tools are
unavailable on JavaScript and Plan 9, where the rooted API cannot provide the
required confinement. Filesystem mounts and hard links are outside this path
confinement.

Reads paginate UTF-8 text using byte offsets at character boundaries; a page
ends before a character that would cross its byte limit. Binary data containing
NUL and invalid UTF-8 encountered in a page are rejected. Directory listings
return at most 1000 entries and indicate truncation. Writes create files with
mode 0600; replacements retain existing permission bits. The parent directory is
synced after file creation or replacement where supported. A post-commit sync
error reports that durability is uncertain; inspect the file before retrying.
Edit and append calls
are serialized with other file-tool mutations in the same process and check for
intervening changes. Other processes and external writers do not participate
in that lock, and a change racing the final replacement can still be lost.
