# GoAster build and test

Keep this stage deliberately small. It validates the approved fork locally;
it is not a deployment, backup, runtime swap, or integration test plan.

Before creating any managed temporary file or directory, or setting a
subprocess temporary path, load `read-write-temp-files` and follow its
workflow. Use its unique private task directory for `TEST_DIR`, resolve the
real user's `~/.goaster/tmp` before any `HOME` override, and export `TMPDIR`,
`TMP`, and `TEMP` for subprocesses. Skill metadata is only a load hint; it does
not automatically enforce these rules for every tool.

Build and test only the approved local fork checkout. This stage must not
deploy, contact GitHub or OVH, change a server, send Telegram messages, or
swap a running executable. Do not read, copy, or print authentication files,
tokens, pairing material, or other secrets into build artifacts, logs, or test
fixtures.

## Preflight and build

Confirm the absolute source path, fork identity, branch, and exact source SHA.
Use the private task directory from `read-write-temp-files` as a temporary
output directory outside the checkout. If Go files were
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

## Agent binaries and authenticated prompt checks

Build the two current runtime executables into the temporary output directory:
`goaster-agent` and `goaster-telegram`. Do not build or start a terminal
runtime binary for this workflow; the terminal binary has been removed.

```sh
go build -trimpath -o "$TEST_DIR/goaster-agent" ./cmd/goaster-agent
go build -trimpath -o "$TEST_DIR/goaster-telegram" ./cmd/goaster-telegram
```

Use only the existing agent credential at the same exact path
`~/.goaster/agent-auth.json`. Never run login as part of this workflow,
inspect or copy credentials, or place secrets in artifacts, logs, or tests. Do
not override `HOME` for either check. The checks may run while the agent or
another process uses the same exact credential path; this is accepted
operational behavior based on observed use, not a cross-process refresh or
concurrency guarantee. Credential refresh can still race across processes.
Do not stop the service. The tester is the running agent, so tests must never
stop, restart, or replace it; doing so causes the test to fail. If existing
credentials cannot be confirmed, mark both checks incomplete.

The pre-swap check below invokes the newly built binary's ordinary `prompt`
command. It uses the same direct-agent construction as the runtime: real
OpenAI provider authentication, all registered tools, runtime skill metadata,
starter-skill installation rules, the real Markdown memory store, and the
runtime model configuration. Set `SOURCE_ROOT` to the recorded absolute
checkout path before running it. Because the binary is in `TEST_DIR`, point
`GOASTER_STARTER_DIR` at that checkout's reviewed starter assets; this does
not replace an existing `~/.goaster/artefacts/skills` directory.
The direct prompt starts a fresh session with chat ID zero, so it does not
exercise an existing chat's conversation history or per-chat model selection.

This is a real provider call and is the realistic pre-swap check. The prompt is
chosen to request no action, but the full runtime can execute model-selected
shell, web, or background-command tools. Run it only with the local state and
allowlist approved for that risk; an exact reply does not prove that every
tool, skill, or memory entry was exercised. The command does not use the
control socket and does not start, stop, restart, or replace the live agent.

```sh
command -v timeout >/dev/null || {
  echo 'timeout is required for the bounded prompt checks' >&2
  exit 1
}
printf '%s\n' 'FULL_RUNTIME_PROMPT_OK' >"$TEST_DIR/expected-full-runtime-prompt.txt"
if ! GOASTER_STARTER_DIR="$SOURCE_ROOT/skills" timeout --foreground 120s \
  "$TEST_DIR/goaster-agent" prompt \
  'Reply with exactly FULL_RUNTIME_PROMPT_OK and no other text.' \
  >"$TEST_DIR/full-runtime-prompt.txt" 2>"$TEST_DIR/full-runtime-prompt-error.txt"; then
  exit 1
fi
cmp "$TEST_DIR/expected-full-runtime-prompt.txt" "$TEST_DIR/full-runtime-prompt.txt" || exit 1
```

Retain the lightweight check when a bounded authentication/model smoke test is
useful or when the full-runtime check is not authorized. `test-prompt` is
deliberately not a parity check: it sends one message with the default model
and no tools, skills, or real memory.

```sh
printf '%s\n' 'PROMPT_OK' >"$TEST_DIR/expected-prompt.txt"
if ! timeout --foreground 120s \
  "$TEST_DIR/goaster-agent" test-prompt \
  'Reply with exactly PROMPT_OK and no other text.' \
  >"$TEST_DIR/prompt.txt" 2>"$TEST_DIR/prompt-error.txt"; then
  exit 1
fi
cmp "$TEST_DIR/expected-prompt.txt" "$TEST_DIR/prompt.txt" || exit 1
```

Treat either prompt failure, timeout, or reply mismatch as a failed result when
that check is authorized to run. Do not print either prompt output or error
file in the report; retain only the pass/fail result. The deterministic Go
tests validate construction and request parity offline, but they do not
replace the full-runtime provider call. If authentication, local state, or
tool risk prevents a check, report it as incomplete rather than claiming
parity.

## Retained local artifacts

Only after the build, core tests, and the full-runtime prompt check pass, retain
both executables outside the checkout under the private, non-deployment path.
A lightweight `test-prompt` pass alone does not authorize a full-runtime
pre-swap result or artifact retention:

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
agent_prompt_full_runtime=pass|fail|incomplete|not-run
agent_prompt_smoke=pass|fail|incomplete|not-run
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

After retaining and verifying artifacts, clean up only the private temporary
task directory. Preserve the existing GoAster authentication files and all
existing retained build directories; never clean them as part of this stage.

The `goaster-deploy` skill describes the handoff for these artifacts; a host-specific deployment and rollback procedure still needs review and approval.
