# GoAster migration handoff notes

Status: the GoAster-side guidance and narrow Telegram delivery helper are
implemented locally. No live migration, provider request, deployment, SSH
connection, or credential access was performed here.

The paired GoAsterWeb implementation is present in the sibling
`GoAsterWeb/migration-handoff-20261112` checkout at local commit `ef8bf6e`.
That commit is not deployed. Its exact routes are used as a contract reference,
not as evidence that a production origin exists.

## GoAster-side scope

- `skills/goaster-migration` gives the owner-approved flow and invokes the
  root-only `goaster-telegram migration` helper: read-only old-host inventory,
  a new BotFather bot, a verified current CX/CAX target such as 4 GB or 8 GB,
  all eligible locations by default, one approved 30-minute monitor, exactly
  one create, and manual reconciliation on every error or timeout.
- The helper remains read-only until the exact approved target matches. It then
  generates a fresh Ed25519 login key, registers the web grant and provider
  SSH key, and persists the exact returned server name and migration label.
  The durable state and grant/key material are protected at
  `/var/lib/goaster-telegram/migration-state`; the owner URL fragment and
  private key are staged exclusively at
  `/var/lib/goaster-telegram/migration-handoff`, never in a temporary
  directory, argument, log, or model output.
- The paired Telegram adapter accepts `/get_migration` only from the paired
  owner in a private chat. It sends the mode-0600 staged handoff file directly
  as `goaster-migration-handoff.txt`; it does not call the agent or model and
  rejects unauthorized users, groups, and messages with attachments. A
  successful send removes the exact file identity read; failure retains it and
  expiry cleanup never removes a replacement file.
- Data selection, fresh OpenAI and Telegram setup, manual copying, cutover,
  old-agent stop, and server deletion remain owner-controlled. No GoAster-owned
  provisioning or transfer service was added.

The helper persists a fingerprint of the exact approved job and serializes
invocations with a protected lock. Its `creating` state is written before the
single provider `POST /servers`; a restart in any mutation state is terminal
and manual rather than a replay. It returns only safe IDs/status and instructs
the existing cron tool to stop the exact job after `terminal` or confirmed
creation. Development tests use mocked HTTP only and cover matching-only
monitoring, parallel calls, restart, timeout, stale handoff, expiry cleanup,
and replacement protection. After expiry, its job-identity-only cleanup action
can remove the exact saved handoff without receiving a provider token.

## GoAsterWeb contract

`POST /migration/register` accepts exactly one JSON field:

```json
{"login_public_key":"ssh-ed25519 AAAA... comment"}
```

A successful `201` response contains `attempt`, `callback_secret`,
`owner_secret`, `server_label`, `owner_url`, and `expires`. The `owner_url` has
no query string; its secret is in the fragment:

```text
https://<origin>/migration#attempt=<attempt>&owner=<owner_secret>
```

`attempt` is lowercase hexadecimal. The exact server name and label returned
by the contract are:

```text
name:   goaster-migration-<first 16 characters of attempt>
label:  goaster-migration-attempt=<attempt>
```

Before SSH setup, the provider lookup must match the token-scoped project,
server ID, exact name, exact migration label, and public IPv4 address.

The one-shot callback is:

```text
POST /migration/host-key
Authorization: Bearer <callback_secret>
X-Goaster-Attempt: <attempt>
Content-Type: text/plain
body: canonical ssh-ed25519 host public key
success: HTTP 204
```

The callback user-data contains only the callback secret, attempt, and public
HTTPS origin. It reads `/etc/ssh/ssh_host_ed25519_key.pub`, posts the canonical
key with `curl --config -`, does not retry a lost response, and exits unless
the response is exactly `204`. It never contains the owner secret, owner URL,
login private key, provider token, bot token, or database credential.

Owner setup uses the fragment exchange and short-lived session, exact provider
binding, a `private_key` multipart upload, and CSRF-protected JSON requests to
`/migration/owner/start` and `/migration/owner/telegram`. Machine status is
`POST /migration/status` with `{}` and the owner bearer secret; it never
returns the host key. Machine `/migration/bind` is rejected. There is no
cancel, finalize, delete, unsafe-state, or retry endpoint.

The database is trusted normal infrastructure for this contract. Its handoff
record has an absolute seven-day expiry; reads and retries never extend it.
Expiry does not delete a server. Any detectable outage, invalid response,
missing record, expiry, or conflict stops the flow and requires manual
reconciliation. The expiry-only decision is closed.

## Remaining live readiness

Before an owner approves a live run, the operator still needs to verify:

1. A public HTTPS GoAsterWeb deployment is reachable and identifies the
   reviewed handoff build corresponding to local `ef8bf6e`; the local sibling
   checkout is not a deployment.
2. The trusted database is configured server-side, isolated to this namespace,
   and its seven-day expiry and fail-closed error behavior have passed the
   GoAsterWeb offline tests and review.
3. The deployed web worker's immutable GoAster revision and destination
   preflight are identified. This repository does not provide a universal
   installer or service layout.
4. Current Hetzner availability, architecture, image, network, locations, and
   price are checked at approval time. The owner has approved one create at
   those exact terms, with no second prompt or separate price ceiling.
5. The new BotFather bot exists and is distinct from the old bot. The owner has
   a private browser/Telegram channel and a manual plan for any uncertain
   provider result; no automatic retry or deletion is allowed.
6. The installed Telegram binary includes `/get_migration`; the staged file is
   a protected regular file at `/var/lib/goaster-telegram/migration-handoff`,
   and the direct private-only delivery tests pass.

No live provider, Redis, Telegram, OpenAI, or production check is implied by
the local tests in this repository.

## Local validation

From the GoAster repository root, use the cached/offline toolchain when the
environment provides it:

```sh
go test ./...
go vet ./...
sh packaging/test-skill-assets.sh
git diff --check
```

The normal GoAster build/test guidance remains authoritative for any additional
platform-specific or runtime checks. These commands do not deploy or contact
the migration provider.
