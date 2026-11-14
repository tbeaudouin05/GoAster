# GoAster central migration

**Migration execution is currently unavailable:** trusted paired Telegram identity and durable one-use approval are not fully wired. Approval requests, monitor startup, and automatic monitor resumption are disabled pending completion.

GoAster implements the source side of the versioned central migration machine
API. It uses the existing scheduler and paired Telegram delivery path; it does
not add a migration database, a second attempt ledger, a provisioning daemon,
or a provider-create helper.

The source workflow is deliberately small:

1. The paired owner asks for an exact Hetzner server type and optional strict
   location preference.
2. GoAster performs a current, read-only public price estimate and presents
   the per-location gross hourly terms.
3. Only after explicit paired private owner approval does GoAster check
   central availability immediately and schedule the same check every 30
   minutes while no eligible location exists.
4. When availability is returned, GoAster disables and verifies the exact
   monitor, generates the login key and owner-link secret locally, and sends
   exactly one creation request.
5. GoAster stores the private key and protected handoff in a private
   `~/.goaster/tmp/migration-*` task directory and sends the setup link plus
   private key directly to the locally paired Telegram user. The model sees
   only safe status and provider identity fields.

The task directory and handoff are intentionally retained for the owner’s
manual cleanup. There is no automatic cleanup implementation.

## Central machine API

The fixed public origin is `https://goasterweb-thomas.fly.dev` with no trailing
slash. The source uses only:

```text
POST https://goasterweb-thomas.fly.dev/migration/v1/machines/availability
POST https://goasterweb-thomas.fly.dev/migration/v1/machines/create
```

Every request is bounded `application/json`, uses exact lower-case field names,
has no query string, and has no browser `Origin` header. GoAster never logs a
request body, public key, owner digest, provider token, owner URL, or private
key. The `hetzner_token` is read locally and placed only in the individual
request.

Availability sends:

```json
{
  "server_type": "cx22",
  "locations": ["fsn1", "nbg1"],
  "hetzner_token": "request-scoped"
}
```

`locations` may be absent or empty. In that case GoAsterWeb considers all
eligible locations for the exact `server_type`. Otherwise it is a strict
allow-list and is never widened by the source. The v1 response contains only
`version`, `server_type`, and eligible `locations`; it never contains a price.
GoAsterWeb does not call Hetzner pricing for this endpoint.

Creation adds the locally generated authorized-key public value and the
lower-case SHA-256 hex digest of the locally generated 32-byte unpadded
base64url owner secret:

```json
{
  "server_type": "cx22",
  "locations": ["fsn1"],
  "login_public_key": "ssh-ed25519 AAAA...",
  "owner_secret_digest": "64 lower-case hex characters",
  "hetzner_token": "request-scoped"
}
```

The private key and owner secret never cross this request. GoAsterWeb owns the
exact provider create sequence, Ubuntu 24.04 image, SSH registration, callback
secret, and Redis handoff. Its successful response has `version`, `state`,
`attempt`, `server_label`, `server_id`, `ip`, an owner URL containing only the
attempt fragment, and `expires`. GoAster appends `&owner=<owner-secret>` to
that fragment only for the protected Telegram handoff.

## Price, monitor, and uncertainty

The source’s `estimate`/`quote` operation uses read-only provider pricing data
to show a current public gross estimate, including public IPv4, for the exact
type and requested locations. It may omit locations not currently available;
those locations remain eligible if allowed by the request. This is a source-side estimate and is not sent
to or returned by GoAsterWeb. A `start` request from the paired private
Telegram conversation obtains a fresh estimate and sends the exact server
type, strict locations, currency, per-location gross prices, and one-create
statement directly to the protected paired user. The model-facing operation
does not accept an approval flag, quoted price, currency, monitor ID, chat ID,
or private material.

The owner must reply exactly `APPROVE GOASTER MIGRATION <code>` in that private
chat. The source-generated code is bound to those exact terms, is never
returned to the model, is ignored in groups, expires after ten minutes, and is
consumed before the monitor/create path. A changed, replayed, expired, missing,
or process-restarted approval fails closed without a provider mutation. After
the private approval, the model may retry `start` with the same exact type and
locations; a different request creates a new approval requirement.

The first availability request runs immediately. If its `locations` result is
empty, the existing scheduler stores one non-secret 30-minute monitor. If a
location becomes available, GoAster disables and verifies that job before
calling creation. No HTTP retry, provider delete, automatic rollback, or
second create exists. A timeout, lost response, missing details, malformed
response, or Redis/provider binding uncertainty is a manual-reconciliation
condition. Inspect the token-scoped Hetzner project and GoAsterWeb owner/status
page before any fresh owner-approved attempt.

## Protected handoff and ownership boundary

The resolved GoAster temporary root is:

```text
<real user home>/.goaster/tmp
```

Each migration uses a fresh mode-0700 task child. The Ed25519 private key and
handoff are exclusive mode-0600 files and are synced before use. The task
directory remains until the owner manually removes it after delivery or
abandonment. It is never copied into the repository, a scheduler message,
model context, log, or shared temporary directory.

The handoff is sent as a document to the positive user ID already stored in
protected Telegram pairing state. No model-selected chat or group is used.
If delivery fails, the monitor remains stopped and the protected handoff is
left for manual inspection; it is not resent automatically.

The owner then uses the normal GoAsterWeb migration page for exact provider
verification, private-key upload/match, callback host-key pinning, destination
setup, and new OpenAI/Telegram authorization. This repository does not deploy,
SSH to, install, configure, start, stop, delete, or cut over either host and
does not modify GoAsterWeb or Redis.

## Offline validation

The migration tests use only synthetic HTTP servers and local temporary
directories. They do not contact GoAsterWeb, Hetzner, Telegram, OpenAI, or
Redis.

```sh
go test ./internal/migration ./internal/chat ./internal/telegram ./cmd/goaster-agent
go test ./...
go vet ./...
sh packaging/test-skill-assets.sh
git diff --check
```
