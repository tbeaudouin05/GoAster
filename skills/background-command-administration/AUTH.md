# Authorization

Only the owner or operator may request changes to the local background-command
allowlist. Confirm the exact executable, ordered arguments, working directory,
purpose, and whether bounded input or a PTY is required before adding it.
Treat every configured command as executable code that can act with the
permissions and environment of the GoAster process. Interactive input is not a
separate authorization grant: it is available only for commands whose private
allowlist entry explicitly enables it. Do not store secrets in the
configuration or disclose its contents unnecessarily. Interactive input is
not secret-safe: arguments and PTY output may be visible to the model and
saved. Never enter passwords, passphrases, tokens, API keys, one-time codes,
private keys, or other secrets, and do not use this workflow to add arbitrary
login or authentication commands. The bundled `github-login` profile is the
only narrow exception; keep its fixed browser-login arguments, enter/return or
ctrl-c/c-c input policy, and output visibility unchanged. Its terminal output
may be read only to relay GitHub's official verification URL and one-time code;
never relay passwords, tokens, credentials, or other secrets.
