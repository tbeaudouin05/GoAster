# GoAster migration provisioning

This is a pure skill workflow for provisioning one new Hetzner Cloud server
for a GoAsterWeb-backed handoff. Use the existing `shell_execute`, `cron`, and
authenticated Hetzner/provider tools. There is no migration executable,
provisioning service, replacement script, or background runtime helper.

This workflow is skill-led and uses existing shell/cron/provider tools only.

The skill has one responsibility: after explicit approval, create exactly one
matching Hetzner server with callback-only cloud-init and securely record the
private SSH key (login key) and provider server ID. GoAsterWeb owns
installation, setup, host-key handoff processing, and the later GoAster start.

The skill does not install, build, configure, start, deploy, or transfer
GoAster; open destination SSH; copy data; stop the old agent; or delete either
server. Do not use `background_command` for this workflow: its arguments and
output are not secret-safe.

## Before provisioning: create a distinct new bot

Before any callback registration, provider-key registration, monitor, or server
create, the owner must create a distinct new BotFather bot and keep its new
token in the owner-controlled setup path. Never copy or reuse the old bot
token, Telegram pairing state, or other Telegram identity. The old and new
agents may coexist while the new server is validated; only the owner stops the
old agent after the cutover decision.

## Authorization and secret boundary

Load `goaster-env` and `hetzner-auth` before credentialed inspection. Verify the
intended Hetzner project and current target terms. Authentication and read-only
lookups do not authorize a billable create.

Never expose a provider token, callback secret, owner secret, owner URL,
private key, database credential, or rendered cloud-init in model-visible
output, logs, command arguments, cron messages, ordinary chat, groups, URLs,
temporary files, or project memory. Keep secret values in the configured
protected environment or protected mode-0600 paths. The command must use
variable references and redirected protected output, not secret values in its
arguments. If the available shell/provider tools cannot keep a response or
private key out of output and memory, stop before any mutation. Do not invent
a helper to work around that limitation.

Use the existing root-owned `/var/lib/goaster-telegram` protected directory
for operator-managed migration records when it is present and verified:

- `/var/lib/goaster-telegram/migration-state` records the approved attempt,
  exact target, provider server ID, and non-secret status alongside protected
  material as required by the local workflow.
- `/var/lib/goaster-telegram/migration-handoff` is the optional owner bundle
  for the exact owner URL fragment, private SSH key, and provider server ID.

The fixed handoff file is a root-owned, mode-0600 regular file at
`/var/lib/goaster-telegram/migration-handoff`. Its exact local envelope is one
JSON object with no extra fields:

```json
{"expires": 1735689600, "document": "owner URL and private-key text"}
```

`expires` is an absolute Unix-second expiry and `document` is the text sent to
the owner. `/get_migration` performs a protected bounded read of at most 32
KiB, rejects a missing or invalid expiry, an expired envelope, an expiry more
than seven days in the future, malformed/extra JSON, or an oversized file, and
sends only `document`. It never provisions, reads `migration-state`, extends
expiry, or removes the file. Web remains the migration authority; the owner
manually removes this local handoff after delivery or abandonment.

Inspect names, types, owners, and modes without reading existing secret
contents. Create files exclusively with `umask 077` and mode `0600`; never
replace an existing record whose attempt or identity is not certain. Do not
use the temporary-file skill for migration keys, grants, or tokens. If these
protected paths or a safe write/record operation are unavailable, stop.

## Approval gate and monitor

1. Confirm that the deployed GoAsterWeb origin is a public HTTPS service using
   the reviewed migration contract and that its Redis store is available as
   trusted normal infrastructure. Local source or an undeployed checkout is
   not evidence of a reachable service.
2. Use read-only provider operations to confirm the token-scoped project,
   current server type and ID/name, official Ubuntu 24.04 image, supported
   provider architecture (`x86` for `x86_64` or `arm` for `aarch64`), exact
   location and network choices, public-network flags, current gross hourly
   price, and expected overlap cost. Do not rely on a remembered offer.
3. Present those exact terms and the fact that the operation is one billable
   `POST /servers`. Obtain informed approval before registering a callback,
   registering a provider key, or creating anything. Approval to start the
   monitor includes exactly one create at those terms; there is no second
   create prompt or separate price ceiling.
