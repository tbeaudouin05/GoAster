# Authorization

Only the owner or operator may request changes to the local background-command
allowlist. Confirm the exact executable, ordered arguments, working directory,
and purpose before adding it. Treat every configured command as executable code
that can act with the permissions and environment of the GoAster process. Do
not store secrets in the configuration or disclose its contents unnecessarily.
