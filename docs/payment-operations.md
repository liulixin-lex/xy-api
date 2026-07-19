# Payment Gateway Operations and Security

## Background

Payment callbacks, retries, credential rotation, refunds, disputes, and legacy
orders all cross process and database boundaries. A payment integration is safe
only when the provider evidence and the local entitlement change are both
durable, idempotent, and auditable.

This document describes the operational contract for the canonical payment
flow and the Epay, Stripe, and XORPay gateways. Creem, Waffo Pancake, and Waffo
remain outside this migration scope.

## Goals

- Keep provider credentials and payment authority on the server.
- Price every purchase from an immutable server-side quote.
- Apply each verified provider event and entitlement change exactly once.
- Preserve evidence for mismatches, credential incidents, refunds, and disputes.
- Support SQLite, MySQL, and PostgreSQL upgrades from the v0.1.6 schema.
- Give administrators an explicit, audited recovery path instead of requiring
  direct database edits.

## Architecture

The canonical flow is:

1. The authenticated user requests a short-lived server quote.
2. The server freezes price, currency, payment method, entitlement, and expiry.
3. A client request ID consumes the quote and creates one canonical order.
4. The provider adapter creates or recovers the provider payment.
5. The signed webhook is normalized into the durable event inbox.
6. The event, order state, entitlement, projection, ledger, affiliate reward,
   debt, and reversal are committed in one primary-database transaction.
7. Contract conflicts remain visible in Payment Operations for explicit review.

`TopUp` and `SubscriptionOrder` are compatibility projections. They are not
the authority for new payment settlement.

## Required Configuration

### Dedicated payment encryption key

Set `PAYMENT_SECRET_KEY` to a strong random value of at least 32 characters
before saving any payment credential. Every application node must use the same
value.

For a planned key rotation:

1. Put the old value in `PAYMENT_SECRET_KEY_PREVIOUS`.
2. Put the new value in `PAYMENT_SECRET_KEY` on every node.
3. Restart or reload every node and verify payment readiness.
4. Confirm stored secrets and pending start payloads were re-encrypted.
5. Remove `PAYMENT_SECRET_KEY_PREVIOUS` from every node.

Never rotate only one node. Never log, commit, export, or paste either key into
an administrator reason field.

### Dedicated HTTPS callback origin

Set `CustomCallbackAddress` to the public HTTPS origin that receives payment
webhooks and browser returns. Payment providers do not fall back to
`ServerAddress`. Local HTTP is accepted only for loopback development.

The public routes are:

- Epay: `/api/payment/epay/notify`
- Stripe: `/api/stripe/webhook`
- XORPay: `/api/xorpay/notify`

The reverse proxy must preserve the raw Stripe request body and the
`Stripe-Signature` header. Apply normal edge-level request limits, but do not
rewrite form fields or webhook payloads.

### Multi-node deployments

All nodes must share the primary database, payment encryption keys, and the
same provider configuration. Payment configuration changes use a database CAS
version and credential-generation fence; a node that cannot synchronize the
current version must refuse payment creation or settlement rather than use
stale credentials.

PostgreSQL master nodes serialize schema migration with a connection-scoped
advisory lock. This prevents two instances starting against the same empty or
legacy database from racing while creating payment tables and compatibility
columns. The lock does not replace the normal requirement to run production
upgrades from a tested database backup.

### Trusted reverse proxies

Client IP addresses are part of privileged payment-operation evidence and are
also used by request limits. Forwarded IP headers are ignored by default. If an
HTTPS reverse proxy sits directly in front of the application, set
`TRUSTED_PROXY_CIDRS` to that proxy network only. Every entry must be a CIDR;
global ranges such as `0.0.0.0/0` and `::/0` are rejected at startup. Never add
an end-user or public network merely to make a forwarded header appear in logs.

## Gateway Rules

### Epay

- The upstream endpoint must use HTTPS outside loopback development.
- Canonical Epay payments use CNY.
- Merchant ID and key changes are atomic and generation-scoped.
- The MD5 signature is required by the Epay protocol. Verification uses the
  signed amount, order number, provider trade number, merchant ID, method, and
  status before any local state transition.
- Provider trade numbers are namespaced by credential generation so two
  merchants cannot claim the same raw trade number after a rotation.
- Previous credentials are accepted only for orders created inside the
  configured overlap window.

### Stripe

