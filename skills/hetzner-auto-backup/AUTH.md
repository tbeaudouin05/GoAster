# Authentication

Load the existing `hetzner-auth` skill for Hetzner credentials, project
identity, and server verification. Reuse its `goaster-env` credential path;
do not create a second authentication flow. Keep the API token only in
memory. Never print, log, save, put it in command arguments, or include it in
reports.

Authentication and read-only identity checks do not authorize enabling
backups or accepting recurring charges. Obtain separate explicit approval for
the exact project, server, and quoted backup terms before any mutation.
