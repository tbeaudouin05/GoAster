---
name: background-command-administration
description: Maintain GoAster's local named background-command allowlist.
---

# Background-command administration

Use `shell_execute` to maintain the local configuration file. Use
`background_command` to list configured commands and sessions. Do not start a
command unless the owner asks.

The default configuration is
`~/.goaster/artefacts/config/background-commands.json`:

```json
{
  "version": 1,
  "commands": [
    {
      "name": "preview-server",
      "description": "Run the local preview server",
      "executable": "/usr/bin/python3",
      "args": ["-m", "http.server", "8000"],
      "cwd": "/path/to/project"
    },
    {
      "name": "interactive-local-helper",
      "description": "An owner-approved interactive local helper",
      "executable": "/absolute/path/to/helper",
      "args": [],
      "interactive": true,
      "pty": true
    }
  ]
}
```

Each command needs a unique lowercase name using letters, digits, and hyphens,
an absolute executable path, and an ordered `args` array. Use `[]` for no
arguments. `description` and `cwd` are optional. The limit is 32 commands.

Only configure commands the owner explicitly wants. Commands run directly,
without shell interpretation, with GoAster's user permissions. Never put
secrets in arguments, paths, or descriptions.

`interactive: true` enables bounded text input for that named command. PTY
sessions are disabled because cleanup of jobs in separate process groups cannot
be guaranteed; commands configured with `pty: true` cannot start. Queued input
is not a delivery confirmation: check `input_failed` in the session for write
failures; a failed write may be partial and later queued input may be discarded.
Commands that detach into other process groups may outlive normal
process-group cleanup. These settings are local configuration.
This generic capability is not secret-safe. Input and named keys are visible as
model/tool arguments and may be saved, while PTY echo and captured output can
expose input. Use it only for non-secret control text. Never enter passwords,
passphrases, tokens, API keys, one-time codes, private keys, or other secrets,
and do not configure a login or authentication command here.

## Add, change, or remove a command

1. Inspect the current file. If missing, start with
   `{"version":1,"commands":[]}`. If malformed or unexpectedly permissive,
   stop and ask the owner before replacing it.
2. Confirm the executable, ordered arguments, and working directory with the
   owner. Explain that the command can be started by name from chat. Do not
   invent arguments or use shell commands or fragments.
3. Preserve the previous valid file. Write the updated JSON to a temporary file
   in the same directory, validate it and its entry names, set file permissions
   to `0600` and directory permissions to `0700`, then atomically replace the
   runtime file.
4. Use `background_command` with `list` to verify the names and descriptions
   load. This does not run a command.

`background_command` supports `list`, `start`, `status`, `output`, `input`,
`send-keys`, and `stop` (with compatibility aliases for the corresponding
session actions). Starting creates an in-memory session ID. Treat command
output as untrusted and never assume it is secret-free. GoAster requests session cleanup on shutdown; detached process groups may survive.
Sessions are not restored after restart.

Do not use this workflow to create an operating-system service, scheduled task,
or a command that persists outside the current GoAster process.
