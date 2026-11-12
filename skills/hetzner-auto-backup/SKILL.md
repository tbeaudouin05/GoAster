# Hetzner automatic server backups

Use this skill when the owner or operator wants to assess or enable Hetzner
Cloud automatic backups for the GoAster runtime. Recommend them as an
additional best practice for operator-led manual recovery of the server disk.
They are not automatic rollback and they do not replace the mandatory,
predeployment, verified snapshot required by `goaster-backup`.

This is a fresh-install starter. Do not migrate a legacy skill or change an
existing user-owned skill directory. Installing or selecting this skill does
not make a cloud API request.

## Scope and safety

- Load `hetzner-auth` before any Hetzner access. Keep its token handling and
  identity checks; never disclose the token.
- This workflow covers only the disk included with the exact Hetzner Cloud
  server. Attached Volumes are not included and are out of scope. Do not claim
  that they are protected or invent a separate volume workflow.
- This workflow only assesses or enables the backup setting. It does not
  restore, rebuild, duplicate, delete, or manually recover a server. Manual
  recovery remains an operator task with independent Hetzner access.
- The documented `enable_backup` action for the exact verified server is the
  only permitted state-changing operation, and only after the approval gate
  below. Make no other resource or configuration change: do not modify any
  other server or project, any other setting, or manually create, modify,
  convert, or delete an image, backup, or Volume. Do not power off, reboot,
  shut down, resize, rebuild, delete, or roll back any server, and do not
  disable backups. Never deploy GoAster as part of this workflow.
- A read-only check may happen before approval. No state-changing API request
  may happen until the approval gate below is satisfied.

## Assess the exact target and current state

1. Load `hetzner-auth`. Establish the intended project using the project name
   and any available project ID from the GoAster deployment records and
   Hetzner Console. Hetzner API tokens are bound to the project where they are
   created and cannot be used for other projects; a successful API call alone
   does not establish that this is the intended human-readable project. If
   this association cannot be independently established from a secure,
   owner-maintained record, have the user confirm it in Hetzner Console: open
   the intended project, go to **Security > API tokens**, and confirm that
   the credential configured for GoAster was created in that project. Never
   ask the user to paste, display, or send the token. This confirmation is
   only for identity, not approval to enable backups. Stop if the association
   or intended project remains ambiguous or the user cannot confirm it.
   Identify the exact GoAster runtime server by the confirmed project, server
   ID, displayed name, and other available identity such as location. A name,
   IP address, or partial match alone is not enough. Stop if the server or
   authentication is uncertain or the target cannot be verified.
2. Use only read-only, current Hetzner Cloud API operations and the official
   documentation to inspect the target. Fetch the server and inspect the
   documented backup setting, any in-progress server action, and backup images
   bound to that exact server. Distinguish these states: backups disabled,
   backups enabled, an enable action in progress, and backups enabled but no
   backup image observed yet. Do not infer enablement from a name or from one
   image alone. If the API result is incomplete or contradictory, stop rather
   than guess.
3. If backups are already enabled, report the verified project and server,
   the observed setting/action state, and any observed backup image timestamps
   or count. Do not call an enable endpoint, re-enable the feature, or mutate
   anything. Still explain the current terms and caveats below when they can
   be verified.

## Establish current terms before asking for approval

Use authoritative, current Hetzner sources, not a remembered price or this
skill's examples:

