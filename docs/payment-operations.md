# Payment Gateway Operations and Security

## Background

Payment callbacks, retries, credential rotation, refunds, disputes, and legacy
orders all cross process and database boundaries. A payment integration is safe
only when the provider evidence and the local entitlement change are both
durable, idempotent, and auditable.

This document describes the operational contract for the canonical payment
flow and the independently configured Epay, Stripe, XORPay, Creem, Waffo, and
Waffo Pancake gateways. More than one internal route may remain available for
compatibility or failover, but the public projection exposes only one Alipay
choice and one browser-appropriate WeChat Pay choice. A route is never silently
remapped to another provider.

## Goals

- Keep provider credentials and payment authority on the server.
- Price every purchase from an immutable server-side quote.
- Apply each verified provider event and entitlement change exactly once.
- Preserve evidence for mismatches, credential incidents, refunds, and disputes.
- Preserve SQLite, MySQL, and PostgreSQL legacy-schema compatibility for the
  staged `v0.1.6` to `v0.2.0` to `v0.2.1` path. A direct production
  `v0.1.6` to `v0.2.1` upgrade remains unclaimed.
- Give administrators an explicit, audited recovery path instead of requiring
  direct database edits.

## Architecture

The canonical flow is:

1. The authenticated user requests a short-lived server quote.
2. The server freezes price, currency, payment method, entitlement, and expiry.
3. A client request ID consumes the quote and creates one canonical order.
4. A durable database task creates or recovers the provider payment outside the
   browser request lifecycle, using a lease and fencing token.
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
Production validation rejects localhost names, literal private or IANA
special-purpose addresses, and ambiguous alternate IPv4 spellings even when
they are written with HTTPS. Public DNS resolution, certificate validity, and
external reachability remain operator gates.

The public routes are:

- Epay canonical: `/api/payment/epay/notify`
- Epay legacy top-up adoption: `/api/user/epay/notify`
- Epay legacy subscription adoption: `/api/subscription/epay/notify`
- Epay legacy subscription browser return:
  `/api/subscription/epay/return` (display-only, never settlement authority)
- Stripe: `/api/stripe/webhook`
- XORPay: `/api/xorpay/notify`
- Creem: `/api/creem/webhook`
- Waffo: `/api/waffo/webhook`
- Waffo Pancake: `/api/waffo-pancake/webhook/{test|prod}`

The reverse proxy must preserve the raw Stripe request body and the
`Stripe-Signature` header. Apply normal edge-level request limits, but do not
rewrite form fields or webhook payloads.

### Multi-node deployments

All nodes must share a MySQL or PostgreSQL primary database, Redis, payment
encryption keys, session and crypto keys, callback settings, and the same
provider configuration. SQLite is single-node only. Payment configuration
changes use a database CAS version and credential-generation fence; a node
that cannot synchronize the current version must leave readiness and refuse
payment creation or settlement rather than use stale credentials.

Multi-node payment mode is explicit and fail-closed. Set all of the following
identically on every replica except `NODE_NAME`:

- `PAYMENT_MULTI_NODE_ENABLED=true`;
- one stable `PAYMENT_CLUSTER_ID`;
- `PAYMENT_CLUSTER_NODES`, a comma-separated allowlist of every stable
  `NODE_NAME` that may join;
- `PAYMENT_CLUSTER_MIN_LIVE_NODES`, a strict majority of the allowlist and no
  greater than its size.

Every local `NODE_NAME` must be manually configured and present in the
allowlist. Unknown or duplicate live names are rejected. Requiring a strict
majority prevents two disjoint partitions from both serving payment. The
database live inventory must contain at least the configured minimum and every live member
must publish matching non-secret cluster, Redis-target, configuration, and key
fingerprints before any payment task is claimed. This makes an isolated
wrong-database replica fail closed even before it sees a peer. Use at least
three allowed replicas with a minimum of two when one-node restart tolerance
is required; a two-node deployment pauses payments while either node is down.

