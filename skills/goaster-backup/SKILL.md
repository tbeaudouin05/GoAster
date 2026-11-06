# GoAster backup and rollback preparation

Run this stage after a passing build/test and before deployment. The local
rollback bundle is required for every deployment. An OVH snapshot or backup is
an optional second path and never replaces the local bundle.

## Mandatory server-local rollback bundle

Confirm the approved host identity, service, active release SHA, install path,
stable helper path, and the source/binary checksum. On that same server,
create a private, non-secret bundle under a dedicated rollback directory such
as:

```text
/var/lib/goaster/rollback/<timestamp>-<active-sha>/
├── manifest.json
├── release/goaster
├── release.sha256
├── service.unit
└── helper.sha256
```

The exact directory may follow the host's approved layout, but it must be
server-local, uniquely named, on a filesystem with enough space, and protected
from ordinary users. The manifest records the host identity, service name,
active release path, active SHA, helper path and checksum, creation time, and
the files intentionally excluded.

Include the currently active executable and the non-secret service/helper
configuration needed to restore it. Do not copy `env` files, OpenAI or
Telegram credentials, private keys, cookies, or other secret-bearing state by
default. If a secret-bearing state file is essential, stop for explicit
approval before copying it; record its protection and retention without
printing its value.

Verify the bundle before deployment: every listed file is a regular file, the
recorded SHA matches the active executable, the manifest matches the approved
host and service, the bundle is readable by the deployment operator, and a
local restore dry run can identify the previous release without changing the
service. Keep existing bundles; do not delete them automatically.

## Optional OVH snapshot or backup

Only after the local bundle passes, ask whether the user wants the optional OVH
path. It is not part of the essential workflow. The existing project-scoped rows
`GET /cloud/project`, `GET /cloud/project/*`, and `POST /cloud/project/*`
cover documented Public Cloud instance backup creation and inspection. The
[OVH instance-backup guide](https://docs.ovhcloud.com/en/guides/public-cloud/compute/save-an-instance)
uses `GET /cloud/project/{serviceName}/region` and
`POST /cloud/project/{serviceName}/region/{regionName}/instance/{instanceId}/snapshot`;
backup listing and read operations are under `/cloud/project/*`. The
[OVH US backup guide](https://support.us.ovhcloud.com/hc/en-us/articles/4460743125395-Backing-up-an-instance)
documents `GET /cloud/project/{serviceName}/snapshot` for verification.
No separate credential extension is required for those creation and inspection
operations.

Before using OVH, verify the current account endpoint, project, instance,
capability, and exact method/path and rights for the selected operation against
current official OVH documentation and the project UI. Other operations, such
as volume backups, restore, or attachment, may require different rights. If
GoAster performs deletion or retention changes, it needs `DELETE` rights for
the relevant documented endpoint and any other required rights. For example,
the US backup guide documents `DELETE /cloud/project/{serviceName}/snapshot/{snapshotId}`
for a backup and
`DELETE /cloud/project/{serviceName}/region/{regionName}/workflow/backup/{backupWorkflowId}`
for a schedule. Do not broaden to account-wide access. If creation or
inspection cannot be verified, skip the optional backup and continue with the
local bundle. For other operations, skip only the unverified operation.

OVH documents default automatic schedules that retain the last 7 or last 14
daily backups, as well as custom schedules. No universal cap on manually
created backups was verified; do not assume a rolling maximum for them.

The [OVH US Public Cloud price list](https://us.ovhcloud.com/public-cloud/prices/)
lists Instance Backup storage replicated x3 at about US\$0.000017/GB/hour or
US\$0.0123/GB/month, excluding tax. Used space is billed with 1 GB granularity.
A distant backup is charged separately at the remote region's storage price.
Actual prices vary by OVH account region and current pricing; verify the user's
price before estimating, or say the cost is unknown.

Before creating an OVH backup, show the target project and resource, expected
cost or the fact that cost is unknown, retention plan, deletion behavior, data
location when verified, and whether any secret-inclusive data is present.
Explicitly tell the user: "You are responsible for managing and deleting old
backups and schedules if you want to minimize ongoing storage costs. Retained
backups can continue to incur charges after the instance is deleted."

Obtain explicit approval for the exact operation and cost, and separate
approval for retention, deletion, restore, and any secret inclusion. Approval
for a local bundle does not approve an OVH backup. GoAster must never delete
backups or schedules automatically unless separately approved. OVH-managed
schedule rotation can delete older backups; explain that behavior and obtain
separate approval before creating or changing such a schedule. After creation,
inspect completion and, where verifiable, restore readiness before reporting
success.

## Result and rollback handoff

Return a structured, non-secret result:

```text
host=<verified host identity>
local_bundle=<server-local path>
local_bundle_sha256=<checksum>
local_bundle=verified
ovh_backup=not-requested|not-authorized|verified-and-created|failed
ovh_project=<project ID or redacted safe label when approved>
ovh_resource=<safe resource identity>
retention=<approved value or not-applicable>
secrets_included=no|approved-and-protected
```

Deployment may continue only with `local_bundle=verified`. Rollback must use
that bundle and the recorded previous SHA first. An OVH restore is a separate,
approved recovery action after the same exact-resource and permission checks.
