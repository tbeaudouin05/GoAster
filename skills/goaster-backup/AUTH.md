# Authorization

A verified server-local rollback bundle is mandatory before every deployment.
If it cannot be created and checked for the exact host and active release,
stop; do not deploy. Exclude credentials, environment files, tokens, private
keys, and other secrets by default. Including any secret requires explicit
approval naming the data, purpose, protection, retention, and deletion plan.

OVH snapshot or backup is optional and never implied by the current
provisioning credentials. It needs a separate, project-scoped permission
extension beyond the current provisioning rights, exact method/path and
endpoint verification against current official OVH documentation or the
project UI, and explicit approval of the exact resource and operation. Obtain
separate approval for cost, retention, deletion, and any secret-inclusive
backup. Never guess an OVH endpoint or permission row.
