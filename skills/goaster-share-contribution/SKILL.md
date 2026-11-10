---
name: goaster-share-contribution
description: Safely publish an approved GoAster source or local-skill contribution from a verified user fork using goaster-customizations and manifest-only goaster-contributions branches.
---

# Share a GoAster contribution

Use this skill when the user asks to share one GoAster customization publicly.
Load the [github-auth skill](../github-auth/SKILL.md) first and use the verified
handoffs from [goaster-fork](../goaster-fork/SKILL.md) and
[goaster-source](../goaster-source/SKILL.md) when they apply. This workflow
publishes a small manifest in the user's own fork; it never writes to
`tbeaudouin05/GoAster`.

## Ownership and branch contract

- `github-auth` owns GitHub authentication, identity checks, read-only
  discovery, and the generic approval rules for GitHub writes.
- `goaster-fork` owns fork preparation and branch creation. Use it when either
  standard branch is missing; do not invent a fork or branch setup here.
- `goaster-source` owns source-code changes in the approved checkout. This
  skill does not edit or duplicate source implementation.
- This skill owns validation and staging of a selected local skill artifact,
  and the contribution manifest that describes an already committed change.

Use these exact branch names in the approved user fork:

- `goaster-customizations` contains the user's existing source changes and any
  local skill artifact being shared.
- `goaster-contributions` contains contribution manifests only. It must not
  contain copied Go code, source files, or skill implementation.

Do not infer the fork owner/name, checkout path, branch base, artifact path,
or manifest path from a skill name, directory name, Git remote, or local Git
configuration. Require the exact values from the user or a verified handoff.
Keep upstream read-only, preserve every existing user change, and never use a
symlink or other pointer in place of a regular artifact file.

## Read-only preflight

Before proposing a write:

1. Verify the `github.com` login with `github-auth`, then verify the exact
   public fork, its owner/name, its parent `tbeaudouin05/GoAster`, and both
   exact branch refs and full commit SHAs. Recheck the login and destination
   immediately before each later GitHub write.
2. Verify the approved checkout path and that `origin` fetches and pushes only
   to that exact fork. Any `upstream` remote is fetch-only. Inspect local
   status and preserve dirty or staged user changes; do not reset, clean,
   overwrite, or silently stash them.
3. Require both standard branches. If one is absent, stop and use
   `goaster-fork` to propose its exact base and obtain approval before creating
   it. If the handoff names another branch, do not rename or merge it
   silently.
4. Decide whether this is a source contribution or a skill contribution.
   Keep the two paths separate.

## Source contribution

The requested source change must already be committed in the approved fork's
`goaster-customizations` branch. Use `goaster-source` for any missing source
edit or source commit. Verify the exact full commit SHA and the existing path
or paths to be shared before preparing a manifest. If the source branch is
dirty or the requested change is not in a committed tree, stop and hand back
to `goaster-source`.

The manifest on `goaster-contributions` references that exact repository,
branch, full SHA, and existing path. Do not copy the implementation into the
manifest branch, and do not create a second source change just to publish its
manifest.

## Local skill contribution

Skills normally live only on the GoAster server under its local artefacts
directory; a skill name or manifest alone is not a publishable artifact. The
selected skill's exact local path must be supplied or verified; never guess it
from the name.

Before publishing a skill manifest, complete this sequence in order:

1. Inspect the selected artifact as a real directory containing exactly the
   regular files `META.md`, `SKILL.md`, and `AUTH.md`. Verify that the metadata
   name matches the selected skill, the files are valid text, no symlinks or
   other pointer-like entries are present, and no secrets or credential files
   are included. A missing, ambiguous, or changed artifact is a blocker.
2. Show the exact source path, destination checkout, destination
   `skills/<skill-name>/` path, and the current `goaster-customizations` SHA.
   Obtain approval for the local copy/staging write. Copy the three regular
   files into the approved fork's `goaster-customizations` branch. If the
   destination already differs or contains user changes, preserve it and
   stop rather than replacing it.
3. Stage only the selected skill artifact. Show the exact staged paths, branch,
   parent SHA, and commit message, then obtain separate explicit approval to
   commit. Commit the artifact on `goaster-customizations`; never include
   unrelated user changes.
4. Verify the new full commit SHA and inspect the committed tree and file
   contents. Before publishing anything, obtain separate approval to push that
   exact commit to the verified fork's exact `goaster-customizations` ref.
   Recheck the destination and verify the remote branch resolves to that
   commit and contains the artifact.
5. Only after the artifact commit is verified, create the contribution
   manifest on `goaster-contributions` that references the committed artifact.
   The manifest branch contains the reference only; it never receives a copy
   of the skill files.

If the artifact is already present at the exact verified commit, inspect and
reuse that commit rather than creating a duplicate. An uncommitted or
conflicting copy must follow the approval and commit sequence above.

## Manifest format and publication

Create one regular JSON file at an approved path such as
`contributions/<slug>.json` on `goaster-contributions`. Use a user-approved
lowercase slug and this minimal schema:

```json
{
  "schema": "goaster-contribution/v1",
  "kind": "skill",
  "name": "<skill-name>",
  "description": "<short non-secret description>",
  "source": {
    "repository": "<verified fork owner/name>",
    "branch": "goaster-customizations",
    "commit": "<full 40-character commit SHA>",
    "path": "skills/<skill-name>"
  }
}
```

For source code, use `"kind": "source"` and set `source.path` to the
existing source path. Keep the exact repository, standard branch, full commit
SHA, and path; do not use a moving branch tip without a SHA. Do not include
credentials, private paths, or secret values. Inspect an existing manifest at
the same path and preserve it unless the user explicitly approves an exact
replacement.

Before writing or committing the manifest, show the exact manifest path,
contents, branch SHA, and referenced customization commit. Obtain a separate
explicit approval for the manifest write and another for its commit. Verify
that the manifest branch still contains manifests only, then obtain a separate
approval to push the exact manifest commit to the verified fork. Re-read the
remote ref and manifest after the push.

Creating a pull request is not part of sharing. If the user later requests
one, perform a fresh read-only check and obtain separate approval for its exact
head, base, repository, and title before using `github-auth`; never create one
automatically.

Report only the verified fork, branch, full SHAs, manifest path, and result.
If any identity, path, SHA, or write result is uncertain, stop and report the
uncertainty instead of retrying or guessing.
