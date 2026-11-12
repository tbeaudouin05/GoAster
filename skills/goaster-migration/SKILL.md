# GoAster manual server migration

This skill is owner-managed migration guidance. It does not implement a
migration backend, provision a destination, transfer state, switch an agent,
or delete the old server.

## Hard blocker: GoAsterWeb handoff is not available

The GoAsterWeb migration/setup backend is incomplete and unverified in the
current approved scope. Treat that as a hard blocker. Until the handoff is
fully implemented, deployed by a separately approved process, and verified
against the acceptance requirements below, the agent must stop at guidance
and read-only preflight.

Do not instruct an agent to register, schedule, or create a server until the
GoAsterWeb handoff is fully available and verified. It is not fully available
or verified in this checkout.

The agent must not:

- register a migration handoff or create a callback, owner-access, Redis, or
  setup session;
- schedule or run an availability monitor, cron job, or recurring check for
  this migration;
- create, reserve, allocate, rebuild, or otherwise request a Hetzner server;
- generate or deliver the migration SSH key, put a callback secret in
  provider user-data, or send a migration link;
- perform SSH, install GoAster, transfer files, start a second agent or
  Telegram consumer, switch credentials, restart services, or delete either
  server.

An approved plan is not an implementation or an authorization. Do not remove
this blocker because a user asks to proceed, because a server ID is known, or
because a provider request appears simple. Do not invent an endpoint, tool,
URL, cloud-init contract, Redis operation, installer command, service name,
path, or recovery capability. This starter contains no GoAsterWeb migration
API and no migration-specific private-key delivery capability through
Telegram.

The blocker also applies to a request to “just monitor” or “just create one.”
There is no safe partial handoff: an unknown, expired, conflicted, lost, or
unverified handoff must fail closed. Expiry must not be treated as deletion of
the billable server.

## Current capability boundary

This checkout contains no migration command or migration backend. In
particular, it has no GoAsterWeb handoff endpoint, owner session, availability
monitor, Hetzner provisioning client, migration installer or transfer service,
or migration-specific private-key delivery path. The existing Telegram
adapter's private-chat behavior does not turn ordinary messages or document
attachments into a migration key channel.

Do not replace a missing capability with a shell command, a guessed URL, a
manual API request, an ordinary Telegram message, or a link. A read-only host
inspection can collect facts for the owner, but it cannot create the missing
handoff or authorize a cloud operation.

## What this skill may do now

With the owner's existing authorization, the agent may explain the workflow
and perform read-only inspection of the current host. It may:

1. Derive the current server, project, installation paths, service names,
   running processes, agent version, and architecture from the actual host.
   Never guess paths or treat the observed GoAsterWeb installation notes as a
   universal contract.
2. Inventory persistent state without displaying values: agent configuration,
   project and durable memory, user-created skills, scheduled jobs,
   environment and secret references, authentication and pairing state,
   workspace data, and other user-owned files. Classify secrets, large data,
   external data, symlinks, permissions, timestamps, filesystem space, and
   compatibility. Do not copy provider credentials by default.
3. Explain what is essential, what is excluded, what must be reauthenticated,
   and how separate agents, Telegram bot credentials, active pollers, and
   scheduled jobs behave during migration. The old and new agents operate
   independently and may run concurrently; use distinct Telegram bot tokens
   for them.
4. Help the owner write down a target preference and the exact approval they
   would eventually need. Do not present a current price, server offer,
   location, or architecture as verified without checking an authoritative,
   current source in an approved workflow.

Read-only inspection is not approval to create a server, incur cost, modify
state, or deploy. If identity, paths, ownership, or scope are ambiguous,
stop and ask the owner to resolve them.

## Approved availability monitor: future policy only

An “approved monitor” means a separately implemented and reviewed,
owner-approved, read-only availability check. It is not available in this
checkout. The current agent must not schedule, run, emulate, or claim to have
run one, including a 30-minute recurring check.

If a later release passes the handoff gate, the monitor must remain separate
from provisioning and must:

- use the owner's exact approved target terms and a bounded, explicitly
  approved cadence and duration;
- report availability and current terms privately without reserving,
  creating, rebuilding, or deleting a server, generating a key, or starting a
  setup session;
