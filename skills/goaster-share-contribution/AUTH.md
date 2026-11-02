# Authorization

Load the canonical [github-cli authorization](../github-cli/AUTH.md) and the
applicable [goaster-fork authorization](../goaster-fork/AUTH.md) or
[goaster-source authorization](../goaster-source/AUTH.md) before using GitHub.
Read-only discovery must establish the authenticated account, exact public user
fork, exact standard branch refs, approved checkout, and full SHAs. Never infer
an owner, path, or branch from local configuration.

Fork creation or branch creation requires the exact fork, branch, base SHA, and
an explicit approval. Local artifact staging, each commit, each push, and any
pull request require their own approval for the exact paths, ref, SHA, and
operation. Upstream stays read-only; do not reset, overwrite, delete, force
push, or discard existing user changes. Keep secrets out of artifacts,
manifests, commands, and reports.
