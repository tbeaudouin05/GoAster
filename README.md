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
working directory and bounded timeout in milliseconds. The separate
`background_command` tool manages named local commands from a private allowlist
and keeps their sessions only for the lifetime of the GoAster process. GoAster
has no provider, API-key, or web-search flag configuration.

The [internet-fetch skill](skills/internet-fetch/SKILL.md) guides normal
retrieval first, bounded retries, and a one-request IPv4 check for suspicious
endpoint-specific failures. It is loaded from the local skills directory when
needed. The built-in `web_fetch` networking behavior is unchanged.

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

On first launch, GoAster installs starter skills from the package's ordinary
`skills` files into `~/.goaster/artefacts/skills/`. It also creates a default
`artefacts/memory/MEMORY.md` with project-memory guidance if that file is
missing. The remote agent does the same in its own home. Installation needs no
network connection. An existing skills directory, even an empty one, and an
existing MEMORY.md are left untouched; GoAster never updates user skills
automatically.

### Update existing runtime skills

Updating the package or `GOASTER_STARTER_DIR` does not update an existing
runtime skills directory. In particular, an older installed `ovh-remote`
workflow will not acquire the socket, secret-rotation, or transport-selection
guidance merely by installing a new release. Before using those steps on an
existing installation, review and explicitly migrate the installed workflow.
The local app uses `~/.goaster/artefacts/skills/`; a root-run remote agent uses
`/root/.goaster/artefacts/skills/`. They are separate installations.

1. Choose the matching `skills/` tree from the reviewed new package or source
   release. Inspect the existing runtime tree and its files; stop if paths are
   symlinked or their ownership or contents are unexpected. Stop the processes
   using that runtime while changing files; for a remote setup, stop the
   adapter before the agent. Include those actions in the approved checklist.
2. Make a private backup of the existing runtime skills, including all user
   edits, outside the discovered `skills/` directory. Preserve file ownership
   and permissions. Compare each affected `META.md`, `SKILL.md`, and `AUTH.md`
   with the reviewed new version. If the previous starter version is available,
   use it as a common baseline; otherwise review the differences manually.
3. Prepare a manual merge that retains user changes and incorporates the
   reviewed guidance. Inspect the complete resulting files and diff, obtain
   approval for the intended changes, and verify the runtime files still match
   the backup before applying the approved merge. Do not recursively copy the
   new starter tree over existing files or automatically replace edited files.
   Keep the backup for recovery. Leave existing memory untouched.
4. Restart the relevant processes under the approved checklist, agent before
   adapter on a remote host, and verify the workflow. A conversation that had
   selected a changed skill must use `/clear` to discard that selection, or
   restore the exact original files before continuing that conversation.

Each starter contains `META.md`, `SKILL.md`, and `AUTH.md`. If installation is
interrupted, GoAster reports the `.skills-installing` marker and stops; inspect
the partial files, preserve any edits, and remove or move them and the marker
aside before retrying. File and directory sync failures are reported where
supported; unsupported directory syncing is best effort.
Default-memory publication requires either hard links or an atomic
non-replacing rename. Linux, macOS, and Windows try the latter when hard links
fail. If neither method is available, installation stops without replacing or
exposing a partial MEMORY.md; create that file yourself, or use a supported
filesystem, before retrying. Other platforms currently require hard-link support.

Authentication is part of this normal startup journey and does not require a
separate command. On the first launch, or whenever no valid subscription
credential is available, GoAster starts the device-code OAuth flow in the same
terminal process. Open the displayed URL, sign in to ChatGPT, and enter the
one-time code. Existing credentials are refreshed when needed and then chat
starts directly.

The OAuth tokens and ChatGPT account ID are saved locally in
`~/.goaster/auth.json` with `0600` permissions inside a `0700` directory.

## Ask GoAster for what you need

At GoAster's chat prompt, describe your goal in ordinary language. In an
interactive terminal, GoAster shows a compact header and readable message
bubbles; redirected or scripted input keeps the line-oriented `you>` prompt.
GoAster follows the relevant installed skill; you do not need to name a skill or
enter a slash command to select one. For example:

- “Create a new OVH server and install GoAster on it.”
- “Provision a new OVH server.”
- “Install GoAster on my existing OVH server.”
- “Find and explain this public document.”
- “Help my Telegram bot receive messages in a group.”
- “Review my daily memory notes and propose durable facts to keep.”

GoAster asks for clarification if it cannot tell whether you want a new OVH
resource or help with an existing host. Memory consolidation is optional and
runs only when requested. For local secret setup and non-secret file inspection,
use the operational `/env`, `/get-env`, and `/file` flows described below.

## Build packages (developers)

