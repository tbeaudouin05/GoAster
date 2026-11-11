# Authorization

Work only in the user-approved fork's `goaster-customizations` branch and local
checkout from the fork handoff. The user may customize any source, Markdown
skill, packaging asset, documentation file, or test in that fork; do not
silently narrow the request to a fixed patch list. Preserve existing user
changes.

Keep upstream read-only and do not merge, rebase, reset, overwrite, delete,
or create a pull request unless that exact action is separately approved.
Never write secrets, tokens, private keys, or credential files into the
checkout. A changed repository, branch, checkout path, or host requires fresh
approval. An unexpected SHA change also requires fresh approval; a SHA change
from a commit made as part of the requested customization does not.