- stop and report on expiry, authentication failure, provider error, timeout,
  inconsistent data, or an unknown result; it must not blindly retry or turn
  availability into creation; and
- stop before any separately approved create operation, and stay disabled
  until the owner manually reconciles an uncertain or billable result.

Finding a matching offer is not approval to create it. The owner must still
review the exact server type, architecture, location, image, network, price,
and overlap cost in the approved workflow.

## Target guidance, not a provisioning instruction

For a future owner decision, the proposed direction is to consider a current
Hetzner CX or CAX option only when its architecture and current availability
are verified. Four GB RAM is the baseline suggestion for GoAster plus a few
browser tabs; eight GB provides more headroom; a cheaper compatible target may
fit light use without builds, tests, browsers, or other heavy work. Ask for a
location preference and otherwise prefer all eligible locations for faster
availability. These are preferences, not current product or price claims.

Before any future approved creation, the owner must see the selected server
type, architecture, eligible locations, image, network, expected hourly price,
and overlap cost for both servers. There is no separate price ceiling in the
approved direction: stop if the current terms differ from what the owner
approved. Creation would be billable immediately, including if migration
fails or a handoff expires. None of these checks authorizes creation while
the hard blocker remains.

## Owner-managed migration checklist

This checklist describes actions for the owner after a separately approved
destination exists and its identity and access are verified. The agent may
explain the checklist; it must not perform the actions or claim that this
repository supplies the missing installer or transfer service.

### 1. Preflight the old host

- Record the exact current server and project identity, installation layout,
  service definitions, process state, version, architecture, disk space,
  permissions, and available recovery access from the host itself.
- Inventory agent settings, project memory, durable memory, user skills,
  schedules, workspace files, authentication and pairing state, and
  environment/secret references. Record names and classifications, never
  secret values.
- Mark each item as required, optional, reauthenticated, excluded, or
  owner-owned external data. Do not assume that copying an environment file,
  provider token, OAuth state, Telegram token, pairing state, or private key
  is safe or necessary.
- Keep the old server available for manual recovery. Do not change its SSH
  keys or delete its data as part of preflight.

### 2. Verify the destination before touching it

The owner must independently verify the exact Hetzner project, server ID,
address, image, operating system, architecture, disk space, dependencies,
and login identity. Verify the server's SSH host key through the approved
handoff and then use strict host-key verification. Never accept an unknown
host key, an address-only match, a mismatched login key, a different project,
or an ambiguous provider record.

The current repository has no supported GoAster installer, upgrade process,
or restore process. Use only a separately reviewed procedure based on the
actual target host. Do not infer service ownership, paths, restart order,
commands, or permissions from `docs/goasterweb-installation.md`; that page
records observations, not a universal contract.

### 3. Prepare the independent new agent

Before migration, the owner must create a new Telegram bot with BotFather for
the new agent. Use distinct bot tokens for the old and new agents; never share
one token between them. Stage the destination only under the owner's manual
control. Installation or file placement must not overwrite unrelated files.
The old and new agents are independent and may be started and run concurrently.
The owner chooses when to stop the old agent, and may keep it running for as
long as they choose. Never automatically cut over, stop either agent, or
require the old agent to stop before the new one starts. The owner should
review scheduled work and other external integrations for duplicate effects.

Pair the new bot independently. Do not copy the old bot token, pairing state,
or Telegram adapter state as a shortcut. A bot identity and a pairing record
are separate from the files selected for the manual move. The old bot remains
the old agent's identity until the owner deliberately changes that setup.

The approved move is manual and agent-guided, not direct server-to-server
transfer. The owner chooses the files to move, checks permissions, symlinks,
timestamps and checksums, and reauthenticates what should not be copied. A
manual archive can be used only if the owner first verifies its exact scope
and secure handling; no archive, backup, snapshot, or automated transfer is
required by this workflow. Do not send a secret through an ordinary chat,
URL, command line, log, project memory, or repository.

### 4. Validate and retain recovery

The owner controls the sequence: move only the selected state and validate
the new agent independently. Both agents may remain active concurrently,
each using its own Telegram bot token. The owner decides whether and when to
stop the old agent; no automatic cutover or stopping occurs. The agent may
provide a checklist and help interpret non-secret results, but must not
perform the cutover or delete the old server.

