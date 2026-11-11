# Hetzner authentication

Use this skill when GoAster needs access to Hetzner Cloud.

Get the Hetzner API token through the `goaster-env` skill. Keep it in memory:
never print it, save it to a file, or include it in logs.

Verify authentication and confirm the Hetzner project and target server
before continuing. If authentication fails or the target is ambiguous, stop
and explain the problem.

Authentication does not authorize creating snapshots, changing servers, or
incurring costs. Obtain the required approval for those actions separately.
