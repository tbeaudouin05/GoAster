# Repository conventions

- Avoid pointers at all cost.
- If introducing a pointer is unavoidable, obtain explicit user confirmation before introducing it.

# Strict runtime error-logging rule

- At every runtime failure boundary, preserve the wrapped/joined cause and emit one clear, loud `ERROR`-level diagnostic to stderr (or the system journal) with the operation and safe status/model/backend context available there. Do not replace an underlying cause with a generic error before the diagnostic is written, and do not silently discard a runtime failure.
- Diagnostics must be bounded and safe: never log secrets, credentials, tokens, authorization headers, request bodies, prompts, model responses, tool output, arbitrary provider bodies, private content, secret-bearing files, or URLs containing credentials. Redact credential-shaped data and control/ANSI/newline injection before logging; retain only safe HTTP status, backend error type/code, request ID, and transport cause where available.
- Avoid duplicate per-layer diagnostics: the boundary with the complete cause and best context owns the `ERROR` record. Friendly user-facing messages may remain concise and must not be replaced by diagnostic detail.
- Expected user cancellation and normal EOF may remain unlogged; all other runtime failures require the `ERROR` diagnostic.