Keep the old server until the owner has used the new agent long enough to be
satisfied and has confirmed manual recovery. Both servers can accrue charges
while retained. The owner, in Hetzner, decides whether to delete the old
server; deletion permanently removes its data. Never delete automatically.
No backup or snapshot is required for this manual-move workflow; `goaster-backup`
is not an authorization to change the plan.

## Private owner channel and key handling

Use the current runtime behavior, not a guessed command or capability:

- Initial pairing requires a private Telegram chat and a one-time pairing
  code. The adapter binds the first valid numeric Telegram user ID; messages
  from other users are ignored, and a private-chat message must come from the
  same user as the private chat.
- After pairing, ordinary conversations and some commands can also be used in groups or supergroups. A paired account and a group are therefore not a private delivery channel.
- Telegram's group-privacy setting can affect which group messages arrive; it
  does not make a group a private delivery channel.
- The current private-only document flows are the existing `goaster.env`
  update and saved-environment retrieval. They do not establish a
  migration-specific private-key delivery API.

For any future migration key delivery, first verify that the chat is private,
that it belongs to the already authorized owner, and that the delivery method
is explicitly reviewed for this purpose. For this workflow, a private owner
channel means a one-to-one private chat whose sender is the already paired
owner. A paired account in a group is not a private owner channel; Telegram's
group-privacy setting does not change that. The current `/env` and `/get_env`
flows are limited to the saved `goaster.env` file and must not be used to
carry a private key, owner-access secret, or migration link.

Never run this workflow in a group, send a private key or owner-access secret
into a group, put a private key in a link or URL query, or assume that a server
ID authorizes access. If a reviewed delivery capability is not present, stop
and ask the owner to use an approved private method outside this skill; do not
improvise one.

The approved future direction is a fresh, dedicated SSH login key for the
newly created server. A later reviewed handoff would define how the owner
receives and uploads the private half; this checkout cannot generate, carry,
verify, or deliver it. The private key must never be placed in provider
user-data, a link, logs, project memory, a command line, or a group.

## Errors and manual reconciliation

Every migration error is a stop condition, not permission to retry, fall back
to an unreviewed path, or clean up automatically. The following policy is for
owner guidance and any future reviewed implementation; it is not an API for
the current agent:

| Condition | Required behavior | Owner action |
| --- | --- | --- |
| Handoff is missing, expired, lost, conflicted, or unverified | Fail closed; do not SSH, create, transfer, or start setup. | Reconcile the approved system manually and obtain a new approved handoff if needed. Expiry never deletes infrastructure. |
| Monitor or provider returns an error, timeout, inconsistent record, or unknown result | Stop the monitor and any create workflow; do not blindly retry or create a duplicate. | Inspect the exact attempt and provider project manually. Treat an unknown result as unknown until reconciled. |
| Host key is missing, changed, or conflicts with the recorded key | Do not connect over SSH. | Identify the exact affected server; if the reviewed procedure requires starting over, the owner deletes that server in Hetzner after seeing the permanent-data-loss warning, then retries manually. |
| Key, project, server ID, address, login identity, or owner channel does not match | Do not deliver a key, upload state, or continue setup. | Correct the identity or stop; never substitute an address-only or server-ID-only match. |
| Manual copy, permissions, checksum, install, or validation fails | Do not cut over or stop the old agent. | Keep the old server for recovery and repair or repeat the selected move manually. |
| A server may have been created but the result is uncertain | Do not repeat creation and do not delete it automatically. | Reconcile by the exact approved attempt label and decide in Hetzner what to retain or delete. |

No automatic deletion applies to the old server, a partially prepared new
server, a server with a missing or conflicting host key, or infrastructure
whose handoff expired. The owner must explicitly decide deletion in the
provider after identifying the exact server and understanding that deletion
is permanent.

## Requirements before the blocker can be lifted

The GoAsterWeb implementation and deployment must be inspected separately and
must demonstrate, with offline tests and security review before any billable
end-to-end test, all of the following. This list is a release gate, not a list
of APIs for the agent to invent:

- public registration has bounded request size, attempts per IP, active
  records, concurrent attempts, and request rates; the handoff has an
  absolute seven-day (7-day) expiry that retries and reads cannot extend;
