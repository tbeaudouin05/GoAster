---
name: internet-fetch
description: Retrieve public internet content reliably with bounded retries and endpoint-specific network-path checks.
---

# Internet fetch

This is an instruction skill for the GoAster agent, not executable code. It
guides read-only retrieval of public internet content and does not change a
client's networking defaults. Apply it when an ordinary-language request
requires public internet retrieval; do not require a skill name or slash
command.

## Retrieval workflow

### Apply an explicit address-family requirement first

Before using the generic workflow below, check whether the endpoint or task
has an explicit address-family requirement. If one applies, use that family
from the first request and on every request in its approved redirect chain;
this takes precedence over normal-first retrieval and diagnostic fallback.
For the official pinned Go archive bootstrap in the OVH remote skill, use
per-request IPv4 from the initial archive request through each validated HTTPS
redirect and any separately retrieved official checksum. Never make a
dual-stack first request or switch a redirected request to a default client
for this workaround. Keep the existing timeout, retry, redirect, destination,
credential, and integrity safeguards. Validate redirect destinations as
public before requesting them, preserve the hostname and TLS checks, do not
downgrade HTTPS, and never forward credentials across hosts.

### Make the normal request first when no address-family requirement applies

When no endpoint-specific or task-required address-family selection applies,
use the ordinary, approved retrieval tool with the exact URL from the most
authoritative source available. Preserve the original hostname, path, query,
and HTTPS certificate checks. Record the requested URL, final URL, status,
error, and whether the body is complete and plausible. A timeout, connection
reset, incomplete body, or HTTP error is not a successful retrieval.

Do not force an address family for this generic first attempt. A normal
request provides the baseline for deciding whether a later result differs by
network path.

### Retry only transient failures, within a fixed limit

For an idempotent, read-only GET, retry transient DNS errors, connection
resets, timeouts, and temporary server errors at most twice after the initial
attempt. Use short backoff (about one second, then two seconds), and honor a
`Retry-After` delay only up to ten seconds. Keep each attempt and the overall
operation within the retrieval tool's timeout budget. Do not retry a write or
other side effect.

Do not repeat stable 400, 401, 403, or 404 responses just to get a different
answer. A 404 may be genuine; use the endpoint-specific checks below only when
there is concrete evidence that the response is anomalous.

### Use targeted IPv4 fallback only for a suspicious endpoint when no requirement applies

When no endpoint-specific or task-required address-family selection applies,
consider one IPv4-only request only when the exact public endpoint has
evidence of a path-specific anomaly, for example:

- an official index or manifest lists the exact resource, but its URL returns
  an unexpected 404 or an implausible response;
- results from the same exact URL conflict across known-good network paths; or
- diagnostics show that the failing request used IPv6 and a related request to
  the same service succeeds.

An arbitrary 404, an unfamiliar site, or a general outage is not enough. After
the normal request and any allowed transient retries, make at most one
IPv4-only probe for the same exact hostname and URL. Use a per-request option
such as `curl -4` when an authorized, suitable client is available. For
example, a bounded one-shot HTTPS check can use:

```sh
curl --ipv4 --connect-timeout 5 --max-time 15 --retry 0 --proto '=https' 'https://example.org/path'
```

Keep the URL hostname so TLS SNI, certificate validation, and virtual hosting
continue to work. Never replace the hostname with a guessed IP, disable
certificate validation, or use IPv4 fallback to reach a private, loopback,
link-local, metadata, or otherwise prohibited address. If the available fetch
tool cannot select IPv4 and no authorized per-request client is available,
say that the fallback could not be attempted; do not claim it was tested.
Before using an alternate client, follow the active tool's safety rules and
confirm that the resolved destination is public. Do not use a shell client to
bypass a fetch tool's destination restrictions.

IPv4 selection must apply only to this one request. Never change system or
application network settings, resolver configuration, environment defaults,
or client-wide defaults to force IPv4. Do not make an IPv4 retry across an
unrelated host merely because the original URL redirected there.

## Redirects, timeouts, and response handling

Keep a finite total timeout and a finite connection timeout; never wait
indefinitely. Limit redirects to five and allow only HTTP or HTTPS. Do not
follow an HTTPS-to-HTTP downgrade. Inspect each redirect target and validate a
new host as a public endpoint before requesting it. If a redirect changes
host, handle it as a separate request; never forward credentials, cookies, or
other authorization across hosts. Do not use a redirect option that forwards
credentials to another host. Redact sensitive query values from reports.

A timeout, DNS failure, TLS error, reset, truncated body, or malformed response
means retrieval is incomplete or uncertain, not that the resource is missing.
When the endpoint returns a status such as 404, distinguish that observed
status from a confirmed missing resource. Check the official listing, release
metadata, or checksum when available before concluding the resource is gone.

## Diagnose network-path artifacts clearly

Compare the exact requested URL, address family when observable, final URL,
status, response headers, and a small body excerpt from each path. Confirm a
successful artifact against its official index or checksum when available.
If IPv4 succeeds where the normal request returned an anomalous 404, report a
likely network-path or edge-routing artifact and name the differing results.
Do not claim a root cause from the status mismatch alone. If both paths return
the same 404 and the authoritative listing agrees, report that the resource
appears unavailable. If evidence is mixed, say the result is inconclusive.

When comparing an archive download, check the exact version and archive name
against the official release listing and checksum. A difference between paths
is evidence to investigate, not proof that every 404 has the same cause.
