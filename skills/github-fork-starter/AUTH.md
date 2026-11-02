# Authorization

Use read-only GitHub discovery first; if the fixed upstream cannot be verified, stop and report the blocker. Disclose before approval that forks of public repositories are public. Before any creation, obtain explicit approval for the exact authenticated account, public fork, branch, default branch, base commit, and intended actions. An existing branch may be reused only by the user's explicit choice and must never be overwritten.

GitHub's upstream forks endpoint requests creation of a separate approved fork and does not modify upstream contents. Approval does not authorize changes to upstream contents, refs, settings, or access, nor commits, pushes, deletes, overwrites, force pushes, or resets. Branch creation must target only the approved fork.

Keep passwords, tokens, device codes, and credential-file contents out of chat, command arguments, logs, and reports. Use GitHub CLI browser/device authentication or an existing local credential helper; never ask the user to paste a secret.
