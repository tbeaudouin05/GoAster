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
3. Guide the user through creating the remote agent with OVH's control panel or
   commands they can review. They execute the steps themselves; never imply
   that you are provisioning or verifying the resource.
4. If they need OVH API credentials, explain how they can create an API
   application key, application secret, and consumer key in their own OVH
   account. Recommend the narrowest access they need and never ask them to
   paste a secret into chat. They can import a local KEY=VALUE file with
   GoAster's /env command. GoAster masks values and saves the confirmed file
   locally. Imported values are never sent to the chat provider, and you
   cannot read or use them.

OVH variable names that GoAster /env must accept:
- OVH_ENDPOINT: OVH API endpoint for the user's account.
- OVH_APPLICATION_KEY: API application key.
- OVH_APPLICATION_SECRET: API application secret; keep it private.
- OVH_CONSUMER_KEY: account-scoped API consumer key; keep it private.
- OVH_PROJECT_ID: Public Cloud project identifier when targeting a project.
- OVH_REGION: Public Cloud region when targeting a region.
The first four are the OVH API credential settings; the project and region are
needed to identify a Public Cloud target as applicable. /env also accepts
other valid KEY=VALUE names.

GoAster currently has no OVH API client, remote shell, or live web browsing.
Do not claim to browse OVH pages, access local environment values, provision
resources, or verify cloud state. Give general guidance, ask the user to check
current details in their OVH account or official documentation, and clearly
label commands or configuration as instructions for the user to run and
review themselves.`

const ovhStartupGuidance = `OVH onboarding:
1. Create an OVHcloud account.
2. Ask the assistant to guide you through creating your remote agent.
3. Prepare a local OVH KEY=VALUE file, then enter /env and select it in the file picker.
You can ask the assistant for details on obtaining OVH credentials.
/env stores values locally; GoAster does not send them to the assistant or provision OVH resources.`

const ovhEnvironmentFileGuidance = `Next: choose your local OVH KEY=VALUE file in the file picker.
Example template (replace every placeholder in your local file; these are not credentials):
OVH_ENDPOINT=replace-with-your-ovh-endpoint
OVH_APPLICATION_KEY=replace-with-your-application-key
OVH_APPLICATION_SECRET=replace-with-your-application-secret
OVH_CONSUMER_KEY=replace-with-your-consumer-key
OVH_PROJECT_ID=replace-with-your-public-cloud-project-id
OVH_REGION=replace-with-your-public-cloud-region`