Packaging requires Git, Go 1.22+, and a POSIX-compatible shell. From the repository
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

The build accepts only intended tracked regular package assets and rejects
unexpected files or symlinks in `skills/`. Add newly created intended assets
to Git's index before building. The archive helper uses only Go's standard
library, and the validation script checks package structure, starter files,
and launcher wiring without opening a host GUI. `./packaging/test.sh` also
builds and checks actual archives and, on Linux, tests fresh local and remote
installations offline; it requires Python 3.
For source development, explicitly set `GOASTER_STARTER_DIR` to the
absolute path of this checkout's `skills` directory, for example:

```sh
GOASTER_STARTER_DIR="$(pwd)/skills" go run ./cmd/goaster
```

This override also works when running from another directory. A separately
installed binary also looks for `skills` beside the executable and under its
installation prefix at `share/goaster/skills`. Keep the assets together with
the binary; they are not embedded in it.

## User-owned fork workflow (developers)

The simple customization journey is user-owned and sequential: use the
[GitHub CLI skill](skills/github-cli/SKILL.md) and
[fork workflow](skills/goaster-fork/SKILL.md) to verify `github.com`, create or
reuse a public fork, and create or reuse the standard `goaster-customizations`
and `goaster-contributions` branches. Upstream remains read-only;
all source work happens in the user's fork and this workflow does not create a
pull request. The [source workflow](skills/goaster-source/SKILL.md) allows
arbitrary user customization in that fork, including Go code, Markdown skills,
packaging assets, documentation, and tests.

To share an approved change, use the
[contribution-sharing skill](skills/goaster-share-contribution/SKILL.md). It
keeps implementation and locally selected skills on the user's
`goaster-customizations` branch and stores only exact-commit JSON manifests on
the manifest-only `goaster-contributions` branch. A local skill is copied into
the customization branch and committed before its manifest is published;
source contributions reference an existing customization commit without
duplicating its implementation. Use
[contribution discovery](skills/goaster-find-contributions/SKILL.md) for
read-only manifest inspection. These workflows keep upstream read-only and
require explicit approval for fork/branch writes, commits, pushes, and pull
requests.

Then use the [build/test workflow](skills/goaster-build-test/SKILL.md) for
`go build`, the explicit core Go tests, an isolated binary start, and one short
prompt smoke test. It intentionally does not run Telegram or adapter tests.
Before deployment, the [backup workflow](skills/goaster-backup/SKILL.md)
requires a verified rollback bundle stored on the server. An OVH instance
backup is optional; existing provisioning rights cover documented creation and
inspection. GoAster verifies the current endpoint and rights, explains cleanup
responsibilities, and requires approval for the operation, cost, retention,
deletion, and any secret-inclusive data. The [deployment workflow](skills/goaster-deploy/SKILL.md)
uses the stable `/usr/local/libexec/goaster-deploy` helper, stages and checksums
the versioned release, swaps it atomically, restarts, runs health and prompt
smoke checks, and rolls back to the local bundle on failure. The
[coordinator](skills/goaster-coordinator/SKILL.md) sequences these stages and
invalidates approvals when identity, SHA, path, or host changes.

## Environment variables

When setup needs environment variables, GoAster explains each one's purpose and
where to find its value, and invites you to ask for more explanation of any
variable. Enter secret values only through GoAster's local `/env`
environment-file flow; never paste them into chat. GoAster masks imported
values before saving them locally.

Type `/get-env` to edit the current environment file or get a blank template.
If no environment file has been saved, GoAster creates a blank template with
`OVH_APPLICATION_KEY=`, `OVH_APPLICATION_SECRET=`, `OVH_CONSUMER_KEY=`, and
`TELEGRAM_BOT_TOKEN=`. For OVH setup, GoAster detects the account/API region
automatically; you do not provide it. Once the credentials are available
through `/env`, onboarding probes all supported account/API endpoints with the
same restricted credentials and read-only `GET /cloud/project` discovery to
detect the working region. GoAster probes all supported account/API endpoints and does not ask the user to identify the account/API region. You do not enter the endpoint in the environment file.
Fill in the values, then re-upload the completed file with `/env`. The saved
file is written with `0600` permissions.

To inspect a local file, type `/file` in the terminal and choose it in the
native file picker. Files up to 20 MiB are supported. PNG, JPEG, GIF, and WebP
images are sent as images; other files are attached as documents for the
agent's local tools to inspect. Do not select files containing secrets; enter
secret values only through `/env`.

