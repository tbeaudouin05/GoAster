# Authorization

This is the canonical GitHub authorization workflow. Other GitHub skills defer
to it for authentication, host selection, read-only discovery, and safe writes.

Use `github.com` explicitly and verify the authenticated login before every
write. GitHub credentials, passwords, tokens, device codes, and credential-file
contents stay in the user's local GitHub CLI or credential helper. Never ask
for them in chat, read credential files, put them in arguments or URLs, or
include them in output.

Treat upstream repositories as read-only unless a separate request explicitly
approves a precise upstream action. A write requires the exact account,
repository, ref, commit SHA, and intended operation. Do not delete, overwrite,
force-push, reset, or create a pull request without a separate explicit
approval. Inspect an uncertain result before considering a write again.
