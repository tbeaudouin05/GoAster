# Authentication and authorization

This skill does not grant access to Hetzner, GoAsterWeb, Telegram, a server,
the migration database, or any secret. It does not by itself approve a
monitor, a billable create, SSH, setup, data transfer, cutover, restart, or
deletion.

For owner-approved read-only identity checks, load `goaster-env` for local
secret lookup and `hetzner-auth` for the token-scoped project/server check.
Use the existing shell/cron or background-command and temporary-file guidance
for local workflow. Keep credentials in protected memory or files; never print
or put them in arguments, logs, URLs, model output, groups, or ordinary chat.

The migration-specific `/get_migration` command is not a secret input channel:
it sends an already staged handoff document directly to the paired owner's
private chat and never forwards it to the agent.
