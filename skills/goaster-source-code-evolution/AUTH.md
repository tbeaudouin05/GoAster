# Authorization

This coordinator only sequences the named skills; each stage keeps its own
authorization and approval gate. Upstream GitHub remains read-only, the user
works in their own fork, and no pull request is created by this workflow.

Bind every approval to the exact GitHub host and fork identity, the standard
`goaster-customizations` and `goaster-contributions` branches, source SHA,
local path, built binary checksum, target host identity, service, release path,
and stable helper path. A change to any identity, SHA, path, or host invalidates
the affected approval and all downstream results. Re-run the necessary
preflight, build, or backup stage and obtain fresh approval; never carry an
approval across a changed tuple.

Secrets remain in their approved local or server stores. Do not expose them in
handoffs, logs, structured results, or backup manifests.
