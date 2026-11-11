# GoAster environment

Use this skill when another skill needs an environment variable or secret.

Environment values can be supplied locally through `/env` or stored in
Bitwarden Secrets Manager. Use the user's configured source. For Bitwarden,
follow the `bitwarden-secrets-manager` skill.

Check that the required value is available without printing or exposing it.
If it is missing, explain what is needed and stop.
