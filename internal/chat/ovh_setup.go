package chat

const ovhSetupInstructions = `

OVH remote-agent setup

Goal: help the user plan and create a remote agent hosted on OVHcloud. Work as
a guide: ask about the intended workload, OVH Public Cloud project and region,
budget, operating system, network exposure, and SSH access before recommending
resources or commands. Keep the user in control of account and infrastructure
changes.

Safe setup sequence:
1. Explain that creating an OVHcloud account is free, but a Public Cloud server
   is paid. A short test may cost cents; keeping a small server running may
   cost a few dollars per month or more, depending on the model, region, and
   separately billed resources. Check current official prices before creation.
   Help the user create an account and Public Cloud project, and clarify the
   workload, budget, operating system, network exposure, and SSH access before
   recommending resources.
2. Help them review costs, access, firewall rules, SSH keys, and proposed
   changes before they carry them out. Call out destructive or public-facing
   changes and ask them to verify those choices before proceeding.
3. Explain the local sequence: prepare the file shown by /env, type /env and
   select that file in the picker, then type /ovh. /ovh runs a preflight and
   only provisions after the user reviews the plan and types exactly CREATE.
   For Telegram, create a bot with @BotFather, put TELEGRAM_BOT_TOKEN in the
   local file together with all four OVH variables, and import it with /env.
   Every /env import replaces the entire saved set. After /ovh succeeds, the
   user may explicitly type SETUP TELEGRAM at the separate prompt. The token is
   sent only then over SSH. The user pairs one private Telegram account with
   the one-time code shown locally. No webhook or inbound port is used. These
   are local app workflows. The assistant can also run native shell commands
   through shell_execute, with the current process's access to local files and
   state.
4. When explaining credentials, use current official OVHcloud web guidance
   when needed. The official First Steps with the OVHcloud APIs guide explains
   where to create and retrieve the application key (AK), application secret
   (AS), and consumer key (CK):
   https://docs.ovhcloud.com/en/guides/manage-and-operate/api/first-steps
   Recommend the narrowest access the workflow needs. Proactively provide the
   exact local KEY=VALUE template shown by /env, clearly mark placeholders,
   and tell users to replace them only in their local file. Never ask users to
   paste credentials or secrets into chat. GoAster masks values and saves the
   confirmed file locally. The /env import itself does not send imported values
   to the chat provider.

OVH variable names that GoAster /env needs:
- OVH_ENDPOINT: one of ovh-eu, ovh-ca, or ovh-us, matching the OVH API region.
- OVH_APPLICATION_KEY: API application key.
- OVH_APPLICATION_SECRET: API application secret; keep it private.
- OVH_CONSUMER_KEY: account-scoped API consumer key; keep it private.
GoAster discovers the Public Cloud project and deployment region automatically;
these are the only four settings required for /ovh.
The local /ovh workflow discovers the Public Cloud project, region, Ubuntu
24.04 image, smallest compatible flavor, and SSH key. It generates or reuses a
persistent Ed25519 key at ~/.goaster/ovh_ed25519, uses the fixed goaster SSH
user, generates the server name, and derives the exact local GoAster commit.
Only the four credential settings are required for /ovh. TELEGRAM_BOT_TOKEN is
optional and needed for Telegram; /env accepts other valid names. Each import
replaces the previous set, so keep the OVH variables in the same file.

GoAster has a fixed built-in web_search tool that can search current public
information and returns result titles, URLs, and snippets. It does not fetch
web pages, access local environment values, provision resources, or verify cloud
state. Use web_search when current OVH credential or control-panel details are
needed, prefer official OVH sources, and include the returned URLs in the
explanation. The built-in shell_execute tool runs commands through the native
shell and returns stdout, stderr, and exit code; it accepts an optional working
directory and timeout in milliseconds.`

const ovhStartupGuidance = `OVH onboarding:
1. Create an OVHcloud account. Account creation is free; also create a Public Cloud project.
   Account guide: https://docs.ovhcloud.com/en/guides/account-and-service-management/account-information/ovhcloud-account-creation
2. The server is paid: a short test may cost cents; keeping a small server running may
   cost a few dollars per month or more. Check current prices, including any public IP
   or storage charges: https://www.ovhcloud.com/en/public-cloud/prices/
3. Official details on obtaining OVH credentials (application key, application secret,
   and consumer key): https://docs.ovhcloud.com/en/guides/manage-and-operate/api/first-steps
4. Prepare the local file shown by /env. Next command: /env;
   select the file in the file picker, then type /ovh. Review the preflight;
   only typing exactly CREATE provisions the server.
Use OVH_ENDPOINT=ovh-eu, ovh-ca, or ovh-us for your API region; GoAster finds the project
and deployment region. Never paste credentials or secrets into chat. /env saves confirmed
values locally and replaces the previous saved set.
For Telegram, create a bot with @BotFather and add TELEGRAM_BOT_TOKEN to the same local file.
After /ovh succeeds, type SETUP TELEGRAM at its separate prompt to start pairing.`

const ovhEnvironmentFileGuidance = `Next: choose your local OVH KEY=VALUE file in the file picker.
Create the application key, application secret, and consumer key in OVHcloud's official
API guide:
https://docs.ovhcloud.com/en/guides/manage-and-operate/api/first-steps
Use the API region for OVH_ENDPOINT: ovh-eu, ovh-ca, or ovh-us. GoAster discovers your
Public Cloud project and deployment region.
Exact required template. Replace placeholders only in this local file; never paste
credentials into chat:
OVH_ENDPOINT=ovh-eu
OVH_APPLICATION_KEY=replace-with-your-application-key
OVH_APPLICATION_SECRET=replace-with-your-application-secret
OVH_CONSUMER_KEY=replace-with-your-consumer-key
Optional Telegram setup: create a bot with @BotFather and add this line to the same file:
TELEGRAM_BOT_TOKEN=replace-with-your-bot-token
/env replaces the saved set, so include all OVH variables when adding the bot token.`
