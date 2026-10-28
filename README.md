# GoAster

A small standalone terminal chat app, built with Go and a native file-picker
integration. Requires Go 1.22+. Normal chat uses an OpenAI ChatGPT/Codex
subscription through the Codex backend; it does not use an OpenAI API key. A
fixed built-in `web_search` tool uses no-key DuckDuckGo HTML search and returns
search-result titles, URLs, and snippets. It has no provider, API-key, or
web-search flag configuration and does not fetch web pages.

## Build and run

Build the single binary:

```sh
go build -o goaster ./cmd/goaster
```

Start GoAster:

```sh
./goaster
```

Command-line flags configure or debug the app; authentication is part of this
normal startup journey and does not require a separate command.

On the first normal startup, or whenever no valid subscription credential is
available, GoAster starts the device-code OAuth flow in the same process. Open
the displayed URL, sign in to ChatGPT, and enter the one-time code. The OAuth
tokens and ChatGPT account ID are saved locally in `~/.goaster/auth.json` with
`0600` permissions inside a `0700` directory. After authorization completes,
GoAster continues directly into chat. Existing credentials are refreshed when
needed and then chat starts directly.

Enter one message per line. Blank lines are ignored. End the input stream with
EOF (Ctrl-D on Unix) to exit. Ctrl-C or closing the terminal also stops the
process where supported. A different credential file can be selected with the
configuration flag:

```sh
./goaster -auth-file /path/to/auth.json
```

## OVH remote-agent setup

GoAster can provision one OVHcloud Public Cloud instance through an explicitly
invoked local workflow. It never provisions automatically at startup or when
settings are imported. `/env` saves settings only after confirmation; `/ovh`
performs read-only API preflight and then requires a separate `CREATE`
confirmation before the billable instance is created. The assistant cannot
invoke the workflow, access the local settings, or inspect OVH state. Imported
values are never sent to the chat provider.

The workflow discovers a usable Public Cloud project and region, selects an
active Ubuntu 24.04 image, chooses the smallest compatible available flavor
(preferring an explicitly advertised resizable Flex flavor), and reuses or
registers the GoAster SSH key. If more than one candidate remains, `/ovh`
shows a short numbered choice instead of guessing. The server is public and
billed hourly until you delete it. Review OVH pricing, network exposure, SSH
access, and account permissions before confirming. The workflow needs
permission to list projects, regions, images, flavors, and SSH keys, to create a
project SSH key, and to create and read instances. It never deletes an
instance.

