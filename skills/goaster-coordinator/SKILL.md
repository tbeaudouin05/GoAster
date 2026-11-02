# GoAster workflow coordinator

Use this skill for the complete user-owned fork-to-host journey. Load the
stage skills as needed and stop at every approval boundary. The essential
sequence is:

```text
fork -> source -> build/test -> local backup -> optional OVH backup -> deploy -> verify
```

## Stage gates

1. **Fork** — Run `goaster-fork`, using `github-cli` and
   `github-fork-starter`. Verify the user's authenticated account, public fork,
   `goaster-customizations` branch, manifest-only `goaster-contributions`
   branch, and exact SHAs. Keep upstream read-only.
2. **Source** — Run `goaster-source`. Confirm the fork checkout, branch, exact
   source SHA, and path. Support arbitrary user customization in that fork;
   preserve existing changes and do not auto-merge upstream.
3. **Build/test** — Run `goaster-build-test`. Require the Go build, explicit
   core tests, isolated binary start, and concise prompt smoke test. Do not run
   Telegram or adapter tests.
4. **Local backup** — Run `goaster-backup` on the exact target host. Require a
   verified server-local rollback bundle before any deployment action.
5. **Optional OVH backup** — Offer the optional project-scoped OVH
   instance backup only after the local bundle. Existing provisioning GET and
   POST rows cover documented creation and inspection; verify current endpoint
   and rights before use. If GoAster performs deletion or retention changes,
   it needs `DELETE` rights for the relevant documented endpoint. Before
   creation, tell the user they are responsible for managing and deleting old
   backups and schedules to minimize ongoing storage costs. Never delete them
   automatically unless separately approved. Require explicit approval for the
   operation and cost, and separate approval for retention, deletion (including
   OVH-managed rotation), and secret-inclusive data. If skipped or not
   verifiable, continue with the local bundle and report it clearly.
6. **Deploy** — Run `goaster-deploy` with the approved binary checksum. Stage,
   verify the checksum, atomically swap the release selector, restart the
   approved service through the stable helper, and retain the previous release.
7. **Verify** — Require the structured deployment result, health check, and
   concise prompt smoke test. If verification fails after the swap, use the
   recorded local bundle to roll back and verify the previous release.

## Approval invalidation

Treat the handoff as a tuple, not a general “go ahead”:

```text
(github_host, fork_owner/name, goaster-customizations,
goaster-contributions, source_sha, source_path,
 binary_sha256, target_host_identity, service, release_path, helper_path)
```

If the GitHub identity, source SHA, local path, binary checksum, target host,
service path, or helper path changes, invalidate downstream approvals. A source
change requires a new build/test and checksum; a host change requires a new
host preflight and local backup; an install-path or helper change requires a
new deployment approval. If any stage is uncertain, inspect its result before
retrying and never repeat a consequential write blindly.

## Final handoff

Report one concise result containing the fork and source SHA, build/test status,
local bundle path and verification, optional OVH backup status, deployed
release checksum, helper path, target host identity, health, prompt smoke, and
rollback status. Include no credentials, tokens, private keys, or secret file
contents.