PostgreSQL and MySQL master nodes serialize schema migration with
connection-scoped locks. PostgreSQL uses an advisory lock; MySQL uses a
schema-scoped `GET_LOCK` name. In both cases the migration runs on the same
dedicated connection that owns the lock and releases it before returning that
connection to the pool. This prevents two instances starting against the same
empty or legacy schema from racing while creating payment tables and
compatibility columns. The lock does not replace the normal requirement to run
production upgrades from a tested database backup.

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
- A normal rotation retains the previous credential generation for 30 days
  plus 3 hours. This covers the two-hour local order lifetime, the full 30-day
  late-callback recovery window after expiry, and a one-hour clock and
  scheduling margin. Previous credentials remain eligible only for orders
  created no later than the rotation cutoff.
- Another normal Epay rotation is rejected while that overlap is active. An
  audited emergency revocation does not wait for the overlap: it immediately
  revokes the affected generation and routes any dependent evidence to the
  incident/manual-review flow instead of accepting another callback with it.

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
- Subscription plans keep their server-authoritative base price in USD. The
  Stripe unit-price multiplier converts that base amount into the configured
  Stripe settlement currency for both wallet top-ups and fixed-term plan
  purchases; it does not change the plan's base-currency contract.
- The server quote controls the amount. The configured active Stripe Price is
  used to preflight product, currency, account, mode, and read permission; the
  Checkout Session creates server-controlled price data for the exact quote.
- API credential mode, authenticating account, optional connected account,
  webhook mode, and configuration fingerprint must agree.
- Stripe-owned `*.stripe.com` Checkout hosts are always accepted. A merchant
  using a Stripe custom Checkout domain must add each exact DNS hostname to
  `StripeCheckoutAllowedHosts`; the option is empty by default and is included
  in both the verified Stripe configuration fingerprint and the multi-node
  runtime fingerprint. Wildcards, URLs, user information, explicit ports
  (including `:443`), IP addresses, `localhost`, non-DNS names, and oversized
  lists are rejected.
- Checkout creation, URL recovery, and the authenticated `/continue` handoff
  apply the same host policy. Adding exact hosts is safe while Stripe orders
  are unfinished. Removing or replacing a configured custom host is blocked
  until no unfinished Stripe order can depend on it, preventing an existing
  Checkout Session from becoming unreachable during payment.
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
- Configure the Stripe webhook endpoint API version as
  `2026-06-24.dahlia`, matching the pinned `stripe-go` SDK. Financial refund
  and dispute events from the v0.1.6 `acacia` train remain explicitly covered
  for upgrade compatibility. A validly signed refund or dispute from another
  train, a preview train, or an invalid/missing API version is retained with
  provider identities for manual review, but it cannot automatically reverse
  an entitlement. One-time Checkout payment events are separately confirmed
  by re-fetching the exact Session before settlement.
- Refund and dispute events select their order only through the immutable
  `ProviderPaymentKey`. Canonical Checkout orders bind this key to the
  PaymentIntent; mutable `trade_no` metadata and the Checkout/order identity
  cannot redirect a reversal. Missing bindings remain in manual review, and
  conflicting metadata is preserved as a mismatch rather than used as
  authority.
- Each Stripe dispute is tracked by its own `ProviderResourceKey`, derived from
  the Stripe dispute ID. Current exposure is the aggregate of the latest state
  of every dispute resource, so closing or winning one dispute cannot clear or
  overwrite another open dispute on the same payment.
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
- New purchases use one-time Checkout payment mode. Historical recurring
  subscriptions are an observation inventory and never extend local
  entitlement from renewal events. A positive-total paid legacy recurring
  Checkout is retained under its dedicated review classification and can be
  closed only after the inventory proves that future renewal is scheduled to
  stop or is already terminal and an administrator confirms a completed
  external full refund. The terminal transaction verifies the Session,
  subscription, trade, customer, amount, currency, mode, credential generation,
  and historical pending order, then records a refunded canonical order and
  zero-entitlement receipt/refund ledgers. Zero-total trial or promotion
  Checkouts remain inventory-only and are not eligible for that refund action.
  A Payment Operations administrator may
  schedule one verified legacy subscription to cancel at period end only after
  browser step-up verification. The action verifies the configured Stripe
  account and livemode, customer and subscription identity, uses a stable
  idempotency key, stores the authoritative Stripe response with the operator
  reason, and does not refund or mutate local quota.

