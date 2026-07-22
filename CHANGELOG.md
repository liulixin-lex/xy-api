# Changelog

All notable changes to this maintained fork are documented here. Release notes
remain the authoritative record for published binaries and images.

## Unreleased

- No unreleased changes.

## v0.2.2 - 2026-07-22

### Payment experience

- Hardened payment settings against stale or failed configuration loads, kept
  field-level validation attached to the relevant control, and made payment
  operations loading, empty, unavailable, and retry states explicit.
- Refined Default and Classic payment administration for small screens without
  reducing operational detail. Classic provider panels now load on demand so
  hidden optional integrations cannot fail the whole settings page.
- Stabilized checkout branding while system information and logos load,
  improved WeChat action contrast, and stopped expiry timers after terminal
  order states.
- Removed the user-facing balance-confirmation explanation from payment result
  copy while preserving server-authoritative settlement behavior.

### Compatibility and verification

- Kept payment provider, callback, ledger, credential, idempotency, database,
  and multi-node contracts unchanged; this release has no database migration.
- Moved the multi-node smoke listeners out of the runner ephemeral-port range
  so release validation cannot collide with unrelated outbound connections.
- Verified both frontend themes with browser regression fixtures across desktop,
  mobile, light, dark, loading, success, empty, and failure states.
- Preserved exact-version publication and the existing global container
  `latest` policy.

## v0.2.1 - 2026-07-21

### Payments and Billing

- Moved Epay, Stripe, XORPay, Creem, Waffo, and Waffo Pancake payment creation
  behind durable database tasks with leases, fencing, provider-specific
  ambiguity recovery, local-only user polling, and reconciliation outside the
  browser request lifecycle.
- Added opaque public payment routes, products, and options so all gateway
  groups can coexist without exposing internal provider identifiers or mapping
  independent Epay, Stripe, Creem, Waffo, or Waffo Pancake channels to XORPay.
- Added merchant-configured single-payment and daily limits with integer minor
  units, timezone-aware day boundaries, active-order reservations, paid usage,
  expiry release, repair, and atomic multi-node enforcement.
- Routed new Creem, Waffo, and Waffo Pancake callbacks through canonical event,
  order, limit, and exactly-once settlement paths while retaining verified
  legacy fallback. Create-time amount and currency snapshots, upstream-order
  uniqueness, duplicate-event evidence, and manual review prevent guessed
  settlement of older incomplete records.
- Clarified Stripe semantics: current top-up and fixed-term entitlement
  purchases use one-time Checkout payment mode, while historical recurring
  subscriptions remain conditional, read-only administrator inventory.

### Checkout and Administration

- Added independent Default and Classic checkout pages with desktop, mobile,
  WeChat-browser, preparation, waiting, confirmation, success, expiry, and
  recovery states.
- Implemented XORPay Alipay face-to-face QR, validated mobile Alipay opening,
  WeChat Native QR, and one-use encrypted OpenID/JSAPI authorization without
  treating the browser bridge result as authoritative settlement.
- Removed internal provider, upstream status, credential generation, raw
  errors, and legacy Stripe inventory from canonical user payment APIs and
  views; all hosted URLs and signed form fields now stay behind authenticated
  no-store continuation endpoints. Compatibility endpoints return only the
  first-party order needed to enter the same local checkout page.
- Split administrator payment overview, routes, provider connections, device
  capabilities, limits, exceptions, rotation, and emergency operations into
  independently saved sections with single-owner notifications and impact
  previews.
- Completed payment and administrator copy for English, Simplified Chinese,
  Traditional Chinese, French, Japanese, Russian, and Vietnamese in both
  frontend themes.

### Security and Multi-node Operations

- Added payment-aware readiness, non-secret runtime fingerprints, shared
  configuration/key checks, database-backed maintenance leases, and fail-closed
  callback and worker behavior for inconsistent nodes.
- Preserved signed-event persistence, amount/currency/merchant/credential
  validation, exactly-once ledger settlement, credential rotation, emergency
  revocation, audit evidence, and manual-review controls.
- Added cross-database migration and concurrency coverage for SQLite, MySQL
  5.7, and PostgreSQL 9.6 payment contracts. Multi-node payment deployment
  requires shared MySQL or PostgreSQL plus shared Redis; SQLite remains
  single-node only.

### Verification Boundary

- Repository tests and protocol fixtures verify the implemented contracts.
  This release does not claim a real merchant charge for any configured
  payment integration, production XORPay JSAPI authorization, public callback
  delivery, refund, dispute, or payout unless separately recorded by an
  operator.

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
