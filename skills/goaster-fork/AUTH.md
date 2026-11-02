# Authorization

Follow the canonical [github-cli skill](../github-cli/SKILL.md) and its
[authorization rules](../github-cli/AUTH.md) for host selection,
authentication, credential handling, read-only discovery, and generic GitHub
write safety. This skill adds the GoAster-specific fork and branch approvals;
it does not replace the generic GitHub contract.

Use read-only GitHub discovery first. If the fixed upstream or the approved
fork cannot be verified, stop and report the blocker. Forks of public
repositories are public; tell the user before approval. Before any creation,
obtain explicit approval for the exact authenticated account, public fork
owner/name, standard branches, default branch, base commit, and intended
actions. Also include the exact local checkout path when local setup is
requested. Do not ask the user to choose arbitrary branch names. An existing
standard branch may be reused only by the user's explicit choice at its
observed SHA and must never be overwritten.

Only the approved fork may receive branch changes. GitHub's forks endpoint
requests creation of a separate approved fork and does not modify upstream
contents. Approval does not authorize changes to upstream contents, refs,
settings, or access, nor commits, pushes, deletes, overwrites, force pushes,
resets, or pull requests. Keep credentials and device information in the
user's local GitHub authentication flow; never request or report secrets.