The terminal conversation is saved automatically in
`~/.goaster/artefacts/state/sessions/session.json`
with `0600` permissions. It contains bounded user/assistant conversation text,
any generated summary, and the names and fingerprints of selected skills;
OAuth credentials, skill text, and tool-call internals are not automatically
copied into it.
At 75% of the fixed 300,000-token context window, or when the
256-message limit is reached, the active subscription-backed model summarizes
older messages while recent messages remain available. The summary is saved
and included in future requests. If summarization fails, GoAster trims older
messages to keep context bounded and reports the trimming in the terminal and
to the model. Session storage remains bounded to 8 MiB and 32 KiB per message.
If the saved conversation is malformed, GoAster moves it aside safely and
starts a new conversation.

Only one process can run a turn against a saved conversation at a time. If
another process changes it, restart to load the current conversation before
continuing.

Type `/clear` in the terminal to clear the persistent terminal conversation.
In Telegram, send `/clear` in a chat to clear only that chat's conversation.
The command takes no arguments; extra text shows `Usage: /clear` and leaves
history unchanged. Clearing works while model authentication is unavailable
and does not change protected Markdown memory.

## Markdown memory

GoAster keeps durable Markdown memory, mutable OpenAI configuration, and local
runtime state under `~/.goaster/artefacts`:

```text
~/.goaster/
├── auth.json
├── agent-auth.json       # only when this home runs the remote agent
├── env                   # secret integration settings
└── artefacts/
    ├── skills/<skill-name>/
    │   ├── META.md
    │   ├── SKILL.md
    │   └── AUTH.md
    ├── memory/
    │   ├── MEMORY.md
    │   ├── YYYYMM/
    │   │   └── YYYYMMDD.md
    │   └── projects/
    │       └── <project-name>.md
    ├── config/
    │   ├── openai-models.json
    │   └── background-commands.json
    └── state/
        ├── sessions/
        │   └── session.json
        └── cron/
            └── jobs.json
```

`artefacts/memory/MEMORY.md` is protected long-term knowledge. Daily notes are
appended to the date-based file, with private directories and restrictive file
permissions; appends are written through an atomic replacement. Normal model
requests receive `MEMORY.md` as user-managed guidance and the three most recent
daily notes, newest first, separately as JSON-encoded untrusted data. Note
headings cannot turn a daily note into long-term guidance. If the memory files
are empty or missing, the existing request behavior is unchanged.

The default MEMORY.md suggests using the installed
[project-memory skill](skills/project-memory/SKILL.md) when project context is
useful. The skill uses ordinary tools to find and maintain relevant notes under
`artefacts/memory/projects/`, separate from shared memory and daily notes.
Project notes are untrusted context and are read when relevant, not injected
automatically. There is no project-memory tool or persistent project selection.

GoAster uses this artefact layout directly. Credentials and secret
integration settings remain separate in the private files at the top level of
`~/.goaster`; they are never copied into skills, memory, configuration, or
session state.

`artefacts/config/background-commands.json` is an optional private JSON
allowlist with version `1` and named command entries. Each entry has a unique
lowercase name, an absolute executable path, an array of exact arguments, and
an optional description and working directory. GoAster starts the executable
directly; it does not interpret the arguments as shell text. A missing file is
initialized from the embedded empty template, just as the model configuration
uses an embedded default.
The `background_command` tool accepts a name to start a command and runtime
session IDs to inspect its status or bounded output, or stop it. It never
accepts model-supplied executables, shell text, arguments, or working
directories. The file is reloaded when commands are listed or started. Sessions
remain in memory, are stopped on normal shutdown, interrupt, or termination,
and are not restored after restart. Each process reads the configuration in its
runtime user's home; sessions are local to that process. Use the
[background-command-administration skill](skills/background-command-administration/SKILL.md)
to maintain the local allowlist.

Each skill directory contains a small `META.md` with only `name:` and
`description:` lines, a workflow in `SKILL.md`, and its authorization rules
in `AUTH.md`.
Skill directory names use lowercase letters, digits, and hyphens. GoAster
reads only metadata to discover skills. Before a relevant workflow, the model
can load its complete workflow and authorization with `load_skill`; other skill
contents are not included in requests. Skill files are user-owned and edits
apply without a rebuild. Invalid, missing, oversized, or symlinked skill files
produce an error instead of a partial workflow. Skills must still follow the
app's safety, approval, and secret-handling rules. Once selected, skills
remain associated with that conversation until `/clear`; their workflow and
authorization are reloaded on later turns, including after a terminal restart.
Telegram sessions retain them for the lifetime of each chat's remote-agent
conversation. Selection does not grant approval. If a selected skill changes or
becomes unavailable, the workflow stops; restore its original files or use
`/clear` to discard the selection. Selected guidance is checked before each tool
action. If loading fails or is interrupted, check whether earlier actions
completed before continuing or repeating them. A conversation may select up to
10 skills, with combined loaded skill text limited to 512 KiB. If the complete
text cannot fit within the estimated conversation context, the request stops
instead of truncating the skill or its authorization rules.

