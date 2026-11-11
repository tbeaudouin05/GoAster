# Authentication

Use the authenticated SSH/operator session for the exact approved host and
verify its host identity immediately before transfer. Reuse only protected
service credentials through their approved local or remote stores; never put
them in release artifacts, command arguments, logs, or reports. Readiness or
authentication health checks may refresh credentials, so follow the approved
deployment session and check before running them.
