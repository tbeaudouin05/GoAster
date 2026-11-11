# Observed GoAsterWeb installation integration

This page describes the current GoAsterWeb implementation for installing GoAster on a new
Hetzner server running Ubuntu 24.04. It describes a GoAsterWeb integration;
it is not a universal GoAster standard and does not describe a GoAster-owned
installer. Verify these details on the target host because they are GoAsterWeb
implementation details.

## Observed details

The observed GoAsterWeb setup has the following characteristics:

- GoAsterWeb `prepare_remote.sh` generates `goaster-agent.service` and builds
  the binaries.
- Telegram setup generates `goaster-telegram.service` and requires the agent.
- The agent binary is `/usr/local/bin/goaster-agent`.
- The Telegram binary is `/opt/goasterweb/bin/goaster-telegram`.
- The source is `/opt/goasterweb/source`.
- The agent socket is `/run/goasterweb/agent.sock`.
- Both services currently run as `root`.

These are observations of the current GoAsterWeb implementation, not a
supported interface or a promise that another host has the same layout.

## Upgrade and restore status

There is currently no supported upgrade process or tested restore process for
this integration. The GoAster repository does not provide the GoAsterWeb
service units or installer. Any change therefore needs a reviewed procedure
based on the actual target host configuration; do not infer paths, service
ownership, restart order, or recovery commands from this page.

## Proposed design and open decisions

A future supported process could establish a host-specific contract that:

- records the actual service definitions, dependencies, paths, permissions,
  and persistent data before a change;
- builds and verifies both binaries, retains their exact checksums, and stages
  them through an approved deployment plan;
- requires an approved and verified Hetzner snapshot before deployment, with a
  separate approval for the deploy; snapshot approval alone would not
  authorize deployment;
- defines manual recovery and verifies it with a tested restore procedure
  before calling upgrades supported.

Open decisions include ownership of the installer and service units, whether
the current root execution model should remain, the stable release and
rollback mechanism, the agent/Telegram restart and health-check contract, and
which on-host and external data a snapshot and restore must cover.
