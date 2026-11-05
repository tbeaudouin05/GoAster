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
Apply it when the user asks in ordinary language for OVH setup; do not require
a skill name or slash command. First follow the onboarding path selection
below. For an existing server, follow the OVH remote SSH skill. For a newly
created server and installation, proceed to that remote SSH workflow after
successful creation and verification, using the evidence from this same
provisioning flow. Automatically trust the exact new instance on its first
SSH contact as described in the handoff below; do not ask the user to
retrieve, compare, approve, or paste its SSH host-key fingerprint.

## Select an onboarding path

At the start of OVH onboarding, explain that GoAster can list existing OVH
servers with read-only checks, then ask whether the user wants to connect to an
existing server or create a new one. A missing GoAster connection record on
this computer only means that no local record is available; it does not mean
that no OVH server exists, express intent to create one, or authorize creation.

If the user chooses an existing server, use read-only project discovery, wait
for the user's project selection, and list that project's existing instances
using read-only checks. Present the candidates and ask the user to select the
exact server before following the OVH remote SSH skill. If no usable project
or server is found, report that result and ask what the user wants to do next;
do not switch to creating a server. The choice to list or connect to an
existing server permits read-only discovery only.

If the user chooses a new server, follow the provisioning workflow below. That
choice does not approve creation: before any server or other OVH resource is
created, present its exact configuration and price estimate and obtain the
user's explicit approval for that creation.

## Run host commands

Use GoAster's built-in `shell_execute` tool to create and run a temporary script on the host. Its input is a JSON object with required string `command` and optional string `cwd` and integer `timeout_ms` (omitted or zero uses GoAster's fixed safety timeout; choose a positive bounded timeout for longer work). `command` runs through the host's native shell. Use a platform-matched script (`.sh` for a POSIX shell, `.ps1` for PowerShell on Windows) in the host temporary directory; create, run, and remove it in one call when practical. Do not create provisioning files in the repository.

Read the complete result: `stdout`, `stderr`, and `exit_code`; an `execution_error` may also be present. Treat a nonzero exit or timeout as a failure or uncertain result, not success. After an uncertain create request, reconcile OVH state with read-only calls before considering any retry so you do not create a second paid resource. Never print credentials or include them in script text, command arguments, or reports.

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
  be sent to OVH. Report the key label, public client-key fingerprint, and
  local private-key path so the identity can be used for SSH later.
- Inbound network: allow SSH (TCP 22) from unrestricted sources by default
  (`0.0.0.0/0`, and `::/0` when IPv6 rules apply). Do not require or ask for a
  trusted source IP. Keep application ports closed by default. If the selected
  OVH API or project profile requires a concrete source value, use the
  unrestricted CIDR value it accepts and report the configured rule; do not
  stop only because a trusted source IP is unknown.
- Budget: estimated recurring OVH charges must not exceed **US$8 per month**.
  Include the instance and required storage, public address, and predictable
  network charges when applicable pricing is known. The standard d2-2 plus
  pay-as-you-go public IPv4 has the illustrative US-list estimate in the
  Public price estimate section below; it is not an account-specific quote.
  This is a hard default ceiling when the estimate is available. If the
  smallest suitable configuration is known to exceed it, do not create it until the user
  explicitly overrides the ceiling. Before creation approval, present the
  applicable estimate or say that the cost is unknown. If an applicable price
  is unknown, get the user's acknowledgment before requesting exact creation
  approval. Do not guess a price or stop solely because pricing could not be
  retrieved. No web search is mandatory.

The user may explicitly override any default. The OVH account/API region and endpoint are separate from the Public Cloud deployment region. Before probing any account/API endpoint, verify that all three required OVH credential values (`OVH_APPLICATION_KEY`, `OVH_APPLICATION_SECRET`, and `OVH_CONSUMER_KEY`) are available through the local `/env` flow. If any are missing, tell the user to configure the missing values through the local `/env` flow, then resume setup once they are available. After the OVH credentials are available through the local `/env` flow, proactively probe every supported account/API endpoint with the same credentials and the existing read-only `GET /cloud/project` discovery request. Use only the same restricted rights; do not add permissions or make a write request.

