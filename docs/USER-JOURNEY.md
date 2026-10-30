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
When relaying those two values to the user, put the URL as unformatted text on
its own line, followed by only the code in a fenced code block, so both values
are easy to copy:

https://auth.openai.com/codex/device

```text
ABCD-1234
```

Do not wrap the URL in Markdown link formatting. Never relay passwords, OAuth
tokens, or saved credential files. Open the URL in a browser, sign in with the
ChatGPT account that has the subscription, and enter the code. GoAster saves
and refreshes this local login at `~/.goaster/auth.json`. It does not require
an OpenAI API key.

## 2. Chat in the local terminal

Enter a message at `you>` and press Enter. GoAster replies in the same
terminal. The local chat includes its built-in web search, web fetch, and
`shell_execute` tools. Enter one message per line; blank lines are ignored.
Use EOF (Ctrl-D on Unix) or close the terminal to exit.

For help with an OVHcloud dashboard screen, type `/ovh-help` and choose an
image in the native file picker. Do not choose screenshots containing API
keys or other secret values. The OVH skill gives guidance for the Create API
Keys screen.

The conversation is saved in `~/.goaster/session.json`; durable Markdown
memory is kept under `~/.goaster/memory/`. Scheduled chat turns are saved
locally and run only while the packaged terminal process is active.

## 3. Import setup values with `/env`

When an OVH setup or Telegram configuration needs secret values, enter them
through GoAster's local environment-file flow, never in a chat message. Make a
local text file with one `KEY=VALUE` setting per line, type `/env` at the
terminal prompt, and choose that file in the local file picker. GoAster shows
the variable names with their values masked and asks whether to save them.
Confirm with `y` to store them in `~/.goaster/env` with `0600` file and `0700`
directory permissions. The imported file replaces the previously saved set;
canceling leaves it unchanged. The relevant skill explains which settings are
needed and where to find them.

## 4. Choose the correct OVH path

The two packaged OVH skills cover different starting points:

| Starting point | Skill sequence | What it does |
| --- | --- | --- |
| There is no OVH host yet | [OVH provisioning](../skills/ovh/SKILL.md), then [OVH remote SSH](../skills/ovh-remote/SKILL.md) | The first skill checks the OVH project, quota, options, and current price, then provisions and verifies a new resource after explicit approval. The second connects to that already-created host and bootstraps GoAster on it. |
| An OVH host already exists | [OVH remote SSH](../skills/ovh-remote/SKILL.md) only | The SSH skill checks and bootstraps that host. It does not create or manage OVH resources through the OVH API. |

**New resource path:** provision and verify the resource first, then use the
remote SSH skill to bootstrap it. **Existing host path:** start with the
remote SSH skill and skip provisioning.

For SSH bootstrap, the skill confirms the exact host and starts with read-only
checks. It then builds `goaster-agent` and `goaster-telegram` on the existing
Linux host from the repository's `main` branch, using pinned Go 1.27.3, and
installs the binaries. The local package does not need Go for this remote
build. Review each proposed remote change and approve it separately as the
skill requires.

## 5. Log in to the remote agent separately

The remote agent starts without a ChatGPT subscription credential. After its
approved installation and service setup, open a separate interactive SSH
terminal and run `goaster-agent login` as the same service account and with the
same `HOME` used by the agent service. For example, when the service user is
`goaster`, its home is `/var/lib/goaster`, and the binary is installed at
`/usr/local/bin`:

```sh
ssh -t USER@OVH_HOST 'sudo -u goaster env HOME=/var/lib/goaster /usr/local/bin/goaster-agent login'
```

Use the URL and one-time code displayed in that SSH terminal to authorize this
second device with the subscription account. This login writes a separate
`~/.goaster/agent-auth.json` on the remote host. Keep the local
`~/.goaster/auth.json`, its tokens, and the local conversation on your own
machine; never copy them to the host. Do not run the interactive remote login
through GoAster chat or a chat tool that relays its output.

Once the remote agent is running, check its credential and readiness on the
host:

```sh
curl --fail http://127.0.0.1:8765/healthz
```

It should return `ok`. The agent must pass this check before Telegram chat can
work.

## 6. Set up Telegram and pair one user

Create or select a bot with `@BotFather` and keep its bot token private. Use
GoAster's local `/env` flow for the token; do not put it in a chat message,
SSH argument, or script. Follow the Telegram setup steps in the
[OVH remote SSH skill](../skills/ovh-remote/SKILL.md). That setup stores the
token and a verifier for a fresh, random, one-time pairing code on the remote
host. The approved remote setup receives the bot token and only the pairing
code's SHA-256 verifier; give the raw pairing code privately to the intended
Telegram user. The pairing code is distinct from either OpenAI device-login
code.

After the agent passes `/healthz`, start the separately approved Telegram
adapter service. With systemd, configure the adapter as `Type=notify`; it
reports ready after the agent is healthy and Telegram accepts its first poll.
Once the adapter is polling, the intended user opens a private chat with the
bot and sends `/start`, then `/start <PAIRING_CODE>`. The first private
Telegram account that submits the correct code is paired. GoAster saves that
account's numeric Telegram user ID and removes the verifier. The paired ID
persists across adapter restarts. The code has no automatic expiry, so keep it
private and use it only for the intended user.

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

Telegram's Group Privacy Mode can prevent ordinary group text from reaching a
bot. See the [Telegram BotFather Group Privacy Mode
skill](../skills/telegram-group-privacy/SKILL.md) if group chat is needed.
Turning privacy mode off is optional and requires approval; Telegram also
requires removing and re-adding the bot to an existing group for the change to
take effect.

## 8. Keep GoAster running

Keep the local packaged app open for local terminal chat and scheduled turns.
For remote Telegram operation, keep the approved agent and adapter services
running on the OVH host. The agent must remain authenticated and healthy; the
adapter depends on it and on a successful Telegram poll. If the adapter was
started before remote login completed, restart it after `/healthz` returns
`ok`.

Remote credentials refresh when needed. To reauthenticate, stop the agent
service, run the separate remote `goaster-agent login` again as its service
user, verify `/healthz`, then restart the agent and adapter as required. The
remote bootstrap does not add self-updating behavior; a future remote build or
install follows the reviewed steps in the OVH remote SSH skill.