- Stripe test credentials are disabled by default. Set
  `PAYMENT_STRIPE_TEST_MODE_ENABLED=true` on every node only in a sandbox that
  has an isolated database, users, quota, webhook endpoint, and Stripe test
  account. Never enable it in an environment that contains production
  entitlements. Readiness reports whether the switch is enabled and whether a
  saved test credential is currently blocked. The initial administrator
  options expose the read-only keys
  `payment_setting.stripe_test_mode_enabled`,
  `payment_setting.stripe_test_mode_blocked`, and
  `payment_setting.stripe_test_mode_isolation_required`; clients cannot mutate
  these environment-derived values.
- Turning the switch off immediately blocks new test-mode Checkout creation,
  queries, recovery, legacy adoption, and paid fulfillment. A verified pending
  test payment is retained in manual review and grants no quota or subscription.
  Verified refunds and disputes for test payments that were legitimately
  fulfilled while the sandbox was enabled remain processable so economic
  recovery cannot be disabled accidentally.
- One-time top-ups and application subscription entitlements use Stripe-hosted
  Checkout Sessions in `payment` mode. They are not recurring Stripe Billing
  subscriptions.
- The server quote controls the amount. The configured active Stripe Price is
  used to preflight product, currency, account, mode, and read permission; the
  Checkout Session creates server-controlled price data for the exact quote.
- API credential mode, authenticating account, optional connected account,
  webhook mode, and configuration fingerprint must agree.
- Before any one-time paid Checkout webhook can grant an entitlement, the
  server re-fetches that exact Session with the currently verified API key and
  connected-account context. Session identity, trade number and metadata,
  payment mode and status, amount, currency, livemode, PaymentIntent, and
  Customer must all match the signed event. A lookup failure is retryable and
  grants nothing.
- If a connected account is configured, Checkout and queries use that account
  consistently and webhook `account` must match it.
- Enable these webhook events:
  - `checkout.session.completed`
  - `checkout.session.async_payment_succeeded`
  - `checkout.session.async_payment_failed`
  - `checkout.session.expired`
  - `charge.refunded`
  - `charge.dispute.created`
  - `charge.dispute.closed`
- Keep test and live deployments isolated. Do not reuse one database while
  switching between unrelated Stripe accounts or modes; account and customer
  history is intentionally retained as payment authority.
- Saving Stripe settings verifies the Price and performs a fresh authenticated
  write probe by expiring a deterministic nonexistent Checkout Session ID. The
  probe deliberately sends no Stripe idempotency key, so a later verification
  rechecks current permissions instead of replaying a cached failure. An exact
  `resource_missing` response proves that the key reached the Checkout Sessions
  write operation without creating a payment resource, while restricted keys
  without Checkout write permission are rejected. Still complete one test-mode
  Checkout to verify the end-to-end webhook, refund, and dispute path.
- Retire a deleted Stripe Customer only through Payment Operations. Retirement
  preserves immutable ownership evidence so callbacks for older Checkout
  Sessions remain verifiable while new checkouts can bind a new Customer.

The repository pins `stripe-go` in `go.mod`; that SDK release carries its API
version. Upgrade it through a reviewed dependency change and rerun webhook
fixtures before changing the Stripe webhook endpoint version.

### XORPay

- The server talks only to the fixed `https://xorpay.com` origin with TLS 1.2+
  and redirects disabled.
- Canonical XORPay payments use CNY and the documented native or Alipay method.
- Create and callback signatures use the exact field order defined by XORPay.
- `order_id` is the local canonical trade number; `aoid` is the provider order
  authority used for query and reconciliation.
- Previous credentials are accepted only for their bound order generation and
  overlap window.
- XORPay can report whether an `aoid` was paid, but its public query API cannot
  recover QR content after the create response is lost. In that case the order
  remains `state_unknown` and is reconciled by query, webhook, or expiry. Do not
  create a second upstream order with the same business request.

## Browser Traffic and Secret Exposure

Payment security does not rely on JavaScript obfuscation or hiding network
requests. A user can inspect every browser request. Browser-visible Epay form
fields, Stripe Checkout URLs, XORPay QR content, trade numbers, and one-time
signatures must therefore be treated as public order data.

Provider secrets, webhook secrets, encryption keys, pricing snapshots, and raw
verified webhook bodies stay server-side. TLS, signature verification,
server-side quotes, exact contract checks, idempotency, and the event ledger are
the security boundary. No frontend change can make a leaked provider secret
safe; revoke and rotate it immediately.

## Idempotency, Limits, and Retention

- New clients must send a stable request ID for payment start.
- Reusing the same request ID with the same contract returns the original order;
  reusing it with a different contract is rejected.
