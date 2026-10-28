# GoAster

A small terminal chat CLI, built with Go's standard library. Requires Go 1.22+.
The default provider deterministically echoes messages locally: no credentials,
network access, or model inference. Model names are configuration labels.

```sh
go run ./cmd/goaster
go test ./...
```

Enter one message per line. Blank lines are ignored. Use `/quit`, `/exit`, or EOF
(Ctrl-D on Unix) to exit. Use `/history` to display numbered user and assistant
entries for the current session, and `/clear` to erase them and restart numbering.
Clearing confirms with `History cleared.`; empty history displays `History is empty.`
Commands and blank lines are not recorded. Each submitted message is recorded
once, along with its successful reply; a failed reply leaves only the user entry.
History is kept only in memory and is lost on exit. It is not sent to the provider;
the echo provider still replies to each message independently. Clearing history
does not reset the active model or fallback reason.

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