Conversation summarization continues to update only the conversation summary
in `artefacts/state/sessions/session.json`; it never rewrites
`artefacts/memory/MEMORY.md`. Ask GoAster to review and
consolidate memory when you want the optional, user-managed workflow in the
[self-improvement-memory-consolidation skill](skills/self-improvement-memory-consolidation/SKILL.md)
to review daily notes and promote only verified durable facts into protected
long-term memory. Users may invoke it from their own cron or scheduler; GoAster does not
install or provide an operating-system cron entry. The skill remains
user-managed and is not automatically scheduled by GoAster.

## Scheduled chat turns

GoAster includes a small local scheduler for model-requested reminders and
recurring chat turns. Jobs are saved atomically in
`~/.goaster/artefacts/state/cron/jobs.json` with private `0700` directory and `0600` file
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
`at_seconds` jobs are normally deleted after their execution. Updates are
partial, so omitted message, schedule, and `max_runs` fields are preserved.

Due jobs send their saved message through the same terminal chat session as
interactive input; the session serializes turns so scheduled execution cannot
corrupt conversation state. Scheduler instances coordinate across processes;
if a run is interrupted after it may have started, it is disabled for review.
If OpenAI authentication fails after a tool starts, the scheduler retains the
job and its warning with `review_required` set, including for one-time jobs.
The job stays disabled across restarts and future scheduled times. Reconcile
any actions and resolve authentication before deciding whether it should run
again. If `max_runs` has been reached, first update it to `0` (unlimited) or
to a value greater than `run_count`, then explicitly enable the job. Changing
the limit alone does not clear its review state or enable it. You can instead
leave the job disabled or remove it. Enabling schedules a new execution; it
does not recover the results of the uncertain run. While a job is running, a
positive `max_runs` update must be greater than its current `run_count` to
leave room for that run; otherwise the entire update is rejected. A running
job cannot be removed until its current execution finishes.
Skill preflight failures and conversation recovery defer jobs that have not
started. If a saved conversation is missing or invalid, inspect the saved or
quarantined file and any actions that may already have completed; use `/clear`
to explicitly start a new conversation before chat or scheduled work resumes.
This feature deliberately does not schedule shell
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
rules, and the GoAster installation itself. `shell_execute` is not sandboxed:
it can run with the agent's permissions, access its files and network, and
does not enforce skill selection or approval rules on each command. Skill
instructions, approval requests, and untrusted-data guidance depend on model
behavior; they are not a security boundary and can be bypassed by a mistake or
prompt injection. Command output is returned to the model without general
secret redaction. Time and output limits do not isolate commands. Protect
access to the agent and its accounts, and retain strict host-key checks and
separate local and remote OpenAI credentials.

GoAster's root-run unrestricted shell can access locally stored `gh`
credentials. The standard web login does not create a credential sandbox; do
not describe those credentials as inaccessible to the agent or model.

### Protected agent socket for OVH systemd setups

For a fresh Linux OVH setup running both services as root, configure the
agent's systemd `[Service]` section with:

```ini
User=root
RuntimeDirectory=goaster-agent
RuntimeDirectoryMode=0700
RuntimeDirectoryPreserve=yes
Environment=GOASTER_AGENT_SOCKET=/run/goaster-agent/agent.sock
Restart=on-failure
RestartSec=5s
```

Set the same `GOASTER_AGENT_SOCKET` environment assignment in the Telegram
service's `[Service]` section. The agent's runtime directory is root-owned and
private; its socket is mode `0600`. Both services must see the same directory,
including when a unit uses a filesystem namespace. Its ancestors must be
root-owned, without symlinks or untrusted writable access, except that a
root-owned sticky directory protects root-owned children. The adapter also
checks the socket owner and kernel-reported peer before sending requests.
Root and the root-run GoAster shell still have full host access.

With the variable set, the agent listens only on the socket and the adapter
never falls back to TCP if the socket is missing or unsafe. A configured control
secret file also requires this socket setting in each service; omitting it fails
startup. When both settings are absent, existing loopback TCP behavior remains
unchanged, including with the legacy control-secret environment value. Socket
mode is supported on Linux for the root service setup; paths must be absolute, clean,
and at most 107 bytes. An explicitly empty value fails closed. The preserve
setting prevents systemd from removing the runtime directory on service stop
or failure; `/run` is still cleared on reboot. Check effective units, drop-ins,
and cleanup jobs for other removal. The agent does not overwrite an existing
socket or other file.

