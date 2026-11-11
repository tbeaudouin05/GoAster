# GoAster deployment

GoAsterWeb is a one-shot installer for a fresh server installation. It does
not own subsequent GoAster runtime updates. After initial installation,
GoAster owns changes to its runtime; do not rerun the GoAsterWeb installer as
an update mechanism. The repository documents no supported upgrade or tested
restore process for the observed GoAsterWeb integration, so every later change
still requires a reviewed, host-specific procedure based on the actual server.

The observed GoAsterWeb layout below is a clue for inspection, not a contract.
Inspect the target server's real service definitions, executable and source
paths, permissions, dependencies, persistent data, and recovery scope before
planning any change. Do not assume that another server uses the documented
paths, services, ownership, or setup.

Deploy only the two retained, passing artifacts from `goaster-build-test`, after
`goaster-backup` has verified an available Hetzner snapshot for the exact
approved project and server, including required recovery planning for separate
volumes or external data.
Snapshot approval does not approve deployment. Deployment is a separate action:
obtain explicit approval for the exact source SHA, both binary
checksums, host, affected services, install paths, restart order, checks, and
rollback actions. Do not deploy from a dirty or unverified build.

## Resolve the host-specific procedure first

The repository builds `goaster-agent` and `goaster-telegram`; it does not ship
service units, an installer, a stable deployment helper, a release selector,
or a complete deployment procedure for a particular host. Do not assume any
of these exist or invent paths, service names, users, restart ordering, atomic
switches, or rollback commands. Inspect the approved host's actual service
configuration and executable paths without exposing secrets. Identify which
services and binaries are in scope, how they communicate, dependencies,
permissions, existing release mechanism, and how both are restored. If a safe
switch or rollback cannot be verified, stop and report the gap for review.

The agent serves local `/livez`, `/healthz`, and `/chat` endpoints; its `live`
and `health` commands use its configured control transport. `live` checks
liveness only. `health` checks saved authentication and may refresh it. The
Telegram adapter runs as root, polls Telegram and forwards messages to the
agent; when `NOTIFY_SOCKET` is set it signals readiness after an available
agent and a successful poll. These facts do not establish the host's service
configuration or prove end-to-end messaging works.

## Deployment handoff

1. Verify host identity, the approved source SHA and both retained artifact
   checksums. Inspect the actual services and paths, and verify the Hetzner
   snapshot ID, available status, exact project and server, approval, and
   required external-data recovery handoff.
2. Agree on and approve a host-specific procedure for staging and verifying
   each binary, switching releases without exposing partial files, service
   restart order, liveness and adapter checks, and restoring the previous
   binaries and services. Include a prompt smoke test only through an existing,
   approved interface; do not use Telegram without explicit authorization.
   Direct `prompt` and `test-prompt` use the same exact credential path as the
   agent and may run while other processes use it; this is accepted operational
   behavior based on observed use, not a cross-process refresh or concurrency
   guarantee. They are not live-service probes.
3. Recheck host identity and artifact checksums immediately before transfer.
   Stage the approved binaries away from active paths, verify their regular-file
   status, permissions and SHA-256, and stop on mismatch. Keep credentials and
   other secrets out of releases and reports.
4. Execute only the approved switch and restart plan. Check the affected
   services and the approved liveness and adapter checks. Run authentication
   health or a prompt only if specifically included in the plan; these can
   refresh credentials or make external requests. If any required check fails,
   stop and request a separately approved manual recovery. Do not restore or
   roll back automatically. Report the failure explicitly and stop.

Report the host, source SHA, both checksums, affected services and paths,
staging, switch, restart, checks, and rollback results without secrets. Do not
report deployment success until all approved required checks pass.
