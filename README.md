# GoAster

A small standalone terminal chat app, built with Go and a native file-picker
integration. Package builders require Go 1.22+. Normal chat uses an OpenAI
ChatGPT/Codex subscription through the Codex backend; it does not use an
OpenAI API key. A fixed built-in `web_search` tool uses no-key DuckDuckGo HTML
search and returns search-result titles, URLs, and snippets. The built-in
`web_fetch` tool retrieves
readable content from public HTTP or HTTPS URLs with response and redirect limits.
The built-in `shell_execute` tool runs commands through the platform's native
shell and returns stdout, stderr, and the exit code; it accepts an optional
working directory and bounded timeout in milliseconds. GoAster has no provider,
API-key, or web-search flag configuration.

GoAster packages the [internet-fetch skill](skills/internet-fetch/SKILL.md)
with the executable and includes it in tool-enabled model instructions. It
guides normal retrieval first, bounded retries, and a one-request IPv4 check
for suspicious endpoint-specific failures, with safe redirect and timeout
handling and careful diagnosis of apparent missing resources. This is
instruction guidance; the built-in `web_fetch` networking behavior is
unchanged.

## Install and launch

The packaged GoAster app is the only supported user entrypoint. Download the
archive for your operating system, extract it, and launch the matching
entrypoint:

- macOS: double-click `GoAster.app`.
- Windows: double-click `GoAster.cmd`.
- Linux: double-click `GoAster.desktop`. If the desktop environment asks,
  choose **Allow Launching** or **Trust and Launch**.

Each entrypoint opens or preserves a terminal and runs the embedded GoAster
binary. The embedded binary is an internal package payload; do not launch it
directly. The terminal interface is intentional: it preserves the normal
interactive onboarding and chat experience on every supported operating
system.

Authentication is part of this normal startup journey and does not require a
separate command. On the first launch, or whenever no valid subscription
credential is available, GoAster starts the device-code OAuth flow in the same
terminal process. Open the displayed URL, sign in to ChatGPT, and enter the
one-time code. Existing credentials are refreshed when needed and then chat
starts directly.

The OAuth tokens and ChatGPT account ID are saved locally in
`~/.goaster/auth.json` with `0600` permissions inside a `0700` directory.

## Build packages (developers)

Packaging requires Go 1.22+ and a POSIX-compatible shell. From the repository
root, build the macOS, Windows, and Linux distributables with:

```sh
./packaging/build.sh all
./packaging/validate.sh dist
```

The default build creates arm64 and amd64 macOS app bundles plus amd64 Windows
and Linux packages under `dist/`. To build one platform or architecture:

```sh
./packaging/build.sh macos
GOASTER_ARCH=arm64 ./packaging/build.sh linux
```

The archive helper uses only Go's standard library, and the validation script
checks package structure and launcher wiring without opening a host GUI.

## Environment variables

When setup needs environment variables, GoAster explains each one's purpose and
where to find its value, and invites you to ask for more explanation of any
variable. Enter secret values only through GoAster's local `/env`
environment-file flow; never paste them into chat. GoAster masks imported
values before saving them locally.

To inspect a local file, type `/file` in the terminal and choose it in the
native file picker. Files up to 20 MiB are supported. PNG, JPEG, GIF, and WebP
images are sent as images; other files are attached as documents for the
agent's local tools to inspect. Do not select files containing secrets; enter
secret values only through `/env`.

The terminal conversation is saved automatically in `~/.goaster/session.json`
with `0600` permissions. It contains bounded user/assistant conversation text
and any generated summary; OAuth credentials and tool-call internals are not
copied into it. At 75% of the fixed 300,000-token context window, or when the
256-message limit is reached, the active subscription-backed model summarizes
older messages while recent messages remain available. The summary is saved
and included in future requests. If summarization fails, GoAster trims older
messages to keep context bounded and reports the trimming in the terminal and
to the model. Session storage remains bounded to 8 MiB and 32 KiB per message.
If the saved conversation is malformed, GoAster moves it aside safely and
starts a new conversation.

## Markdown memory

GoAster keeps durable Markdown memory separate from `session.json` under the
default workspace `~/.goaster`:

