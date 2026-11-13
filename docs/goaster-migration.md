# GoAster migration provisioning notes

This repository now provides migration guidance as a pure skill. It has no
special provisioning CLI, migration monitor implementation, provisioning
service, replacement script, or runtime helper. No live provider request,
GoAsterWeb request, Redis access, deployment, SSH connection, or credential
access was performed for this change.

## GoAster-side responsibility

The `goaster-migration` skill is a pure workflow using existing shell, cron,
environment, and authenticated Hetzner/provider tools. Before mutation it
verifies protected delivery prerequisites, and after explicit approval it can:

1. Inspect the current project and exact target terms read-only.
2. Obtain pre-create callback authorization, register one fresh provider SSH
   key, and create exactly one explicitly approved Hetzner server.
3. Submit callback-only cloud-init and securely record the private login key
   and returned provider server ID in protected mode-0600 paths.
4. End the exact 30-minute, 24/7 create monitor on a confirmed server create or
   a create failure/uncertain create result. The monitor does not wait for or
   claim to observe the later guest callback.

It does not install, build, configure, start, or deploy GoAster; use
destination SSH; transfer data; cut over identities; stop the old host; or
delete either server. GoAsterWeb owns destination installation, setup, exact
provider binding, and the later GoAster start. GoAsterWeb was not modified.

## Fixed public GoAsterWeb service

GoAsterWeb is an existing public website available to everyone. Its canonical
origin is `https://goasterweb-thomas.fly.dev` with no trailing slash; its
service URL is `https://goasterweb-thomas.fly.dev/` with a trailing slash.
Resolve every GoAsterWeb `/migration/...` endpoint against this canonical
service, including registration, host-key callback, status, and owner-page
routes. Do not use an origin placeholder or substitute another address. The
owner is not expected to deploy, run, or own GoAsterWeb or configure Redis.
Never ask whether GoAsterWeb has been deployed or request its public HTTPS
address. Do not add a readiness question or reachability preflight. Assume the
public service is available on the normal path; if an actual HTTPS request
fails, stop and report a safe failure. Preserve TLS verification and never
blindly retry a request whose outcome is uncertain.

Before provisioning, the owner must create a distinct new BotFather bot. Do
not copy or reuse the old bot token or Telegram pairing state. The old and new
agents may coexist while the new server is validated; the owner stops the old
agent only after deciding the cutover is complete.

The protected operator paths are the existing root-owned directory and files
`/var/lib/goaster-telegram/migration-state` and
`/var/lib/goaster-telegram/migration-handoff`. The latter is an optional owner
bundle containing the exact owner URL fragment, private SSH key, and provider
server ID. If a safe protected write or redacted response path is unavailable,
the skill stops rather than inventing a helper. Secrets never appear in
model-visible output, logs, command arguments, cron messages, temporary files,
or project memory.

The fixed root-owned mode-0600 handoff file at
`/var/lib/goaster-telegram/migration-handoff` contains exactly this JSON
envelope shape, with no extra fields, and is bounded to 32 KiB:

```json
{"expires": 1735689600, "document": "owner URL and private-key text"}
```

`expires` is an absolute Unix-second expiry no more than seven days in the
future, and `document` contains the owner URL fragment, private login key, and
provider server ID. The expiry does not extend on reads. The GoAster runtime
does not read or send this file. The owner manually removes it after delivery
or abandonment; there is no automatic local cleanup. GoAsterWeb remains the
migration authority.

## Approval and monitor

Use the fixed public GoAsterWeb service above for every migration endpoint.
Its Redis store is service-managed and is not an owner setup or readiness
gate. Do not ask for deployment or address confirmation and do not add a
reachability preflight. Before any mutation, load `goaster-env` and
`hetzner-auth`, verify the token-scoped Hetzner project, and use current
read-only provider data. Confirm the exact server type and ID/name, official
Ubuntu 24.04 image, provider architecture (`x86`/`x86_64` or `arm`/`aarch64`),
location, network, public-net flags, current gross hourly price, and expected
overlap cost.

The owner must knowingly approve those exact terms and one billable
`POST /servers`. Approval to start the existing 30-minute, 24/7 monitor
through `cron` includes exactly one create at those terms. There is no second
create prompt or separate price ceiling. The exact cron job ID is recorded
without secrets. Scheduled availability checks are read-only. Once available,
create and fsync an exclusive protected claim for the one attempt, then disable
and verify that exact cron job before any callback registration, provider-key
registration, or create. If it cannot be disabled, stop without mutation and
require owner manual resolution. Stop the monitor on success, error, or
uncertainty; it never polls for callbacks.

If terms change, the target is ambiguous, or informed billing approval is
missing, stop before callback registration or provider mutation.

## Existing GoAsterWeb wire contract

The pre-create request is:

```text
POST /migration/register
Content-Type: application/json

{"login_public_key":"ssh-ed25519 AAAA... comment"}
```

