# Read/write temporary files

Load this skill before creating, reading, writing, or cleaning any managed
temporary file or directory, or before choosing a temporary path for a
subprocess. Load any other skill required by the task as well.

The `META.md` description is a discovery hint only. Skill discovery does not
automatically apply these rules to `shell_execute`, a subprocess, or another
tool, and this skill cannot enforce every tool's behavior. Do not claim that
metadata provided enforcement. If the required skill cannot be loaded, stop
the temp-dependent workflow.

## Resolve the GoAster temporary root first

Before changing `HOME`, `USERPROFILE`, or another profile environment variable,
capture the verified real user home and resolve the absolute GoAster temporary
root as:

```text
<real user home>/.goaster/tmp
```

Save that resolved path in a variable before any profile override. Do not
recompute `~/.goaster/tmp` after an override, because it could point at a test
home or an unrelated user's directory. Inspect each existing component as a
real directory, reject symbolic links and unexpected file types, and create
missing components with mode `0700` without broadening existing permissions.

Do not override `HOME` for an authenticated GoAster operation. If an isolated
subprocess genuinely needs a profile override, resolve and save the real
GoAster root and the exact credential paths first, then apply the override
only to that subprocess. Never copy credentials into the temporary area to
make the override work.

## Create one private task directory

Use a fresh, unique child of the resolved root for the whole task. Set a
restrictive umask before creation, use an exclusive `mktemp -d`-style
operation, and verify that the result is an absolute real directory with mode
`0700`. Never use the GoAster root, its `tmp` root, the repository, or a shared
system temporary directory as the task directory itself.

Point all common subprocess temporary-directory variables at that one task
directory before starting a child:

```sh
export TMPDIR="$TASK_DIR"
export TMP="$TASK_DIR"
export TEMP="$TASK_DIR"
```

Use the platform equivalent when a shell does not use POSIX syntax. Keep the
variables exported so descendants inherit them. A child working directory may
also be set to `TASK_DIR` when that is part of the approved task. Use bounded
commands and do not start, stop, restart, or replace services as a side effect
of temporary-file handling.

## Read and write safely

Keep temporary contents non-secret and limited to the current task. Do not put
passwords, tokens, OAuth state, private keys, pairing material, control
secrets, or auth headers in temporary files, filenames, command arguments,
logs, or subprocess output. Reject paths that escape `TASK_DIR`, and do not
follow or create symlinks in the managed tree. Create files privately (normally
mode `0600`) and use exclusive creation. For an existing durable file, use a
private same-directory temporary file and an atomic replacement only when the
owning workflow requires it; do not replace an unrelated file.

GoAster's own per-turn attachment directories are runtime-managed and have
their own validation and cleanup contract. Use that contract when a runtime
supplies an attachment path; do not delete the process temporary root or
rewrite durable GoAster state merely to make it fit this workflow.

## Clean up narrowly

Arrange cleanup when the task starts so it runs on success, failure, and
interruption after child processes have exited. Before removing anything,
verify that the target is the exact generated `TASK_DIR`, is not the root or
`tmp` directory, is still a private real directory, and remains below the
resolved GoAster temporary root. Remove only that task directory. Never use a
wildcard or a broad parent path, and never remove the GoAster root, artefact
retention directories, the repository, or files belonging to another task.

Preserve authentication and authorization files, including the local
`~/.goaster/agent-auth.json` and `~/.goaster/auth.json` paths and any configured
control-secret, Telegram token, or pairing-state files. Do not inspect, copy,
move, overwrite, or delete them. If one is found inside the task directory,
stop, preserve it for explicit recovery, and report that cleanup is incomplete
instead of deleting it. Report cleanup failures without printing temporary
contents or secret-bearing paths.
