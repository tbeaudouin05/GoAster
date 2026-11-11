# GoAster deployment

Deploy a new version of GoAster by building and testing the binaries, installing them, and restarting the services. GoAster owns its runtime after installation and does not depend on GoAsterWeb for later deployments.

1. Read the GoAster README for the standard installation layout. Verify the actual binary paths and service configuration before deploying.
2. Identify the source revision to deploy.
3. Build and test the new binaries. Stop and report any failure.
4. Safely replace the installed binaries in place.
5. Arrange for the restart request to finish independently of the agent process being stopped.
6. Warn the user that the restart will interrupt the conversation and that they must send a new message afterward to request verification.
7. If both services are installed, request their restart together: systemctl restart goaster-agent.service goaster-telegram.service. If Telegram is not installed, restart only the agent service.
8. When the user asks for verification after the restart, check the service status and confirm that the running binaries match the tested deployment. Report any failure or uncertainty.

Use the read-write-temp-files skill for temporary files. Do not add automatic rollback or recovery mechanisms. Report exceptional cases clearly for manual follow-up.