The repository pins `stripe-go` in `go.mod`; that SDK release carries its API
version. Upgrade it through a reviewed dependency change, add the new release
train to the explicit financial-event compatibility policy only after its
Charge/Dispute contracts are covered, and rerun signed webhook fixtures before
changing the Stripe webhook endpoint version.

### XORPay

- The server talks only to the fixed `https://xorpay.com` origin with TLS 1.2+
  and redirects disabled.
- Canonical XORPay payments use CNY and the documented native or Alipay method.
- Create and callback signatures use the exact field order defined by XORPay.
- `order_id` is the local canonical trade number; `aoid` is the provider order
  authority used for query and reconciliation.
- A normal rotation retains the previous credential generation for 30 days
  plus 25 hours. This covers the maximum accepted 24-hour provider order
  lifetime, the full 30-day late-callback recovery window after expiry, and a
  one-hour clock and scheduling margin. Previous credentials remain eligible
  only for orders created no later than the rotation cutoff.
- Another normal XORPay rotation is rejected while that overlap is active. An
  audited emergency revocation immediately invalidates the affected
  generation and moves dependent evidence into the incident/manual-review
  path.
- XORPay can report whether an `aoid` was paid, but its public query API cannot
  recover QR content after the create response is lost. In that case the order
  remains `state_unknown` and is reconciled by query, webhook, or expiry. Do not
  create a second upstream order with the same business request.
- XORPay creation accepts both integer and quoted-decimal expiration hints. A
  malformed, empty, negative, overflowing, conflicting, or excessive hint is
  logged as a response-contract change and bounded by the local order expiry;
  it never discards an otherwise valid provider identity and payment
  instruction.
- Native WeChat protocol URLs, exact HTTPS Alipay QR URLs, and the exact
  `ipay.yltg.com.cn` XORPay-hosted payment origin are validated separately.
  The observed HTTP hosted-page contract is accepted only for WeChat Native;
  Alipay hosted pages must use HTTPS. Userinfo, ports, fragments, deceptive
  subdomains, dangerous schemes, and external redirect targets are rejected
  before instructions are persisted or rendered.

### Creem, Waffo, and Waffo Pancake

- New payments from all three integrations use the canonical quote, local
  order, limit reservation, durable worker, encrypted continuation, event
  inbox, and exactly-once settlement path. Provider-specific compatibility
  endpoints return only the local order needed to enter the first-party
  checkout page.
- Public product and payment-option identifiers are opaque. Real Creem product
  IDs, Waffo option values, Waffo Pancake product IDs, hosted URLs, and provider
  identity remain server-side.
- Creem accepts only its exact Checkout host. Waffo HTTPS cashier hosts and App
  schemes use separate exact-match administrator allowlists. Waffo Pancake
  accepts only the authenticated SDK Checkout URL shape and keeps its JWT in
  the encrypted redirect fragment.
- Waffo uses a deterministic payment request ID and performs repeated inquiry
  before any create retry. If an existing Waffo order has no recoverable action,
  it moves to manual review.
- Creem and Waffo Pancake cannot safely reconstruct an instruction after an
  ambiguous create response. Those orders move to manual review and are not
  blindly recreated.
- Waffo Pancake test and production modes use distinct signing and merchant
  environments. The saved environment is part of configuration readiness and
  every new order. The webhook path, signed event mode, provider inquiry mode,
  current configuration, and order snapshot must agree before settlement; a
  mismatch is retained for manual review and grants no entitlement.
- Waffo Pancake's unit-price multiplier converts the server-authoritative USD
  wallet or fixed-term plan amount into provider settlement currency. Its
  minimum top-up is a USD wallet-input rule, not a provider-global limit.
