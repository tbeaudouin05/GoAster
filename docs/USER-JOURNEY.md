# GoAster user journey

This guide follows GoAster from its packaged local launch to an OVH-hosted
agent that can answer a paired user's Telegram messages. The packaged app
handles local subscription chat; the remote agent uses its own subscription
login.

## 1. Launch GoAster

Download and extract the package for your operating system, then launch its
entrypoint:

- macOS: double-click `GoAster.app`.
- Windows: double-click `GoAster.cmd`.
- Linux: launch `GoAster.desktop`; if prompted, allow or trust it.

The entrypoint opens a terminal and starts the embedded app. Use the package
launcher rather than running its internal payload directly. No local Go
installation is needed.

On first launch, or when no valid subscription credential is available,
GoAster displays an OpenAI device-login URL and one-time code in that terminal.
Open the URL in a browser, sign in with the ChatGPT account that has the
subscription, and approve the device login using the displayed code. Keep the
URL, code, OAuth tokens, and saved credential files out of chat. GoAster saves
and refreshes this local login at `~/.goaster/auth.json`. It does not require
an OpenAI API key.

## 2. Chat in the local terminal

Enter a message at GoAster's chat prompt and press Enter. An interactive
terminal shows a compact header and readable message bubbles; redirected input
keeps the line-oriented `you>` prompt. GoAster replies in the same terminal.
The local chat includes its built-in web search, web fetch, and
`shell_execute` tools. Enter one message per line; blank lines are ignored.
Use EOF (Ctrl-D on Unix) or close the terminal to exit.

Describe what you want in ordinary language. GoAster follows the relevant
packaged skill without requiring a skill name or slash command. For example,
ask “Create a new OVH server and install GoAster on it,” “Install GoAster on
my existing OVH server,” “Help my Telegram bot receive group messages,” or
“Review my daily memory notes and propose durable facts to keep.” You can also
ask GoAster to find or explain public web content. Memory consolidation is
optional and runs only when requested.

To inspect a local file, type `/file` and choose it in the native file picker.
GoAster accepts supported images and other files up to 20 MiB, attaching images
for visual inspection and documents for its local tools. Do not select files
containing secrets. This works for any task, including OVHcloud dashboard
screens; the OVH skill gives domain guidance for the Create API Keys screen.

The conversation is saved in `~/.goaster/session.json`; durable Markdown
memory is kept under `~/.goaster/memory/`. Scheduled chat turns are saved
locally and run only while the packaged terminal process is active.
Use `/clear` in the terminal to clear that persistent conversation. The command
takes no arguments; extra text displays `Usage: /clear` and leaves history as
it was. Clearing does not change Markdown memory and does not require model
authentication.

## 3. Import setup values with `/env`

When an OVH setup or Telegram configuration needs secret values, enter them
through GoAster's local environment-file flow, never in a chat message.
Use `/get-env` to edit the current environment file or get a blank template
containing `OVH_APPLICATION_KEY=`,
`OVH_APPLICATION_SECRET=`, `OVH_CONSUMER_KEY=`, and `TELEGRAM_BOT_TOKEN=`. For
OVH setup, the onboarding agent derives the API endpoint from the Create API
Keys page URL shared in the conversation; it is not an environment-file entry.
Fill in a blank template, then re-upload the completed file with `/env`. GoAster
shows the variable names with their values masked and asks whether to save
them. Confirm with `y` to store them in `~/.goaster/env` with `0600` file and
`0700` directory permissions. The imported file replaces the previously saved
set; canceling leaves it unchanged. The relevant skill explains what each
setting is for and where to find it. Secret values are never printed in the
terminal.

## 4. Ask for the OVH setup you need

Ask GoAster to create a new OVH server and install GoAster, or to install it on
an OVH server you already have. GoAster follows the appropriate workflow and
asks if your intended starting point is unclear.

