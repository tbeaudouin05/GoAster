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
install or provide a cron subsystem or scheduler implementation.

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
approval immediately before remote mutations.

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
