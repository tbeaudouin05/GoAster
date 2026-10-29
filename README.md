# GoAster

A small standalone terminal chat app, built with Go and a native file-picker
integration. Requires Go 1.22+. Normal chat uses an OpenAI ChatGPT/Codex
subscription through the Codex backend; it does not use an OpenAI API key. A
fixed built-in `web_search` tool uses no-key DuckDuckGo HTML search and returns
search-result titles, URLs, and snippets. The built-in `web_fetch` tool retrieves
readable content from public HTTP or HTTPS URLs with response and redirect limits.
The built-in `shell_execute` tool runs commands through the platform's native
shell and returns stdout, stderr, and the exit code; it accepts an optional
working directory and bounded timeout in milliseconds. GoAster has no provider,
API-key, or web-search flag configuration.

## Build and run

Build the single binary:

```sh
go build -o goaster ./cmd/goaster
```

Start GoAster:

```sh
./goaster
```

Command-line flags configure or debug the app; authentication is part of this
normal startup journey and does not require a separate command.

On the first normal startup, or whenever no valid subscription credential is
available, GoAster starts the device-code OAuth flow in the same process. Open
the displayed URL, sign in to ChatGPT, and enter the one-time code. The OAuth
tokens and ChatGPT account ID are saved locally in `~/.goaster/auth.json` with
`0600` permissions inside a `0700` directory. After authorization completes,
GoAster continues directly into chat. Existing credentials are refreshed when
needed and then chat starts directly.

The terminal conversation is saved automatically in `~/.goaster/session.json`
with `0600` permissions. It contains bounded user/assistant conversation text
and any generated summary; OAuth credentials and tool-call internals are not
copied into it. At 75% of the fixed 300,000-token context window, or when the
256-message limit is reached, the active subscription-backed model summarizes
older messages while recent messages remain available. The summary is saved
and included in future requests. If summarization fails, GoAster trims older
messages to keep context bounded and reports the trimming in the terminal and
to the model. Session storage remains bounded to 8 MiB and 32 KiB per message.
If the saved conversation is malformed, GoAster moves it aside safely and
starts a new conversation.

## Markdown memory

GoAster keeps durable Markdown memory separate from `session.json` under the
default workspace `~/.goaster`:

```text
~/.goaster/
├── session.json
└── memory/
    ├── MEMORY.md
    └── YYYYMM/
        └── YYYYMMDD.md
```

`memory/MEMORY.md` is protected long-term knowledge. Daily notes are appended
to the date-based file, with private directories and restrictive file
permissions; appends are written through an atomic replacement. Normal model
requests receive `MEMORY.md` and the three most recent daily notes as clearly
marked Markdown context, newest note first. If the memory files are empty or
missing, the existing request behavior is unchanged.

Conversation summarization continues to update only the conversation summary
in `session.json`; it never rewrites `MEMORY.md`. Use the packaged
[self-improvement-memory-consolidation skill](skills/self-improvement-memory-consolidation/SKILL.md)
for an optional, user-managed daily maintenance workflow that reviews daily
notes and promotes only verified durable facts into protected long-term
memory. Users may invoke it from their own cron or scheduler; GoAster does not
install or provide an operating-system cron entry. The skill remains
user-managed and is not automatically scheduled by GoAster.

## Scheduled chat turns

GoAster includes a small local scheduler for model-requested reminders and
recurring chat turns. Jobs are saved atomically in
`~/.goaster/cron/jobs.json` with private `0700` directory and `0600` file
permissions, so they survive a normal restart. The model uses the built-in
`cron` tool with `add`, `list`, `get`, `update`, `remove`, `enable`, and
`disable` actions. Add a job with exactly one of:

```text
at_seconds     positive number of seconds from now for a one-time turn
every_seconds  positive recurring interval
cron_expr      five-field cron expression (six fields may include seconds)
```

Each job stores its message, enabled state, next and last run times, status,
error, run count, and optional `max_runs` (`0` means unlimited). One-time
`at_seconds` jobs are deleted after their execution. Updates are partial, so
omitted message, schedule, and `max_runs` fields are preserved.