- Signed callbacks try the canonical order first. Verified legacy fallback is
  retained only for pre-existing records; amount, currency, provider order,
  buyer/store/environment identity, and immutable entitlement snapshots must
  match before settlement.

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
- Merchant policies can enforce per-route single-payment and daily limits in
  integer minor units. Active orders reserve capacity atomically; paid usage is
  assigned to the configured merchant day, while failure and expiry release
  unused capacity.
- The scheduled payment task expires unattended orders and deletes expired or
  consumed quotes after the audit-retention window in bounded batches.
- Late verified payment is still retained and settled or reviewed according to
  the immutable order contract; expiry never discards provider evidence.

## Credential Incident Response

Emergency handling is intentionally conservative and provider-specific:

1. For Epay, Stripe, and XORPay, normal rotation creates a new credential
   generation and temporarily retains the previous generation for orders and
   delayed callbacks created before the cutoff. Emergency revocation is a
   separate action that immediately stops trusting the selected generation.
2. Creem, Waffo, and Waffo Pancake currently have one active credential set and
   no previous-generation overlap. Their emergency action disables the current
   credentials: Creem clears its API and webhook secrets; Waffo clears the
   active-environment API key, private key, and callback certificate; Waffo
   Pancake clears its private key and store binding.
3. Every emergency action requires an impact preview, a reason, privileged
   step-up verification, explicit confirmation, configuration-version CAS, and
   an audit record. Pending, unfinished, and callback-dependent candidate
   orders and unmatched monetary events become terminal review evidence.
4. Already fulfilled orders keep their economic projection. They are marked as
   credential incidents but are not automatically reversed.
5. Review provider dashboards and local event/ledger evidence for the affected
   credential and time window. Acknowledge the incident with an investigation
   note, then resolve it only after refunds, disputes, or manual actions are
   complete.

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

Pre-canonical Creem, Waffo, and Waffo Pancake rows may also lack the original
entitlement snapshot. A delayed callback for such a row is kept for manual
review rather than recalculating credit from current mutable settings.

Back up the primary database before upgrading. Validate the migration on a copy
of production data before a stable release, especially pending orders from
every gateway, Stripe legacy subscription inventory, payment customer bindings,
and open payment-limit or billing reservations.

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
  Stripe Customer retirement and legacy subscription end-of-period cancellation
  are exercised with administrator step-up.
- Run the SQLite migration and payment test suites locally. Dedicated CI jobs
  exercise MySQL 5.7 and PostgreSQL 9.6 migration/payment contracts when their
  isolated databases are available; those tests do not by themselves prove a
  production multi-node deployment.
- Before production rollout, run the multi-node smoke test against the chosen
  shared MySQL or PostgreSQL database and shared Redis, then retain evidence for
  cross-node sessions, restart tolerance, task leasing, fencing, and readiness
  mismatch rejection. Retain separate database integration-test evidence for
  concurrent callback event ingestion, settlement, and exactly-once fulfillment.
- Both frontend themes build and Payment Operations is usable at desktop and
  mobile widths.

## Known Operational Limits

- Stripe endpoint permission probing cannot prove external webhook delivery;
  complete a test-mode payment and callback exercise before accepting traffic.
- Historical Stripe subscriptions are not converted into locally renewing
  entitlements. Operators must review the inventory and explicitly schedule
  each still-recurring legacy subscription for end-of-period cancellation; the
  application does not perform an unapproved bulk cancellation.
- XORPay cannot recover a lost create-response QR through its public query API.
- Cluster readiness failures deliberately return a retryable HTTP error before
  automatic callback processing. XORPay documents bounded notification
  retries, but arbitrary Epay-compatible gateways do not share one guaranteed
  retry contract. A prolonged cluster pause can therefore require provider
  reconciliation or administrator review; callback delivery has not been
  proven for a specific merchant until exercised through its public ingress.
- Creem and Waffo Pancake cannot recover a lost create-response hosted
  instruction without provider evidence, so ambiguous creation requires manual
  review. Waffo inquiry can recover only when the provider returns a valid
  order action.
- Legacy clients that omit request IDs cannot guarantee cross-request
  idempotency; upgrade them to the canonical quote/start API.
- Production-data-copy validation remains a release gate even when clean and
  synthetic upgrade databases pass.