For boot ordering, bounded socket readiness, and rate-limited restarts, use
the complete fresh-unit settings in the [OVH remote skill](skills/ovh-remote/SKILL.md).
Enable both reviewed units after Telegram setup when approved. `After=` alone
does not wait for the agent socket; the adapter waits for `/livez` before
starting. An unclean exit
can leave a stale socket that prevents automatic restart. After agent recovery,
verify the adapter and restart it if needed before the real message/reply test.

After an unclean stop, an unexpected socket can prevent a restart. Stop both
services and inspect the exact path, its type and ownership, and the system's
Unix listeners and owning processes. Only after confirming that the entry is
the root-owned stale socket in the reviewed private directory and that no
process is using it, manually remove that exact socket. Leave other entries
untouched and investigate if anything is uncertain. Start and verify the agent
before starting the adapter.

For an existing installation, review both units and coordinate the change:
stop the adapter, update both units, reload systemd, restart the agent and
check it over the socket, then restart the adapter and perform the real
Telegram message/reply check. Inspect the running agent's effective transport
before each login. The OpenAI `process_handoff` requires `agent_transport`
(`socket` or `tcp`); for a socket also pass the reviewed path as `agent_socket`.
The exceptional `goaster remote-login` command requires `--agent-transport`
and, for sockets, `--agent-socket`. Legacy loopback installations use explicit
`tcp`. An omitted or inconsistent selection fails before SSH. The saved
connection record stores neither the transport nor the path, so inspect the
effective unit for later logins. The selection is an operator assertion, not
an automatic detection of the service configuration. A
socket-based liveness check can use
`/usr/bin/env GOASTER_AGENT_SOCKET=/run/goaster-agent/agent.sock /usr/local/bin/goaster-agent live`;
use `health` in place of `live` for readiness. These probes verify the socket
and its peer and do not fall back to TCP. For remote login, socket paths may
contain only ASCII letters, digits, `/`, `.`, `_`, and `-`.

### Shared agent and adapter control secret

For a fresh systemd setup, before either service starts, provision one fresh
32-byte secret as 64 lowercase hexadecimal characters in a root-owned file
such as `/etc/goaster/control-secret`, with mode `0600` (and a non-symlinked,
root-controlled parent). Set the same non-secret file path in both the
`goaster-agent` and `goaster-telegram` units:

```ini
Environment=GOASTER_GITHUB_AUTH_CONTROL_SECRET_FILE=/etc/goaster/control-secret
UnsetEnvironment=GOASTER_GITHUB_AUTH_CONTROL_SECRET
```

After fresh setup, reload systemd when needed, start the agent first, and start
the adapter only after the agent readiness check succeeds. For the final gate,
have the paired user send a fresh ordinary message and require `/chat` to return
HTTP 200 with a non-empty final reply that the adapter delivers in the same
private Telegram chat. An HTTP error, timeout, adapter error message, readiness
result, or pairing confirmation does not pass this check.

For an existing installation, defer the rotation if both services cannot be
restarted and verified together. First stop the adapter before staging a new
secret, changing a file, or editing either unit. Retain a protected, usable
copy of the previous secret and unit settings for recovery without exposing the
secret. Stage a replacement in a new protected file (or atomically replace the
existing file), configure both units with the same file and socket paths, and
reload systemd if either unit changed. Restart the agent and verify its
readiness before starting the adapter. Then repeat the successful `/chat`
final-reply check; readiness alone does not validate the rotated secret. On
failure, stop the adapter, restore the previous protected secret and unit
settings for both services, and reload if needed. Restart the agent after the
restore even if only the secret file changed: the running agent keeps the
secret it loaded at startup. Verify the restarted agent before restarting the
adapter and repeating the message/reply check. If recovery cannot
be verified, leave the adapter stopped and investigate.

Mode `0600` protects the file from other local users, not from GoAster's
root-run `shell_execute`, which can read root files and has host-wide impact.

On Windows, leave `GOASTER_GITHUB_AUTH_CONTROL_SECRET_FILE` unset and use the
legacy `GOASTER_GITHUB_AUTH_CONTROL_SECRET` environment setting instead. The
file setting requires Unix ownership metadata and fails closed on Windows; do
not configure both settings.

### GitHub CLI login

Use an existing `gh` session when it is authenticated to `github.com`. Before
GitHub API requests, and again after any login, verify the session with
`gh auth status --hostname github.com` and
`gh api --hostname github.com user --jq '.login'`. Continue only when the
verified login is the account the user intends to use.

If login is needed, the agent runs this command in the foreground:

```sh
gh auth login --hostname github.com --web
```

