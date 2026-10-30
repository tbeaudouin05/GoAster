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

### Normal resource defaults

Use these defaults when the user has not already given a preference. Do not
repeatedly ask the user to choose the workload, budget, operating system, SSH
method, or network exposure during ordinary onboarding:

- Workload: a GoAster remote agent.
- Operating system: the latest Ubuntu LTS image available for the selected
  deployment region.
- Machine: the smallest available instance that is suitable for running the
  GoAster remote agent.
- Access: SSH keys only. Use an appropriate key already available to the
  project or host. Never fall back to password access. If no usable key exists,
  stop and explain the key setup needed before provisioning.
- Inbound network: allow only SSH (TCP 22), restricted to the known GoAster
  host/operator source IP. Do not expose application ports by default. If a
  suitably restricted SSH rule cannot be established, stop and explain the
  blocker before provisioning.
- Budget: estimated recurring OVH charges must not exceed **US$8 per month**.
  Include the instance and required storage, public address, and predictable
  network charges in the estimate. This is a hard default ceiling. If the
  smallest suitable configuration exceeds it, do not create it; report the
  estimate and wait for the user to explicitly override the ceiling.

The user may explicitly override any default. An OVH account/API region and
endpoint are separate from the Public Cloud deployment region. Reuse the
account endpoint already configured or identified from the Create API Keys
page; do not ask for it again. Ask for the user's preferred deployment region
only when it is genuinely unknown from the conversation, configuration, and
project context, and only after they confirm which project to inspect. If it is
known, continue without asking. Do not confuse a known account/API region with
a deployment-region preference.

After the read-only checks, report the selected project and deployment region,
all defaults or explicit preferences being applied, the proposed machine and
access/network settings, and the current estimated recurring cost. If current
pricing cannot be established or any required cost is unknown, stop and say
what could not be estimated. Then stop for the user's explicit approval of
that exact configuration and cost before any billable creation or other
mutation. Do not treat silence or approval of a different action as consent.

### Public Cloud project readiness

The user must create the Public Cloud project in OVHcloud Manager and complete
payment-method activation before starting provisioning. GoAster does not
create projects. Direct the user to open [Public Cloud in OVHcloud
Manager](https://www.ovh.com/manager/#/hub/public-cloud), sign in, accept the
terms, choose **Discover the Public Cloud universe**, and create and name the
project. After creation, **Discovery mode** is the restricted starting state.
If the project still shows the **Discovery mode** label, have the user click
OVHcloud Manager's **Enable** or **Activate** button only to start the
payment-method activation flow. Then have them provide or add a payment method
in that flow to deactivate and exit Discovery mode. The required final state
is an active project with no **Discovery mode** label. GoAster cannot
provision a server while the project remains in Discovery mode. See OVHcloud's
[official project creation guide](https://docs.ovhcloud.com/en/guides/public-cloud/cross-functional/create-a-public-cloud-project).

Tell the user to return to GoAster only after the project is active and the
Discovery mode label is gone. After the credentials are configured through
the local `/env` flow, verify the user-created project with read-only
`GET /cloud/project` and, as needed, `GET /cloud/project/*`. Treat a project as
usable only when the agent can verify its active status, non-empty project ID,
and name. Never require, infer, or filter on a particular project name,
including `GoAster`; a different name is not a mismatch or a reason to reject
an otherwise usable project. For example, an active project named `Production`
remains usable even though its name differs from `GoAster`. The project name
is not related to the API application name. After the read-only
`GET /cloud/project` discovery, present each discovered project's name, ID,
and status to the user. If there is no usable active project, stop and direct
the user back to OVHcloud Manager to
finish creating the project or completing payment-method activation; do not
attempt project creation through the API. If exactly one usable project is
available, ask whether the user wants to use that named project. If multiple
usable projects are available, ask which named project they want to use. Wait
for the user's answer before making any project-specific inspection calls. A
yes or selection authorizes only the next read-only inspection flow; it is not
approval to change or create any resource.

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

In **Rights**, add exactly these three method/path rows. The `+` button adds a
row; use it to add the remaining rows:

| Method | Path |
| --- | --- |
| GET | `/cloud/project` |
| GET | `/cloud/project/*` |
| POST | `/cloud/project/*` |

Do not add other method/path rows or replace `*` with a project ID. After the
user creates the keys, have them enter the values only through GoAster's local
`/env` flow. Keep the Application Key, Application Secret, and Consumer Key
out of chat, screenshots, and reports. No account-level project-creation
permission is needed because the user creates the project in OVHcloud Manager.
Once the configured OVH client authenticates, use the read-only project
verification workflow above; `POST /cloud/project/*` is for later
project-scoped provisioning operations. The user does not enter a project ID
on the Create API Keys screen.

Price estimates come from OVHcloud's [current Public Cloud price
list](https://www.ovhcloud.com/en/public-cloud/prices/); these API key rights
are the agreed set for GoAster server provisioning.

Use the generic `/file` command in the local terminal to inspect a file,
including a dashboard screenshot. It accepts supported images and documents
and asks for an explanation of what the file shows, next steps, and risks. Do
not select files containing API keys, application secrets, or other secret
values. Do not transcribe, request, or use any secret that appears in a file;
secret values belong only in the local `/env` flow.

1. Use the known OVH account endpoint and credentials from a user-approved
   local credential source already available to the host. If none is available,
   stop and direct the user to configure one through the local `/env`
   environment-file flow. Do not ask again for known account/API details. Do
   not ask for a preferred deployment region before project selection is
   confirmed. Apply the normal resource defaults above for any choice the user
   has not explicitly overridden.
2. Use `GET /cloud/project` to list Public Cloud projects. Do not check the
   project name against `GoAster` or any other expected name. Present each
   discovered project's name, ID, and status. If there is no usable active
   project, stop and direct the user to create one or complete
   payment-method activation in OVHcloud Manager; do not attempt to create it
   through the API. If exactly one usable project is available, ask whether
   the user wants to use it; if multiple usable projects are available, ask
   which one they want to use. Wait for confirmation or selection before
   continuing. That answer permits only read-only inspection. Use the
   preferred deployment region if known; otherwise ask for it. Continue with
   read-only checks for its regional quota, available regions, images,
   flavors, public network, stored SSH keys, and existing instances. Resolve
   only blocking ambiguities, such as no usable SSH key. Present the exact
   proposed resource, applied assumptions, and current price estimate after
   these read-only checks and before changing OVH state.
3. Before creating or changing any billable resource, deleting or replacing
   data or resources, or changing access or network exposure, explain the exact
   action, affected resources, costs or consequences, and obtain explicit user
   confirmation. For resource creation, the confirmation must
   follow the read-only assumptions and cost report and cover that exact
   configuration. Do not exceed the US$8/month default ceiling without an
   explicit budget override. Confirmation for one action does not authorize a
   different action.
4. After the relevant confirmation, run the requested OVH API or installed
   client steps through `shell_execute`. Wait for the resource to become ready,
   then verify its ID, state, address, and requested access or setup. Do not
   retry an uncertain mutation blindly or delete a partially provisioned
   resource without confirmation.
5. Remove temporary scripts when finished. Do not install tools or add repository dependencies as part of provisioning without the user's approval.

## Report

Give a concise result: project and region, resource IDs and selected sizes, estimated ongoing charges, verification status, and any failure or partial resource that remains. State clearly when a resource may still incur charges. Never claim success unless the host output and OVH state verify it; do not include secrets.
