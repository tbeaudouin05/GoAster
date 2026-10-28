---
name: ovh
description: Provision OVHcloud resources from the host with explicit user approval.
---

# OVH provisioning

This is an instruction skill for the GoAster agent, not executable code.

## Run host commands

Use GoAster's built-in `shell_execute` tool to create and run a temporary script on the host. Its input is a JSON object with required string `command` and optional string `cwd` and integer `timeout_ms` (omitted or zero means no timeout). `command` runs through the host's native shell. Use a platform-matched script (`.sh` for a POSIX shell, `.ps1` for PowerShell on Windows) in the host temporary directory; create, run, and remove it in one call when practical. Do not create provisioning files in the repository.

Read the complete result: `stdout`, `stderr`, and `exit_code`; an `execution_error` may also be present. Treat a nonzero exit or timeout as a failure or uncertain result, not success. After an uncertain create request, inspect OVH state before retrying so you do not create a second paid resource. Never print credentials or include them in script text, command arguments, or reports.

## Provisioning workflow

1. Clarify the OVH project, endpoint and region, workload, budget, operating system, access method, and required network exposure. Use credentials only from a user-approved local credential source already available to the host. If none is available, stop and explain how the user can configure one locally; never ask them to paste secrets into chat.
2. Use read-only checks to confirm the project, quota, available regions, images, sizes, and required network and SSH options. Resolve ambiguous choices with the user. Present the exact proposed resources and current price estimate before changing OVH state.
3. Before creating or changing any billable resource, deleting or replacing data or resources, or changing access or network exposure, explain the exact action, affected resources, costs or consequences, and obtain explicit user confirmation. Confirmation for one action does not authorize a different action.
4. After confirmation, run the requested OVH API or installed client steps through `shell_execute`. Wait for the resource to become ready, then verify its ID, state, address, and requested access or setup. Do not retry an uncertain mutation blindly or delete a partially provisioned resource without confirmation.
5. Remove temporary scripts when finished. Do not install tools or add repository dependencies as part of provisioning without the user's approval.

## Report

Give a concise result: project and region, resource IDs and selected sizes, estimated ongoing charges, verification status, and any failure or partial resource that remains. State clearly when a resource may still incur charges. Never claim success unless the host output and OVH state verify it; do not include secrets.
