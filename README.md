# GoAster

A small standalone terminal chat app, built with Go and a native file-picker
integration. Requires Go 1.22+. Normal chat uses an OpenAI ChatGPT/Codex
subscription through the Codex backend; it does not use an OpenAI API key.

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

Enter one message per line. Blank lines are ignored. End the input stream with
EOF (Ctrl-D on Unix) to exit. Ctrl-C or closing the terminal also stops the
process where supported. A different credential file can be selected with the
configuration flag:

```sh
./goaster -auth-file /path/to/auth.json
```

## Local environment settings

In chat, enter `/env` to load local settings for a future OVH integration. A
native file picker opens; choose the local environment text file there. Do not
type or paste a file path or `KEY=VALUE` entries into chat. The file must contain
one `KEY=VALUE` pair per line and be no larger than 1 MiB. GoAster reads it
locally, validates it, then shows the variable names with masked values and asks
for `y/N` confirmation. Only `y` saves the set; any other response, including
canceling the picker, leaves the saved settings unchanged. Unreadable,
malformed, duplicate-key, oversized, symbolic-link, or non-regular files are
rejected without changing the saved settings.

On Linux, the desktop must provide a Zenity-compatible dialog helper such as
`zenity`, `qarma`, or `matedialog`.

Confirmed settings are stored in `~/.goaster/env` with `0600` file permissions
inside a `0700` directory. They are loaded locally at startup and are never sent
to the chat provider. Each confirmed `/env` setup replaces the previous saved
set.

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
