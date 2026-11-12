# Authentication

Get the Hetzner API token through the `goaster-env` skill. Use only the
configured token. Keep it in memory: never print it, save it to a file, or
include it in logs.

Verify authentication and confirm the Hetzner project and target server before
continuing. If authentication fails or the target is ambiguous, stop and
explain the problem.

This skill verifies authentication and target identity only; it does not
authorize snapshots, server changes, or costs.
