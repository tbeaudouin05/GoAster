# GoAster migration tool

This repository contains a small, in-process migration tool for the installed
root `goaster-agent`. It prepares one new Hetzner Cloud server for the
existing GoAsterWeb handoff. It does not install GoAster, use SSH, copy data,
change the old host, deploy GoAsterWeb, or add a Telegram command.

The model-facing tool is `goaster_migration`. Its actions are `quote`,
`start`, `status`, `stop`, and the explicit/manual `repair_delivery` action.
The tool accepts only plain target terms and a stable job ID. It never accepts
a provider token, bot token, private key, owner URL, callback secret, or chat
ID. `HCLOUD_TOKEN` is loaded internally from the existing localenv store or
the process environment.

## Runtime shape

The agent registers the tool during `goaster-agent serve` and starts a
migration-owned persisted scheduler at:

```text
<GoAster artefacts root>/state/migration-monitor/jobs.json
```

Each approved monitor is one exact `every 1800 seconds` job. Its saved
message contains only the safe migration job ID. The normal generic cron tool
does not own this monitor. Start, status, stop, and delivery repair require
the installed root runtime; a non-root agent returns an explicit unavailable
result without attempting `sudo` or deployment changes. If the scheduler,
protected Telegram delivery, or provider credential capability is unavailable,
`start` fails before any cloud mutation. Unrelated agent startup continues if
the optional migration scheduler cannot initialize.

The Web origin is fixed and never supplied by the model. Do not ask for
another origin:
`https://goasterweb-thomas.fly.dev`. The production HTTP client verifies TLS,
does not use a proxy, and does not follow redirects. Tests use local synthetic
HTTP servers only. Do not ask for or verify the Hetzner project; the owner is
responsible for using the correct token and project.

## Quote and approval

`quote` performs bounded read-only provider lookups. It checks the official
server type collection, Ubuntu 24.04 system image, compatible architecture,
currently available locations, and pricing. An empty location allowlist means
all compatible locations; a non-empty allowlist restricts the comparison.

The quote adds the server hourly gross price to every approved primary-IP
hourly gross fee. It compares decimal strings exactly, keeps the provider
currency, and returns the cheapest current baseline. On `start`, an eligible
offer must be no more than exactly 110% of that approved baseline in the same
currency. Missing, duplicate, ambiguous, malformed, or mismatched pricing is
rejected safely.

The skill must present the complete returned baseline, currency, selected
target terms, location policy, and the fact that there will be one billable
server create before asking for approval. There is one approval gate and no
second create prompt. The target keeps the approved server type, image,
architecture, networks, IPv4/IPv6 public settings, and primary-IP setting.

A maintainer-level request shape is:

```json
{
  "action": "quote",
  "job_id": "migration-20261113-01",
  "target": {
    "server_type_id": 22,
    "server_type_name": "cx22",
    "architecture": "x86",
    "image_id": 123,
    "image_name": "ubuntu-24.04",
    "location_allowlist": [],
    "network_ids": [],
    "enable_ipv4": true,
    "enable_ipv6": false
  }
}
```

After the owner approves that exact quote, the only create-start request adds
`"action":"start"`, the same target and job ID, the returned
`approved_baseline_gross`, the returned `approved_currency`, and
`"approved":true`. A non-approved start is a safe no-op.

## Durable one-create state machine

The first approved start durably records the request before scheduling. Every
mutation boundary is then protected by the root-owned state file and lock:

1. An available eligible offer causes the exact migration scheduler job to be
   disabled and verified before any Web or provider mutation.
2. The state is saved as `web_registering` before `/migration/register`.
3. The state is saved as `provider_key_registering` before `POST /ssh_keys`.
4. The state is saved as `creating` before `POST /servers`.
5. A confirmed server ID is saved before the handoff is delivered.

After restart, `web_registering`, `registered`, `provider_key_registering`,
`key_registered`, and `creating` are manual-reconciliation terminal paths;
they are never replayed. A lost, malformed, timed-out, or otherwise
ambiguous create response is terminal and cannot trigger another create. A
read-only unavailable check keeps the exact 30-minute monitor enabled. Every
terminal, error, or uncertain path disables and verifies that exact monitor.
Never retry a possibly completed create.
There is no automatic expiry-cleanup job wired.

Provider requests use the current API shapes: server type responses contain
`server_types[].locations[]` and `prices[]`; pricing contains
`pricing.currency` and `pricing.primary_ips[].prices[]`; create uses numeric
server/image IDs, a location name, SSH-key IDs, network IDs, public-net flags,
labels, callback-only `user_data`, and `start_after_create:true`. Collection
paging is handled and incomplete or ambiguous offers fail closed.

## Callback-only handoff

The only Web calls are the established migration registration/status routes:
`POST /migration/register` and `POST /migration/status`.
The new server receives callback-only cloud-init that reads the existing
Ed25519 host public key and makes one HTTPS `POST` to:

```text
https://goasterweb-thomas.fly.dev/migration/host-key
```

The callback route is `POST /migration/host-key`.

It contains no install, SSH, old-host, start, data-transfer, owner-URL,
private-key, provider-token, or bot-token operation. The owner URL and private
login key are never put in cloud-init. No callback polling is added.

## Private delivery and recovery

After a confirmed create, the migration executor sends one bounded document
containing the owner link, private login key, and provider server ID through
the existing Telegram adapter. The destination is the locally paired,
positive private user ID read from protected root-owned state; it is never a
message-supplied group or chat ID and never comes from the model. The token is
read internally during GoAsterWeb/Telegram setup and is not a tool parameter.

If delivery fails or its outcome is ambiguous, the protected bounded handoff
remains and the migration becomes `delivery_pending`. The monitor is stopped.
No automatic resend or create replay is performed. `repair_delivery` is an
explicit manual action that attempts delivery once and removes only the exact
protected handoff after a confirmed success.

The required distinct new BotFather bot remains a GoAsterWeb setup prerequisite. The
bot token is requested only by that setup flow; this tool does not ask for it
and does not expose it.

## Out of scope and validation

This feature does not add `/get_migration` or any other Telegram command. There
is no Telegram command for migration, nor does it add a
helper CLI, a subprocess boundary, sudo, service deployment, project
discovery, automatic resend, automatic recovery, delete, rollback, SSH, or
old-host changes.

Offline validation from the repository root:

```sh
PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test ./...
PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go vet ./...
PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off sh packaging/test-skill-assets.sh
git diff --check
```

These checks use synthetic/local test seams and do not contact Hetzner,
GoAsterWeb, Telegram, or production services.
