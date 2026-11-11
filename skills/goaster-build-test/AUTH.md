# Authentication

The prompt check uses the existing agent credential at `~/.goaster/agent-auth.json`. Never run login automatically, inspect or copy credentials, or place secrets in artifacts, logs, or tests. The agent and all other credential users must be stopped throughout the check because refresh is not serialized between processes. Do not stop the service; if the condition cannot be confirmed, report the check incomplete.

This workflow has no deployment or runtime-swap authorization. Retaining the
two non-secret executables and their checksums does not authorize transferring
or activating them.
