# Authentication

Use `goaster-env` and `hetzner-auth` for secret lookup and read-only target
verification. Authentication does not authorize snapshot creation, server
changes, or cost. Require separate approval for the exact snapshot operation.
Never include credential values in reports, logs, commands, or handoffs.
