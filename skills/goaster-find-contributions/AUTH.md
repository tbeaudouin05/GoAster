# Authorization

This skill performs read-only GitHub discovery only. Use the canonical
[github-auth authorization](../github-auth/AUTH.md), verify the exact public fork
and `goaster-contributions` ref, and never infer identity or paths. Do not
write to either branch, alter local files, install a skill, execute discovered
content, create a fork or branch, commit, push, or create a pull request.
