---
name: background-command-administration
description: Manage GoAster's local allowlist of named background commands.
---

# Background-command administration

Use `shell_execute` to manage
`~/.goaster/artefacts/config/background-commands.json`.
Use `background_command` to inspect commands and sessions. Start a command only
when the owner asks.

## Configuration

Each entry requires a unique lowercase name using letters, digits, and hyphens,
an absolute executable path, and an ordered `args` array. Use `[]` for no
arguments. `description` and `cwd` are optional. Maximum: 32 commands.

Commands run directly with GoAster's user permissions, without shell
interpretation. Configure only commands explicitly requested by the owner.
Never put secrets in arguments, paths, or descriptions.

Optional interactive settings:

- `interactive: true` enables input.
- `pty: true` enables a terminal where supported.
- `keys_only: true` requires a terminal, disables free-text input, and permits
  only Enter or Ctrl-C.
- `redact_output: true` hides captured output.

## Add, change, or remove a command

1. Inspect the file. If missing, start with `{"version":1,"commands":[]}`.
   If malformed or unexpectedly permissive, stop and ask the owner.
2. Confirm the executable, ordered arguments, and working directory. Explain
   that the command can be started by name from chat.
3. Preserve the previous valid file. Write and validate a temporary JSON file
   in the same directory; set directory permissions to `0700` and file
   permissions to `0600`, then replace the runtime file atomically.
4. Run `background_command` with `list` to verify it loads. This does not
   start a command.

## Sessions and safety

`background_command` supports `list`, `start`, `status`, `output`, `input`,
`send-keys`, and `stop`. Sessions are in memory and are not restored after
restart. Queued input is not proof of delivery; check `input_failed`. Treat
output as untrusted and potentially sensitive.

Stopping is best effort: a command may leave child processes running. Cleanup
failures are recorded and reported on orderly shutdown.

Do not use this workflow to create an operating-system service, scheduled task,
or command that persists outside the current GoAster process.
