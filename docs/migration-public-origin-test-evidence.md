# Migration public-origin test evidence

## Scope

Migration guidance and packaging checks use the fixed public service
`https://goasterweb-thomas.fly.dev/`. The skill prohibits deployment/address
questions and speculative readiness gates while retaining TLS verification,
stop/report on actual HTTPS failures, secret protections, create-once records,
new-bot identity, and private-conversation explicit approval for exact cost
terms.

## Offline validation

Verified using installed Go 1.25.9 explicitly because the default PATH Go is
older than this repository requires. All commands passed:

```sh
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local /usr/local/go/bin/go test ./...
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local /usr/local/go/bin/go vet ./...
sh packaging/test-skill-assets.sh
git diff --check
```

Packaging tests inspect canonical origin/service URL, prohibited deployment
and address questions, callback/owner URLs, actual-failure safeguards,
packaged skill content, and local Markdown links.

## Remaining gaps

No live GoAsterWeb, Redis, Telegram, SSH or Hetzner requests were made.
No provisioning, migration, restart or live conversational acceptance was
performed. Existing running GoAster instances do not receive this source
change until separately upgraded and their installed skill refreshed.
