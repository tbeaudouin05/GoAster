# Authentication and authorization

**Migration execution is currently disabled:** read-only estimates and
availability checks may be inspected, but monitor startup, provider creation,
and automatic monitor resumption are disabled. Before any future billable
operation, require ordinary explicit user approval in a private conversation.
That conversational approval is not a trusted cryptographic binding.

This skill does not grant access to Hetzner, GoAsterWeb, Telegram, a server,
SSH, or any secret. Load `goaster-env` and `hetzner-auth` for the configured
Hetzner credential source and token-scoped project checks. A read-only price
estimate and availability check do not authorize a billable creation.

The user must explicitly approve the exact server type, optional strict
location list, current public gross estimate/currency (or unavailable
pricing), and exactly one create in a private conversation before any future
billable operation. Do not require a special command, button, code, callback,
or dedicated approval record. Ordinary conversational approval expresses user
intent but is not a trusted cryptographic binding or authorization proof. A
model-supplied approval flag, price, currency, monitor ID, or chat ID is not
authorization. The disabled runtime cannot start the monitor or create a
provider resource. When execution is eventually enabled, retain the source
monitor’s terminal disable on success, error, or uncertainty. Approval does
not authorize installation, destination SSH, deletion, cutover, or a retry
after an uncertain create.

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
