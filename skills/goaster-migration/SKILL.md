# GoAster migration

Use this skill to guide the paired owner through a GoAsterWeb-backed move to a
new Hetzner server. It is pure guidance: GoAster does not add a provisioning
service, copy data automatically, cut over the agents, stop the old host, or
delete a server. Use the existing shell, cron/background-command, environment,
and Hetzner skills for the workflow.

## Scope and current implementation status

The GoAsterWeb side is implemented locally at commit `ef8bf6e` in the sibling
`GoAsterWeb/migration-handoff-20261112` checkout, but it is not deployed. The
local implementation and [the GoAster-side contract notes](../../docs/goaster-migration.md)
are the reference for a future live readiness check; a local checkout is not a
reachable service. This task performs no live registration, monitoring,
provisioning, SSH, deployment, or credential access.

The handoff uses a trusted, normally operated database with an absolute
seven-day expiry. Expiry is the only lifecycle mechanism: reads and retries do
not extend it, and expiry never deletes infrastructure. Any detectable database
outage, invalid response, lost record, or conflicting record fails closed.
There is no cancellation route and no unsafe-status escape.

## Authorization and privacy

The only supported operator is the already paired owner in a one-to-one
private Telegram chat. A paired account in a group is not private. Never ask
for or echo a key, owner URL, bot token, provider token, callback secret, or
database credential in model output, ordinary chat, a group, a URL query, a
log, a command argument, or project memory.

Before an owner-approved live workflow, load the existing `goaster-env`,
`hetzner-auth`, `read-write-temp-files`, and shell/cron or background-command
guidance as applicable. Those skills authenticate or run local commands; they
do not add approval for a billable server. Keep secret values in memory or in
protected local files only.

## Default owner flow

### 1. Inspect the old host, read-only

Start with the actual host. Do not infer paths from the GoAsterWeb installation
notes. These commands collect names and state without printing file contents or
process arguments that could contain credentials:

```sh
hostnamectl --static
uname -m
. /etc/os-release && printf '%s %s\n' "$ID" "$VERSION_ID"
df -hT
sudo systemctl list-unit-files --type=service | grep -Ei 'goaster|telegram' || true
sudo systemctl show goaster-agent.service goaster-telegram.service \
  -p Id,LoadState,ActiveState,SubState,User,Group,FragmentPath,DropInPaths \
  2>/dev/null || true
ps -eo pid,user,comm,state --sort=pid | grep -Ei 'goaster|telegram' || true
find "$HOME/.goaster" /etc/goaster /var/lib/goaster-telegram \
  -xdev -maxdepth 4 -printf '%M %u:%g %s %p\n' 2>/dev/null | sort
```

Inventory configuration, memory, skills, schedules, workspaces, pairing and
authentication *names, permissions, sizes, and classifications* only. Do not
`cat` environment files, OAuth files, bot tokens, pairing state, private keys,
or saved conversations. Record each item as required, optional,
reauthenticated, excluded, or owner-owned external data. Keep the old server
unchanged and available for recovery.

### 2. Create the new bot before the new server

The owner creates a NEW BotFather bot first. The new token must be distinct
from the old bot token. Do not copy the old Telegram token, pairing state, or
OAuth state. The old and new agents have independent identities and may run
at the same time; the owner decides when to stop the old consumer.

### 3. Choose and approve one current target

Use a current, authenticated Hetzner read-only lookup. A CX or CAX target with
4 GB RAM is a reasonable baseline; 8 GB gives more headroom. These are examples,
not hardcoded product or architecture requirements. Verify the current offer's
architecture, image, network, hourly price, and availability. Use all eligible
locations by default unless the owner specifies a narrower preference.

The owner approves the exact server type, architecture, image, network, all
eligible locations, current hourly price, and expected overlap cost. Approval
to start the monitor also approves exactly one create operation at those terms:
there is no second create prompt and no separate price ceiling.

### 4. Generate a fresh key and register before creation

Generate a dedicated Ed25519 login key in the protected temporary directory
from `read-write-temp-files`; never print the private half:

