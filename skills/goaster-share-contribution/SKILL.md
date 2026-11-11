---
name: goaster-share-contribution
description: Share a GoAster skill or source feature by proposing a Markdown description in upstream contributions/ that points to its implementation in a contributor fork.
---

# Share a GoAster contribution

Use this skill when the user asks to share a GoAster skill or source feature.
The shared contribution is one Markdown description in
`tbeaudouin05/GoAster`'s `main` branch `contributions/` folder. Submit it
through the normal approved upstream pull request process. The description
identifies the contributor's fork and the implementation location. The actual
skill or source implementation stays in that fork; do not copy it upstream.

## Prepare the description

- Identify whether the contribution is a skill or source feature.
- Verify the contributor fork and the implementation path in that fork. Use
  the applicable `goaster-fork`, `modify-goaster-source-code`, or other
  handoff to locate existing work; do not edit or move its implementation as
  part of sharing.
- Create one concise Markdown file under `contributions/` describing what the
  contribution does, how it is used where relevant, and where to find it.
  Include the fork's `owner/repository` and a clear implementation path. Add
  a stable revision or other reference when available. Do not include secrets
  or private local paths.
- Follow the format of existing contribution descriptions and preserve
  unrelated files and user changes.

## Review and publish

Before any public write, show the exact Markdown contents and proposed
upstream repository, base branch, and path. Obtain the user's approval for
the public contribution and its pull request. Use the repository's normal
approved PR process; do not invent or require a contribution-specific branch
name. Verify the resulting PR targets `tbeaudouin05/GoAster`'s `main` and
contains only the intended description. Do not merge it; upstream review and
approval govern merging.

Report the contribution file and PR result. If the fork, implementation
location, or destination is uncertain, resolve that uncertainty before
publishing.
