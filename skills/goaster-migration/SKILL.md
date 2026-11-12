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
   and how shared Telegram bot credentials, active pollers, and scheduled jobs
   can cause two active agents or duplicate work.
4. Help the owner write down a target preference and the exact approval they
   would eventually need. Do not present a current price, server offer,
   location, or architecture as verified without checking an authoritative,
   current source in an approved workflow.

Read-only inspection is not approval to create a server, incur cost, modify
state, or deploy. If identity, paths, ownership, or scope are ambiguous,
stop and ask the owner to resolve them.

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

### 3. Prepare without starting a duplicate

Stage the destination only under the owner's manual control. Installation or
file placement must not overwrite unrelated files. Stop before starting the
new GoAster agent, Telegram consumer, or scheduled jobs. The owner must
ensure that the old and new agents do not consume the same Telegram bot or
other shared credentials concurrently and that scheduled work is not
duplicated.

The approved move is manual and agent-guided, not direct server-to-server
transfer. The owner chooses the files to move, checks permissions, symlinks,
timestamps and checksums, and reauthenticates what should not be copied. A
manual archive can be used only if the owner first verifies its exact scope
and secure handling; no archive, backup, snapshot, or automated transfer is
required by this workflow. Do not send a secret through an ordinary chat,
URL, command line, log, project memory, or repository.

### 4. Cut over and retain recovery

The owner controls the sequence: move only the selected state, validate the
new agent, stop or switch the old consumer as appropriate, and confirm that
exactly one intended agent is active. The agent may provide a checklist and
help interpret non-secret results, but must not perform the cutover or delete
the old server.

Keep the old server until the owner has used the new agent long enough to be
satisfied and has confirmed manual recovery. Both servers can accrue charges
while retained. The owner, in Hetzner, decides whether to delete the old
server; deletion permanently removes its data. Never delete automatically.
No backup or snapshot is required for this manual-move workflow; `goaster-backup`
is not an authorization to change the plan.

## Telegram privacy and key handling

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
is explicitly reviewed for this purpose. Never run this workflow in a group,
send a private key or owner-access secret into a group, put a private key in a
link or URL query, or assume that a server ID authorizes access. If a reviewed
delivery capability is not present, stop and ask the owner to use an approved
private method outside this skill; do not improvise one.

The approved future direction is a fresh, dedicated SSH login key for the
newly created server. The owner would receive the private key only through a
verified private channel and upload it to GoAsterWeb; it must never be placed
in provider user-data, a link, logs, project memory, or a group. The current
checkout cannot carry out or verify that delivery, so it remains blocked.

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
- setup remains stop-before-start, the state move and agent switch remain
  manual, and the owner retains the old server until satisfied. Any restart
  during setup reports uncertain state and requires manual reconciliation.

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
