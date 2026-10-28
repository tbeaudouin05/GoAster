# GoAster

A small terminal chat CLI, built with Go's standard library. Requires Go 1.22+.
The default provider deterministically echoes messages locally: no credentials,
network access, or model inference. Model names are configuration labels.

```sh
go run ./cmd/goaster
go test ./...
```

Enter one message per line. Blank lines are ignored. Use `/quit`, `/exit`, or EOF
(Ctrl-D on Unix) to exit. Messages are not persisted; the echo provider has no
conversation memory.

Primary and fallback models default to `gpt-6.1-sol` and `gpt-5.6-luna`:

```sh
go run ./cmd/goaster -primary-model gpt-6.1-sol -fallback-model gpt-5.6-luna
go run ./cmd/goaster -echo-unavailable-model gpt-6.1-sol
go run ./cmd/goaster -help
```

The second command simulates an unavailable primary model. On the first message,
GoAster retries that message with the fallback, displays the active model and
fallback reason, and keeps the fallback active for the session. Only provider
errors wrapping `ErrModelUnavailable` trigger fallback; other errors terminate
the loop with a nonzero exit status. A failed fallback also terminates the loop.
Each response shows its model name. Input lines are limited to roughly 1 MiB.

`internal/chat` contains configuration, a small `Provider` interface, model
selection, and the stream-based terminal loop. `cmd/goaster` wires them together.
Remote hosting, Telegram, credential login, and production integrations are
outside this initial slice.
