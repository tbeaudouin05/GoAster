# GoAster fork

Prepare and verify the user's public fork of `tbeaudouin05/GoAster`
and its two standard branches:

- `goaster-customizations`: persistent user changes.
- `goaster-contributions`: contribution manifests.

Use the [github-auth skill](../github-auth/SKILL.md) and its
[authorization rules](../github-auth/AUTH.md) for all GitHub access.

## Preflight

1. Verify the authenticated GitHub account and the public upstream
   repository, including its default branch and commit.

2. Tell the user the fork will be public and named `GoAster`. Confirm
   the GitHub account and, if local setup is requested, checkout path.
   The fork owner must be the authenticated user.

3. Check for `<account>/GoAster`. If it exists, verify that it is the
   user's public fork of `tbeaudouin05/GoAster`, and verify its default
   branch and commit. If it is not the correct fork, stop and ask the
   user how to proceed. A 404 alone does not prove it is absent.

4. Check both standard branches if the fork exists. Ask the user
   explicitly before reusing an existing branch at its observed commit.
   For a missing branch, propose the fork's default commit as its base.

   If the fork does not exist, propose the verified upstream default
   commit as the base for both branches. Check the branches after the
   fork is created.

## Approval

Before creating anything, present the exact plan: account, public fork,
default branch and base commit, both branch names and their creation or
reuse status, and any local checkout path and setup.

Get explicit approval. If an approved detail changes, stop and obtain
new approval. Recheck the account and target immediately before each
creation.

## Create and verify

1. If needed, create the approved fork once. Wait for it to become ready
   and verify its identity, visibility, parent, default branch, and commit.
   Do not automatically retry an uncertain creation.

2. Recheck each standard branch. Create a missing branch at its approved
   base commit. If a branch already exists, ask the user to explicitly
   choose whether to reuse it at its observed commit. Never overwrite it.

3. If a creation fails or times out, inspect the result without writing.
   Stop if the result is uncertain; do not blindly retry.

4. If local setup was approved, use the approved path, preserve existing
   changes, set `origin` to the verified fork, and check out
   `goaster-customizations`. Keep any `upstream` remote fetch-only.

5. Verify the upstream, fork, and both exact branch commits. New branches
   must match their approved base; reused branches must match the commits
   the user explicitly chose.

## Handoff

Report the verified account, fork URL and visibility, upstream default
branch and commit, both branch names and commits, whether each was created
or reused, and the checkout path if applicable.

Stop after verification. Do not modify or push to the upstream repository.