4. Use the existing `cron` tool to add one non-overlapping chat-turn monitor
   every 30 minutes, 24/7. The cron message may contain only the non-secret
   job ID and workflow instructions. Record the returned exact job ID. While
   the target is unavailable, scheduled availability checks are read-only. As
   soon as it is available, make the durable exclusive attempt claim below,
   then disable that exact cron job and verify it is disabled before any
   callback registration, provider-key registration, or create. If disablement
   cannot be verified, stop without mutation and require owner manual
   resolution. Success and every error or uncertainty also stop the monitor;
   it never polls for the guest callback.

A suitable approval is:

> I approve one create for Hetzner project `<project>`, server type
> `<type and ID>`, Ubuntu 24.04 image `<image and ID>`, provider architecture
> `<x86 or arm>`, location `<location>`, network `<network>`, public-network
> flags `<flags>`, at the current gross price `<price>` and expected overlap
> cost `<cost>`. I approve the 30-minute 24/7 monitor, and its approval
> includes exactly one create at these terms.

Do not proceed if the owner changes any term, has not understood the current
price/overlap, or cannot approve the exact one-create boundary.

## Provision the one approved server

Each monitor turn starts with read-only inspection of the protected records and
the exact provider project. Check the exact migration label and name before
creating. Provider inventory is reconciliation only; absence of a matching
server does not prove that replay is safe.

When the approved target first becomes available, create one protected durable
attempt-claim file exclusively (for example, `O_EXCL` or shell noclobber),
write and fsync it before any mutation. The claim identifies this approved
attempt and is never replaced or cleared to enable replay. If a claim already
exists, or its creation, contents, or durability are uncertain, do not
register a callback, register a provider key, or create a server. No later
monitor turn may replay a claimed attempt, including after interruption or an
invisible provider create. Reconcile manually with the owner; do not infer
replay permission from provider inventory.

### 1. Prepare the callback grant and login key

Generate one fresh Ed25519 login key with the existing shell tool directly in
the protected path. Keep the private half there; use only the canonical public
key for the Web and provider requests. Do not use destination SSH or ask the
model to read the private half.

Call the existing GoAsterWeb contract before the provider create:

```text
POST /migration/register
Content-Type: application/json

{"login_public_key":"ssh-ed25519 AAAA... comment"}
```

This pre-create request is callback authorization only. It is not host handoff
recording. A successful response authorizes the exact attempt and contains an
`attempt`, `callback_secret`, `owner_secret`, `server_label`, fragment-only
`owner_url`, and absolute `expires`. Keep every secret response field in the
protected record; return or display only non-secret status/identity fields.
The expiry is absolute and never extends on reads or retries; it is at most
seven days.

The returned identity must remain exact:

```text
server name:   goaster-migration-<first 16 lowercase hex characters of attempt>
server label:  goaster-migration-attempt=<attempt>
owner URL:     https://<origin>/migration#attempt=<attempt>&owner=<owner_secret>
```

Do not put the owner secret or owner URL in provider user-data, a command
argument, a cron message, or a log. If the registration response cannot be
captured safely, disable the exact monitor and stop before provider mutation.

### 2. Register the provider login key

Using the existing authenticated provider tool, make one `POST /ssh_keys` for
the canonical public key. Use the exact returned migration server name and
the label `goaster-migration-attempt=<attempt>`. Verify the returned key ID,
name, public key, and label before using it for the server. Do not retry a
request whose response is lost or malformed. On any key-registration error or
uncertainty, disable the exact monitor and stop.

### 3. Submit exactly one server create

After a fresh read confirms the approved offer is still available and the
provider project is unchanged, submit one `POST /servers` with the numeric
approved server-type and image IDs, selected approved location name, provider
SSH-key ID, approved networks and public-network flags, exact migration name
and label, `start_after_create: true`, and the callback-only user-data below.
The request must not contain the provider token outside its protected
authentication mechanism, and user-data must not contain the owner secret,
owner URL, private login key, provider token, bot token, or database
credential.

Record the returned positive provider server ID immediately in the protected
state and owner bundle without printing the response body. The ID, exact name,
label, project, and public IPv4 are the identity used by GoAsterWeb later.
After a confirmed create, the exact cron job must already be disabled and
verified; do not invoke another create.

## Callback-only cloud-init

Render this one-shot final-stage payload only inside the secret-safe provider
request. It does not install packages or GoAster. It sends only the new
server's canonical Ed25519 host public key to GoAsterWeb, once, and exits on
anything other than HTTP `204`:

