# GoAster build and test

Keep this stage deliberately small. It validates the approved fork locally;
it is not a deployment, backup, runtime swap, or integration test plan.

Build and test only the approved local fork checkout. This stage must not
deploy, contact GitHub or OVH, change a server, send Telegram messages, or
swap a running executable. Do not read, copy, or print authentication files,
tokens, pairing material, or other secrets into build artifacts, logs, or test
fixtures.

## Preflight and build

Confirm the absolute source path, fork identity, branch, and exact source SHA.
Use a temporary output directory outside the checkout. If Go files were
changed, run `gofmt` on those files and inspect the diff before continuing.
The source path and exact source SHA are part of the source/build approval. If
either changes, stop and obtain fresh approval. Preserve user changes, run
`gofmt` only for Go files touched by the requested work, and report any
formatting change.

Require a clean committed checkout, including no staged, unstaged, untracked, or ignored files. Check `git status --porcelain` and `git clean -ndx` without deleting anything. Record `git rev-parse HEAD` and verify it equals the approved exact SHA before building. Repeat both checks after testing and immediately before retaining artifacts; if either differs, stop and discard the result. Never label binaries from a dirty tree with the commit SHA.

Run the repository build, which compiles all Go commands without starting any
runtime:

```sh
go build ./...
```

Run the core tests explicitly, including deterministic agent and Telegram
adapter tests. These tests do not contact Telegram or send messages:

```sh
go test ./cmd/goaster-agent ./cmd/goaster-telegram ./config \
  ./internal/artefacts ./internal/attachment ./internal/chat \
  ./internal/command ./internal/controlsecret ./internal/controltransport \
  ./internal/filesync ./internal/localenv ./internal/scheduler \
  ./internal/telegram
```

On Linux, the protected socket tests require root and a short, secure `TMPDIR`
whose ancestors are root-owned and safe. If those tests cannot run, report
that coverage as incomplete. Use only a temporary directory you are
authorized to use and preserve its existing files and policy markers.

## Agent binaries and authenticated prompt smoke test

Build the two runtime executables into the temporary output directory. Do not
build or start a terminal runtime binary for this workflow; the terminal
binary has been removed.

```sh
go build -trimpath -o "$TEST_DIR/goaster-agent" ./cmd/goaster-agent
go build -trimpath -o "$TEST_DIR/goaster-telegram" ./cmd/goaster-telegram
```

Use only the existing agent credential. Never run login as part of this workflow. Before the prompt, require the operator to verify that the agent and every other process using its credential are stopped, and remain stopped throughout. Refresh is not serialized across processes; the flag is an acknowledgement, not an automatic state check. Do not stop the service. If this condition or existing credentials cannot be confirmed, mark the check incomplete.

The test prompt has no tools, skills, or real memory; it uses the default model. It is intentionally different from the ordinary `prompt`, which uses tools and real memory.

```sh
printf '%s\n' 'GOASTER_BUILD_TEST_OK' >"$TEST_DIR/expected-prompt.txt"
"$TEST_DIR/goaster-agent" test-prompt --agent-stopped \
  'Reply with exactly GOASTER_BUILD_TEST_OK and no other text.' \
  >"$TEST_DIR/prompt.txt" 2>"$TEST_DIR/prompt-error.txt"
cmp "$TEST_DIR/expected-prompt.txt" "$TEST_DIR/prompt.txt"
```

Treat a prompt failure, timeout, or reply mismatch as a failed
result. Do not print the prompt output or error file in the report; retain only
the pass/fail result. This is the real agent prompt check, not the
deterministic fake-provider test in the Go suite.

## Retained local artifacts

Only after the build, core tests, and authenticated prompt check pass, retain
both executables outside the checkout under the private, non-deployment path:

```text
~/.goaster/artefacts/builds/<source-sha>/
├── goaster-agent
├── goaster-agent.sha256
├── goaster-telegram
├── goaster-telegram.sha256
└── manifest.txt
```

Create the `builds` parent and the exact `<source-sha>` directory with mode
0700, reject symlinked path components, and do not overwrite an existing
source-SHA directory. Install the two tested files with mode 0755. From the
artifact directory, write each checksum with a relative filename, for example:

```sh
sha256sum goaster-agent >goaster-agent.sha256
sha256sum goaster-telegram >goaster-telegram.sha256
```

The manifest must contain only non-secret metadata: source path, fork,
branch, exact source SHA, artifact directory, executable names, and their
SHA-256 values. Verify that each retained file is a regular executable and
that each checksum matches before reporting success. Keep the artifact for a
later, separately approved runtime swap; this stage never transfers, swaps,
restarts, backs up, or deploys it.

## Result

Record a structured, non-secret result:

```text
source_sha=<exact SHA>
source_path=<absolute path>
fork=<approved fork identity>
branch=<approved branch>
build=pass|fail
core_tests=pass|fail|incomplete
agent_prompt_smoke=pass|fail|not-run
artifact_dir=<retained private path or not-retained>
agent_binary_path=<retained path or not-retained>
agent_binary_sha256=<checksum or not-retained>
telegram_binary_path=<retained path or not-retained>
telegram_binary_sha256=<checksum or not-retained>
manifest_path=<retained path or not-retained>
deployment=not-run
runtime_swap=not-run
```

A passing result authorizes only the recorded local artifacts for later
review. A changed source SHA, artifact path, executable, or checksum requires
a new build/test result; no deployment or runtime swap is implied.

The `goaster-deploy` skill describes the handoff for these artifacts; a host-specific deployment and rollback procedure still needs review and approval.
