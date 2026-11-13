# Authentication and authorization

This skill does not grant access to Hetzner, GoAsterWeb, Redis, Telegram, a
server, SSH, or any secret. It does not by itself authorize a monitor, billable
create, installation, setup, data transfer, cutover, restart, or deletion.

Load `goaster-env` for the already configured secret source and `hetzner-auth`
for token-scoped project identity and read-only provider checks. Obtain a
separate, informed approval for the exact current server terms, expected
overlap cost, one create, and the 30-minute 24/7 monitor. That approval also
authorizes terminal disable of the exact cron job on success or error; it does
not authorize SSH or GoAster setup.

Before provisioning, the owner must create a distinct new BotFather bot. Never
copy the old bot token or Telegram pairing state. The old and new agents may
coexist during validation; the owner stops the old agent only after the
cutover decision.

Keep provider tokens, Web grant secrets, owner URLs, private keys, and
database credentials in protected environment/path handling only. Never put
them in model-visible output, logs, arguments, cron messages, temporary files,
or memory supplied to the model. If existing shell/provider tools cannot
perform the required protected write and redacted response handling, stop.
Do not invent a migration executable, script, or runtime helper.

Use the existing shell for inline protected Telegram delivery as specified in
SKILL.md. The destination is the locally paired user ID, never an arbitrary
chat. Verify access without changing permissions before mutation, and stop if
the protected delivery prerequisites cannot be met. Do not add a command,
tool, script, or runtime helper.
