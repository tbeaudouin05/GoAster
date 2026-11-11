# GoAster Bitwarden Secrets Manager

Use this skill when the user wants GoAster to store or access environment secrets in Bitwarden Secrets Manager.

Setup and migration change credential storage. Explain the proposed steps first
and obtain explicit approval before creating machine-account access, writing
secrets, switching GoAster away from the server-local environment, or deleting
the local copy.

## Setup principles

- Bitwarden Secrets Manager is optional. Do not block normal GoAster installation or operation if the user does not choose it.
- Use Secrets Manager, not a Bitwarden secure note or Password Manager vault item.
- GoAster uses one machine account and one project named exactly **`goaster`**.
- GoAster creates the project automatically if it does not already exist. Tell the user **not to create a project manually**.
- Do not ask the user which project to use or require them to provide a project ID.
- The only credential the user needs to provide is `BWS_ACCESS_TOKEN`.

## User onboarding

Guide the user through these steps:

1. Open their Bitwarden organization and activate **Secrets Manager**, if it is not already active. They can choose the free tier if it meets their needs.
2. Open **Secrets Manager** and create a machine account for GoAster.
3. Open the machine account’s **Access tokens** section and create a token. Select **Read and write** in the permissions dropdown.
4. Copy the token when Bitwarden displays it.
5. Provide it to GoAster through an approved secure method, under the exact name:

   ```text
   BWS_ACCESS_TOKEN=<machine-account-access-token>
   ```

   If GoAster’s `/env` command provides a secure way to enter environment variables, use it. Do not send the token through ordinary chat, Telegram, logs, tickets, or email.
6. Do not create a project. GoAster will find or create its own `goaster` project.

## GoAster project setup

1. Check that the Bitwarden Secrets Manager CLI, `bws`, is available. Install it through the approved installation method if necessary.
2. Use `BWS_ACCESS_TOKEN` to connect to Secrets Manager.
3. Look for an accessible project whose name is exactly `goaster`.
4. If exactly one exists, use it.
5. If none exists, create `goaster` and verify that it is accessible.
6. If multiple accessible projects have that exact name, stop and report the ambiguity. Do not guess.
7. If access or project creation fails, report the error without exposing the token or secret values. Do not switch to a different project.

GoAster must use only this `goaster` project for reading and writing its secrets. Never ask the user to choose a different project.

## Migration from the server environment

When the user chooses to migrate existing environment variables:

1. Confirm the source environment and the migration scope. The destination is always the `goaster` project.
2. Inspect variable names without displaying their values in chat, logs, tickets, or progress messages.
3. Create one Secrets Manager secret per required variable, preserving each variable name exactly.
4. Verify every secret by name and controlled retrieval, without displaying its value.
5. Keep the existing server environment as a protected rollback copy until the user explicitly approves its removal.
6. Switch GoAster to Bitwarden only after all required secrets have been verified.
7. Run the relevant health check and report non-secret results and counts.

If any secret cannot be created or verified, do not switch sources or delete the existing environment. Report the affected variable names and leave the existing setup in place.

## Normal operation

- Read and write GoAster secrets only in the `goaster` project.
- Validate that required variable names exist without exposing their values.
- Protect `BWS_ACCESS_TOKEN` as a credential. Never print, log, commit, or include it in a skill file, backup bundle, or support report.
- Do not silently fall back to the server environment if Bitwarden is selected but unavailable. Report the failure for manual follow-up.
- Keep read-and-write access if GoAster needs to manage secrets. Reduce access only if the user chooses a read-only setup and understands that GoAster will no longer be able to write secrets.

## Rollback and deletion

To roll back, verify the protected server environment still exists, switch back to it, and run the health check.

Never automatically delete the `goaster` project, machine account, token, secrets, or retained server environment. Require the user’s explicit approval for the specific deletion.
