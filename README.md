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

## OVH provisioning and remote access

For the end-to-end path from packaged launch to remote Telegram operation, see
the [GoAster user journey](docs/USER-JOURNEY.md).

GoAster includes two packaged OVH skills for separate stages. Use the
[OVH provisioning skill](skills/ovh/SKILL.md) to provision new OVHcloud
resources. It starts with read-only project discovery, can create a missing
project only after separate explicit confirmation, then checks quota, region,
options, and current price estimate. For an ordinary new-host setup, its
defaults are a GoAster remote agent on Ubuntu LTS, the smallest suitable
machine, SSH-key-only access, minimal inbound exposure, and a hard US$8/month
recurring-cost ceiling. It asks for a preferred deployment region only when
that preference is genuinely unknown, reports its assumptions and current
expected cost after read-only checks, and waits for explicit approval before
billable creation or other mutations. Use the
[OVH remote SSH skill](skills/ovh-remote/SKILL.md) to connect to and bootstrap
an already-existing OVH host, including a host just provisioned with the first
skill. It starts with read-only SSH preflight, requires fresh explicit approval
before each remote mutation, and verifies the result; it does not provision
resources through the OVH API.

An OVHcloud Public Cloud project is needed before resource provisioning. After
the credentials are configured through local `/env`, GoAster starts with the
read-only `GET /cloud/project`. If it returns no usable project, GoAster asks
for the intended project name and can automatically create it with `POST /cloud/project`
after explicit confirmation immediately before the call. Project creation is a
separate account-level, billing-relevant action; the agent must explain the
exact name and relevant consequence, and must verify the returned project ID
and name before continuing. That confirmation does not authorize a billable
resource. A user who prefers the manual route can use [OVHcloud
Manager](https://www.ovh.com/manager/#/hub/public-cloud) and the [official
project creation guide](https://docs.ovhcloud.com/en/guides/public-cloud/cross-functional/create-a-public-cloud-project).

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
`GET /cloud/project/*`, `POST /cloud/project`, and `POST /cloud/project/*` as
rights rows; the `+` button adds a row. The collection-level POST is for the
separately confirmed account-level project creation; the project-scoped POST
is for later project-scoped operations. See the [OVH provisioning skill's
Create API Keys instructions](skills/ovh/SKILL.md#create-api-keys-screen).
After configuring the keys through local `/env` and authenticating, follow the
read-only project check above. If a project is created, verify its returned ID
and name before continuing. Do not enter a project ID on the Create API Keys
screen. Never paste keys into chat or include them in screenshots.

**End-to-end:** Start in local GoAster chat and sign in to OVHcloud directly on
its website. Enter the generated OVH API key values and Telegram bot token only
through GoAster's local `/env` environment-file flow. Never paste keys into
chat or include them in screenshots. For a new host, provision
the OVH resource with explicit approval, then use the SSH skill to bootstrap
it; for an existing host, start with SSH bootstrap.
Next, complete a separate remote OpenAI device login from an SSH terminal and
verify `/healthz`; then complete the separately approved Telegram token and
pairing setup below. Keep the local terminal's OpenAI credentials on the local
machine.

The local package does not need Go to bootstrap the remote agent: the SSH skill
verifies or installs pinned Go 1.27.3 on the existing Linux host, clones the
latest `main` branch from this repository at bootstrap time, records the
resolved commit, builds `goaster-agent` and `goaster-telegram` from that
checkout on the host, then installs the resulting binaries. The Go archive
checksums and remote build steps are documented in the skill. This manual SSH
bootstrap does not manage the OVH resource lifecycle or add an agent
self-update feature.

The terminal and remote agent require **two independent OpenAI device logins**.
The local terminal login described above stays local. Never copy or send its
`auth.json`, a custom `-auth-file`, its OAuth tokens, or its saved session to the
OVH host. A freshly installed remote agent has no subscription credentials and
does not use the terminal's credential file. The agent can run while waiting
for login, but its loopback `/healthz` and `/chat` endpoints return unavailable.

After the remote agent is installed, open a separate SSH terminal to the OVH
host and run `goaster-agent login` as the same user and with the same `HOME`
that the agent service uses. For example, if the service runs as `goaster`,
uses `/var/lib/goaster` as its home, and the binary is installed in
`/usr/local/bin`:

```sh
ssh -t USER@OVH_HOST 'sudo -u goaster env HOME=/var/lib/goaster /usr/local/bin/goaster-agent login'
```

Use the URL and one-time code displayed in that SSH terminal to authorize the
remote device. Do not paste the code, tokens, or login output into GoAster or
Telegram chat, and do not run this interactive login through a chat tool that
would relay its output to the model. The remote login saves its own credentials
as `~/.goaster/agent-auth.json` under the service user's remote home, with
`0600` file and `0700` directory permissions. This file is separate from
`~/.goaster/auth.json`, even if both programs use the same home. If login fails
or is canceled, run the remote login again; keep the host and any separately
configured Telegram state for inspection. The same command can reauthenticate
the remote agent later; stop the agent service first and restart it afterward.

After login completes and the agent is running, check its readiness on the OVH
host with `curl --fail http://127.0.0.1:8765/healthz`. It returns `ok` only
when the agent can load its separate subscription credential, refreshing it if
needed; this does not test a model response or detect every revoked token.
Start or restart the Telegram adapter after the check succeeds. For systemd
readiness reporting, configure its service as `Type=notify`: the adapter sends
the ready notification only after the agent is ready and the first Telegram
poll succeeds. It also checks agent readiness before telling a paired user
that chat is ready. If the adapter was started too early, restart it after
login. Telegram token and pairing setup are separate steps below; neither the
OpenAI device code nor the subscription tokens belong in Telegram.

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