Telegram is mandatory and the only remote-agent access path. Create a new bot
by talking to [@BotFather](https://t.me/BotFather)
in Telegram and following its instructions. Keep the bot token private. Prepare
a local text file with one `KEY=VALUE` pair per line. All four OVH settings and
a valid `TELEGRAM_BOT_TOKEN` are required before provisioning can proceed.
`OVH_ENDPOINT` accepts `ovh-eu`, `ovh-ca`, or `ovh-us`.

```text
OVH_ENDPOINT=replace-with-your-ovh-endpoint
OVH_APPLICATION_KEY=replace-with-your-application-key
OVH_APPLICATION_SECRET=replace-with-your-application-secret
OVH_CONSUMER_KEY=replace-with-your-consumer-key
TELEGRAM_BOT_TOKEN=replace-with-your-bot-token
```

On `/ovh`, GoAster creates or reuses an Ed25519 pair at
`~/.goaster/ovh_ed25519` with owner-only private-key permissions. The private
key remains local; only the public key is uploaded through the OVH API. The
fixed SSH account is `goaster`, created by Ubuntu cloud-init. The server name
is generated automatically. GoAster derives the exact commit from the local
checkout or build VCS metadata; it does not ask for a shortened or 40-character
commit ID. That commit must be available in the public repository for the
remote installer to clone it.

In chat, enter `/env` and choose the local file in the native picker. Do not
paste paths or credentials into chat. GoAster validates the file, displays the
variable names with masked values, and asks for `y/N`; only `y` saves the set.
Canceling or rejecting a malformed, duplicate-key, oversized, symbolic-link, or
non-regular file leaves saved settings unchanged. The file must contain one
pair per line and be no larger than 1 MiB. On Linux, the desktop must provide a
Zenity-compatible helper such as `zenity`, `qarma`, or `matedialog`. Confirmed
settings replace the previous set at `~/.goaster/env`, with `0600` permissions
inside a `0700` directory. **Each `/env` import replaces the entire saved set**:
when adding a bot token, include all four OVH variables in the same file. The
importer also accepts other valid names; Telegram needs no other local secret.

When you are ready, enter `/ovh`. GoAster validates the stored settings and
local key, contacts the OVH API to discover and validate the target, and
displays the proposed IDs, names, repository, commit, and hourly billing. Type
exactly `CREATE` to authorize the key upload if needed, server creation, and
subsequent SSH installation, runtime credential transfer, and automatic Telegram
setup, startup, verification, and private pairing; any other response
cancels. The workflow waits
for an active instance and public IP, checks SSH as `goaster`, clones the
public repository at the derived commit, installs required packages and the
GoAster, agent, and Telegram adapter binaries, transfers the runtime credentials
through encrypted SSH stdin, and then enables and checks
`goaster-agent.service`, automatically sets up and starts
`goaster-telegram.service`, and verifies its first successful polling request
before showing the private pairing command. The image must support
passwordless `sudo`, `apt`, and systemd, and have outbound access to Ubuntu
package sources, GitHub, and Go module sources. The local machine needs
OpenSSH; `StrictHostKeyChecking=accept-new` trusts an unseen host key on first
use and rejects subsequent changes, so verify the server's host key through a
trusted channel where possible.

The result reports the project, region, image, flavor, SSH key and server IDs,
public IP, checked-out commit, and service status. If a later step fails or is
canceled, the server is preserved and its known ID and IP are reported so you
can inspect or delete it yourself. A failed create request can have an unknown
outcome even when no ID is returned; check the OVH project before retrying to
avoid creating a second billable server.

Telegram setup is part of the same provisioning workflow after `CREATE`; there
is no separate activation or skip prompt. The SSH target is retained only during
this workflow. Running `/ovh` again creates another billable server after its own
confirmation, so inspect any retained server before retrying.

Confirmed provisioning transfers the complete local OpenAI subscription
credential (access token, refresh token, expiry, and account ID), all four OVH
settings (`OVH_ENDPOINT`, `OVH_APPLICATION_KEY`, `OVH_APPLICATION_SECRET`, and
`OVH_CONSUMER_KEY`), and the required `TELEGRAM_BOT_TOKEN`. Unrelated imported
settings are excluded. The subscription comes from the active local login,
including a custom `-auth-file`; a missing or invalid credential stops the
workflow before any billable creation. A missing or invalid saved bot token stops the
workflow before any OVH API calls or billable creation.

The bundle travels as JSON only on encrypted SSH stdin to a fixed remote setup
command, before the agent starts. Secrets are never included in command arguments,
logs, systemd unit text, OVH metadata, Git, or Telegram messages. The **SSH private
key stays local** at `~/.goaster/ovh_ed25519`: it authenticates the SSH connection
and is not a runtime credential. Only its public key goes to OVH.

The remote subscription is stored in `/var/lib/goaster/.goaster/auth.json`, and
OVH/Telegram settings in `/var/lib/goaster/.goaster/env`, both owned by `goaster`
with `0600` permissions inside a `0700` directory. The agent service sets
`HOME=/var/lib/goaster` and reads and refreshes that subscription file directly.
The adapter reads its separate
root-owned `0600` `/etc/goaster/telegram-token` inside root-owned `0700`
`/etc/goaster`. Runtime installation rejects existing authentication directories;
a failed transfer prevents agent startup and preserves the billable server for
inspection. A failed later step may leave protected credentials on that server.

Storing OVH credentials does not add autonomous infrastructure tools or authorize
OVH changes. Billable creation and automatic Telegram setup require `CREATE`;
destructive OVH self-management must receive its own
explicit confirmation. This workflow never deletes servers.

`goaster-telegram.service` starts automatically after `CREATE` and is
verified after its first successful polling request. Use a fresh bot without a
webhook; a webhook prevents polling. The server needs outbound HTTPS access to
Telegram. The adapter uses long polling and opens no inbound port.

When startup succeeds, GoAster displays a one-time pairing command. In a
**private** Telegram chat with your new bot, send the displayed command:

```text
/start YOUR-ONE-TIME-CODE
```

Keep the code private: the first account to submit it becomes the sole paired
user. Sending `/start` without the code explains how to pair but grants no
access. The paired numeric user ID is stored in a root-owned `0600` file on
the server, and the verifier is removed after pairing. Other accounts, group
messages, and non-text messages cannot reach the model. Only private text chat
is supported; there are no Telegram administration, history, or clear
commands. Each message starts a separate conversation.

The agent listens only on `127.0.0.1:8765` on the server; its health check
verifies the process, not subscription login or a successful chat. Telegram
startup likewise verifies polling, not a successful model request. A separate
server login is no longer needed: paired private messages use the transferred
subscription. Local and remote copies may refresh independently; if the provider
invalidates a copied refresh token, reauthentication may be needed.

Telegram is the only remote-agent access path. The loopback listener is an
internal connection for the Telegram adapter; the workflow opens no public agent
port. Restrict server access because local processes can reach that listener.

## Models and fallback

Primary and fallback models default to `gpt-6.1-sol` and `gpt-5.6-luna`:

```sh
./goaster -primary-model gpt-6.1-sol -fallback-model gpt-5.6-luna
```

Provider errors wrapping `ErrModelUnavailable` trigger one retry on the
fallback model. The fallback remains active for the session and its reason is
displayed. Other provider errors terminate the loop with a nonzero exit status.

Input lines are limited to roughly 1 MiB.

```sh
go test ./...
./goaster -help
```
