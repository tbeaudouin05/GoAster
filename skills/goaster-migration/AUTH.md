# Authentication and authorization

**Migration execution is currently unavailable:** trusted paired Telegram identity and durable one-use approval are not fully wired. Approval requests, monitor startup, and automatic monitor resumption are disabled pending completion.

This skill does not grant access to Hetzner, GoAsterWeb, Telegram, a server,
SSH, or any secret. Load `goaster-env` and `hetzner-auth` for the configured
Hetzner credential source and token-scoped project checks. A read-only price
estimate and availability check do not authorize a billable creation.

The owner must explicitly approve the exact server type, optional strict
location list, current public gross estimate/currency, and exactly one create
from the paired private owner conversation before the `start` action. The
source sends the exact terms directly to the protected paired private user;
the owner must reply exactly `APPROVE GOASTER MIGRATION <code>`. The code is
not returned to the model, is rejected in groups, expires after ten minutes,
and is consumed once before the monitor/create path. A model-supplied approval
flag, price, currency, monitor ID, or chat ID is not authorization. That
approval includes the source monitor’s terminal disable on success, error, or
uncertainty. It does not authorize installation, destination SSH, deletion,
cutover, or a retry after an uncertain create.

The provider token is request-scoped and stays in protected local handling.
The owner secret, digest, private login key, owner URL, and callback material
never appear in model output, scheduler messages, logs, temporary files other
than the explicitly protected migration task directory, or ordinary chat.
The source sends the protected handoff only to the locally paired Telegram
user ID. If that protected delivery path is unavailable, stop before the
create request.

The migration task directory is a fresh private child of
`<real user home>/.goaster/tmp`; preserve it for manual owner cleanup. Do not
weaken permissions, override `HOME`, use a shared temporary directory, add a
new command/helper, or invent a second attempt ledger.
