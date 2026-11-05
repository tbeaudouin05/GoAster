# GoAster build and test

Keep this stage deliberately small. It validates the approved fork locally;
it is not a deployment or integration test plan.

## Preflight and build

Confirm the absolute source path, fork identity, branch, and exact source SHA.
Use a temporary output directory outside the checkout. If Go files were
changed, run `gofmt` on those files and inspect the diff before continuing.

Run the repository build, which compiles all Go commands but does not run any
tests:

```sh
go build ./...
```

Run the core test packages explicitly. This list intentionally excludes
`cmd/goaster-telegram` and `internal/telegram`; no Telegram or adapter tests
belong in this workflow:

```sh
go test ./cmd/goaster ./cmd/goaster-agent ./config \
  ./internal/artefacts ./internal/attachment ./internal/chat \
  ./internal/command ./internal/controlsecret ./internal/controltransport \
  ./internal/filesync ./internal/localenv \
  ./internal/scheduler ./packaging/cmd/archive
```

On Linux, the protected socket tests require root and a short, secure `TMPDIR`
whose ancestors are root-owned and safe. If those tests cannot run, report that
coverage as incomplete. Use only a temporary directory you are authorized to
use and preserve its existing files and policy markers.

## Isolated binary and prompt smoke test

Build the local terminal binary into the temporary output directory:

```sh
go build -trimpath -o "$TEST_DIR/goaster" ./cmd/goaster
```

Start it once with an isolated home and the reviewed starter directory. Use
`-help` so the check never starts device login or contacts the model service;
assert that help text is produced and that no file is written outside the
temporary home. This is the isolated binary-start check.

Use the repository's deterministic fake-provider loop as the concise prompt
smoke test:

```sh
go test ./internal/chat -run '^TestLoop$' -count=1
```

The smoke test must exercise one short prompt and observe one assistant reply;
it must not use Telegram, the adapter, a live OVH host, a production service,
or a real secret. If the binary start or prompt smoke test cannot run, mark
the build result incomplete rather than claiming success.

## Result and handoff

Record a structured, non-secret result:

```text
source_sha=<exact SHA>
source_path=<absolute path>
build=pass|fail
core_tests=pass|fail
binary_start=pass|fail
prompt_smoke=pass|fail
binary_path=<temporary or retained reviewed path>
binary_sha256=<checksum when retained for deployment>
```

Only a passing build, core test set, isolated start, and prompt smoke test may
be handed to backup and deployment. A different source SHA or binary checksum
requires a new result and invalidates downstream approvals.
