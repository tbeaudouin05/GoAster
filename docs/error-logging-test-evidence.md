# Runtime error-logging evidence

Final scoped review on 2026-11-13, from this repository root only. Read `agents.md`; the repository scan found no CLAUDE.md variants or additional agents.md variants.

All commands used the installed `/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.13.linux-amd64/bin/go`, with `GOTOOLCHAIN=local GOPROXY=off`. Tests used local servers and fake transports; no live provider, SSH, deployment, commit or push was used.

Final results after the fixes:

- `go test ./...` : exit 0, all packages passed (some cached).
- `go vet ./...` : exit 0, no output.
- `go test -race ./internal/diagnostics ./internal/chat ./internal/telegram ./cmd/goaster-agent ./cmd/goaster-telegram` : exit 0, all five packages passed, no race reports.
- `go test -race ./internal/chat -run TestCodexProviderRequiresStartupAuthenticationBeforeBackendRequest -count=5` : exit 0, five repetitions passed after making the response-body cancellation test deterministic.
- `go test ./internal/chat ./internal/diagnostics ./internal/telegram -run 'Test(RuntimeDiagnostics|RemoteMetadata|Error|Untrusted|SessionDiagnostic)'` : exit 0. Later full/race runs also cover the final edits.
- `git diff --check` : exit 0.

The final review retained numeric HTTP statuses, allowlisted backend type/code values, exact allowlisted fixed remote rejection messages, standard bounded request IDs (`req_`, `req-`, UUID), and model IDs resolved against the configured trusted registry. Known application errors explain missing credentials and common protocol failures. Typed JSON errors report invalid JSON/type mismatch and byte offsets; typed DNS/TLS/system causes explain transport failures without copying hosts, paths, URLs or arbitrary error strings. Unknown backend codes/messages and nonstandard IDs remain suppressed. No arbitrary response body, prompt, tool output or token-bearing URL is logged.

Regression coverage asserts concrete missing-credential, malformed credential JSON, DNS, HTTP/SSE rejection, request ID, configured model, and Telegram rejection diagnostics, plus credential/control injection suppression and one owning request boundary. Credential JSON parse causes now use `%w`. Startup errors preserve `errors.Is/As` causes behind safe messages; provider/session cancellation retains joined failure causes. Summary failures log at the owning boundary, including fallback, and Telegram formatting rejection emits an ERROR record before its existing fallback.

Intermediate failures were resolved, not omitted: an initial focused run caught token-bearing URL exposure in newly wrapped startup messages; safe startup error wrappers fixed it while preserving causes. The first focused race run failed the existing post-401 cancellation test without a data-race report: its socket reader could prefetch the canceling body before `Do` returned headers. That test now uses a fake transport to return headers before body cancellation, matching its intended boundary. Five race repetitions and the final five-package race run passed. An earlier compile failure during the method conversion was also corrected before the final checks.

Verdict: no unresolved security/correctness blocker found in the scoped logging diff. Unknown untrusted messages deliberately remain undisclosed. These offline checks do not verify a live journal, external services, every runtime error path or a race run of the entire repository.
