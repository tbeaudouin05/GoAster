# Authorization

Deploy only the exact source SHA and binary checksum approved after build/test,
to the exact host, service, and install path approved by the user. A verified
server-local rollback bundle is required first. Restart, health checks that may
refresh credentials, prompt smoke tests, and rollback to the recorded previous
release must be part of the explicit deployment approval.

Keep the stable helper separate from the versioned application. Never replace
the helper with a release binary, expose secrets in arguments or logs, or
silently change the service, host, path, or release selector. If a checksum,
host identity, path, or SHA changes, stop and obtain fresh approval.
