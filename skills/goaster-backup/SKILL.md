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
path. It is not part of the essential workflow. Before any OVH call, perform a
fresh verification of the current account endpoint, the selected project, the
target instance, and the exact snapshot/backup capability in the official OVH
documentation and the project UI. The verification must identify the exact
method/path and permission names that are valid now. This repository
intentionally does not name those unverified endpoint paths; never substitute
a guessed path.

Treat the permission extension as separate from the current provisioning scope.
At minimum, verify which project-scoped rights cover the needed categories:

- listing or reading available backup/snapshot capabilities and state;
- creating a snapshot or backup for the selected instance or volume;
- inspecting completion and restore readiness;
- restoring or attaching a selected backup when rollback requires it; and
- deleting or changing retention only when the approved plan includes it.

The current provisioning rights do not imply any of these categories. If the
exact rights cannot be verified, stop the optional path and continue with the
local bundle only. Do not broaden an account-wide scope to make the check pass.

Before creating an OVH backup, show the target project and resource, expected
cost or the fact that cost is unknown, retention period, deletion behavior,
data location when verified, and whether any secret-inclusive data is present.
Obtain explicit approval for the exact operation. Approval for a local bundle
does not approve an OVH backup, extra retention, deletion, restore, or secret
inclusion. Never delete a snapshot or backup automatically.

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
