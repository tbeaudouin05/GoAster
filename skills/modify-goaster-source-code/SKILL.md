# GoAster source customization

You can customize GoAster in the fork created using [goaster-fork](../goaster-fork/SKILL.md). Make the changes you want on the `goaster-customization` branch and, after approval, commit and push them to your fork on GitHub.

The next steps explain how to build and test those changes, then safely switch
the running GoAster to the new version. Ask the user before making any source,
Markdown skill, packaging, documentation, or test change; preserve existing
user changes. Do not silently narrow the request to a fixed patch list.

Work only in the approved fork checkout on the `goaster-customization` branch.
Keep upstream read-only. Do not merge, rebase, reset, overwrite, delete, or
create a pull request unless that exact action is separately approved. Never
write secrets, tokens, private keys, or credential files into the checkout. A
changed repository, branch, checkout path, or host requires fresh approval. An
unexpected SHA change also requires fresh approval; a SHA change from a commit
made as part of the requested customization does not.
