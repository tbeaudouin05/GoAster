# Authentication

The prompt check uses the existing agent credential at
`~/.goaster/agent-auth.json`. Use only this credential; never run login
automatically, inspect or copy credentials, override `HOME`, or place secrets in
artifacts, logs, or tests. If the credential cannot be confirmed, mark both
checks incomplete. Concurrent credential refresh can race; do not stop the
running agent. The agent and other processes may use that same exact path during
the check; this is accepted operational behavior based on observed use, not a
cross-process refresh or concurrency guarantee. The tester is the running agent,
so tests must never stop, restart, or replace it; doing so causes the test to
fail.

This workflow has no deployment or runtime-swap authorization. Retaining the
two non-secret executables and their checksums does not authorize transferring
or activating them.
