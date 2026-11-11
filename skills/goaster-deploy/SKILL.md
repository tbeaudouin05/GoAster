# GoAster deployment

Deploy only a passing `goaster-build-test` result to the approved host. Before
starting, obtain explicit deployment approval naming the exact source SHA,
binary checksum, host, service, install paths, rollback bundle, restart,
health checks, prompt smoke test, and any rollback action. The deployment
contract keeps a stable helper in place while application releases remain
versioned and replaceable.

## Stable helper and release layout

The stable service helper is:

```text
/usr/local/libexec/goaster-deploy
```

It is installed, owned, reviewed, and checked separately from application
releases. The service's launch command invokes this helper; the helper selects
the approved current release and starts it. A versioned release may live under
an immutable path such as:

```text
/usr/local/lib/goaster/releases/<binary-sha256>/goaster
/usr/local/lib/goaster/current
```

`current` is an atomically replaced selector for a complete release. The
versioned `/usr/local/bin/goaster` binary is never the stable helper. Do not
overwrite a legacy file at that path as part of helper installation; inspect
it and obtain separate approval for any unrelated change.

## Deployment procedure

1. Confirm the exact host identity, service name, approved source SHA, binary
   SHA-256, release path, helper checksum, and verified local rollback bundle.
   Recheck the host immediately before transfer. A changed checksum, host
   identity, address, path, or SHA requires fresh approval.
2. Stage the artifact in a same-filesystem temporary release directory that is
   not active. Preserve the source checksum and a small non-secret manifest.
   Do not build on the target, pull arbitrary code, or put secrets in the
   staging directory.
3. Verify the staged file's size, regular-file status, mode, and SHA-256
   against the approved build result. Verify the helper checksum separately.
   On any mismatch, remove only the unactivated staging directory and stop;
   do not swap or retry with a different artifact.
4. Complete the release directory, then atomically replace the `current`
   selector in the same filesystem. Preserve the previous selector and release
   identified by the local rollback manifest. Never expose a half-copied
   binary as current.
5. Restart only the approved service through its existing service manager.
   Do not edit the unit or add ports as part of this workflow. Confirm the
   service is active and the stable helper is the executable that launched it.
6. Run the local health check: service state plus the approved loopback
   liveness endpoint. Run readiness/authentication health only when it is in
   the approved plan, because it may refresh a credential.
7. Run a concise prompt smoke test through the approved local or loopback
   interface, for example a fixed request asking for exactly
   `GOASTER_DEPLOY_SMOKE_OK`. Keep the prompt and reply out of reports when
   they contain user data, do not use Telegram, and treat a timeout, error, or
   unexpected reply as a failed deployment.

## Structured result

Return one structured result without credentials or command secrets:

```json
{
  "status": "success|failed|rolled_back|rollback_failed",
  "host": "verified host identity",
  "source_sha": "exact source SHA",
  "binary_sha256": "approved checksum",
  "helper_path": "/usr/local/libexec/goaster-deploy",
  "release_path": "verified release path",
  "service": "approved service",
  "staged": "pass|fail",
  "checksum": "pass|fail",
  "atomic_swap": "pass|fail",
  "restart": "pass|fail|not-run",
  "health": "pass|fail|not-run",
  "prompt_smoke": "pass|fail|not-run",
  "rollback": "not-needed|pass|fail"
}
```

Do not report success until staging, checksum verification, atomic swap,
restart, health, and prompt smoke are all successful.

## Failure and rollback

Before a swap, a failure leaves the active release untouched. After a swap, a
restart, health, or prompt-smoke failure triggers rollback to the exact
previous release recorded in the verified server-local bundle: stop the failed
service, atomically restore the previous selector, restart the same service,
and repeat the health check. Report `rolled_back` only when the previous
release is healthy. If rollback fails, stop, leave both release records for
inspection, report `rollback_failed`, and do not attempt another release or
delete evidence automatically.
