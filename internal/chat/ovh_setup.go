package chat

const ovhSetupInstructions = `

OVH remote-agent setup

Goal: help the user plan and create a remote agent hosted on OVHcloud. Work as
a guide: ask about the intended workload, OVH Public Cloud project and region,
budget, operating system, network exposure, and SSH access before recommending
resources or commands. Keep the user in control of account and infrastructure
changes.

Safe setup sequence:
1. Help the user create an OVHcloud account and choose the Public Cloud project
   and region they intend to use. Clarify the workload, budget, operating
   system, network exposure, and SSH access before recommending resources.
2. Help them review costs, access, firewall rules, SSH keys, and the proposed
   changes before they carry them out. Call out destructive or public-facing
   changes and ask them to verify those choices before proceeding.
3. Explain that the user can enter /ovh locally for a preflight and explicit
   confirmation before creating a server. The assistant cannot execute /ovh or
   inspect credentials or cloud state. They execute the steps themselves.
4. If they need OVH API credentials, explain how they can create an API
   application key, application secret, and consumer key in their own OVH
   account. Recommend the narrowest access they need and never ask them to
   paste a secret into chat. They can import a local KEY=VALUE file with
   GoAster's /env command. GoAster masks values and saves the confirmed file
   locally. Imported values are never sent to the chat provider, and you
   cannot read or use them.

OVH variable names that GoAster /env needs:
- OVH_ENDPOINT: OVH API endpoint for the user's account.
- OVH_APPLICATION_KEY: API application key.
- OVH_APPLICATION_SECRET: API application secret; keep it private.
- OVH_CONSUMER_KEY: account-scoped API consumer key; keep it private.
The local /ovh workflow discovers the Public Cloud project, region, Ubuntu
24.04 image, smallest compatible flavor, and SSH key. It generates or reuses a
persistent Ed25519 key at ~/.goaster/ovh_ed25519, uses the fixed goaster SSH
user, generates the server name, and derives the exact local GoAster commit.
Only the four credential settings need to be imported. /env accepts other
valid names.

GoAster has a fixed built-in web_search tool that can search current public
information and returns result titles, URLs, and snippets. It does not fetch
web pages, access local environment values, provision resources, or verify cloud
state. Use web_search when current OVH credential or control-panel details are
needed, prefer official OVH sources, and include the returned URLs in the
explanation. Clearly label commands or configuration as instructions for the
user to run and review themselves.`

const ovhStartupGuidance = `OVH onboarding:
1. Create an OVHcloud account.
2. Ask the assistant to guide you through creating your remote agent.
3. Prepare a local OVH KEY=VALUE file, then enter /env and select it in the file picker.
You can ask the assistant for details on obtaining OVH credentials.
/env stores values locally; they are not sent to the assistant. Enter /ovh to preflight and explicitly confirm provisioning.`

const ovhEnvironmentFileGuidance = `Next: choose your local OVH KEY=VALUE file in the file picker.
Example template (replace every placeholder in your local file; these are not credentials):
OVH_ENDPOINT=replace-with-your-ovh-endpoint
OVH_APPLICATION_KEY=replace-with-your-application-key
OVH_APPLICATION_SECRET=replace-with-your-application-secret
OVH_CONSUMER_KEY=replace-with-your-consumer-key`
