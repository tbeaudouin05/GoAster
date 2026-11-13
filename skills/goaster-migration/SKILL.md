# GoAster migration

Use the registered `goaster_migration` tool. Do not use shell, generic cron,
SSH, a helper CLI, a subprocess, or a Telegram command for this workflow. No
Telegram command is added for migration; there is no Telegram command for
migration.

The fixed GoAsterWeb origin is `https://goasterweb-thomas.fly.dev`. Do not ask
for another origin, deployment confirmation, or reachability confirmation.
Do not ask for or verify the Hetzner project. The owner is responsible for the
correct provider token and project. A distinct new BotFather bot is required,
but its token is requested only by GoAsterWeb setup.

First call `quote` with the approved server type, Ubuntu 24.04 image,
architecture, networks, public-network settings, and optional location
allowlist. An empty allowlist means all compatible locations. The quote
includes the server plus approved primary-IP hourly gross fees, exact
currency, and the cheapest current baseline. Present that full baseline and
the one-create consequence to the owner before asking for approval.

After one explicit approval, call `start` once with the same target, job ID,
baseline, currency, and approval flag. There is no second create prompt. The
tool owns the persisted 30-minute monitor, disables and verifies it before
any external mutation, and performs no install, SSH, data transfer, old-host
change, or callback polling. There is no callback polling after create.

Use `status` for local safe state, `stop` for an explicit stop, and
`repair_delivery` only when the owner explicitly requests a delivery repair.
If delivery fails or is ambiguous, do not resend automatically and do not
start again: the protected bounded handoff remains for the manual repair.

The tool reads `HCLOUD_TOKEN` and Telegram credentials internally. Never place
a provider token, bot token, private key, owner URL, callback secret, or
Telegram chat ID in a request. The confirmed handoff goes only to the existing
paired private user; it is never sent to a group or a message-supplied
destination.

If the tool reports unavailable, terminal, uncertain, non-root, or manual
review, explain the safe status without repeating an action. Never retry a
possibly completed create. This workflow ends at the callback-only server
handoff with callback-only cloud-init; GoAsterWeb and the owner handle
installation and setup.
