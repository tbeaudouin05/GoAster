---
name: ovh
description: Provision OVHcloud resources from the host with explicit user approval.
---

# OVH provisioning

## Purpose

This is an onboarding utility, not the final GoAster agent. It helps set up
the real GoAster agent on a low-cost OVH Public Cloud server and connect it to
Telegram. You provide your OVH credentials and Telegram bot token through the
local `/env` flow. You can ask the bot where to find either credential or
anything else. Keep every reply very concise: give only the information needed
now and one clear next step. Do not repeat this purpose unless it is relevant
or asked.

This is an instruction skill for the GoAster agent, not executable code.
Apply it when the user asks in ordinary language to provision a new OVH
resource; do not require a skill name or slash command. If the user wants to
install GoAster on an existing host, follow the OVH remote SSH skill instead.
If the intended starting point is unclear from the request and available
context, clarify it before provisioning. For a requested new host and
installation, proceed to the remote SSH workflow after provisioning and
verification.

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
- Access: SSH keys only. Reuse a project SSH key only when its matching private
  key is available locally. Never fall back to password access. If no usable
  key exists, automatically generate a dedicated local Ed25519 keypair at
  `~/.ssh/goaster-ovh` (on Windows, under `$HOME/.ssh/goaster-ovh`), without
  overwriting existing files; use a fresh unique path if needed. Generate it
  non-interactively with an empty passphrase (`ssh-keygen -N ""`) and restrict
  access to the private-key file to the local account (use an owner-only ACL on
  Windows). Never ask the user to provide a key. Keep the private key on the
  host and never print, upload, or report it; only the public `.pub` key may
  be sent to OVH. Report the key label, fingerprint, and local private-key
  path so the identity can be used for SSH later.
- Inbound network: allow SSH (TCP 22) from unrestricted sources by default
  (`0.0.0.0/0`, and `::/0` when IPv6 rules apply). Do not require or ask for a
  trusted source IP. Keep application ports closed by default. If the selected
  OVH API or project profile requires a concrete source value, use the
  unrestricted CIDR value it accepts and report the configured rule; do not
  stop only because a trusted source IP is unknown.
- Budget: estimated recurring OVH charges must not exceed **US$8 per month**.
  Include the instance and required storage, public address, and predictable
  network charges when official pricing is available. This is a hard default
  ceiling when the estimate is available. If the smallest suitable
  configuration is known to exceed it, do not create it until the user
  explicitly overrides the ceiling. Complete the public price precheck before
  creation approval; the estimate is informational. If official pricing is
  unavailable, report that the cost is unknown and continue the approval flow
  based on the smallest suitable machine only after the user's acknowledgment.
  Do not guess a price or stop solely because pricing could not be retrieved.

The user may explicitly override any default. An OVH account/API region and
endpoint are separate from the Public Cloud deployment region. Reuse the
account endpoint already configured or identified from the Create API Keys
page; do not ask for it again. Ask for the user's preferred deployment region
only when it is genuinely unknown from the conversation, configuration, and
project context, and only after they confirm which project to inspect. If it is
known, continue without asking. Do not confuse a known account/API region with
a deployment-region preference.

After the read-only checks, report the selected project and deployment region,
all defaults or explicit preferences being applied, the proposed smallest
suitable machine and access/network settings, and the official recurring-cost
estimate if it was retrieved. If official pricing cannot be retrieved, say
clearly that the cost is unknown and continue to request approval for the
smallest suitable machine; do not let unavailable pricing block the workflow.
Before any billable creation or other mutation, obtain the user's explicit
approval of that exact configuration and, when pricing is unavailable, their
acknowledgment that the current cost could not be verified.
Do not treat silence or approval of a different action as consent.

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
For OVH and Telegram setup, the only environment values to ask the user to
provide are `OVH_APPLICATION_KEY`, `OVH_APPLICATION_SECRET`,
`OVH_CONSUMER_KEY`, and `TELEGRAM_BOT_TOKEN`. The OVH API endpoint is derived
agent-side from the Create API Keys page URL in the conversation.

### Create API Keys screen

Before giving key-creation instructions, ask the user to paste the OVHcloud
Create API Keys page URL they are currently viewing. The page URL is not a
secret; do not ask the user to paste API keys, passwords, or other secrets in
chat. Derive the OVH API endpoint from the URL's hostname and use it in OVH API
requests. Never ask the user to supply the endpoint separately; if the URL is
not in the conversation, ask for the Create API Keys page URL. The endpoint is
based on the OVHcloud account, not the Public Cloud deployment region. Use the
hostname to detect the account region and choose the canonical endpoint below.

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

### Public price estimate