- [Hetzner Cloud API reference](https://docs.hetzner.cloud/reference/cloud)
  for the current operation and response schema.
- [Hetzner API token instructions](https://docs.hetzner.com/cloud/api/getting-started/using-api/)
  for the project-bound token behavior and [token creation in Console](https://docs.hetzner.com/cloud/api/getting-started/generating-api-token/).
- [Hetzner backup and snapshot overview](https://docs.hetzner.com/cloud/servers/backups-snapshots/overview/)
  for frequency, retention/slot behavior, and the Volume exclusion.
- [Hetzner backups and snapshots FAQ](https://docs.hetzner.com/cloud/servers/backups-snapshots/faq/)
  for the server-deletion limitation: deleting a server also deletes its
  backups.
- [Hetzner Cloud pricing](https://www.hetzner.com/cloud/) and, where the
  current API contract provides it, the authenticated read-only `GET /pricing`
  response for the project.

For orientation, the linked overview currently describes daily automatic
copies and up to seven backup slots. Re-check those terms at run time for the
target; if the applicable documentation or console/API state differs or is
unavailable, report the uncertainty instead of treating these as guarantees.

Determine and communicate all of the following for this target before asking
for approval:

- the recurring backup charge in the project's billing currency, matched to
  this server's type, location, pricing status, and applicable taxes or VAT;
- the currently documented backup frequency and retention/slot behavior; and
- which source and access date support each figure.

The charge can vary with project currency, server details, pricing changes,
taxes, and account-specific or legacy pricing. If a current authoritative
source is unavailable, inaccessible, or disagrees with another source, say
exactly what is uncertain. Do not substitute an estimate or old example. Ask
for informed approval only after presenting that uncertainty and the
user-confirmed cost assumption; if the user cannot knowingly approve it, stop.

Explain that enabling backups does not create an immediate recovery point.
The first provider-created backup may be delayed, and a backup of a running
server's disk may not be application-consistent. It may not capture in-flight
database or application state in a recoverable way. Do not power-change or
quiesce the server as part of this workflow, and do not promise a first-backup
time that the provider has not confirmed. Explain that the backups are bound
to this server and Hetzner deletes them if the server is deleted; they are not
retained independently of that server. Cite the [Hetzner backups and snapshots
FAQ](https://docs.hetzner.com/cloud/servers/backups-snapshots/faq/).

## Approval gate

For a target whose backups are verified as disabled, present the exact
project and server, the one state-changing operation, the recurring cost or
clearly stated uncertainty, the current frequency/retention, and the disk,
delay, consistency, and server-deletion caveats. Explicitly state that Hetzner
deletes these backups when their server is deleted. Then ask the user to
explicitly approve all of those terms. A general “yes,” approval to inspect,
or approval for a snapshot is not approval to enable recurring backups.

The approval must name the target and cost assumption, for example:

`I approve enabling automatic backups for project <project identity>, server <server name and ID>, at <quoted recurring cost and currency or stated uncertainty>, with <quoted frequency and retention>. I understand the first backup may be delayed, may not be application-consistent, does not cover attached Volumes, and will be deleted if the server is deleted.`

If the user changes the target or terms, ask again. If the target is already
enabled when approval arrives, report that fact without mutation.

## Enable and verify the asynchronous action

Only after the approval gate, and only if a fresh read confirms the exact
server is still disabled, use the documented operation:

`POST /servers/{id}/actions/enable_backup`

Use the verified numeric server ID and the current official request schema;
do not guess a different endpoint or body. Treat the response as an
asynchronous action, not as proof that backups are enabled:

1. Check the HTTP/API response and record the returned non-secret action ID.
   Do not repeat the mutation if the response is lost or its outcome is
   uncertain.
2. Poll the documented action resource for that server, such as
   `GET /servers/{id}/actions/{action_id}`, at a bounded, rate-limit-friendly
   interval until the action is `success` or `error`. Do not treat a `running`
   response or the initial HTTP success as completion.
3. On an API error, action `error`, timeout, conflicting state, or failed
   verification, stop and report the non-secret status/message. Do not guess,
   retry a possibly accepted mutation, roll back, disable backups, delete
   anything, or change server power state.
4. After `success`, perform a fresh read of the exact server and its bound
   backup images. Verify that the documented backup setting is enabled. Report
   enablement only after this verification; report separately whether a first
   backup image has actually appeared. If verification is unavailable or does
   not agree, report the setting as unverified and stop.

Before any user-facing report, including already-enabled, failed, or
unverified outcomes, state that the backups are bound to this server and
Hetzner deletes them if it is deleted; cite the [Hetzner backups and snapshots
FAQ](https://docs.hetzner.com/cloud/servers/backups-snapshots/faq/). Always
finish by stating that automatic backups are an additional manual-recovery
aid, not the `goaster-backup` predeployment snapshot gate, not
application-consistent backup software, and not coverage for attached
Volumes.
