# Authentication and authorization

This is a pure guidance skill. It grants no access to Hetzner, GoAsterWeb,
Upstash, Telegram, a server, or a secret, and it does not authorize a cloud
operation, schedule, deployment, transfer, restart, cutover, or deletion.

If an owner later requests a read-only identity check, use the existing
`goaster-env` skill for secret lookup and the existing `hetzner-auth` skill for
Hetzner project and server verification. Keep credentials in memory and never
print, save, copy, or put them in logs, commands, archives, links, or chat.
Authentication and target verification do not authorize a server change or
cost.

For current Telegram command behavior, use the existing `telegram-commands`
skill and its runtime reference. Do not infer a migration command or a
private-key delivery capability from a command name.
