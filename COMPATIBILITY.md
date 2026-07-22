# v0.2 Compatibility Policy

## Background

The `v0.2` development line starts from `xy-api` tag `v0.1.6`, commit
`2014e1d596a482beb10b1943aa292e1fa991f636`.

The channel-routing development introduced in `v0.1.7` through `v0.1.14` is
intentionally not part of this line. Existing New API and QuantumNous
attribution, licensing, and upstream history remain protected.

## Goal

`v0.2.0` must preserve the user-visible behavior and production configuration
contract of `v0.1.6` while providing a clean base for future development.

## Scope

The first repository-governance phase may change documentation, GitHub
workflows, repository policies, and release tooling. It must not change runtime
behavior.

The following areas are frozen during that phase:

- Backend runtime packages and database migrations.
- Authentication, billing, quota, channel selection, relay, and cache logic.
- Default and classic frontend application source.
- Go and frontend dependency manifests and lock files.
- Docker runtime arguments, Compose environment defaults, ports, volumes,
  database settings, and Redis settings.

## Compatibility Rules

1. The existing `v0.1.6` tag, release assets, and published images are the
   preservation baseline and must remain unchanged. The tag is repository-rule
   protected; historical asset checksums and image digests must be retained.
2. Changes from `v0.1.7` through `v0.1.14` must not be merged wholesale.
3. A later non-routing fix may be ported only after its dependencies and runtime
   impact are reviewed independently.
4. Every runtime change must include a regression test for the affected
   `v0.1.6` behavior.
5. Database changes must support SQLite, MySQL, and PostgreSQL and must be
   validated against a copy of production data before release.
6. `v0.2.0` supports upgrades from `v0.1.6` and clean installations. Direct
   upgrades from `v0.1.7` through `v0.1.14` are not claimed unless separately
   verified.

## Release Acceptance

A stable release requires all of the following:

- Backend tests and both frontend builds pass from a clean checkout.
- amd64 and arm64 images build and pass startup smoke tests; both standalone
  Linux binaries execute version checks and runtime acceptance on native
  architecture runners.
- `/api/status`, login, token creation, model listing, normal relay, streaming,
  quota charging, and no-charge top-up, payment, and subscription
  read/fail-closed probes are verified.
- SQLite, MySQL, PostgreSQL, Redis-enabled, and Redis-disabled startup paths are
  verified using the `v0.1.6` schema contract.
- Release notes describe compatibility, backup requirements, upgrade scope,
  rollback boundaries, and known limitations.
- No stable tag, release asset, or image tag is overwritten.
- GitHub Immutable Releases is enabled before a stable tag is created, and a
  public release is treated as an irreversible publication boundary.

## v0.2.0 Verification Baseline

The pre-release data and runtime rehearsal completed on 2026-07-19:

- A verified `v0.1.6` PostgreSQL 15 production backup was restored into an
  isolated database and migrated by `v0.2.0` without touching the source
  installation. The table set expanded additively from 35 to 50, the selected
  core-data digest remained identical, and user, option, setup, token, log,
  channel, top-up, redemption, and subscription row counts were preserved.
- The restored PostgreSQL copy passed repeated startup and two-node concurrent
  startup with Redis enabled. The original `v0.1.6` application, PostgreSQL,
  and Redis services remained healthy after the rehearsal.
- Independent `v0.1.6` upgrade fixtures passed on SQLite, MySQL 5.7.44, and
  PostgreSQL 9.6. Their selected core-data digests remained unchanged and all
  15 canonical payment and billing tables were added on each dialect.
- The versioned `v0.2.0` runtime passed initialization, login, token creation,
  model listing, normal relay, streaming relay, exact quota/log reconciliation,
  safe recharge probes, and subscription fail-closed checks on clean SQLite and
  on all three upgraded database fixtures. Redis-enabled and Redis-disabled
  paths were both exercised.

Publication remains conditional on the tag-triggered release workflow. It must
still pass the clean backend test and race suites, blocking vet and frontend
checks, native amd64/arm64 image startup, complete binary and Electron asset
checksums, multi-architecture manifest inspection, and Sigstore verification
before it can make the GitHub Release public. The workflow publishes the exact
`v0.2.0` container tag and digest, but leaves the global container `latest`
alias on the prior line while v0.1.7-v0.1.14 direct upgrades are unsupported.
Real merchant charges remain explicitly outside this release's asserted
verification scope.

The repository Compose baseline requires the operator to provide the verified
v0.2.0 GHCR digest and independent strong database, Redis, session, crypto, and
payment secrets. Its active helper images are digest-pinned and its preflight
container rejects weak, placeholder, reused, or invalid rotation keys before
data services start.
Run-scoped registry candidate references are release intermediates only; the
immutable Release manifest digest remains the deployment authority.

## v0.2.1 Source Candidate Boundary

The repository `VERSION` may identify the `v0.2.1` source candidate before a
tag, release assets, or container image exists. Publication and deployment
claims begin only after the protected release workflow produces and verifies
the immutable release artifacts.

The supported candidate scope is a clean installation or an additive upgrade
from `v0.2.0`, after backup and restore testing. A direct production upgrade
from `v0.1.6` to `v0.2.1` is not claimed by this release; use the supported
`v0.1.6` to `v0.2.0` path first, then rehearse the `v0.2.0` to `v0.2.1` step on
a restored backup. Direct production upgrades from `v0.1.7` through `v0.1.14`
also remain unclaimed unless separately verified.

The Compose health check uses `/api/readiness`, which is a `v0.2.1` runtime
contract. Operators must use the verified `v0.2.1` digest from the immutable
release manifest after publication; a `v0.2.0` image does not implement this
readiness endpoint and is not a compatible substitute for this Compose file.

The durable asynchronous task flow, opaque continuation contract, and merchant
limit reservation engine apply to new Epay, Stripe, XORPay, Creem, Waffo, and
Waffo Pancake orders. Each provider remains an independent gateway group;
Creem, Waffo, Waffo Pancake, Epay, and Stripe must never be described as XORPay
routes. Verified legacy callback fallback remains only for pre-existing records
that do not have a canonical payment order.
