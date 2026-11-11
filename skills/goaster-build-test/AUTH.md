# Authentication

The prompt check uses the existing agent credential at `~/.goaster/agent-auth.json`. Never run login automatically, inspect or copy credentials, or place secrets in artifacts, logs, or tests. The agent and other processes may use that same exact path during the check; this is accepted operational behavior based on observed use, not a cross-process refresh or concurrency guarantee. The tester is the running agent, so tests must never stop, restart, or replace it; doing so causes the test to fail. If existing credentials cannot be confirmed, report the check incomplete.

This workflow has no deployment or runtime-swap authorization. Retaining the
two non-secret executables and their checksums does not authorize transferring
or activating them.
