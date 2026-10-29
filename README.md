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
with `0600` permissions. It contains only bounded user/assistant conversation
text; OAuth credentials and tool-call internals are not copied into it. Older
messages or oversized message text are trimmed automatically when the fixed
300,000-token context window is reached, matching PicoClaw's `context_window`
default. PicoClaw's `summarize_token_percent` default is 75%; GoAster does not
yet summarize, so it retains its safe trimming behavior at the full context
limit. Trimming is reported in the terminal and to the model. Session storage
remains bounded to 8 MiB, 256 messages, and 32 KiB per message. If the saved
conversation is malformed, GoAster moves it aside safely and starts a new
conversation.

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
