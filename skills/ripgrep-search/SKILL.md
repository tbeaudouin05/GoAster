# Ripgrep search

Use this skill whenever a task requires finding text, filenames, symbols, or
references in local source trees or other directory trees.

## Prefer ripgrep in normal repository work

Check first:

```sh
command -v rg
rg --version
```

Use `rg` by default for repository searches. Ripgrep is usually better than
classic `grep` for source trees because it is fast, recursive by default, has
clear include and exclude options, respects `.gitignore` by default, handles
common text encodings safely, and gives useful line-numbered output. It is the
normal GoAster choice for searching a checkout.

Do not claim that ripgrep is always better. Use `grep` when portability is the
priority, the target is a minimal system where only POSIX tools are available,
a script explicitly requires standard `grep` behavior, or the search is a
small single-file/pipeline operation where installing another tool would add
no value. Use `git grep` when the search should be limited to tracked files in
a Git worktree and Git is already the desired boundary.

## Practical patterns

```sh
# Search tracked-source-like files while respecting ignore rules.
rg 'pattern' path/

# Search only selected file types.
rg --glob '*.go' 'pattern' .

# Search filenames rather than file contents.
rg --files | rg 'config|secret'

# Include ignored files only when the user explicitly needs them.
rg --hidden --no-ignore 'pattern' path/

# Show context around matches.
rg -n -C 3 'pattern' path/
```

Treat search output as potentially sensitive. Do not paste secret values,
tokens, private keys, or full secret-bearing files into normal chat. Narrow the
path and pattern, report filenames and line numbers when enough, and redact
sensitive matching text.

## If ripgrep is missing

If `command -v rg` fails, tell the user that ripgrep is recommended but is not
currently installed. Offer `grep` or `git grep` for an immediate read-only
search, then recommend installing ripgrep through the host's official package
manager.

First identify the operating system and available package manager. Do not
assume `sudo`, a distribution, a package manager, network access, or permission
to install software. Ask for explicit approval before running the install.
Examples of commands to show only after the target package manager is verified:

```sh
# Debian/Ubuntu
sudo apt-get update && sudo apt-get install ripgrep

# Fedora/RHEL-like systems
sudo dnf install ripgrep

# macOS with Homebrew
brew install ripgrep

# Windows with winget
winget install BurntSushi.ripgrep.MSVC
```

The examples are guidance, not permission to execute. Prefer the smallest
package-manager operation, avoid unrelated upgrades, and verify the result
with `command -v rg` and `rg --version`. If installation is unavailable or not
approved, continue with `grep` or `git grep` and state the limitation.

## Search boundary

Do not search outside the requested directory or expose unrelated host data.
Do not use ripgrep as a substitute for a database query, secret-manager read,
or binary inspection. For binary files, use an appropriate file-specific tool
instead of forcing text search.