Before requesting approval to create any server or other OVH resource,
perform a fresh public-web search with GoAster's built-in
`web_search` for current official OVHcloud Public Cloud prices. Include the
selected deployment region and relevant option types in the search, such as
compute, required storage, public addressing, and predictable network charges.
Start with OVHcloud's [Public Cloud price list](https://www.ovhcloud.com/en/public-cloud/prices/).
Use `web_fetch` to read the matching official OVHcloud price page or another
official OVHcloud pricing source before estimating; search snippets alone and
third-party prices are not confirmation. Use public prices only, not an OVH
billing API or account-specific pricing endpoint. This is a read-only check.

Run this check after read-only inspection identifies the deployment region,
smallest suitable machine, and required options, and before requesting
creation approval. Use a broad, rounded estimate or range based on official
published prices. If converting an hourly rate to a monthly amount, state the
approximate-hours-per-month assumption.

Keep the user-facing price explanation concise, for example:
“OVH’s public price for this setup is approximately [currency and amount]/month, based on
the [official OVH price list](URL) checked [YYYY-MM-DD]. This is a broad
estimate, not an exact quote.” When an hourly estimate is available, also
include this concise cost and cleanup information:
“Estimated cost: ~US$0.02 for 1 hour, or ~US$0.40 for 24 hours, before tax.
Delete the server after testing to stop compute charges. Retained storage or
snapshots may still incur fees.” Keep the `~` qualifiers; these amounts are
approximate, not an exact quote. Add a short note with region, currency, VAT
treatment, or material uncovered charges only when needed to interpret the
estimate. Do not present a numeric price as applying to the selected region
unless the official source establishes both the region and currency, or imply
that listed options are available in the user's account.

If no usable official price covers the selected region or options, do not
guess or use third-party prices. Say the current price is unknown, briefly
identify the official source checked and date, and get the user's
acknowledgment before requesting explicit creation approval. These API key
rights are the agreed set for GoAster server provisioning.

Use the generic `/file` command in the local terminal to inspect a file,
including a dashboard screenshot. It accepts supported images and documents
and asks for an explanation of what the file shows, next steps, and risks. Do
not select files containing API keys, application secrets, or other secret
values. Do not transcribe, request, or use any secret that appears in a file;
secret values belong only in the local `/env` flow.

1. Derive the OVH account endpoint from the Create API Keys page URL in the
   conversation using the hostname mapping above and use it for OVH API
   requests. Retrieve the three OVH credentials from the local `/env`
   environment file. If the URL is not in the conversation, ask for that page
   URL; if any credential is unavailable locally, direct the user to configure
   credentials through `/env`. Do not ask again for known account/API details.
   Do not ask for a preferred deployment region before project selection is
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
   read-only checks for its regional quota, available regions, images, flavors,
   public network, stored SSH keys, matching local SSH identities, and existing
   instances. Select the smallest available machine suitable for the workload.
   If no usable project key has a matching local private identity, generate a
   dedicated local Ed25519 keypair automatically. Use a restricted local
   private-key file and never display or send its contents; only use the `.pub`
   key for OVH. Do not overwrite an existing key. Report the safe key label,
   fingerprint, and local key path. Generating the local key does not authorize
   an OVH change; defer registering the public key in the project and creating
   the instance until explicit approval.
3. Before asking for approval to create a server or other OVH resource,
   complete the Public price estimate above. Present the exact proposed
   resource, assumptions, SSH rule, and brief estimate. If pricing is
   unavailable, say the current cost is unknown and get the user's
   acknowledgment before requesting approval.
4. Before creating any server or other OVH resource, changing any billable
   resource, deleting or replacing data or resources, registering the generated
   public SSH key, or changing access or network exposure, explain the exact
   action, affected resources, costs or consequences, and obtain explicit user
   confirmation. For resource creation, the confirmation must follow the
   read-only assumptions and completed price report (including a clear
   statement when price is unknown) and cover that exact configuration. Do
   not exceed the US$8/month default ceiling when a known estimate is available
   without an explicit budget override.
   Confirmation for one action does not authorize a different action.
5. After the relevant confirmation, run the requested OVH API or installed
   client steps through `shell_execute`. Wait for the resource to become ready,
   then verify its ID, state, address, and requested access or setup. Do not
   retry an uncertain mutation blindly or delete a partially provisioned
   resource without confirmation.
6. Remove temporary scripts when finished. Do not install tools or add repository dependencies as part of provisioning without the user's approval.

## Report

Give a concise result: project and region, resource IDs and selected sizes, official ongoing charges if retrieved (otherwise say pricing could not be retrieved and the cost is unknown), verification status, and any failure or partial resource that remains. State clearly when a resource may still incur charges. Never claim success unless the host output and OVH state verify it; do not include secrets.
