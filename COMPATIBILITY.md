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

1. The existing `v0.1.6` tag, release assets, and published images are immutable.
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
- amd64 and arm64 images build and pass startup smoke tests.
- `/api/status`, login, token creation, model listing, normal relay, streaming,
  quota charging, recharge, and subscription flows are verified.
- SQLite, MySQL, PostgreSQL, Redis-enabled, and Redis-disabled startup paths are
  verified using the `v0.1.6` schema contract.
- Release notes describe compatibility, backup requirements, upgrade scope,
  rollback boundaries, and known limitations.
- No stable tag, release asset, or image tag is overwritten.

## Pending Verification

- Production-data-copy verification has not yet been performed for `v0.2.0`.
- A stable `v0.2.0` release must not be published until the acceptance checks
  above are complete.