```sh
umask 077
ssh-keygen -q -t ed25519 -N '' -f "$MIGRATION_DIR/login" -C goaster-migration
ssh-keygen -y -f "$MIGRATION_DIR/login" > "$MIGRATION_DIR/login.pub"
chmod 600 "$MIGRATION_DIR/login" "$MIGRATION_DIR/login.pub"
```

Register the canonical `ssh-ed25519` public key with the deployed GoAsterWeb
origin before creating a server. Capture the JSON into a mode-0600 file; do
not let the response reach model output or stderr:

```sh
jq -n --arg key "$(cat "$MIGRATION_DIR/login.pub")" \
  '{login_public_key:$key}' > "$MIGRATION_DIR/register.json"
status=$(curl --silent --output "$MIGRATION_DIR/grant.json" \
  --write-out '%{http_code}' --header 'Content-Type: application/json' \
  --data-binary @"$MIGRATION_DIR/register.json" \
  "$GOASTERWEB_ORIGIN/migration/register")
[ "$status" = 201 ] || exit 1
chmod 600 "$MIGRATION_DIR/grant.json"
```

The successful response contains `attempt`, `callback_secret`, `owner_secret`,
`server_label`, `owner_url`, and `expires`. Keep it private. The exact server
name is `goaster-migration-<first 16 lowercase hex characters of attempt>`;
use the returned `server_label` unchanged. The provider label must be exactly:

```text
goaster-migration-attempt=<attempt>
```

Do not substitute the normal setup label or an address-only identity.

### 5. Monitor and create exactly once

After registration, use the existing cron/shell or background-command workflow
for one non-overlapping availability check every 30 minutes, 24/7, with no
fixed stop date. Do not build a GoAster provisioning daemon or start a monitor
in this repository. The check is read-only until one offer matches every
approved term; then it immediately submits the one authorized create request.

The create request must use the exact returned name, the exact migration label,
the fresh public key, and the callback user-data below. The owner approval
covers this one create at the approved terms. On any availability error,
authentication error, timeout, inconsistent result, or creation error, stop
the monitor and manually reconcile the exact attempt. An unknown create result
is never a reason to retry. Do not retry automatically and never delete a
server automatically. A confirmed successful create ends the monitor.

### 6. Use the current callback user-data contract

The callback is a one-shot cloud-init final-stage script. It posts only the
new server's Ed25519 host public key; it does not retry an ambiguous response.
Provider user-data can be retained by the provider, so it may contain only the
callback secret, attempt, and public HTTPS origin. It must never contain the
owner secret, owner URL, login private key, Hetzner token, bot token, or
database credential.

The current web contract is equivalent to this template. Substitute values
locally without printing the rendered script:

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
  /etc/ssh/ssh_host_ed25519_key.pub > "$keyfile" || exit 1
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

The callback request is `POST /migration/host-key` with
`Authorization: Bearer <callback_secret>`, `X-Goaster-Attempt: <attempt>`,
`Content-Type: text/plain`, and the canonical first line of
`/etc/ssh/ssh_host_ed25519_key.pub`. A non-204 or lost response stops the
attempt; do not replay the callback blindly.

### 7. Complete the owner handoff in the browser

Keep the returned owner URL as a fragment-only URL:

```text
https://<deployed-web-origin>/migration#attempt=<attempt>&owner=<owner_secret>
```

Open it only in the owner's browser. Never put the fragment in a query string,
Telegram group, model message, or log. GoAsterWeb exchanges the fragment for a
short-lived secure session, verifies the exact token-scoped Hetzner project,
server ID, name, migration label, and public IPv4, and then binds that server.
The machine `/migration/bind` route is deliberately rejected; do not invent a
bearer binding request.

The owner uploads the dedicated private key through the web page as the
`private_key` multipart file. The page then performs the final provider and
host-key checks and starts the fresh setup. It performs a fresh OpenAI device
login and accepts the new BotFather token through the owner page. The new bot
is paired in a private chat. GoAsterWeb sends neither the private key nor the
owner secret through cloud-init.

