# GoAster

A small standalone terminal chat app, built with Go's standard library. Requires
Go 1.22+. Normal chat uses an OpenAI ChatGPT/Codex subscription through the
Codex backend; it does not use an OpenAI API key.

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

Enter one message per line. Blank lines are ignored. Use `/quit`, `/exit`, or
EOF (Ctrl-D on Unix) to exit. A different credential file can be selected with
the configuration flag:

```sh
./goaster -auth-file /path/to/auth.json
```

## Models and fallback

Primary and fallback models default to `gpt-6.1-sol` and `gpt-5.6-luna`:

```sh
./goaster -primary-model gpt-6.1-sol -fallback-model gpt-5.6-luna
```

Provider errors wrapping `ErrModelUnavailable` trigger one retry on the
fallback model. The fallback remains active for the session and its reason is
displayed. Other provider errors terminate the loop with a nonzero exit status.

The deterministic local echo flow remains available without credentials:

```sh
./goaster -offline
./goaster -offline -echo-unavailable-model gpt-6.1-sol
```

The second command simulates an unavailable primary model and demonstrates the
same fallback behavior. Input lines are limited to roughly 1 MiB.

```sh
go test ./...
./goaster -help
```