- callback and owner-access credentials are independent, high-entropy,
  scoped, secret-bearing material is not logged or put in URLs, and only
  hashes plus minimal metadata are persisted; Redis credentials stay
  server-side;
- the proposed Upstash Redis Europe store's actual supported operations,
  transaction/compare-and-set semantics, TTL behavior, durability,
  replication, eviction, access control, regional location, pricing, and
  outage behavior are verified before use; Redis loss or ambiguity fails
  closed;
- the cloud-init handoff contains only the callback secret and required
  non-secret data, never the private login key, Hetzner token, Redis
  credential, or owner-access secret; provider user-data visibility is
  documented;
- the first authenticated host-key callback is recorded atomically, identical
  retry callbacks after a lost response are accepted until finalization or
  expiry, a different key records a conflict, and conflicted state blocks
  SSH; binding and finalization are race-safe;
- callbacks before the provider server ID is returned, provider-ID binding,
  exact-attempt labels, cross-attempt replay, inconsistent provider records,
  IP changes, and ambiguous create results are handled without trusting an
  unknown host or blindly creating a duplicate;
- owner access verifies the exact project, server ID, IP, host key, login key
  correspondence, expiry, conflict state, Redis availability, and setup
  session. No SSH occurs on any failed trust check. Missing host key,
  temporary Redis failure, and conflict are distinct outcomes;
- a missing host key identifies the exact affected server and tells the owner
  to delete it in Hetzner and try again, with a permanent-data-loss warning.
  Deletion is never automatic, and handoff expiry does not delete billable
  infrastructure;
- a migration link or session has no secret in a URL query, uses a reviewed
  short-lived owner session, and has restrictive referrer policy, no
  third-party scripts, CSRF protection, upload bounds, cleanup, and rate
  limits. A seven-day handoff is not a seven-day browser session;
- restart, multi-instance retrieval, concurrent callbacks, finalization,
  Redis outage/eviction/lost state, response-loss retries, and uncertain
  destructive or billable operations require manual reconciliation. No
  automatic repeat is allowed.

Until every applicable item is verified against the actual GoAsterWeb code and
deployment, keep the hard blocker. The agent must not register, schedule, or
create a server merely because this checklist exists.

## Future approved orchestration, still owner-controlled

If a later reviewed release explicitly clears the handoff gate, it must add
an implementation-specific workflow and tests before enabling any of these
operations. The intended policy is:

- the owner approves the exact target terms, current hourly price, eligible
  locations, and immediate creation of exactly one matching server; a
  30-minute availability cadence could run 24/7 only after the handoff is
  fully available and verified and the owner has approved it;
- the workflow registers one immutable attempt before creation, uses a fresh
  dedicated SSH key, and submits one provider create operation. On a timeout
  or unknown result it stops, disables any approved monitor, reconciles by
  the exact attempt label, and never blindly retries or creates a duplicate;
- on a confirmed creation error or timeout, it reports the issue and asks the
  owner to inspect and delete any accidentally created server. It never
  retries or deletes automatically. On confirmed success it marks the task
  done and prevents another creation;
- the owner receives status and cost information privately. The owner uploads
  the private key to the verified GoAsterWeb handoff, and the web side pins
  and strictly verifies the captured host key before SSH;
- the state move and agent setup remain manual; old and new agents operate
  independently and may run concurrently with distinct Telegram bot tokens.
  The owner chooses when to stop the old agent, if at all, and the old server
  remains available until the owner is satisfied. No automatic cutover or
  stopping occurs. Any restart during setup reports uncertain state and
  requires manual reconciliation.

These are policy constraints for a future implementation, not executable
instructions for the current agent. Do not name or call a provider API,
GoAsterWeb route, Redis command, cron mechanism, installer, or Telegram
delivery method unless that capability is later implemented, documented, and
verified in the actual owning system.

## Validation and reporting

For this starter, report exactly what was inspected and what was not. State
that no handoff was registered, no monitor was scheduled, no server was
created, no secret was generated or transferred, no deployment or SSH was
performed, no agent was switched, and no server was deleted.

Report the remaining gaps plainly: the GoAsterWeb handoff backend, its
restart-safe persistence and security controls, the reviewed private-key
delivery path, the target-host installer/transfer procedure, and any live
Hetzner offering or cost. Do not call host-key handoff a complete migration.
