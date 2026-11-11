# Ripgrep search

Use this skill to find text, filenames, symbols, or references in local files.

## Search

Check whether `rg` is installed:

```sh
command -v rg
rg --version
```

Prefer `rg` for repository searches. It searches recursively and respects
`.gitignore` by default. Use `git grep` for tracked files only, or `grep`
when portability matters or the search is simple.

```sh
rg 'pattern' path/                  # Search contents
rg --glob '*.go' 'pattern' .        # Filter by file type
rg --files | rg 'config|secret'     # Search filenames
rg -n -C 3 'pattern' path/          # Show surrounding lines
```

Only include ignored files when explicitly needed:

```sh
rg --hidden --no-ignore 'pattern' path/
```

Search only the requested directory. Treat results as sensitive: never share
secrets or full secret-bearing files. Prefer filenames and line numbers, and
redact sensitive matches.

## If ripgrep is missing

Install ripgrep using the host's verified package manager, then check
`command -v rg` and `rg --version`.

First identify the operating system and package manager. Do not assume `sudo`
or network access, run downloaded scripts, add unverified repositories, or
change unrelated packages.

If installation fails or is not permitted, use `grep` or `git grep` and
report the limitation.

## Boundaries

Do not use ripgrep in place of a database query, secret-manager read, or
binary-specific inspection tool.
