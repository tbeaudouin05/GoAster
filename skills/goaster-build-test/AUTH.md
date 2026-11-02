# Authorization

Build and test only the approved local fork checkout. This stage is local and
must not deploy, contact GitHub, contact OVH, change a server, send Telegram
messages, or run Telegram adapter tests. Do not read or copy secrets into build
artifacts, logs, or test fixtures.

The source path and exact source SHA are part of the approval. If either
changes, stop and obtain a fresh source/build approval. Preserve user changes;
run `gofmt` only for Go files that the requested work touched, and report any
formatting change.
