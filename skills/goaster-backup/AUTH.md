# Authorization

A verified server-local rollback bundle is mandatory before every deployment.
If it cannot be created and checked for the exact host and active release,
stop; do not deploy. Exclude credentials, environment files, tokens, private
keys, and other secrets by default. Including any secret requires explicit
approval naming the data, purpose, protection, retention, and deletion plan.

OVH backup is optional. The existing project-scoped GET and POST
rows cover documented Public Cloud instance backup creation and inspection;
they do not themselves approve those actions. Verify the current endpoint,
method/path, and rights against official OVH documentation and the project UI.
If GoAster performs deletion or retention changes, it needs `DELETE` rights for
the relevant documented endpoint and any other required rights. Obtain explicit
approval for the exact resource, operation, and cost, and separate approval for
retention, deletion, and any secret-inclusive backup. Never guess an endpoint or
permission row.

Before creating a backup, tell the user they are responsible for managing and
deleting old backups and schedules to minimize ongoing storage costs. GoAster
must never delete them automatically unless separately approved; separately
approve any OVH-managed schedule rotation that deletes older backups.