```sh
#!/bin/sh
set +x
umask 077
command -v curl >/dev/null 2>&1 || exit 1
[ -r /etc/ssl/certs/ca-certificates.crt ] || exit 1
[ -r /etc/ssh/ssh_host_ed25519_key.pub ] || exit 1
keyfile=$(mktemp /run/goaster-hostkey.XXXXXX) || exit 1
trap 'rm -f "$keyfile"' EXIT HUP INT TERM
awk 'NR == 1 && $1 == "ssh-ed25519" && NF >= 2 {print $1 " " $2; ok=1} END {if (!ok) exit 1}' \
  /etc/ssh/ssh_host_ed25519_key.pub >"$keyfile" || exit 1
status=$(curl --silent --max-time 10 --connect-timeout 5 \
  --output /dev/null --write-out "%{http_code}" --config - <<EOF
url = "${GOASTERWEB_ORIGIN}/migration/host-key"
request = "POST"
header = "Authorization: Bearer ${CALLBACK_SECRET}"
header = "X-Goaster-Attempt: ${ATTEMPT}"
header = "Content-Type: text/plain"
data-binary = "@$keyfile"
EOF
)
[ "$status" = "204" ]
exit $?
```

The callback is `POST /migration/host-key` with the callback bearer, exact
attempt header, `text/plain` canonical host key body, and no retry. The create
monitor has already ended on confirmed server create or create failure, does
not wait for or claim to observe the later guest callback, and makes no
callback-observation claim. The guest treats a non-204 or lost
response as failure-closed and exits. GoAsterWeb and the owner detect a
missing callback through the existing Web handoff/status path and stop for
manual reconciliation. There is no destination SSH and no new callback
polling. GoAsterWeb records this host-key handoff in Redis and then owns exact
server binding, installation, configuration, fresh authorization, and setup.
The skill does not SSH to or start the destination.

## Owner handoff and lifecycle

Keep the owner URL as a fragment-only URL and never put it in a query string.
The owner uses GoAsterWeb's page to exchange the fragment, verify the
token-scoped project/server ID/name/label/public IPv4, and upload the private
key. The page owns installation and setup; this skill does not run those
steps.

The existing machine status contract remains `POST /migration/status` with
`{}` and the owner bearer secret; it never returns the host key. Machine
`/migration/bind` remains rejected. The owner page owns
`/migration/session`, `/migration/owner/status`, `/migration/owner/bind`,
`/migration/owner/upload`, `/migration/owner/start`, and
`/migration/owner/telegram`. Do not add cancel, finalize, delete, unsafe-state,
or retry routes.

The existing `/get_migration` Telegram command may remain only as transport:
the paired owner in a one-to-one private chat can request the protected
`goaster-migration-handoff.txt` document. It reads the fixed mode-0600 handoff
envelope directly, never sends the envelope through the agent/model, rejects
groups, unauthorized users, and attachments, and does not provision, inspect
Redis, extend expiry, or perform cleanup. It sends only the envelope's
`document` text and never sends it through the agent/model. Remove the
protected bundle manually using the existing shell workflow after the owner
has it or when the attempt is abandoned. If safe private delivery cannot be
provided, stop and document that limitation; do not add an executable or
helper script.

The Web/Redis lifecycle is expiry-only with an absolute maximum of seven days
(7 days):
reads and retries never extend it, expiry never deletes infrastructure, and a
database outage, missing/expired record, malformed response, or conflict is a
failure. There is no cancellation route, unsafe-status escape, or automatic
recovery. Do not invent one.

## Stop and reconcile

Stop immediately and keep the old host unchanged when the Web origin, Redis
state, project, offer, price, identity, protected path, or secret handling is
uncertain. Also stop on any registration, provider-key, create, callback,
timeout, malformed response, authentication error, or monitor error. On every
error or uncertainty, including pre-create registration/key failures, disable
the exact monitor; success also stops it. If disablement cannot be verified,
make no mutation and require owner manual resolution. A callback failure or
missing callback is detected by GoAsterWeb and the owner after that monitor
has ended; do not claim monitor observation, SSH to the guest, or add callback
polling.

Never blindly repeat a create. If a response may have created a server, the
user must manually identify the unwanted new server in the verified Hetzner
project by the exact attempt label/name and manually delete it if appropriate,
then start over with a new attempt and fresh one-create approval. No automatic
delete, retry, rollback, recovery, or second server is allowed.

## Explicitly out of scope

This skill does not build or install GoAster, configure or start services, use
destination SSH, copy files or credentials, cut over Telegram/OpenAI
identities, stop the old consumer, delete the old server, create backups, or
modify GoAsterWeb. Those are separate owner-controlled workflows.