If using the GoAster Telegram adapter for delivery, stage the complete owner
URL fragment and private key in the root-only mode-0600 file
`/var/lib/goaster-telegram/migration-handoff` using an approved local step
without printing its contents or passing them as arguments. The already paired
owner can send `/get_migration`; GoAster sends the fixed document
`goaster-migration-handoff.txt` directly, does not call the agent, and rejects
groups, unauthorized users, and messages with attachments. A missing or failed
delivery returns only a generic error. This helper does not generate keys or
call GoAsterWeb.

### 8. Move data and retire the old host manually

GoAsterWeb installs a fresh independent target and authorizes fresh OpenAI and
Telegram identities. It does not copy GoAster data. The owner chooses the
selected files, checks permissions, symlinks, timestamps and checksums, and
reauthenticates secrets that should not be copied. Do not copy the old bot
token, pairing state, OAuth credentials, private keys, or saved environment by
default. Review schedules and integrations for duplicate effects.

Validate the new agent while the old one remains available. Only the owner
decides when to stop the old agent and when to delete the old server. Deletion
is permanent and manual; no migration error, handoff expiry, or callback
failure deletes either server. No backup or snapshot is required for this
manual move, though the owner may choose an independent recovery plan.

## Exact web contract used by this skill

Machine routes are public-HTTPS-only `POST` requests. Registration is JSON:

```json
{"login_public_key":"ssh-ed25519 AAAA... comment"}
```

The callback uses the headers and text body above. Owner status uses
`POST /migration/status` with the owner bearer secret and `{}`; it never
returns the host key. `/migration/bind` is rejected for machine callers.
The owner page owns `/migration/session`, `/migration/owner/status`,
`/migration/owner/bind`, `/migration/owner/upload`, `/migration/owner/start`,
and `/migration/owner/telegram`. There is no cancel, finalize, delete,
unsafe-state, or retry endpoint. Database errors and malformed/expired state
are service failures, not permission to continue.

## Stop and reconcile

Stop immediately and keep the old host unchanged when any of these occur:

- GoAsterWeb is not confirmed deployed at a public HTTPS origin, or the
  database is unavailable, expired, conflicted, or returns an invalid state.
- The offer differs from the approved type, architecture, location, image,
  network, or price.
- Registration, callback, provider creation, or any response times out or
  returns an error. Do not retry a billable or ambiguous mutation.
- The exact server name, migration label, token-scoped project, server ID,
  public IPv4, host key, or login key does not match.
- Manual transfer, fresh authorization, pairing, or validation fails.

For a possibly created server, reconcile in the exact Hetzner project by the
exact `goaster-migration-attempt` label and returned name. Do not create a
second server and do not delete the first automatically. The owner decides
what to retain or delete after identifying it and understanding the permanent
data-loss warning.

## Future live-readiness preflight

Before any live workflow, confirm all of the following in the owning systems:

1. The public HTTPS GoAsterWeb deployment is the reviewed handoff build
   corresponding to local `ef8bf6e` (and its deployment documentation names the
   actual deployed revision and origin); local code alone is insufficient.
2. The configured database is available as trusted normal infrastructure,
   isolated to this namespace, with the seven-day expiry and fail-closed error
   behavior exercised. Do not reopen the expiry-only design or add cancellation.
3. GoAsterWeb's offline migration tests and security review pass, including
   exact provider binding, first host-key callback, private-key/public-key
   matching, strict SSH pinning, the permanent marker, and no automatic
   cleanup/retry path.
4. The GoAster binary revision and destination preflight used by the deployed
   web worker are explicitly identified. This GoAster repository does not
   deploy or claim a universal installer/service layout.
5. The paired-owner Telegram adapter contains the direct `/get_migration`
   document path, its staging file is mode 0600, and unauthorized/group/
   attachment/send-error tests pass. No handoff secret is forwarded to the
   agent or model.
6. The owner has created the distinct new BotFather bot, selected the exact
   current target terms, approved one create, and prepared a private manual
   reconciliation plan for every timeout or error.

This preflight is a live-readiness gate, not a permanent architecture blocker:
current target availability and architecture are checked from authoritative
provider data at the time of approval.