If `gh` displays an official GitHub URL and one-time code, relay only that URL
and code to the user. The user completes authentication in the browser. Let the
command finish, then verify the session and account with the commands above.
Do not enter the code for the user or ask them to send it back. Report only the
verified host and login. The separate profile-based `process_handoff` flow is
unchanged.

## OVH provisioning and remote access

For the end-to-end path from packaged launch to remote Telegram operation, see
the [GoAster user journey](docs/USER-JOURNEY.md).

GoAster follows the [OVH provisioning skill](skills/ovh/SKILL.md) when you ask
for new OVHcloud resources. It requires a user-created active project and
verifies it with read-only project discovery before checking quota, region,
and options. Before
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
approval before billable creation or other mutations. When you ask to install
GoAster on an existing OVH host, including one just provisioned, GoAster follows
the [OVH remote SSH skill](skills/ovh-remote/SKILL.md). It requests approval
for a target-specific routine plan before the read-only SSH preflight and
listed actions, requires separate approval for unlisted changes, and verifies
the result; it does not provision
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

You do not need to provide the OVHcloud account/API region in GoAster chat. After the credentials are available through
`/env`, onboarding probes the Europe, Canada, and United States account/API
endpoints with read-only `GET /cloud/project` using the same restricted
credentials. If one or more probes succeed, GoAster selects a successful
endpoint, reports the detected region, and asks only for a simple confirmation.
If none succeeds, it gives a clear setup error. Never paste API keys or other
secrets in chat. This account/API region is separate from the Public Cloud
deployment region. Ask for the preferred deployment region only when it is
genuinely unknown. The [OVH provisioning skill](skills/ovh/SKILL.md) documents
the probing behavior.

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
in screenshots. These three rows also cover documented Public Cloud instance
backup creation and inspection; no separate credential extension is needed for
those operations. The [OVH instance-backup guide](https://docs.ovhcloud.com/en/guides/public-cloud/compute/save-an-instance)
documents the region lookup and snapshot creation endpoints. GoAster verifies
current methods and rights before use. If it performs deletion or retention
changes, it needs `DELETE` rights for the relevant documented endpoint and any
other required rights. Backup operations and costs require explicit approval;
retention, deletion, and secret-inclusive data require separate approval.

The OVH guide documents daily automatic schedules retaining the last 7 or last
14 backups; no universal cap on manually created backups was verified. You are
responsible for managing and deleting old backups and schedules if you want to
minimize ongoing storage costs. Retained backups can incur charges after the
instance is deleted. GoAster must never delete backups or schedules automatically
unless separately approved; OVH-managed schedule rotation that deletes older
backups also requires separate approval.

The [OVH US Public Cloud price list](https://us.ovhcloud.com/public-cloud/prices/)
lists Instance Backup storage replicated x3 at about US\$0.000017/GB/hour or
US\$0.0123/GB/month, excluding tax, billed by used space with 1 GB granularity.
Distant backups are charged separately at the remote region's storage price.
Actual prices vary by OVH account region and current pricing.

**End-to-end:** Start in local GoAster chat and sign in to OVHcloud directly on
its website. Enter the generated OVH API key values and Telegram bot token only
through GoAster's local `/env` environment-file flow. Never paste keys into
chat or include them in screenshots. For a new host, GoAster provisions
the OVH resource with explicit approval and then bootstraps it over SSH; for
an existing host, it starts with SSH bootstrap.
After remote bootstrap and agent service setup, GoAster checks the existing
remote credential and keeps it if ready. When login is needed and approved,
GoAster starts the SSH-backed remote OpenAI login as `root` through the fixed
`openai` profile in its generic `process_handoff` handoff. The remote agent and Telegram adapter intentionally
run as root so GoAster can fully self-manage its own server. After the exact
host and local key
are approved in chat, the handoff displays them in the local terminal and
requires the administrator to type `YES` before connecting. It then attaches
the terminal directly to the login flow, waits while the administrator
approves the OpenAI device authorization in a browser, and completes the
existing `/healthz` check before continuing onboarding; that check may refresh
and write the remote credential. The
administrator must approve both the host contact and device authorization;
GoAster cannot approve them on their behalf. `process_handoff` is an internal
tool and `goaster remote-login` is a compatibility command for exceptional use;
users do not normally type either one. Keep local OpenAI credentials on
the local machine; remote credentials are stored separately on the host.

The model-facing handoff is generic but intentionally profile-based. Its fixed
allowlist contains only the `openai` profile, and the `process_handoff` chat
tool permits only its protected `interactive` mode. Background mode remains
rejected by that handoff. The separate local `background_command` tool starts
only names from `background-commands.json`; it does not expand the handoff's
profile list or accept model-provided executable names, command strings, shell
fragments, or arbitrary arguments. The interactive handoff returns only a
generic status to the model while its input and output remain attached to the
protected administrator terminal.

The local package does not need Go to bootstrap the remote agent: the SSH skill
verifies or installs pinned Go 1.27.3 on the existing Linux host, clones the
latest `main` branch from this repository at bootstrap time, records the
resolved commit, builds `goaster-agent` and `goaster-telegram` from that
checkout on the host, then installs the resulting binaries and the checkout's
starter files at `/usr/local/share/goaster/skills` before starting the agent.
For fresh installs, the reviewed agent service explicitly selects that asset
directory so an executable-adjacent `skills` directory cannot shadow it. The
Go archive checksums and remote build steps are documented in the skill.
This SSH bootstrap does not manage the OVH resource lifecycle or add an agent
self-update feature.

Before each SSH connection, match the target to the current OVH instance ID and
public IP. A saved address or old screenshot is not authoritative. A saved
server's read-only liveness check uses `/livez`; it does not establish that the
remote login is ready. Any `/healthz` readiness check must be included in the
approved plan because it can refresh credentials.

For the exact instance just created and verified in the same provisioning
flow, GoAster carries forward the instance ID, current public IP, and approved
SSH identity, then checks the ID and IP again through OVH before connecting.
The first SSH contact automatically saves the host key in a unique
target-specific `known_hosts` file using `StrictHostKeyChecking=accept-new`.
All later connections, including the protected login, use that same file with
`StrictHostKeyChecking=yes`. You will never be asked to retrieve, compare,
approve, or paste an SSH host-key fingerprint for this newly created server.
If the instance, IP, readiness, or approved key cannot be confirmed, GoAster
stops without connecting. Pre-existing hosts and hosts of unclear provenance
need an already trusted entry or fingerprint verification through a trusted
channel and approval before connecting.

The terminal and remote agent use **independent OpenAI credentials**.
The local terminal login described above stays local. Never copy or send its
`auth.json`, a custom `-auth-file`, its OAuth tokens, or its saved session to the
OVH host. A freshly installed remote agent has no subscription credentials and
does not use the terminal's credential file. The agent can run while waiting
for login, but its `/healthz` and `/chat` endpoints return unavailable.

GoAster also remembers a successful server connection locally. It saves a
small non-secret connection record beside the dedicated SSH key, containing
only the server address, port, and optional known-hosts file. The
server-management skill can reuse and verify the record during a relevant
user-requested workflow, with strict host-key checking. Starting GoAster does
not connect to a saved server. The record never contains the private
key, OpenAI login, Telegram token, pairing code, or environment values.

### If the administrator changes computers

The server keeps the SSH public key in its authorized keys; the matching
private key stays on the administrator's computer. Preferred: generate a new
Ed25519 key on the new computer, add its public key while the old key still
works, test SSH from the new computer with strict host-key checking, then
remove the old public key. Alternatively, securely transfer the existing
private key. Never send a private key in chat or store it in the repository.
If the only private key is lost, recovery needs another working access path or
the hosting provider's console/recovery access.

For the host described in the user journey, Ubuntu 26.04 LTS was selected and
reported active at creation. This is creation-time provisioning evidence, not a
fresh live OS check. The observed root SSH forced-command redirect to `ubuntu`
is an account policy; it does not identify or change the OS image.

When remote login is needed and root SSH is ready, GoAster's internal
`process_handoff` handoff invokes the protected `openai` profile using `root` as the
SSH target. The normal onboarding path must not create or use a separate
unprivileged GoAster runtime account. The underlying `goaster remote-login`
command is for exceptional manual use, not a normal onboarding step.

The protected flow requires an approved SSH target and local key, invokes OpenSSH
directly through the local CLI's protected controlling-terminal handoff, and
does not pass through the model's `shell_execute` tool. The explicit command
uses only the current process's `/dev/tty` on supported Unix systems. The
`process_handoff` model handoff also tries `/dev/tty` first; if no controlling
terminal is available, it uses inherited stdin and stdout only when both are
the same actual terminal device. Pipes, detached processes without terminal
stdio, and unrelated devices fail before connecting with a generic
terminal-unavailable status. Start GoAster with an attached interactive
terminal to retry. The handoff never searches for another pseudoterminal or
saves device codes to chat or files. The model receives only a generic success,
failure, decline, invalid-request, or terminal-unavailable status. It requires
strict host-key verification, uses
only the supplied key, and never accepts a password, token, or credential-file
argument. The host must already have a trusted entry in a `known_hosts` file;
for the new-server flow above, the first preflight saves it automatically and
GoAster passes that target-specific file to the protected login. Existing or
provenance-unclear hosts follow the trusted-channel process above when they have
no trusted entry. Do not bypass a host-key mismatch.

The interactive terminal displays the URL and one-time code for the
administrator. The administrator opens the URL in a browser and approves the
remote device authorization; GoAster waits for that approval and the login to
finish. Do not relay the URL, code, tokens, or login output through GoAster or
Telegram chat. The remote login saves its own credentials as
`/root/.goaster/agent-auth.json` under root's remote home, with `0600` file and
`0700` directory permissions. This file is separate from
`/root/.goaster/auth.json`, even if both programs use the same home; GoAster never
copies the local file. After login exits successfully, the command runs a
separate SSH health check against the remote agent's `/healthz` endpoint over
the selected socket or loopback transport and reports failure clearly. This
check can refresh remote credentials.
Before login or reauthentication, quiesce credential writers: stop the adapter,
stop the agent and wait for its processes and in-flight requests to finish, and
stop or pause other clients, health checks, and independent writers. Start the
agent again with those clients still quiesced, verify only `/livez`, then keep
them quiesced through the interactive login and its automatic health check.
On a verified fresh install without an existing credential or other writers,
the first agent may remain running once prior requests have finished.
Otherwise, let active work finish before stopping when possible: stopping the
agent interrupts requests and clears its in-memory Telegram conversation
history. Include that impact in the approval.
The agent must be running for that check. Stop and start services only as
approved. If login is canceled or its health check fails, inspect before
retrying; the login may already have saved a credential. See the detailed
[quiescence procedure](skills/ovh-remote/SKILL.md#telegram-setup).

The health check returns `ok` only when the agent can load its separate
subscription credential, refreshing it if needed; this does not test a model
response or detect every revoked token. A nonempty token with no recorded
expiry can pass without contacting OpenAI. If a real model request rejects
authentication, investigate without exposing credentials, keep onboarding
partial, and obtain approval for a quiescent re-login if needed. Start or
restart the Telegram adapter after the check succeeds. For systemd readiness
reporting, configure its service as `Type=notify`: the adapter sends the ready notification after the
agent process is available and the first Telegram poll succeeds. It checks
authentication before telling a paired user that normal chat is ready. The
adapter can receive `/clear` while model authentication is unavailable.
Telegram token and pairing setup are separate steps below;
neither the OpenAI device code nor the subscription tokens belong in Telegram.

An `/healthz` result of `ok` marks only remote agent readiness. Full onboarding
remains **partial** until the existing Telegram token is retained or a new one
is supplied through the approved local `/env` flow and installed by the
separately approved remote setup, the adapter reports ready after a successful
Telegram poll, the intended user is paired, and that user completes a real
Telegram message/reply smoke test. Keep
prompting for the next pending step; agent-ready with Telegram incomplete is
not complete.

### Telegram user pairing

Telegram pairing is a separate setup step after the remote agent passes the
`/healthz` check. Retain an existing token and pairing. For a fresh setup,
create or select a bot with `@BotFather` and keep its bot token private.
During the approved remote setup, the token is installed in a
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
use it; stop and review a separately approved pairing reset. Never reset or
remove token or pairing state while any adapter instance is running. Stop all
instances and prevent restarts through reset and subsequent setup; otherwise
an adapter can pair using a verifier already in memory. Setup does not enforce
this operational precondition.

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
sends the bytes as base64 in the local agent request; the agent
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
the installed
[Telegram BotFather Group Privacy Mode skill](skills/telegram-group-privacy/SKILL.md),
which explains the `/setprivacy` → **Turn off** option for users who want
ordinary group messages.

## Models

The shipped OpenAI-only model template is `config/openai-models.json`. On first
run GoAster materializes the real runtime configuration at
`~/.goaster/artefacts/config/openai-models.json`; it reloads that file during normal requests,
so an owner/operator can add a model without restarting the process. Telegram
users can send `/m` to see the current exact model ID and
`/m MODEL_OR_ALIAS` to select a configured model for that chat. The
resolved exact ID is persisted per chat. A provider/model error is reported
directly; GoAster does not automatically retry another configured model.

If the JSON configuration is unreadable or invalid, GoAster preserves the bad
file, reports a bounded recovery notice once to the agent, and uses the
compiled-in emergency model `gpt-6.1-terra` until a valid configuration is
available. See the installed [model administration skill](skills/model-administration/SKILL.md)
for the owner/operator workflow; catalog membership is discovery only and is
never model verification.

Input lines are limited to roughly 1 MiB.

```sh
go test ./...
./packaging/build.sh all
./packaging/validate.sh dist
```
