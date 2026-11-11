# Contributing to GoAster

External contributions are shared as Markdown descriptions. The
implementation itself stays in your public fork.

## Prepare your implementation

All your GoAster customizations, including skills and source changes, belong
on your fork's `goaster-customization` branch. Build new GoAster binaries
from that branch so your deployed version includes your customizations.

For a skill, place its files in
`contributed-skills/<skill-name>/` on `goaster-customization`.

For a source feature, keep its implementation in the appropriate source
files on `goaster-customization`.

Commit and push the implementation so it is accessible on GitHub. Check
your files for secrets and private information before publishing them.

## Submit your contribution

1. Sync your fork's `main` with the latest upstream `main`.
2. Create a new contribution branch in your fork from that synced `main`.
3. Add a `.md` description directly inside `contributions/`. Explain what
   the contribution does and include your fork's `owner/repository`, the
   `goaster-customization` branch, and the implementation path. Include a
   stable revision when available.
4. Commit and push your contribution branch to your fork.
5. Open a pull request from that branch to
   `tbeaudouin05/GoAster`'s `main`.

Your pull request must only change `.md` files directly inside
`contributions/`. It must not change files anywhere else or inside
subfolders.

A maintainer will review the pull request and decide whether to merge it.