Do not ask the user to identify the account/API region. Probe all supported account/API endpoints with the configured credentials:
- Europe: `https://eu.api.ovh.com`
- Canada: `https://ca.api.ovh.com`
- United States: `https://api.us.ovhcloud.com`
Record every successful probe and select a successful endpoint using table order (Europe, Canada, United States). Report the detected account/API region and ask only for a simple confirmation before continuing with normal project selection. Reuse the successful `GET /cloud/project` response for the project listing below.

If no supported region works, stop with a clear setup error explaining that no supported account endpoint accepted the configured credentials for read-only `GET /cloud/project` discovery. Never print secrets or raw credential/error details.

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

When environment variables are needed, name each one, briefly explain its purpose and where to find its value (the selected OVH client's docs or OVHcloud credential settings), and invite the user to ask for more explanation. Secret values must be entered only through GoAster's local `/env` environment-file flow; never ask for or accept them in chat.
For OVH and Telegram setup, the only environment values to ask the user to provide are `OVH_APPLICATION_KEY`, `OVH_APPLICATION_SECRET`, `OVH_CONSUMER_KEY`, and `TELEGRAM_BOT_TOKEN`. The OVH API endpoint is derived agent-side by probing every supported account/API endpoint after the credentials are available through `/env`.

### Create API Keys screen

Before giving key-creation instructions, do not ask the user to identify the OVHcloud account/API region. The endpoint is based on the OVHcloud account, not the Public Cloud deployment region. Once credentials are available through `/env`, probe every supported endpoint with read-only `GET /cloud/project` as described above. The page may label the application-name field **Script name**. The user signs in directly on the OVHcloud page; do not send OVH login details through GoAster chat.

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

For this initial provisioning credential, do not add other method/path rows or
replace `*` with a project ID. After the user creates the keys, have them enter
the values only through GoAster's local `/env` flow. Keep the Application Key,
Application Secret, and Consumer Key out of chat, screenshots, and reports. No
account-level project-creation permission is needed because the user creates the
project in OVHcloud Manager. Once the configured OVH client authenticates, use
the read-only project verification workflow above; `POST /cloud/project/*` is
for later project-scoped provisioning operations. The user does not enter a
project ID on the Create API Keys screen.

### Verified instance creation request

Do not download the complete OVH API specification, schema bundle, SDK, or CLI
as a prerequisite for ordinary onboarding. Use the verified v1 request below
with the account/API endpoint selected by the read-only credential probe. The
official [OVHcloud instance guide](https://docs.ovhcloud.com/en/guides/public-cloud/compute/getting-started)
documents this route and these main fields; the production API schema confirms
the request model and field types. The deployment region is a request field,
not a replacement for the selected account/API endpoint.

Create the instance only after the exact billable configuration and action have
been explicitly approved:

```text
POST /1.0/cloud/project/{serviceName}/instance
Content-Type: application/json

{
  "name": "<selected instance name>",
  "flavorId": "<selected flavor id>",
  "imageId": "<selected image id>",
  "region": "<selected deployment region>",
  "sshKeyId": "<registered project SSH key id>",
  "monthlyBilling": false
}
```

In this request, `serviceName` is the selected project ID. The schema marks
`name`, `flavorId`, and `region` as required; `imageId`, `sshKeyId`, and
`monthlyBilling` are supported fields used here for the selected image,
registered SSH key, and hourly billing. Populate every value from the preceding
read-only project, region, image, flavor, and SSH-key checks. Do not copy the
nested `bootFrom`, `flavor`, or `network` shape from a different OVH route into
this request, and do not invent a public-network field. If public connectivity
requires an additional `networks` value for the selected project, first verify
the exact public network ID and the schema-supported shape with read-only
checks; otherwise stop and report that the requested public access cannot yet
be verified. Never put credentials or private-key material in the body.

The successful response for this route is an instance object. Retain its
non-empty `id` and verify it with the read-only
`GET /1.0/cloud/project/{serviceName}/instance/{instanceId}` route. Check the
returned `id`, `name`, `flavorId`, `imageId`, `region`, `sshKeyId` when present,
`status`, and `ipAddresses` against the approved configuration before treating
the instance as ready or handing it to the remote SSH workflow.

If the POST times out, the transport fails, returns a non-success result, or
returns no usable instance ID, treat the outcome as ambiguous. Do not retry,
delete, or create a replacement. Reconcile first using read-only calls: use the
exact returned ID when one exists; otherwise list instances with
`GET /1.0/cloud/project/{serviceName}/instance?region=<selected deployment region>`
and compare the returned `name`, `region`, `flavorId`, `imageId`, and `sshKeyId`
with the approved request. A name alone is never enough, and a pre-existing
matching instance must remain distinguishable from the attempted creation. If
reconciliation finds exactly one attributable instance, verify that exact ID
and keep the flow partial until its readiness and address are confirmed. If it
finds none, more than one, a mismatch, or the read-only verification is
unavailable, stop and report the missing or ambiguous evidence; ask before any
retry or cleanup. Never blindly repeat the POST.

### Optional Public Cloud instance backup

The three rows above also cover documented Public Cloud instance backup
creation and inspection. The [OVH instance-backup guide](https://docs.ovhcloud.com/en/guides/public-cloud/compute/save-an-instance)
uses `GET /cloud/project/{serviceName}/region` and
`POST /cloud/project/{serviceName}/region/{regionName}/instance/{instanceId}/snapshot`;
backup listing/read operations are under `/cloud/project/*`. No separate
credential extension is required for those operations.

Before using the optional path, verify the current account endpoint, project,
resource, exact method/path, and rights against official OVH documentation and
the project UI. Other operations may require different rights. If GoAster
performs deletion or retention changes, it needs `DELETE` rights for the
relevant documented endpoint and any other required rights. Do not guess paths
or broaden access account-wide. If creation or inspection cannot be verified,
skip the OVH backup and keep the mandatory server-local rollback bundle. For
other operations, skip only the unverified operation.

Before creating a backup, tell the user they are responsible for managing and
deleting old backups and schedules to minimize ongoing storage costs. GoAster
must never delete them automatically unless separately approved. Obtain
explicit approval for the exact project, resource, operation, and cost, and
separate approval for retention, deletion (including OVH-managed rotation),
and any secret-inclusive data. The [backup workflow](../goaster-backup/SKILL.md)
covers retention and pricing.

### Public price estimate

For the standard configuration of one d2-2 instance with one public IPv4 on
pay-as-you-go pricing, use this rough illustrative baseline without a web
search: the [official OVH US Public Cloud price list](https://us.ovhcloud.com/public-cloud/prices/),
dated 2026-11-05, lists d2-2 at US$0.0129/hour and public IPv4 at
US$0.00319/hour. Adding them gives US$0.01609/hour; a 20% buffer gives
US$0.019308/hour, shown as roughly **US$0.02/hour**. At 730 hours per month,
that is US$14.095/month, rounded up to roughly **US$15/month**. State that
this is before tax, is an illustrative US-list estimate rather than an exact
quote or guaranteed ceiling, and assumes no Savings Plan discount. OVH prices
vary by account and region. This figure covers only the d2-2 and pay-as-you-go
public IPv4 list items; other resources (including storage) and tax are extra.
Do not describe it as an account-specific or selected-region price unless
that has been independently verified.

For a test lasting less than one hour, explain that it may cost roughly
US$0.02 if deleted promptly; this is not a guarantee or maximum. The IPv4
address and other retained resources may continue to cost after the instance
is deleted. Warn the user to clean up the address and any other billable
resources as well as the server.

Do not apply this baseline to a different instance type, address option,
billing option, or additional resource. For a configuration outside this
supported pairing, say its cost is unknown unless applicable official pricing
has otherwise been verified; get the user's acknowledgment of that unknown
cost before requesting exact creation approval. No web search is mandatory.
When a price has not been verified for the selected region/account, label the
US figure as an illustrative reference and make the actual regional/account
cost uncertainty clear. Never guess or imply an unsupported option is
available. The US$8/month default budget ceiling still applies: this roughly
US$15/month estimate exceeds it, so get the user's explicit budget override
before creation. Even with an override, obtain separate explicit approval for
the exact billable configuration after presenting its assumptions, estimate
or unknown cost, and consequences. These API key rights are the agreed set
for GoAster server provisioning.

Use the generic `/file` command in the local terminal to inspect a file,
including a dashboard screenshot. It accepts supported images and documents
and asks for an explanation of what the file shows, next steps, and risks. Do
not select files containing API keys, application secrets, or other secret
values. Do not transcribe, request, or use any secret that appears in a file;
secret values belong only in the local `/env` flow.

1. Retrieve the three OVH credentials from the local `/env` environment file.
   If any credential is unavailable locally, direct the user to configure
   credentials through `/env`; do not ask for secrets in chat. Once credentials
   are available, proactively probe every supported account/API endpoint with
   the same credentials and the existing read-only `GET /cloud/project`
   discovery request. Do not stop after the first success: record all successful
   probes. If one or more regions work, select a successful region using table
   order (Europe, Canada, United States). Report the detected account/API
   region and ask only for a simple confirmation before continuing. If no
   supported region works, stop with the clear setup error described above.
   Never print secrets, raw errors, or credential material. Reuse the
   successful discovery response for the project listing below rather than
   adding a new or broader permission. Do not ask for a preferred deployment
   region before project selection is confirmed. Apply the normal resource
   defaults above for any choice the user has not
   explicitly overridden.
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
   public client-key fingerprint, and local key path. Generating the local key
   does not authorize an OVH change; defer registering the public key in the
   project and creating the instance until explicit approval.
3. Before asking for approval to create a server or other OVH resource, use
   the applicable Public price estimate above. Present the exact proposed
   resource, assumptions, SSH rule, and brief estimate. If applicable pricing is
   unknown, say so and get the user's acknowledgment before requesting
   approval.
4. Before creating any server or other OVH resource, changing any billable
   resource, deleting or replacing data or resources, registering the generated
   public SSH key, or changing access or network exposure, explain the exact
   action, affected resources, costs or consequences, and obtain explicit user
   confirmation. For resource creation, the confirmation must follow the
   read-only assumptions and price report (including a clear
   statement when price is unknown) and cover that exact configuration. Do
   not exceed the US$8/month default ceiling when a known estimate is available
   without an explicit budget override.
   Confirmation for one action does not authorize a different action.
5. After the relevant confirmation, send the verified direct instance request
   above through `shell_execute`; an already-installed client is acceptable
   only if it sends that exact endpoint and body without broadening scope. Do
   not download the API specification or install a client as a prerequisite.
   Wait for the resource to become ready, then verify its ID, state, address,
   and requested access or setup with the read-only exact-instance route.
   Retain the selected project, exact instance ID returned by the successful
   create operation, current public IP returned and verified during
   provisioning, and approved local SSH identity for the remote SSH handoff.
   On any ambiguous POST result, follow the reconciliation procedure above
   before considering a retry; do not delete a partially provisioned resource
   without confirmation.
6. Remove temporary scripts when finished. Do not install tools or add repository dependencies as part of provisioning without the user's approval.

### Handoff after successful instance creation

Whenever SSH contact is part of the requested provisioning flow for a newly
created instance, pass the retained project, instance ID, current public IP,
and approved local SSH identity directly to the OVH remote SSH workflow in
this same provisioning flow. In the protected `process_handoff`, carry the
verified instance ID and current public IP in `provisioned_target`; this field is
omitted for existing or provenance-unclear hosts. Before first SSH contact, that workflow checks
the exact ID and current public IP again through the OVH API. For the matching,
ready instance, it automatically records the
first host key in a unique target-specific `known_hosts` file with
`StrictHostKeyChecking=accept-new`; all later SSH connections use that same
file with `StrictHostKeyChecking=yes`. Never ask the user to retrieve, compare,
approve, or paste an SSH host-key fingerprint for this newly created server,
including during the protected login. If the identity, IP, readiness, or
approved key cannot be confirmed, stop without connecting or accepting a host
key and report the uncertainty. Trusted-channel fingerprint verification is
reserved for pre-existing hosts or hosts whose provenance is unclear when
they lack an already trusted entry.

## Report

Give a concise result: project and region, resource IDs and selected sizes, official ongoing charges if retrieved (otherwise say pricing could not be retrieved and the cost is unknown), verification status, and any failure or partial resource that remains. State clearly when a resource may still incur charges. Never claim success unless the host output and OVH state verify it; do not include secrets.
