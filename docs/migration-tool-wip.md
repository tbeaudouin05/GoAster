# Migration tool work in progress

Status: NOT READY TO MERGE OR DEPLOY. November 13, 2026.

Thomas approved replacing pure-skill provisioning with tested code, all compatible locations or a location allowlist, cheapest available compatible offer, and a 10% increase allowance on the approved total hourly price. The supplied HCLOUD_TOKEN determines the provider project; no separate project confirmation. The new bot token belongs to GoAsterWeb setup only.

This non-release branch preserves a partial source-host provisioning implementation. Full offline `go test -timeout 180s ./...` passed with Go 1.25.9, network module fetching disabled. Passing synthetic tests do NOT establish real provider compatibility, migration readiness, or live correctness.

Independent first review is BLOCKED. Unresolved findings include quote-to-start binding, decimal zero panic, stop/monitor race, endpoint pinning, pricing-type/create mismatch, discarded durable-write/diagnostic failures, tool argument previews, weak identifiers, and missing runtime integration coverage. The reviewer also disputed the availability schema; verify against current official Hetzner source before changing either schema or fixtures. No finding has been independently resolved in this branch.

Thomas subsequently asked whether GoAsterWeb should own provisioning, with GoAster making protected HTTPS requests. Implementation is paused pending that architecture decision; the question is not approval to modify or deploy GoAsterWeb. Do not merge this branch into main or run it against real credentials.

No live provider, GoAsterWeb, Telegram, or Redis migration request, billable server, deployment, service restart, destination SSH, or live migration test was performed. No new Telegram command was added. Existing services remain unchanged.