`/migration/register` is callback authorization, not host handoff recording.
Its successful response contains an `attempt`, `callback_secret`,
`owner_secret`, `server_label`, fragment-only `owner_url`, and absolute
`expires`. The grant has an absolute maximum of seven days; reads and retries
do not extend it. Keep the secret fields protected and expose only safe
identity/status values.

The identity contract remains:

```text
server name:   goaster-migration-<first 16 lowercase hex characters of attempt>
server label:  goaster-migration-attempt=<attempt>
owner URL:     https://goasterweb-thomas.fly.dev/migration#attempt=<attempt>&owner=<owner_secret>
```

Before any callback registration, provider-key registration, or create, the
first available-target turn must exclusively create a protected durable claim
(`O_EXCL` or equivalent shell noclobber), write it, and fsync it. Never replace
or clear the claim. No later turn may replay it, including after interruption
or an invisible provider create. Provider inventory is reconciliation only;
an absent server does not authorize replay. On claim uncertainty, stop for
owner manual resolution. After the claim and verified monitor disablement, the
provider operation is one `POST /ssh_keys` followed by one `POST /servers`. The server request uses the
approved numeric IDs, location name, provider SSH-key ID, networks,
public-network flags, exact name and label, `start_after_create: true`, and
callback-only user-data. Never retry a request with a lost or malformed
response.

The one-shot cloud-init callback is:

```text
POST /migration/host-key
Authorization: Bearer <callback_secret>
X-Goaster-Attempt: <attempt>
Content-Type: text/plain
body: canonical first ssh-ed25519 host public-key line
success: HTTP 204
```

Cloud-init uses the fixed HTTPS URL
`https://goasterweb-thomas.fly.dev/migration/host-key`; it contains only the
canonical public service origin, attempt, and callback secret.
It reads `/etc/ssh/ssh_host_ed25519_key.pub`, sends the canonical host key
once, and does not retry a lost or non-204 response. A non-204 or lost response
fails closed on the guest. The create monitor has already ended at the
provider-create result; it does not observe callback delivery. GoAsterWeb and
the owner detect a missing callback through the existing Web handoff/status
path. There is no SSH to the guest and no new callback polling. Cloud-init
contains no owner secret, owner URL, private login key, provider token, bot
token, or database credential. GoAsterWeb records this host-key handoff in
Redis. Web then owns the handoff, provider binding, installation,
configuration, fresh authentication, and setup; this repository does not
perform those actions or SSH to the destination.

The Redis lifecycle is expiry-only with a maximum of seven days. A read,
retry, outage, missing record, malformed response, conflict, or expiry never
extends the grant. Expiry never deletes infrastructure. There is no
cancellation route, unsafe-status escape, or automatic recovery.

The existing machine status route remains `POST /migration/status` with `{}`
and the owner bearer secret; it never returns the host key. Machine
`/migration/bind` remains rejected. The owner page owns `/migration/session`,
`/migration/owner/status`, `/migration/owner/bind`, `/migration/owner/upload`,
`/migration/owner/start`, and `/migration/owner/telegram`. No cancel,
finalize, delete, unsafe-state, or retry route is added.

## Protected owner handoff delivery

The skill can use inline Python through `shell_execute` to send the document
by Telegram HTTPS directly to the locally paired user's private chat. It reads
the token, paired user ID, and handoff locally without putting secrets in
arguments or captured output. It validates protected file ownership, mode,
type, bounded content, envelope and expiry before sending, verifies TLS, and
suppresses response bodies and errors. The detailed procedure is in the skill.
The shell runs as the agent identity, so installations without access to the
protected files must stop without weakening permissions. The owner manually
removes the bundle after delivery or abandonment.

## Stop and reconcile

Stop and disable the exact create-monitor cron job on any target or price
change, unsafe protected path, unavailable secret-safe tool, provider
authentication failure, registration error (including before provider-key
registration), provider-key error, create error, timeout, lost response,
malformed response, conflict, or uncertainty; stop it on success too. Verify
disablement before any mutation. If disablement cannot be verified, make no
mutation and require owner manual resolution. After the monitor ends, a callback failure or missing callback is a
Web/owner terminal condition; it is not a monitor observation and must not
trigger SSH or new callback polling. The monitor does not wait for or claim to
observe the later guest callback. An actual HTTPS connection or TLS failure
to the canonical service is a stop-and-report condition; keep the report free
of secrets and do not retry a request whose outcome is uncertain.

Never blindly repeat a create. If a response may have created a server, the
user manually identifies the new server in the verified Hetzner project using
the exact attempt label/name, manually deletes the unwanted server if
appropriate, and starts over with a new attempt and fresh one-create approval.
There is no automatic retry, deletion, rollback, recovery, cancellation, or
second server.

## Local validation

These checks are offline and do not contact GoAsterWeb, Redis, Telegram,
OpenAI, or Hetzner:

```sh
go test ./...
go vet ./...
sh packaging/test-skill-assets.sh
git diff --check
```