```text
~/.goaster/
├── session.json
└── memory/
    ├── MEMORY.md
    └── YYYYMM/
        └── YYYYMMDD.md
```

`memory/MEMORY.md` is protected long-term knowledge. Daily notes are appended
to the date-based file, with private directories and restrictive file
permissions; appends are written through an atomic replacement. Normal model
requests receive `MEMORY.md` and the three most recent daily notes as clearly
marked Markdown context, newest note first. If the memory files are empty or
missing, the existing request behavior is unchanged.

Conversation summarization continues to update only the conversation summary
in `session.json`; it never rewrites `MEMORY.md`. Use the packaged
[self-improvement-memory-consolidation skill](skills/self-improvement-memory-consolidation/SKILL.md)
for an optional, user-managed daily maintenance workflow that reviews daily
notes and promotes only verified durable facts into protected long-term
memory. Users may invoke it from their own cron or scheduler; GoAster does not
install or provide an operating-system cron entry. The skill remains
user-managed and is not automatically scheduled by GoAster.

## Scheduled chat turns

GoAster includes a small local scheduler for model-requested reminders and
recurring chat turns. Jobs are saved atomically in
`~/.goaster/cron/jobs.json` with private `0700` directory and `0600` file
permissions, so they survive a normal restart. The model uses the built-in
`cron` tool with `add`, `list`, `get`, `update`, `remove`, `enable`, and
`disable` actions. Add a job with exactly one of:

```text
at_seconds     positive number of seconds from now for a one-time turn
every_seconds  positive recurring interval
cron_expr      five-field cron expression (six fields may include seconds)
```

Each job stores its message, enabled state, next and last run times, status,
error, run count, and optional `max_runs` (`0` means unlimited). One-time
`at_seconds` jobs are deleted after their execution. Updates are partial, so
omitted message, schedule, and `max_runs` fields are preserved.

Due jobs send their saved message through the same terminal chat session as
interactive input; the session serializes turns so scheduled execution cannot
corrupt conversation state. This feature deliberately does not schedule shell
commands, send to Telegram or other channels, install operating-system cron
entries, or add user-facing scheduler configuration or flags. It only runs
while the terminal GoAster process is active.

Enter one message per line. Blank lines are ignored. End the input stream with
EOF (Ctrl-D on Unix) to exit. Ctrl-C or closing the terminal also stops the
process where supported.

## Remote privilege model

The remote GoAster installation is intentionally **root-run**. Both
`goaster-agent` and `goaster-telegram` run as `root`, and remote onboarding
uses `root@OVH_HOST` for SSH and the interactive login. This is required for
GoAster's `shell_execute` and self-management capabilities to administer its
own server, services, users, SSH configuration, firewall, packages, and
protected application state. The normal onboarding path must not create or use
a separate unprivileged `goaster` runtime account.

This is a deliberate full-administration tradeoff, not a least-privilege
design. A compromised prompt, Telegram account, credential, or model mistake
has host-wide impact and can affect system files, SSH access, secrets, firewall
rules, and the GoAster installation itself. The existing strict host-key
checks, explicit approval gates, secret-handling rules, and separate local and
remote OpenAI credentials remain mandatory.

## OVH provisioning and remote access

For the end-to-end path from packaged launch to remote Telegram operation, see
the [GoAster user journey](docs/USER-JOURNEY.md).

GoAster includes two packaged OVH skills for separate stages. Use the
[OVH provisioning skill](skills/ovh/SKILL.md) to provision new OVHcloud
resources. It requires a user-created active project and verifies it with
read-only project discovery before checking quota, region, and options. Before
creation approval, GoAster uses `web_search` and `web_fetch` to check current
official OVH prices and gives a brief, broad estimate with its source. It adds
region, currency, or VAT context when needed. If pricing cannot be verified,
GoAster says the cost is unknown and gets the user's acknowledgment before
requesting explicit approval. For an ordinary new-host setup,
its defaults are a GoAster
remote agent on Ubuntu LTS, the smallest suitable machine, SSH-key-only access
(with a dedicated local key generated automatically if needed), unrestricted
SSH on TCP 22, and no other inbound application ports. It asks for a preferred
deployment region only when that preference is genuinely unknown. The
US$8/month ceiling applies when a price is known. It waits for explicit
approval before billable creation or other mutations. Use the
[OVH remote SSH skill](skills/ovh-remote/SKILL.md) to connect to and bootstrap
an already-existing OVH host, including a host just provisioned with the first
skill. It starts with read-only SSH preflight, requires fresh explicit approval
before each remote mutation, and verifies the result; it does not provision
resources through the OVH API.

