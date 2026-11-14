# GoAster central migration

Migration execution and scheduler resumption are disabled pending full cross-service acceptance. Read-only estimates and availability remain available. Before any future billable operation, obtain ordinary explicit approval in a private conversation; no separate approval record is required. Conversational approval is not a trusted cryptographic binding.

Use the source-side `goaster_migration` tool to inspect one proposed migration
against the existing GoAsterWeb service. The GoAster source implementation owns
the bounded HTTPS requests, local login-key and owner-secret generation,
30-minute availability monitor, and protected Telegram handoff. GoAsterWeb
owns the provider create sequence, callback secret, Redis handoff, destination
installation, host-key pinning, setup, and fresh OpenAI/Telegram authorization.

Do not add a migration daemon, provider-create script, Telegram command,
destination SSH step, or background-command workaround. There is no new
database or second attempt ledger. The existing scheduler file is only the non-secret monitor
schedule; it is not billing or provisioning history.

## Fixed service and v1 contract

The configured public origin is the fixed HTTPS origin
`https://goasterweb-thomas.fly.dev` with no trailing slash. Resolve these
paths against that origin:

```text
POST /migration/v1/machines/availability
POST /migration/v1/machines/create
```

Requests are bounded JSON with exact lower-case fields, no query string, no
browser `Origin` header, and `Content-Type: application/json`. The source and
diagnostic paths never log request bodies, public keys, owner digests, provider
tokens, owner links, or private keys.

Availability sends the exact requested server type, an optional strict
location allow-list, and the request-scoped Hetzner token:

```json
{
  "server_type": "cx22",
  "locations": ["fsn1", "nbg1"],
  "hetzner_token": "request-scoped"
}
```

An absent or empty location list means every eligible location for that exact
type. A non-empty list is never widened. GoAsterWeb returns only `version`,
`server_type`, and eligible `locations`; this endpoint does not return prices
and does not call the Hetzner pricing API.

Use the read-only `availability` action when a current eligibility check is
needed. Its source-side result repeats the exact location policy and requested
locations for presentation, while keeping those fields out of the Web wire
contract. It contains no price cap, approval, credential, or private material.

Creation sends only the authorized-key public value, the lower-case SHA-256
owner-secret digest of the locally generated 32-byte owner secret, the exact server
type/location request, and the request-scoped token. It does not send the
private key or owner secret. The successful response contains the v1 state,
attempt, server label, server ID, public IP, owner URL without the owner
secret, and absolute expiry. The source appends the local owner secret to the
fragment only and never puts it in a query string.

## Estimate and ordinary private-conversation approval

Before asking for approval, use `goaster_migration` with `estimate` (or its
`quote` alias) for the exact server type and optional locations. The source
performs bounded read-only provider lookups for the current public gross
hourly estimate, including the public IPv4 component used by the handoff. Do
explain that the estimate may omit locations not currently available and that
those locations remain eligible if allowed by the request. Do
not substitute a cheaper type, silently choose a different location, or claim
that the GoAsterWeb availability response contains a price.

The estimate repeats whether the request allows any eligible location or uses a
strict allow-list. Public pricing is informational and best-effort; if the
read-only provider lookup is unavailable, the tool reports unavailable pricing
and does not authorize or block a future request.

Present the returned type, strict location preference, currency, per-location
estimate, and the fact that one provider creation sequence would occur. The
model-facing `start` call accepts only the exact type and location request; it
does not accept an approval flag, quoted price, currency, monitor ID, chat ID,
or any secret.

Before any billable operation, obtain ordinary explicit user approval for the
exact server type, strict location preference, current estimate and currency
(or the fact that pricing is unavailable), and exactly one create in a private
conversation. Do not treat approval in a group conversation as sufficient.
Do not invent or require a special command, exact reply, button, code, callback,
or dedicated approval record. Ordinary conversational approval is a workflow
requirement only; it does not provide a trusted cryptographic binding between
the user, the terms, and the future provider request.

The runtime relies on that private-conversation workflow; it does not accept
or create a separate approval mechanism. When execution is enabled after acceptance, call `start` only after the owner has
approved the exact terms conversationally. The source preflights the protected
paired-Telegram delivery path before the create request.

## Monitor and one create

After the required conversational approval, the source checks availability
immediately. If no eligible
location is returned, it leaves one non-secret scheduler job enabled at
exactly 30-minute intervals. The token is loaded only when a check runs; it is
never placed in the scheduler message. A ready result disables that exact job
and verifies the disabled state before the create request. Availability errors,
monitor state errors, and disablement uncertainty stop the workflow for manual
review.

The create endpoint is called once. GoAsterWeb uses the exact server type,
strict location allow-list, Ubuntu 24.04, the generated public key, and its
existing callback user-data. The source does not retry the HTTP request,
inspect provider pricing during creation, delete infrastructure, or infer
that an absent server makes replay safe. A timeout, lost response, malformed
response, or uncertain provider/Web state disables the monitor and requires
manual inspection of the token-scoped Hetzner project and the GoAsterWeb
owner/status page before any new owner-approved attempt.

## Protected owner handoff

The source generates the Ed25519 login key pair and the 32-byte unpadded
base64url owner secret locally. At the owner’s explicit request, the private
key is kept in a fresh child of the resolved GoAster temporary root:

```text
<real user home>/.goaster/tmp/migration-*/id_ed25519
```

The private-key path is therefore
`<real user home>/.goaster/tmp/migration-*/id_ed25519`.

The temporary root and task directory are real private directories; the task
directory is mode `0700` and key/handoff files are mode `0600`, created
exclusively and synced before use. This migration flow deliberately keeps the
key and handoff in that private task directory until the owner removes them;
there is no automatic cleanup implementation. Do not place them in the
repository, a shared system temporary directory, a scheduler message, logs,
model context, or ordinary chat.

After a successful create, the source builds a protected handoff containing
the setup link with the owner secret, server identity, and private login key.
It sends that document directly as a Telegram document to the already paired
positive user ID loaded from protected local Telegram state. No model-selected
chat ID or group destination is accepted. The private key and setup link must
never appear in model output; the tool returns only safe status and provider
identity fields.

If direct delivery fails, leave the protected handoff for manual inspection,
keep the monitor stopped, and do not resend automatically. The owner removes
the temporary migration directory after delivery or abandonment.

## Scope boundary

The owner uses the normal GoAsterWeb migration page for exact provider
verification, private-key upload/match, callback host-key pinning, destination
setup, and new authorization. This source workflow does not deploy, SSH to,
install, configure, start, stop, delete, or cut over either GoAster host. It
does not modify GoAsterWeb or Redis and does not create a second provisioning
record.
