# Changelog

All notable changes to this maintained fork are documented here. Release notes
remain the authoritative record for published binaries and images.

## Unreleased

- No unreleased changes.

## v0.2.0 - 2026-07-19

### Payments and Billing

- Added the XORPay gateway alongside Epay and Stripe, including native WeChat
  and Alipay face-to-face payments, signed callbacks, order queries, credential
  rotation, reconciliation, and manual-review handling.
- Rebuilt payment settlement around authoritative quotes, orders, events,
  immutable ledger entries, debt records, and exactly-once transaction fences.
- Hardened Epay and Stripe identity, amount, currency, environment, account,
  callback, refund, dispute, and legacy-order verification.
- Preserved late Epay/XORPay settlement across a bounded callback-origin
  recovery window and decoupled existing Stripe Session confirmation from
  mutable Price/catalog readiness while protecting API-key removal with
  durable-history and emergency-revocation gates.
- Added durable billing reservations and recovery for relay, task, video,
  Midjourney, Realtime, subscription, and wallet charging paths.

### Security and Operations

- Added encrypted payment credentials, configuration CAS, step-up verification,
  delegated permissions, audit trails, payment rate limits, and trusted-proxy
  enforcement.
- Hardened email rendering, outbound service origin pinning, download limits,
  quota overflow handling, Redis invalidation, and payment-debt account freezes.
- Added payment operations interfaces for reservations, legacy records, Stripe
  inventory, reconciliation, and administrative resolution.
- Made vet, race, Default locale, copyright, runtime, checksum, native
  multi-architecture startup, and signature checks blocking release gates;
  stable publication now occurs only after the complete draft is verified.
- Added a charset-aware MySQL 5.7 long-index capability gate, explicit InnoDB
  DYNAMIC table creation, and a real COMPACT clean/upgrade/log-database CI
  rehearsal without silent prefix indexes.
- Published the stable v0.2 container under its exact version tag and digest.
  Run-scoped candidate references are used only as pre-publication build,
  smoke, attestation, and signing intermediates; they are not deployment
  aliases. The global container `latest` alias remains on the prior line
  because direct upgrades from v0.1.7 through v0.1.14 are not supported.
- Made the repository Compose baseline fail closed on a digest-pinned v0.2
  image and independent strong database, Redis, session, crypto, and payment
  secrets; preflight rejects weak, reused, placeholder, or invalid rotation
  keys, and Redis/PostgreSQL plus optional service examples are digest-pinned.
- Required GitHub Immutable Releases so a published stable tag and asset set
  cannot be replaced or deleted after the final publication boundary.

### Frontend and Compatibility

- Added complete XORPay, QR payment, order tracking, subscription, billing
  history, and payment-operations flows to both Default and Classic frontends.
- Completed the new payment copy across all seven supported frontend locales.
- Preserved the supported v0.1.6 production configuration and database upgrade
  contract for SQLite, MySQL, and PostgreSQL. Direct upgrades from v0.1.7 through
  v0.1.14 remain outside the claimed compatibility scope.
- Added portable static Linux amd64 and arm64 release binaries and a reusable
  no-charge runtime acceptance smoke for login, relay, exact charging, and
  top-up, payment, and subscription read/fail-closed contracts. The arm64
  standalone binary now runs the same acceptance on a native arm64 runner.

### Repository Governance

- Established the `v0.2` compatibility baseline from `v0.1.6`.
- Added contribution, ownership, upstream synchronization, and Codex operations
  policies.
- Added dependency and security automation configuration.
- Began enforcing immutable GitHub Action references and stronger CI checks.

### Compatibility

- The `v0.1.6` tag, release assets, images, and historical branch remain
  unchanged.
- v0.2.0 migrations are additive and retain legacy payment and subscription
  records for reconciliation or explicit administrative resolution.

## v0.1.6 - 2026-07-07

- Production compatibility baseline for the `v0.2` development line.
- Full historical details remain available in the existing GitHub release and
  repository history.