- Quote and start endpoints are rate-limited by authenticated user ID.
- Preview and legacy amount endpoints use bounded request bodies and the quote
  rate limit.
- Each user has bounded active quotes and in-flight provider orders.
- The scheduled payment task expires unattended orders and deletes expired or
  consumed quotes after the audit-retention window in bounded batches.
- Late verified payment is still retained and settled or reviewed according to
  the immutable order contract; expiry never discards provider evidence.

## Credential Incident Response

Emergency revocation is intentionally conservative:

1. Atomically replace or remove the compromised credential and revoke its
   generation.
2. Pending orders and unmatched monetary events from that generation become
   terminal review evidence.
3. Already fulfilled orders keep their economic projection. They are marked as
   credential incidents but are not automatically reversed.
4. Review provider dashboards and local event/ledger evidence for the affected
   generation and time window.
5. Acknowledge the incident with an investigation note, then resolve it only
   after refunds, disputes, or manual actions are complete.

Automatically reversing fulfilled orders is unsafe because revocation alone
cannot prove which earlier signed events were fraudulent. Economic corrections
must use a verified refund/dispute event or an explicit audited administrator
action.

Administrator quota add, subtract, and override operations are financial
mutations. They require the dedicated Payment Operations permission, a browser
dashboard session, and a security verification completed within five minutes.
API access tokens cannot perform them. The database value is authoritative and
the user quota cache is invalidated after every successful adjustment.

## Legacy Upgrade Handling

Legacy Epay rows without a canonical order are adopted only after a valid paid
callback. The signed method, CNY amount, provider identity, credential overlap,
legacy record, and current immutable entitlement snapshot must all match.

Pre-canonical top-up rows do not contain the original quota-per-unit or granted
quota snapshot. Even when their signed amount matches, they remain in durable
manual review and the server never recalculates their entitlement from the
current mutable quota setting. Payment Operations exposes a dedicated action
only when the stored Epay event, credential generation, provider identity,
method, amount, currency, pending legacy row, payload digest, and concurrency
version are still intact. An administrator must then choose exactly one terminal
outcome:

- Fulfill with an explicit quota in the supported 32-bit range. The quota,
  original amount, payload digest, provider identities, missing-snapshot marker,
  and administrator evidence are frozen into immutable accounting history. This
  recovery does not issue a historical affiliate reward because that economic
  contract cannot be reconstructed safely.
- Confirm an external provider refund with its provider reference. No
  entitlement is granted; the canonical order, legacy projection, event,
  refund ledger, and administrator audit become terminal together.

The two outcomes share one compare-and-swap state transition, so concurrent or
modified retries cannot fulfill and refund the same legacy payment.

An old subscription row that predates snapshot columns can be adopted
automatically only when the plan still exists, was not updated after the order
was created, and its price exactly matches the legacy amount. Otherwise the
event remains durable for administrator review or refund; it must never grant
the current plan silently.

Back up the primary database before upgrading. Validate the migration on a copy
of production data before a stable release, especially pending Epay orders,
Stripe legacy subscription inventory, payment customer bindings, and open
billing reservations.

## Acceptance Checklist

- Payment readiness reports no missing encryption key, callback origin, mode,
  account, credential, currency, or Stripe verification fingerprint.
- In a dedicated sandbox with `PAYMENT_STRIPE_TEST_MODE_ENABLED=true`, Stripe
  test-mode payments, plus controlled low-value Epay and XORPay merchant
  verification where available, create one local and one upstream order for
  repeated client retries. With the switch absent or false, test-mode paid
  events must remain in manual review and grant nothing.
- Valid webhooks settle once; modified amount, currency, method, account,
  customer, event key, or credential generation moves to durable review.
- Refund and dispute tests reverse only previously granted entitlement and
  never produce negative or overflowing quota.
- Credential replacement, overlap, emergency revocation, incident review, and
  Stripe Customer retirement are exercised with administrator step-up.
- SQLite, MySQL, and PostgreSQL migrations and settlement transactions pass.
- Both frontend themes build and Payment Operations is usable at desktop and
  mobile widths.

## Known Operational Limits

- Stripe endpoint permission probing cannot prove external webhook delivery;
  complete a test-mode payment and callback exercise before accepting traffic.
- XORPay cannot recover a lost create-response QR through its public query API.
- Legacy clients that omit request IDs cannot guarantee cross-request
  idempotency; upgrade them to the canonical quote/start API.
- Production-data-copy validation remains a release gate even when clean and
  synthetic upgrade databases pass.