Before starting OVH provisioning, create the Public Cloud project yourself in
[OVHcloud Manager](https://www.ovh.com/manager/#/hub/public-cloud). Sign in,
accept the terms, choose **Discover the Public Cloud universe**, then create
and name the project. After creation, **Discovery mode** is the restricted
starting state. If the project still shows the **Discovery mode** label, click
OVHcloud Manager's **Enable** or **Activate** button only to start the
payment-method activation flow. Then provide or add a payment method in that
flow to deactivate and exit Discovery mode. The required final state is an
active project with no **Discovery mode** label. GoAster cannot provision a
server while the project remains in Discovery mode. See OVHcloud's [official
project creation guide](https://docs.ovhcloud.com/en/guides/public-cloud/cross-functional/create-a-public-cloud-project).

Return to GoAster only after the project is active and the Discovery mode
label is gone. After configuring OVH credentials through local `/env`, GoAster
verifies the project with read-only `GET /cloud/project` (and a read-only
project-specific lookup when needed). If there is no usable active project,
GoAster will stop and ask you to finish creating the project or completing
payment-method activation in OVHcloud Manager; it will not create a project
through the API.

While viewing OVHcloud's Create API Keys screen, paste its URL in GoAster chat
so onboarding can identify the account region and choose the matching API
endpoint. The page URL is not a secret; never paste API keys or other secrets
in chat. This account/API region is separate from the Public Cloud deployment
region. Ask for the preferred deployment region only when it is genuinely
unknown. The [OVH provisioning skill](skills/ovh/SKILL.md#create-api-keys-screen)
documents the Europe, Canada, and United States URL mappings.

On the Create API Keys screen, use Application name/Script name `GoAster`,
Description `GoAster server provisioning`, Validity `Unlimited`, and leave
Restricted IPs empty. Add exactly `GET /cloud/project`,
`GET /cloud/project/*`, and `POST /cloud/project/*` as rights rows; the `+`
button adds a row. These rights allow read-only project verification and
project-scoped provisioning. No account-level project-creation permission is
needed because you create the project yourself in OVHcloud Manager. See the
[OVH provisioning skill's Create API Keys instructions](skills/ovh/SKILL.md#create-api-keys-screen).
After configuring the keys through local `/env` and authenticating, GoAster
will verify your active project with read-only checks. Do not enter a project
ID on the Create API Keys screen. Never paste keys into chat or include them
in screenshots.

**End-to-end:** Start in local GoAster chat and sign in to OVHcloud directly on
its website. Enter the generated OVH API key values and Telegram bot token only
through GoAster's local `/env` environment-file flow. Never paste keys into
chat or include them in screenshots. For a new host, provision
the OVH resource with explicit approval, then use the SSH skill to bootstrap
it; for an existing host, start with SSH bootstrap.
After remote bootstrap and agent service setup, GoAster itself automatically
starts the existing SSH-backed remote OpenAI login as `root` through its
`remote_login` handoff. The remote agent and Telegram adapter intentionally
run as root so GoAster can fully self-manage its own server. After the exact
host and local key
are approved in chat, the handoff displays them in the local terminal and
requires the administrator to type `YES` before connecting. It then attaches
the terminal directly to the login flow, waits while the administrator
approves the OpenAI device authorization in a browser, and completes the
existing read-only `/healthz` check before continuing onboarding. The
administrator must approve both the host contact and device authorization;
GoAster cannot approve them on their behalf. The normal path does not ask the
administrator to type `goaster remote-login`. Keep local OpenAI credentials on
the local machine; remote credentials are stored separately on the host.

The local package does not need Go to bootstrap the remote agent: the SSH skill
verifies or installs pinned Go 1.27.3 on the existing Linux host, clones the
latest `main` branch from this repository at bootstrap time, records the
resolved commit, builds `goaster-agent` and `goaster-telegram` from that
checkout on the host, then installs the resulting binaries. The Go archive
checksums and remote build steps are documented in the skill. This SSH bootstrap
does not manage the OVH resource lifecycle or add an agent
self-update feature.

The terminal and remote agent require **two independent OpenAI device logins**.
The local terminal login described above stays local. Never copy or send its
`auth.json`, a custom `-auth-file`, its OAuth tokens, or its saved session to the
OVH host. A freshly installed remote agent has no subscription credentials and
does not use the terminal's credential file. The agent can run while waiting
for login, but its loopback `/healthz` and `/chat` endpoints return unavailable.

GoAster also remembers a successful server connection locally. It saves a
small non-secret connection record beside the dedicated SSH key, containing
only the server address, port, and optional known-hosts file. On the next start,
GoAster checks that saved server first and does not ask you to type the address
again or create another server. If the saved server cannot be reached, it says
so and asks for the next simple action. The record never contains the private
key, OpenAI login, Telegram token, pairing code, or environment values.

After the remote agent is installed and root SSH is ready, GoAster's
`remote_login` handoff invokes the existing local flow below, using `root` as
the SSH target. The normal onboarding path must not create or use a separate
unprivileged GoAster runtime account. This is the onboarder's invocation, not
a command the administrator needs to type during normal onboarding:

```text
goaster remote-login --target root@OVH_HOST --key ~/.ssh/goaster-ovh
```

The command requires an explicit `--target` and local `--key`, invokes OpenSSH
directly through the local CLI's protected controlling-terminal handoff, and
does not pass through the model's `shell_execute` tool. The explicit command
uses only the current process's `/dev/tty` on supported Unix systems. The
`remote_login` model handoff also tries `/dev/tty` first; if no controlling
terminal is available, it uses inherited stdin and stdout only when both are
the same actual terminal device. Pipes, detached processes without terminal
stdio, and unrelated devices fail before connecting with a generic
terminal-unavailable status. Start GoAster with an attached interactive
terminal to retry. The handoff never searches for another pseudoterminal or
saves device codes to chat or files. The model receives only a generic success,
failure, decline, invalid-request, or terminal-unavailable status. It requires
strict host-key verification, uses
only the supplied key, and never accepts a password, token, or credential-file
argument. The host must already have a trusted entry in the user's
`known_hosts`; use `--known-hosts PATH` when the approved file is not the
default. Do not bypass a host-key mismatch or accept an unverified fingerprint.

The interactive terminal displays the URL and one-time code for the
administrator. The administrator opens the URL in a browser and approves the
remote device authorization; GoAster waits for that approval and the login to
finish. Do not relay the URL, code, tokens, or login output through GoAster or
Telegram chat. The remote login saves its own credentials as
`/root/.goaster/agent-auth.json` under root's remote home, with `0600` file and
`0700` directory permissions. This file is separate from
`/root/.goaster/auth.json`, even if both programs use the same home; GoAster never
copies the local file. After login exits successfully, the command runs a
separate read-only SSH health check against the remote agent's loopback
`/healthz` endpoint and reports failure clearly. If login is canceled or the
health check fails, inspect the remote service before retrying. For later
reauthentication, keep the agent service running so the command's built-in
health check can reach it; GoAster invokes the same flow again. Any separate
agent or adapter service restart still requires its own explicit approval.

The health check returns `ok` only when the agent can load its separate
subscription credential, refreshing it if needed; this does not test a model
response or detect every revoked token. Start or restart the Telegram adapter
after the check succeeds. For systemd
readiness reporting, configure its service as `Type=notify`: the adapter sends
the ready notification only after the agent is ready and the first Telegram
poll succeeds. It also checks agent readiness before telling a paired user
that chat is ready. If the adapter was started too early, restart it after
the health check. Telegram token and pairing setup are separate steps below;
neither the OpenAI device code nor the subscription tokens belong in Telegram.

An `/healthz` result of `ok` marks only remote agent readiness. Full onboarding
remains **partial** until the Telegram token is supplied through the approved
local `/env` flow and installed by the separately approved remote setup, the
adapter reports ready after a successful Telegram poll, the intended user is
paired, and that user completes a real Telegram message/reply smoke test. Keep
prompting for the next pending step; agent-ready with Telegram incomplete is
not complete.

### Telegram user pairing

Telegram pairing is a separate setup step after the remote agent login passes
the `/healthz` check. Create or select a bot with `@BotFather` and keep its bot
token private. During the approved remote setup, the token is installed in a
root-only file; generate a fresh, cryptographically random one-time pairing
code locally and send the token plus only the code's SHA-256 verifier to the
remote setup. The code is a Telegram pairing secret, not an OpenAI device
code. Enter the token only through GoAster's local `/env` flow. Never put the
token in any chat, SSH command line, or persistent script; never put the
pairing code in GoAster chat, a Telegram group, an SSH command line, or a
persistent script. Hand the pairing code to its intended user privately.

The person being paired then opens a **private chat** with the bot, sends
`/start`, and follows with `/start <PAIRING_CODE>`. For example, if the
operator privately gives them the placeholder code `EXAMPLE_CODE_ONLY`, they
send `/start EXAMPLE_CODE_ONLY` in the bot's private chat. The example is not
a valid credential. The first valid code received from a private Telegram
account binds that account's numeric Telegram user ID. The adapter removes the
stored verifier after pairing, and the allowed user remains saved across
service restarts. There is no automatic code timeout: keep the code private
and use it only for the intended account. If it may have been exposed, do not
use it; stop and review a separately approved pairing reset.

After pairing, only that numeric user ID can send ordinary text or one image or
document attachment, with an optional caption, for the agent to process. The
paired user may chat privately or in a group where the bot can receive
messages; group delivery may require Telegram Group Privacy Mode to be turned
off. Other Telegram accounts are ignored. The adapter forwards the message to
the remote agent and sends the reply back to the originating chat. Pairing does
not grant SSH access, server administration, or access to another Telegram
account.

After pairing, have the intended user send a fresh ordinary text message in
the private Telegram chat and confirm that the remote agent's reply arrives in
that same chat. This real round trip is the required smoke test; `/healthz`,
adapter readiness, and pairing confirmation alone do not prove it. Report
onboarding complete only after all four Telegram gates pass. Until then, report
the setup as partial and keep nudging the user toward the pending step.

Images are fully decoded and validated from their downloaded bytes, including
images sent as Telegram documents; supported formats are JPEG, PNG, GIF, and
WebP. Images are limited to 8192 pixels per dimension and 20 million pixels;
for animated GIFs the 20 million pixel limit applies across all frames, with
at most 1000 frames. Telegram downloads are limited to 20 MiB. The adapter
sends the bytes as base64 in the loopback request; the unprivileged agent
creates its own private temporary directory and 0600 file for the current
turn, and removes them when the request finishes, including on errors or
timeouts. No shared temporary directory or adapter-owned file is needed. The
agent passes supported images to the current Responses request as an
`input_image`, and the current-turn `shell_execute` tool can read documents.
Encoded image data and temporary paths are not stored as conversation history.
Telegram media albums are unsupported and ignored.

The token installation, pairing-state setup, and service start or restart are
remote changes: review their exact scope and obtain fresh explicit approval
immediately before each mutation. For the detailed SSH workflow and the
BotFather privacy setting, see the
[OVH remote SSH skill](skills/ovh-remote/SKILL.md).

When configuring Telegram on that remote, the SSH skill directs the agent to
the packaged
[Telegram BotFather Group Privacy Mode skill](skills/telegram-group-privacy/SKILL.md),
which explains the `/setprivacy` → **Turn off** option for users who want
ordinary group messages.

## Models and fallback

Primary and fallback models default to `gpt-6.1-sol` and `gpt-5.6-luna`.

Provider errors wrapping `ErrModelUnavailable` trigger one retry on the
fallback model. The fallback remains active for the session and its reason is
displayed. Other provider errors terminate the loop with a nonzero exit status.

Input lines are limited to roughly 1 MiB.

```sh
go test ./...
./packaging/build.sh all
./packaging/validate.sh dist
```
