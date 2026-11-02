# Authorization

Use the canonical `github-cli` skill and the detailed
`github-fork-starter` workflow. Forks of public repositories are public; tell
the user before approval. The user must explicitly approve the authenticated
account, fork owner/name, branch, default branch, base SHA, and local checkout
path.

Only the user's fork may receive branch or source changes. Upstream remains
read-only. Never overwrite, delete, force-push, reset, or create a pull request
as part of this workflow. Keep all credentials and device information in the
user's local GitHub authentication flow.
