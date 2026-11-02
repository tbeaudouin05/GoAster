# GoAster Bitwarden Secrets Manager

Use this pure skill when the user wants GoAster to store environment secrets in
Bitwarden Secrets Manager. This skill describes the workflow and safety rules;
it does not contain credentials, secret values, server-specific paths, or an
embedded implementation.

## Onboarding policy

Bitwarden is optional. Do not add Bitwarden to initial GoAster onboarding or
block a successful installation on Bitwarden setup. GoAster may start with its
existing server-local environment source and offer migration after installation
and health verification.

The free Bitwarden Secrets Manager tier is sufficient for one GoAster machine
account. One machine account is all GoAster needs for one GoAster server
installation. Do not create additional machine accounts unless the user has a
separate, explicit need.

This workflow uses **Bitwarden Secrets Manager**, not a secure note or a
personal-vault item. Store migrated values as Secrets Manager secrets in a
project. Do not suggest copying the entire env file into a note.

## User setup in Bitwarden

Guide the user through these steps in the Bitwarden web app. Labels can vary
slightly by Bitwarden edition or UI version, so use the closest current label:

1. Open the user's Bitwarden organization.
2. On the organization billing or subscription page, choose the option to
   subscribe to or activate **Secrets Manager**. Confirm the free tier when
   offered. GoAster needs only one machine account.
3. Open **Secrets Manager** and create a project for this GoAster installation.
   The project name is not a secret.
4. Create one machine account for the GoAster server.
5. Grant that machine account access to the GoAster project. It needs write
   permission during the initial migration so GoAster can create the secrets.
6. Open the machine account's access-token area and create an access token.
7. Copy the token immediately into the approved secure setup path. Bitwarden
   shows the token only when it is created; it cannot be recovered from the
   GoAster skill later.

The user supplies one secret credential to GoAster:

```text
BWS_ACCESS_TOKEN=<machine-account-access-token>
```

The user also supplies the non-secret project identifier, for example:

```text
BWS_PROJECT_ID=<secrets-manager-project-id>
```

Do not confuse the machine-account access token with a Bitwarden password,
master-password credential, secure-note password, or ordinary Password Manager
API key. Do not ask the user to provide any of those for this workflow.

## Migration from server env

When the user chooses migration:

1. Confirm the exact source env and destination Bitwarden project.
2. Inspect variable names only. Never display or copy secret values into chat,
   model context, tickets, logs, or normal progress messages.
3. Use the approved Bitwarden Secrets Manager integration to create one secret
   per required environment variable, preserving the variable names exactly.
4. Verify every created secret by name and successful controlled retrieval. Do
   not report the retrieved values.
5. Keep the server-local env source as a rollback copy until the user explicitly
   approves its removal.
6. Switch GoAster to Bitwarden only after verification succeeds.
7. Run the relevant health check and report only non-secret status and counts.
8. If the Bitwarden setup supports narrower permissions, reduce the machine
   account from migration write access to the minimum read access needed for
   normal operation.

Never silently fall back to the server env if Bitwarden is selected but
unavailable. Report the source failure and preserve the configured source
boundary for manual recovery.

If any secret fails creation or verification, do not switch sources and do not
delete or overwrite the existing server env. Report the failed variable names
only and leave the installation in its previous working state.

## Normal operation

Skills that need environment variables should reference this skill rather than
hardcode a secret location. The environment-provider contract should resolve
values from the selected source, validate required names, and prevent values
from entering normal model output.

The Bitwarden machine token itself must be protected like any other credential:
never print it, log it, commit it, put it in a skill file, or send it through
ordinary Telegram or chat messages. Do not include it in backup bundles or
support reports.

## Rollback

Rollback is explicit:

1. Verify that the retained server env is still present and protected.
2. Switch the environment source back to server-local env.
3. Run the health check.
4. Only after the user separately approves it may GoAster remove migrated
   Bitwarden secrets or the retained local copy.

Do not delete Bitwarden projects, machine accounts, tokens, or secrets
automatically.