Due jobs send their saved message through the same terminal chat session as
interactive input; the session serializes turns so scheduled execution cannot
corrupt conversation state. This feature deliberately does not schedule shell
commands, send to Telegram or other channels, install operating-system cron
entries, or add user-facing scheduler configuration or flags. It only runs
while the terminal GoAster process is active.

Enter one message per line. Blank lines are ignored. End the input stream with
EOF (Ctrl-D on Unix) to exit. Ctrl-C or closing the terminal also stops the
process where supported. A different credential file can be selected with the
configuration flag:

```sh
./goaster -auth-file /path/to/auth.json
```

## OVH remote access

The chat application does not provision OVHcloud resources. To connect to an
existing OVH remote, use the [OVH remote SSH skill](skills/ovh-remote/SKILL.md).
It runs approved SSH checks and changes through GoAster's `shell_execute` using
temporary host scripts, keeps credentials on the host, and requires explicit
approval immediately before remote mutations. This repository does not include
an automated OVH installer; the following applies when setting up the packaged
remote agent and Telegram adapter on an existing host.

The terminal and remote agent require **two independent OpenAI device logins**.
The local terminal login described above stays local. Never copy or send its
`auth.json`, a custom `-auth-file`, its OAuth tokens, or its saved session to the
OVH host. A freshly installed remote agent has no subscription credentials and
does not use the terminal's credential file. The agent can run while waiting
for login, but its loopback `/healthz` and `/chat` endpoints return unavailable.

After the remote agent is installed, open a separate SSH terminal to the OVH
host and run `goaster-agent login` as the same user and with the same `HOME`
that the agent service uses. For example, if the service runs as `goaster`,
uses `/var/lib/goaster` as its home, and the binary is installed in
`/usr/local/bin`:

```sh
ssh -t USER@OVH_HOST 'sudo -u goaster env HOME=/var/lib/goaster /usr/local/bin/goaster-agent login'
```

Use the URL and one-time code displayed in that SSH terminal to authorize the
remote device. Do not paste the code, tokens, or login output into GoAster or
Telegram chat, and do not run this interactive login through a chat tool that
would relay its output to the model. The remote login saves its own credentials
as `~/.goaster/agent-auth.json` under the service user's remote home, with
`0600` file and `0700` directory permissions. This file is separate from
`~/.goaster/auth.json`, even if both programs use the same home. If login fails
or is canceled, run the remote login again; keep the host and any separately
configured Telegram state for inspection. The same command can reauthenticate
the remote agent later; stop the agent service first and restart it afterward.

After login completes and the agent is running, check its readiness on the OVH
host with `curl --fail http://127.0.0.1:8765/healthz`. It returns `ok` only
when the agent can load its separate subscription credential, refreshing it if
needed; this does not test a model response or detect every revoked token.
Start or restart the Telegram adapter after the check succeeds. For systemd
readiness reporting, configure its service as `Type=notify`: the adapter sends
the ready notification only after the agent is ready and the first Telegram
poll succeeds. It also checks agent readiness before telling a paired user
that chat is ready. If the adapter was started too early, restart it after
login. Continue to use the existing private Telegram token and pairing setup;
neither the OpenAI device code nor the subscription tokens belong in Telegram.

When configuring Telegram on that remote, the SSH skill directs the agent to
the packaged
[Telegram BotFather Group Privacy Mode skill](skills/telegram-group-privacy/SKILL.md),
which explains the `/setprivacy` → **Turn off** option for users who want
ordinary group messages.

## Models and fallback

Primary and fallback models default to `gpt-6.1-sol` and `gpt-5.6-luna`:

```sh
./goaster -primary-model gpt-6.1-sol -fallback-model gpt-5.6-luna
```

Provider errors wrapping `ErrModelUnavailable` trigger one retry on the
fallback model. The fallback remains active for the session and its reason is
displayed. Other provider errors terminate the loop with a nonzero exit status.

Input lines are limited to roughly 1 MiB.

```sh
go test ./...
./goaster -help
```
