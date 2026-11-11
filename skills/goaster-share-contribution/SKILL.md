---
name: goaster-share-contribution
description: Share a GoAster skill or source feature by proposing a Markdown description in upstream contributions/ that points to its implementation in a contributor fork.
---

# Share a GoAster contribution

Use this skill when the user asks to share a GoAster skill or source feature.

A shared contribution is a Markdown description in
`tbeaudouin05/GoAster`'s `contributions/` folder. The implementation stays in
the contributor's fork; do not copy it upstream.

Follow the upstream repository's `CONTRIBUTING.md`.

## Prepare the implementation

- Identify whether the contribution is a skill or source feature.
- Verify the contributor's public fork.
- All customizations belong on the fork's `goaster-customization` branch.
- For a skill, place its files in
  `contributed-skills/<skill-name>/` on that branch.
- For a source feature, keep its implementation in the appropriate source
  files on that branch.
- Verify that the implementation is committed and pushed to
  `goaster-customization` so others can access it on GitHub.
- Before committing or pushing, check the files for secrets and private
  information. Obtain the user's approval before any public write.
- Use the applicable `goaster-fork`, `modify-goaster-source-code`, or other
  handoff where needed. Preserve unrelated files and user changes.

## Prepare the description

- Create one concise `.md` file directly inside `contributions/`. Describe
  what the contribution does and how it is used where relevant.
- Include the fork's `owner/repository`, `goaster-customization` branch,
  and implementation path. Add a stable revision when available.
- Do not include secrets or private local paths.
- Follow the format of existing contribution descriptions.

## Review and publish

Before any public write, show the user the exact files to be published,
the proposed fork and branch, and, for the description, the upstream
repository, base branch, file path, and exact Markdown contents. Obtain
approval for the public writes and pull request.

Sync the fork's `main` with the latest upstream `main`. Create a new
contribution branch in the fork from that synced `main`. Commit only the
description file directly inside `contributions/`, push the branch to the
fork, and open a pull request to `tbeaudouin05/GoAster`'s `main`.

Verify that the implementation is accessible at the stated fork, branch,
and path. Verify that the pull request targets upstream `main` and contains
only the intended `.md` file directly inside `contributions/`.

Do not merge the pull request; upstream review and approval govern merging.

Report the implementation location and pull request result. If the fork,
implementation location, or destination is uncertain, resolve that
uncertainty before publishing.
