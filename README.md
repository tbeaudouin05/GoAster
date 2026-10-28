# GoAster

A small terminal chat CLI, built with Go's standard library. Requires Go 1.22+.
Normal chat uses an OpenAI ChatGPT/Codex subscription through the Codex backend;
it does not use an OpenAI API key.

```sh
go test ./...
```

## Sign in and chat

Run the device-code login once:

```sh
go run ./cmd/goaster login
```

Open the URL printed by GoAster, sign in to ChatGPT, and enter the one-time code
printed below it. The OAuth tokens and ChatGPT account ID are then stored locally
in `~/.goaster/auth.json` with `0600` permissions (inside a `0700` directory).

Start the subscription-backed chat:

```sh
go run ./cmd/goaster
```

Enter one message per line. Blank lines are ignored. Use `/quit`, `/exit`, or EOF
(Ctrl-D on Unix) to exit. If chat is started without a saved login, GoAster stops
before sending the message and tells you to run the login command. Tokens are
refreshed automatically when they are close to expiry. A different credential
file can be selected for either command:

```sh
go run ./cmd/goaster login -auth-file /path/to/auth.json
go run ./cmd/goaster -auth-file /path/to/auth.json
```

## Models and fallback

Primary and fallback models default to `gpt-6.1-sol` and `gpt-5.6-luna`:

```sh
go run ./cmd/goaster -primary-model gpt-6.1-sol -fallback-model gpt-5.6-luna
```

Provider errors wrapping `ErrModelUnavailable` trigger one retry on the fallback
model. The fallback remains active for the session and its reason is displayed.
Other provider errors, including an expired login that cannot be refreshed,
terminate the loop with a nonzero exit status.

The original deterministic local echo flow remains available without credentials:

```sh
go run ./cmd/goaster -offline
go run ./cmd/goaster -offline -echo-unavailable-model gpt-6.1-sol
```

The second command simulates an unavailable primary model and demonstrates the
same fallback behavior. Input lines are limited to roughly 1 MiB.

```sh
go test ./...
go run ./cmd/goaster -help
```
