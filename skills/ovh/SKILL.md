---
name: ovh
description: Provision OVHcloud resources from the host with explicit user approval.
---

# OVH provisioning

This is an instruction skill for the GoAster agent, not executable code.

## Run host commands

Use GoAster's built-in `shell_execute` tool to create and run a temporary script on the host. Its input is a JSON object with required string `command` and optional string `cwd` and integer `timeout_ms` (omitted or zero uses GoAster's fixed safety timeout; choose a positive bounded timeout for longer work). `command` runs through the host's native shell. Use a platform-matched script (`.sh` for a POSIX shell, `.ps1` for PowerShell on Windows) in the host temporary directory; create, run, and remove it in one call when practical. Do not create provisioning files in the repository.

Read the complete result: `stdout`, `stderr`, and `exit_code`; an `execution_error` may also be present. Treat a nonzero exit or timeout as a failure or uncertain result, not success. After an uncertain create request, inspect OVH state before retrying so you do not create a second paid resource. Never print credentials or include them in script text, command arguments, or reports.

## Provisioning workflow

OVH (OVHcloud) is a company that provides cloud servers and other online
services. GoAster needs your OVHcloud account to check what your account can
use and, only after you approve, create the cloud resources you request.

Sign in or create an account on the [official OVHcloud account
page](https://www.ovh.com/auth/).

### Public Cloud project discovery and creation

After the credentials are configured, start with the read-only `GET
/cloud/project`. Treat a project as usable only when it has a non-empty
project ID and name that the agent can verify. The agent can automatically
create a missing project through the confirmed workflow below.

If `GET /cloud/project` returns no usable project, ask the user for the desired
project name and explain that `POST /cloud/project` creates a Public Cloud
project at the account level. This is a separate, billing-relevant account
action, not a billable resource creation step. Present the exact project name
and relevant account or billing consequence, then obtain explicit user
confirmation immediately before calling `POST /cloud/project`. A general
approval to provision resources does not authorize this call.

After that confirmation, call `POST /cloud/project` with the confirmed name.
Verify the returned project ID and name from the response and a follow-up
read-only project lookup such as `GET /cloud/project/*` with that project ID.
Stop if either value is missing or does not match the confirmed project; do
not continue to quota or resource checks until the ID and name are verified.
Report the verified project ID and name. If a usable project already exists,
confirm the selected project's ID and name before continuing.

The user may create the project manually instead: open [Public Cloud in
OVHcloud Manager](https://www.ovh.com/manager/#/hub/public-cloud), sign in,
accept the terms, and choose **Discover the Public Cloud universe**; name the
project and select a payment method when prompted. Activate a project in
discovery mode before provisioning resources. See OVHcloud's [official project
creation guide](https://docs.ovhcloud.com/en/guides/public-cloud/cross-functional/create-a-public-cloud-project).

When environment variables are needed, name each one, briefly explain its
purpose and where to find its value (the selected OVH client's docs or
OVHcloud credential settings), and invite the user to ask for more explanation.
Secret values must be entered only through GoAster's local `/env`
environment-file flow; never ask for or accept them in chat.

### Create API Keys screen

Before giving key-creation instructions, ask the user to paste the OVHcloud
Create API Keys page URL they are currently viewing. The page URL is not a
secret; do not ask the user to paste API keys, passwords, or other secrets in
chat. Use the URL's hostname to detect the OVHcloud account region and choose
the canonical API endpoint below. The endpoint is based on the OVHcloud
account, not the Public Cloud deployment region.

| Account region | Create API Keys URL examples | Canonical OVH API endpoint |
| --- | --- | --- |
| Europe | `https://auth.eu.ovhcloud.com/api/createToken` or `https://eu.api.ovh.com/createToken/` | `https://eu.api.ovh.com` |
| Canada | `https://auth.ca.ovhcloud.com/api/createToken` or `https://ca.api.ovh.com/createToken/` | `https://ca.api.ovh.com` |
| United States | `https://auth.us.ovhcloud.com/api/createToken` or `https://api.us.ovhcloud.com/createToken/` | `https://api.us.ovhcloud.com` |

For example, `https://auth.ca.ovhcloud.com/api/createToken` identifies the
Canada account region and maps to `https://ca.api.ovh.com`. Ignore the URL path
and query when matching a recognized hostname. If the hostname does not match
one of the supported regions, ask the user to confirm their account region
before proceeding; do not guess. The page may label the application-name field
**Script name**. The user signs in directly on the OVHcloud page; do not send
OVH login details through GoAster chat.

Tell the user to enter these values:

| Screen field | Enter |
| --- | --- |
| Application name / Script name | `GoAster` |
| Description | `GoAster server provisioning` |
| Validity | `Unlimited` |
| Restricted IPs | Leave empty |

In **Rights**, add exactly these four method/path rows. The `+` button adds a
row; use it to add the remaining rows:

| Method | Path |
| --- | --- |
| GET | `/cloud/project` |
| GET | `/cloud/project/*` |
| POST | `/cloud/project` |
| POST | `/cloud/project/*` |

Do not add other method/path rows or replace `*` with a project ID. After the
user creates the keys, have them enter the values only through GoAster's local
`/env` flow. Keep the Application Key, Application Secret, and Consumer Key
out of chat, screenshots, and reports. Once the configured OVH client
authenticates, use the read-only project discovery and creation workflow above.
The collection-level `POST /cloud/project` is only for the separately
confirmed account-level project creation; `POST /cloud/project/*` is for later
project-scoped operations. The user does not enter a project ID on the Create
API Keys screen.

Price estimates come from OVHcloud's [current Public Cloud price
list](https://www.ovhcloud.com/en/public-cloud/prices/); these API key rights
are the agreed set for GoAster server provisioning.

Use the generic `/file` command in the local terminal to inspect a file,
including a dashboard screenshot. It accepts supported images and documents
and asks for an explanation of what the file shows, next steps, and risks. Do
not select files containing API keys, application secrets, or other secret
values. Do not transcribe, request, or use any secret that appears in a file;
secret values belong only in the local `/env` flow.

1. Clarify the OVH project, endpoint and region, workload, budget, operating system, access method, and required network exposure. Use credentials only from a user-approved local credential source already available to the host. If none is available, stop and direct the user to configure one through the local `/env` environment-file flow.
2. Use `GET /cloud/project` to list Public Cloud projects. If there is no
   usable project, follow the project discovery and creation workflow above:
   obtain the separate explicit confirmation before `POST /cloud/project` and
   verify the returned project ID and name. Confirm the selected project with
   the user against the project name and ID shown in OVHcloud Manager. Then use
   read-only checks for its regional quota, available regions, images, flavors,
   public network, stored SSH keys, and existing instances. Resolve ambiguous
   choices with the user. Present the exact proposed resource and current
   price estimate before changing OVH state.
3. Before creating or changing any billable resource, deleting or replacing
   data or resources, or changing access or network exposure, explain the exact
   action, affected resources, costs or consequences, and obtain explicit user
   confirmation. Project-creation confirmation is separate and does not
   authorize a billable resource. Confirmation for one action does not
   authorize a different action.
4. After the relevant confirmation, run the requested OVH API or installed
   client steps through `shell_execute`. Wait for the resource to become ready,
   then verify its ID, state, address, and requested access or setup. Do not
   retry an uncertain mutation blindly or delete a partially provisioned
   resource without confirmation.
5. Remove temporary scripts when finished. Do not install tools or add repository dependencies as part of provisioning without the user's approval.

## Report

Give a concise result: project and region, resource IDs and selected sizes, estimated ongoing charges, verification status, and any failure or partial resource that remains. State clearly when a resource may still incur charges. Never claim success unless the host output and OVH state verify it; do not include secrets.
