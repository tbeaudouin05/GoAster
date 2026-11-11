# GoAster backup and recovery

Before every deployment, create and verify a Hetzner snapshot of the exact
target server. A successful, available snapshot is a required deployment gate.

Before creating it:

- Confirm the server and Hetzner project.
- Obtain explicit approval for the snapshot, its expected cost and retention.
- Explain that it can contain secrets and that a snapshot taken while the server
  is running may not capture application data in a fully consistent state.
- Identify essential data outside the server disk, including attached volumes,
  and confirm a separate recovery plan for it.
- Confirm the user has independent Hetzner access for manual recovery.

Use `hetzner-auth` for authentication. Wait for snapshot creation to complete,
then verify that the snapshot is available and belongs to the target server.
Record its ID for recovery.

If any step fails or is unclear, stop. Do not deploy.

Recovery is manual: if deployment fails, an operator decides whether to restore
the snapshot. Do not set up automatic rollback.
