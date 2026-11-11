# GoAster fork

Prepare and verify the user's public fork of `tbeaudouin05/GoAster`
and its standard customization branch for both skills and source changes:

- `goaster-customization`: skill and source customization implementations.

Use the [github-auth skill](../github-auth/SKILL.md) and its
[authentication guidance](../github-auth/AUTH.md) for all GitHub access.

## Preflight

1. Verify the authenticated GitHub account and the public upstream
   repository, including its default branch and commit.

2. Tell the user the fork will be public and named `GoAster`. Confirm
   the GitHub account and, if local setup is requested, checkout path.
   The fork owner must be the authenticated user.

3. Check for `<account>/GoAster`. If it exists, verify that it is the
   user's public fork of `tbeaudouin05/GoAster`, and verify its default
   branch and commit. If the upstream or fork cannot be verified, stop and
   report the blocker. If it is not the correct fork, stop and ask the user how
   to proceed. A 404 alone does not prove it is absent.

4. Check the standard source branch if the fork exists. Ask the user
   explicitly before reusing an existing branch at its observed commit.
   For a missing branch, propose the fork's default commit as its base.

   If the fork does not exist, propose the verified upstream default
   commit as the base for the branch. Check the branch after the
   fork is created.

## Approval

Before creating anything, present the exact plan: account, public fork,
default branch and base commit, the standard branch name and its creation
or reuse status, intended actions, and any local checkout path and setup. Do
not ask the user to choose arbitrary branch names.

Get explicit approval. If an approved detail changes, stop and obtain
new approval. Recheck the account and target immediately before each
creation.

## Create and verify

1. If needed, create the approved fork once. The forks endpoint creates a
   separate public fork and does not modify upstream contents. Wait for it to
   become ready and verify its identity, visibility, parent, default branch,
   and commit.
   Do not automatically retry an uncertain creation.

2. Recheck the standard branch. Create a missing branch at its approved
   base commit. If a branch already exists, ask the user to explicitly
   choose whether to reuse it at its observed commit. Never overwrite it.

3. If a creation fails or times out, inspect the result without writing.
   Stop if the result is uncertain; do not blindly retry.

4. If local setup was approved, use the approved path, preserve existing
   changes, set `origin` to the verified fork, and check out
   `goaster-customization`. Keep any `upstream` remote fetch-only. This
   checkout may be used to prepare and edit any customization, including
   adding, moving, or updating skill files.

5. Verify the upstream, fork, and exact branch commit. A new branch
   must match its approved base; a reused branch must match the commit
   the user explicitly chose.

## Handoff

Report the verified account, fork URL and visibility, upstream default
branch and commit, the branch name and commit, whether it was created
or reused, and the checkout path if applicable.

Stop after verification. Never write to the upstream repository; do not modify
or push to it. Only the approved fork may receive public changes. Before any
commit or push, show the exact changes and destination branch and obtain
explicit approval. Before an upstream pull request, verify that the complete
skill or source implementation is committed on `goaster-customization` and
publicly accessible. Approval for one write does not authorize deletes,
overwrites, force pushes, resets, changes to upstream contents, refs, settings,
or access. Keep credentials and device information in the user's local GitHub
authentication flow; never request or report secrets.