For a new server, before starting OVH provisioning, create the Public Cloud
project yourself in
[Public Cloud in OVHcloud Manager](https://www.ovh.com/manager/#/hub/public-cloud).
Sign in, accept the terms, choose **Discover the Public Cloud universe**, then
create and name the project. After creation, **Discovery mode** is the
restricted starting state. If the project still shows the **Discovery mode**
label, click OVHcloud Manager's **Enable** or **Activate** button only to start
the payment-method activation flow. Then provide or add a payment method in
that flow to deactivate and exit Discovery mode. The required final state is
an active project with no **Discovery mode** label. GoAster cannot provision a
server while the project remains in Discovery mode. See OVHcloud's [official
project creation guide](https://docs.ovhcloud.com/en/guides/public-cloud/cross-functional/create-a-public-cloud-project).

Return to GoAster only after the project is active and the Discovery mode
label is gone. After credentials are configured through local `/env`, GoAster
verifies the user-created project with read-only `GET /cloud/project` and,
when needed, a read-only project-specific lookup. If there is no usable active
project, GoAster stops and directs the user to finish creating the project or
completing payment-method activation in OVHcloud Manager; GoAster does not
create projects through the API.

The packaged OVH workflows cover different starting points:

| Example request | What GoAster does |
| --- | --- |
| “Create a new OVH server and install GoAster on it.” | The [provisioning workflow](../skills/ovh/SKILL.md) expects a user-created active project, then uses defaults for a GoAster remote agent, Ubuntu LTS, the smallest suitable machine, SSH-key-only access, automatic generation of a dedicated local SSH key when needed, unrestricted SSH on TCP 22, and no other inbound application ports. It asks for the preferred deployment region only when genuinely unknown. Before creation approval, it checks current official OVH prices with `web_search` and `web_fetch` and gives a brief broad estimate. If price is unknown, it says so and gets the user's acknowledgment before requesting explicit approval. The US$8/month ceiling applies when a price is known. The [SSH workflow](../skills/ovh-remote/SKILL.md) then connects to that host and bootstraps GoAster. |
| “Install GoAster on my existing OVH server.” | The [SSH workflow](../skills/ovh-remote/SKILL.md) checks and bootstraps that host. It does not create or manage OVH resources through the OVH API. |

**New resource path:** use the account/API endpoint already known from
credentials or the Create API Keys URL; that is separate from the preferred
Public Cloud deployment region. Ask for the deployment region only when it is
genuinely unknown. After read-only project, quota, image, flavor, network, SSH
key, and existing-instance checks, GoAster must use `web_search` and `web_fetch`
to check current official OVH prices for the selected region and options.
Before creation approval, give a brief broad estimate with its official source
and any region, currency, or VAT context needed to interpret it. If pricing
cannot be verified, say the cost is unknown and get the user's acknowledgment
before requesting explicit approval. Apply the US$8/month ceiling when a
price is known. GoAster then uses the remote SSH workflow to bootstrap the
provisioned and verified resource. **Existing host path:** GoAster starts with
the remote SSH workflow and skips provisioning.

### Create API keys for a new resource

While viewing OVHcloud's Create API Keys page, paste its URL into GoAster chat
so the onboarding agent can identify the account region and derive the matching
API endpoint. The page URL is not a secret. You do not need to provide the
endpoint in the environment file. Never paste API keys, passwords, or other
secrets into chat; enter credentials only through GoAster's local `/env` flow.
Determine the region from the URL hostname, using these mappings:

| Account region | Create API Keys URL examples | Canonical OVH API endpoint |
| --- | --- | --- |
| Europe | `https://auth.eu.ovhcloud.com/api/createToken` or `https://eu.api.ovh.com/createToken/` | `https://eu.api.ovh.com` |
| Canada | `https://auth.ca.ovhcloud.com/api/createToken` or `https://ca.api.ovh.com/createToken/` | `https://ca.api.ovh.com` |
| United States | `https://auth.us.ovhcloud.com/api/createToken` or `https://api.us.ovhcloud.com/createToken/` | `https://api.us.ovhcloud.com` |

For example, `https://auth.ca.ovhcloud.com/api/createToken` maps to
`https://ca.api.ovh.com`. The endpoint is based on the OVHcloud account, not
the instance's deployment region. If the URL hostname is not listed, confirm
the account region before proceeding.

Use these exact fields on the Create API Keys screen:

| Screen field | Enter |
| --- | --- |
| Application name / Script name | `GoAster` |
| Description | `GoAster server provisioning` |
| Validity | `Unlimited` |
| Restricted IPs | Leave empty |

In **Rights**, add exactly these three rows. The `+` button adds another row:

| Method | Path |
| --- | --- |
| GET | `/cloud/project` |
| GET | `/cloud/project/*` |
| POST | `/cloud/project/*` |

After creating the keys, configure them through GoAster's local `/env` flow
and authenticate to OVH. These rights allow read-only project verification and
project-scoped provisioning; no account-level project-creation permission is
needed because the user creates the project in OVHcloud Manager. GoAster
verifies the active project read-only before continuing. The user does not
enter a project ID on the Create API Keys screen. Keep the Application Key,
Application Secret, and Consumer Key out of chat and screenshots; enter
credentials only through local `/env`.

For SSH bootstrap, the skill confirms the exact host and starts with read-only
checks. It then builds `goaster-agent` and `goaster-telegram` on the existing
Linux host from the repository's `main` branch, using pinned Go 1.27.3, and
installs the binaries. The local package does not need Go for this remote
build. Review each proposed remote change and approve it separately as the
skill requires.

## Remote privilege model

The remote installation intentionally runs both `goaster-agent` and
`goaster-telegram` as `root`. The SSH target and remote login target are
therefore `root@OVH_HOST`; onboarding must not create or use a separate
unprivileged `goaster` runtime account. Root is required because the agent's
`shell_execute` and self-management capabilities must be able to administer the
full server, including its services, users, SSH configuration, firewall,
packages, and protected state.

This is a deliberate full-administration tradeoff, not a least-privilege
design. A compromised prompt, Telegram account, credential, or model mistake
has host-wide impact. Keep strict host-key verification, explicit approval
gates, secret-handling rules, and separate local versus remote OpenAI
credentials in force.

## 5. Complete the remote agent login

The remote agent starts without a ChatGPT subscription credential. After its
approved installation and agent service setup, GoAster automatically invokes
the existing SSH-backed login flow through the `remote_login` handoff, targeting
`root`, the account that runs both remote GoAster services. The normal path must not create or use a separate unprivileged runtime
account. After the exact host and local key are approved in chat, the handoff displays them in the local terminal and
requires the administrator to type `YES` before connecting. It then attaches
the terminal directly to the SSH login. The administrator does not type the
login command as part of the normal path. `remote_login` is an internal tool;
the underlying `goaster remote-login` command is for exceptional manual use.
Users do not normally type either name. GoAster waits for the administrator
to approve the OpenAI device authorization before it continues onboarding.

The flow requires explicit target and key inputs, launches OpenSSH directly
outside model `shell_execute`, and inherits the local terminal's input and
output. For a newly provisioned OVH host, the preceding bootstrap first checks
the exact instance ID and expected public IP through the OVH API, then uses the
approved client key for one read-only first connection with OpenSSH's
`StrictHostKeyChecking=accept-new`. That connection saves the key in a
target-specific `known_hosts` file; the protected login receives that same
file. The login and all later connections use only that file, strict host-key
checking, and disabled host-key updates, so a changed key is rejected.
Existing hosts still need a trusted `known_hosts` entry or fingerprint
verification through a trusted channel before connection. Do not use
`ssh-keyscan` as an independent trust source.

For this host, Ubuntu 26.04 LTS was selected and reported active at creation.
That is creation-time provisioning evidence, not a fresh live OS verification.
The observed root SSH forced-command redirect to `ubuntu` is an SSH account
policy, separate from the OS image.

### If the administrator changes computers

The server keeps the SSH public key in its authorized keys; its matching
private key stays on the administrator's computer. Prefer generating a new
Ed25519 key on the new computer, adding its public key through the old access,
testing SSH from the new computer with strict host-key checking, and only then
removing the old public key. Alternatively, securely transfer the existing
private key. Never send it in chat or put it in the repository. Losing the only
private key requires another existing access path or OVH console/recovery
access. Keep host-key verification enabled on the new computer; use a trusted
`known_hosts` entry or verify the fingerprint through a trusted channel.

The terminal displays the URL and one-time code. The administrator opens the
URL in a browser, signs in with the subscription account, and approves the
remote device authorization; GoAster waits for that approval. Keep the URL,
code, tokens, and login output in the interactive terminal and browser flow;
never relay them through GoAster or Telegram chat. This login writes a separate
`/root/.goaster/agent-auth.json` on the remote host. Keep the local
`~/.goaster/auth.json`, its tokens, and the local conversation on your own
machine; GoAster never copies them to the host. After successful authorization,
the existing command runs its separate read-only SSH health check against
`http://127.0.0.1:8765/healthz` and reports whether the running agent is ready.
Continue to Telegram setup only when this check returns `ok`; this marks the
remote agent ready, not the full onboarding complete.

The agent must pass the health check before Telegram chat can work. If login is
canceled or the check fails, inspect the remote service before retrying. For
later reauthentication, keep the agent service running so the command's
built-in health check can reach it, and have GoAster invoke the same
interactive flow again. Any separate agent or adapter service restart requires
its own explicit approval.

## 6. Set up Telegram and verify the full path

Full onboarding stays **partial** until all four Telegram gates are complete:
the bot token is supplied through the approved local `/env` flow and installed
by the separately approved remote setup, the adapter reports ready after a
successful Telegram poll, the intended account is paired, and that user
completes a real Telegram message/reply smoke test. GoAster keeps prompting for
the next pending step; agent-ready with Telegram incomplete is still partial.

Create or select a bot with `@BotFather` and keep its bot token private. Use
GoAster's local `/env` flow for the token; do not put it in a chat message,
SSH argument, or script. Ask GoAster to continue Telegram setup; it follows the
[OVH remote SSH skill](../skills/ovh-remote/SKILL.md). That setup stores the
token and a verifier for a fresh, random, one-time pairing code on the remote
host. The approved remote setup receives the bot token and only the pairing
code's SHA-256 verifier; give the raw pairing code privately to the intended
Telegram user. The pairing code is distinct from either OpenAI device-login
code.

After the agent passes `/healthz`, start the separately approved Telegram
adapter service. With systemd, configure the adapter as `Type=notify`; it
reports ready after the agent process is available and Telegram accepts its
first poll. It checks authentication separately before telling a paired user
that normal chat is ready, while `/clear` remains available without model
authentication.
Once the adapter is polling, the intended user opens a private chat with the
bot and sends `/start`, then `/start <PAIRING_CODE>`. The first private
Telegram account that submits the correct code is paired. GoAster saves that
account's numeric Telegram user ID and removes the verifier. The paired ID
persists across adapter restarts. The code has no automatic expiry, so keep it
private and use it only for the intended user.

For the required end-to-end smoke test, have the paired user send a fresh,
ordinary text message in the private Telegram chat and confirm that the remote
agent's reply arrives in that same chat. A healthy `/healthz`, adapter
readiness, or pairing confirmation alone does not pass this test. Report full
onboarding complete only after this real message and reply succeed; otherwise
report partial and continue prompting for the pending step.

## 7. Chat through Telegram

The paired account can message the bot privately or in a group where Telegram
delivers messages to it. GoAster forwards only that paired account's ordinary
text, or one image or document with an optional caption, to the remote agent;
the reply returns to the originating Telegram chat. Other accounts are
ignored. A document is available to the agent's current-turn local tools, and
an image is included for visual analysis. Media bytes and temporary file paths
are removed after the turn and are not saved in conversation history.
Downloads are limited to 20 MiB; supported image formats are JPEG, PNG, GIF,
and WebP. Media albums are ignored.

Send `/clear` in a Telegram chat to clear only that chat's conversation. The
command takes no arguments; extra text displays `Usage: /clear` and leaves
history unchanged. It works while model authentication is unavailable and
does not change Markdown memory.

Telegram's Group Privacy Mode can prevent ordinary group text from reaching a
bot. See the [Telegram BotFather Group Privacy Mode
skill](../skills/telegram-group-privacy/SKILL.md) if group chat is needed.
Turning privacy mode off is optional and requires approval; Telegram also
requires removing and re-adding the bot to an existing group for the change to
take effect.

## 8. Keep GoAster running

Keep the local packaged app open for local terminal chat and scheduled turns.
For remote Telegram operation, keep the approved agent and adapter services
running on the OVH host. The agent must remain authenticated and healthy for
normal model replies; the adapter can still receive `/clear` while model
authentication is unavailable. If the adapter starts before the agent process
is available, restart it after the agent service is running.

Remote credentials refresh when needed. To reauthenticate, have GoAster invoke
the same interactive SSH-backed login flow with its service-account target
while the agent service is running. Wait for the administrator to approve
device authorization and for the built-in `/healthz` check to return `ok`.
Obtain separate approval before any agent or adapter service restart. The
remote bootstrap does not add self-updating behavior; a future remote build or
install follows the reviewed steps in the OVH remote SSH skill.
