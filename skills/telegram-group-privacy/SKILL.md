---
name: telegram-group-privacy
description: Explain Telegram BotFather Group Privacy Mode and guide approved GoAster Telegram setup for ordinary group messages.
---

# Telegram BotFather Group Privacy Mode

This instruction-only skill explains how Group Privacy Mode affects Telegram
messages received by a GoAster bot. Use it whenever configuring Telegram for
GoAster or diagnosing missing messages in a group, including when the user
asks in ordinary language. Do not require a skill name or slash command to
select this workflow; `/setprivacy` remains an operational BotFather command.

## What Group Privacy Mode controls

Group Privacy Mode controls which group messages Telegram delivers to a bot.
It is enabled by default for bots added as regular group members; bots added
as group administrators receive all messages. With privacy mode enabled for a
regular member bot, it does not receive ordinary text messages. It receives
service messages, commands explicitly addressed to it (such as
`/command@botname`), general commands in some cases, messages sent through the
bot, and replies directed to it.

With privacy mode disabled, Telegram delivers all group messages to the bot
except messages sent by other bots. This lets GoAster receive ordinary text
messages, but it does not grant the bot administrator rights or change
GoAster's pairing authorization. The GoAster Telegram adapter forwards text
only from the Telegram account paired during setup; messages from other group
members and non-text messages are not forwarded to the agent.

## Explain the setting during setup

Whenever Telegram is configured for GoAster, explicitly mention this skill by
name: **Telegram BotFather Group Privacy Mode**. Explain that ordinary group
messages will not reach the bot while privacy mode is enabled. If the user
wants GoAster to receive ordinary text messages they send in a group, explain
that this requires turning Group Privacy Mode off and ask for explicit approval
before changing the setting. After approval, guide them to open `@BotFather`,
send `/setprivacy`, select the correct bot, and choose **Turn off**.

Changing privacy mode is optional. Before changing any BotFather setting,
state the exact setting and its effect, then obtain the user's explicit
approval. General approval to configure Telegram does not authorize changing
privacy mode or another BotFather setting. Telegram requires removing and
re-adding the bot for disabled privacy mode to take effect in an existing
group. Explain this membership change and obtain separate explicit approval
before doing it; the group administrator may need to carry out this step.

If privacy mode remains enabled, explain that users can still reach the bot
with commands or replies Telegram makes visible to it, but ordinary group text
will not be delivered. Telegram notes that changes can take a few minutes to
take effect.

## Protect the bot token

Never ask the user to paste a bot token into chat. Never reveal, quote, log,
screen-capture, place in a command argument, or include a token in a report or
repository file. Keep it in the approved protected local secret store used by
the setup workflow. If a token is exposed, stop and tell the user to revoke or
regenerate it with BotFather; do not repeat it.

## References

- [Telegram Bot Features: Privacy Mode and BotFather settings](https://core.telegram.org/bots/features#privacy-mode)
- [Telegram Bots FAQ: messages bots receive](https://core.telegram.org/bots/faq#what-messages-will-my-bot-get)
