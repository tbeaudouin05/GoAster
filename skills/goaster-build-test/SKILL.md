# GoAster build and test

Validate an approved local GoAster fork and retain tested binaries for a
separately approved runtime swap. This workflow does not deploy, contact
GitHub or a cloud provider, modify servers, send Telegram messages, or stop, restart,
or replace the running agent.

## Prepare

- Load `read-write-temp-files`. Use its private task directory as `TEST_DIR`
  and follow its temporary-directory and environment-variable rules.
- Record the approved absolute source path, fork, branch, and exact commit SHA.
- Require every new binary intended for deployment to be built from the
  approved checkout on the fork's `goaster-customization` branch, whether the
  implementation is a skill or source customization. Confirm the branch with
  `git branch --show-current`; stop if it differs. This branch requirement
  does not authorize deployment. If the path or SHA changes, stop and obtain
  fresh approval.
- Preserve user changes. If requested Go files need formatting, run `gofmt`
  only on those files, inspect the diff, and report any changes.
- Require a clean committed checkout, including ignored files. Check
  `git status --porcelain` and `git clean -ndx` without deleting anything.
  Confirm `git rev-parse HEAD` matches the approved SHA. Repeat these checks
  after testing and before retaining artifacts; discard results if they differ.
- Never read, copy, or print credentials or other secrets into artifacts,
  logs, or fixtures.

## Build and test

Before running the full test suite, check whether any tests could contact
external services or cause other side effects. Do not run those tests without
the necessary approval.

```sh
go build ./...
go test ./...
```

Build the two runtime executables into the private task directory:

```sh
go build -trimpath -o "$TEST_DIR/goaster-agent" ./cmd/goaster-agent
go build -trimpath -o "$TEST_DIR/goaster-telegram" ./cmd/goaster-telegram
```

Do not build or start a terminal runtime. On Linux, protected socket tests
require root and a short, secure `TMPDIR` with safe, root-owned ancestors.
Report tests as incomplete if they cannot run. Do not claim the full suite
passed if any tests failed or were skipped.

## Prompt checks

Use only the existing `~/.goaster/agent-auth.json`; do not inspect or copy
it, override `HOME`, or run login. If the credential cannot be confirmed,
mark both checks incomplete. Concurrent credential refresh can race; do not
stop the running agent.

The full-runtime check makes a real provider call with runtime tools, skills,
and memory. It can execute model-selected tools despite the no-action prompt.
Run it only when that risk and local state are approved. It starts a fresh
session, so it does not test existing chat history or per-chat model choice.

Set `SOURCE_ROOT` to the recorded checkout path, then run:

```sh
command -v timeout >/dev/null || exit 1
printf '%s\n' 'FULL_RUNTIME_PROMPT_OK' >"$TEST_DIR/expected-full-runtime-prompt.txt"
if ! GOASTER_STARTER_DIR="$SOURCE_ROOT/skills" timeout --foreground 120s \
  "$TEST_DIR/goaster-agent" prompt \
  'Reply with exactly FULL_RUNTIME_PROMPT_OK and no other text.' \
  >"$TEST_DIR/full-runtime-prompt.txt" 2>"$TEST_DIR/full-runtime-prompt-error.txt"; then
  exit 1
fi
cmp "$TEST_DIR/expected-full-runtime-prompt.txt" "$TEST_DIR/full-runtime-prompt.txt" || exit 1
```

The starter directory does not replace an existing
`~/.goaster/artefacts/skills` directory.

Optionally run this lighter authentication/model check. It does not test
runtime tools, skills, or real memory and cannot replace the full check:

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

Report an authorized check's failure, timeout, or mismatched reply as a
failure. If a check cannot safely run, report it as incomplete. Do not print
prompt output or error files. Offline tests and exact replies do not prove
that all runtime behavior was exercised.

## Retain artifacts

Only after the build, full test suite, and full-runtime prompt check pass,
retain the two tested binaries under:

```text
~/.goaster/artefacts/builds/<source-sha>/
```

Create the parent and SHA directory with mode 0700, reject symlinked path
components, and never overwrite an existing SHA directory. Install
`goaster-agent` and `goaster-telegram` with mode 0755. Write a relative-name
SHA-256 checksum file for each and a non-secret `manifest.txt` containing
the source path, fork, branch, exact SHA, artifact directory, binary names,
and checksums. Verify both binaries are regular executables and their
checksums match.

## Report and clean up

Report what passed, failed, or couldn’t be tested. Confirm that no deployment
or runtime swap was performed.

Clean up only the private task directory. Preserve authentication files and
existing retained builds. A changed source, artifact, or checksum requires a
new build/test result. Deployment and runtime swap require separate review
and approval; see `goaster-deploy`.
