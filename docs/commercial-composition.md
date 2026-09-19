# Self-hosted profiles and optional commercial integration

A subsequent [enforcement strategy refinement](enforcement-strategies.md) adds
independent egress enforcement selection. It preserves the commercial/profile
behavior documented here; validation and byte-comparison results below describe
this commercial refinement's baseline.

## Historical baseline and safety

This refinement builds on the uncommitted local-delivery extraction, not bare
Git HEAD. Git base: `94c226dae1999d27895d75fc14d27d790b0b5f2f`, schema **3**.
The input tree was preserved as `/tmp/tpahub-local-delivery-baseline.tar`, SHA-256
`51db3b486fbc8cc0f17bc5dc8a995aaaf53df834278dcd54794648f0019fe704`.
Work is isolated on `feature/optional-commercial-composition`.

That refinement did not change schema, grants or accounting. The subsequent
[Schema 5 compatibility cut](schema-upgrades.md) supersedes the development-lineage
blocker. Historical results below are not current release qualification. No
production upgrade/downgrade or TPA modification is authorized.

Before changes, ordinary tests and race tests failed only the inherited
`TestPublicBootstrapAndVersionEndpoints` installer-ID assertion. Vet passed.

## Fresh installation and compatibility

For a NEW empty data directory:

```sh
export TPAHUB_DATA_DIR=./data
./tpahub init
# Supply the existing TPA/signing configuration as usual.
./tpahub serve
```

`init` acquires the existing data-directory lock, refuses any existing content
other than that lock file, and exclusively creates a mode-0600
`server-profile.json` selecting the version-1 `self-hosted` profile. It does not
create a database or migrate a schema. Finish initialization before starting the
server. Reinitialization is refused, not treated as a configuration reset.

The profile selects:

- metrics-only delivery and volatile local usage;
- billing **none**, not a mock provider;
- operator-local grant provisioning (see the subsequent
  [local policy refinement](local-capability-provisioning.md));
- registration enabled on a loopback listener unless explicitly configured;
- localhost operator WebAuthn defaults unless explicitly configured;
- no payment provider, exporter, collector, or billing endpoint.

Registration is NOT implicitly enabled when binding a non-loopback address.
Configure the public operator RP ID/origin and registration policy explicitly for
remote administration. Existing Linux sandbox, TPA, GPG and signing-identity
requirements still apply. This is not zero-configuration signing/bootstrap.

### Existing installations

No initialized profile means **legacy**:

- strict-accounted delivery remains the default;
- legacy mock billing remains the development simulator default;
- existing Stripe test configuration remains recognized;
- registration/provisioning defaults remain unchanged.

Database absence or version is deliberately not used to guess installation age.
Thus a bare `serve` without initialization also remains legacy-compatible. The
natural new self-hosted entry point is `init` followed by `serve`.

Explicit environment configuration overrides profile defaults. A deployment may
also explicitly select `TPAHUB_PROFILE=self-hosted` or `legacy`. That is an
operator configuration decision, not an inferred migration. Invalid/corrupt
profile content fails startup; it is not silently ignored. The profile file
stores only the selector, not duplicated resolved configuration or grant limits.

For an intentional existing-server change, stop the server and explicitly choose
the desired profile or delivery/billing settings. Do not run `init` on existing
state. Switching off accounting does not clear historical uncertainty or grants.

## Configuration

`TPAHUB_BILLING` selects commercial integration independently of delivery:

| Value | Meaning |
|---|---|
| `none` | No commercial service/provider. Hosted secrets, cards and return URLs are irrelevant. Purchases enabled with this mode are rejected. |
| `simulator` | Existing mock billing preview/finalization tools; no payment collection. |
| `hosted` | Current co-located billing-service adapter; its installed payment backend is Stripe **test**. |
| `remote` | Rejected: no remote billing transport is implemented. |

When `TPAHUB_BILLING` is absent, explicitly supplied legacy
`TPAHUB_BILLING_PROVIDER` remains compatible: `none` → none, `mock` → simulator,
`stripe` → hosted. The initialized profile supplies none only when neither
commercial selector is explicitly set. A missing Stripe key never means none.

Delivery remains independently selectable with `TPAHUB_DELIVERY_MODE`:
`unmetered`, `metrics-only`, or `strict-accounted`. Reported delivery is not added.

Example non-commercial strict accounting:

```sh
TPAHUB_DELIVERY_MODE=strict-accounted TPAHUB_BILLING=none tpahub serve
```

Example current hosted test composition:

```text
TPAHUB_DELIVERY_MODE=strict-accounted
TPAHUB_BILLING=hosted
TPAHUB_BILLING_PROVIDER=stripe
TPAHUB_BILLING_ENVIRONMENT=test
TPAHUB_BILLING_RATE_CARD=production-nano-v1
TPAHUB_BILLING_PURCHASES_ENABLED=true
TPAHUB_STRIPE_TEST_SECRET=<configured securely>
TPAHUB_STRIPE_WEBHOOK_SECRET=<configured securely>
```

These are documented examples, not production configuration changes.

`TPAHUB_CHECKOUT_SUCCESS_URL` and `TPAHUB_CHECKOUT_CANCEL_URL` configure the hosted
adapter's return addresses. Only that adapter supplies the hosted public
`https://tpa.run/` fallback. Generic purchase orchestration and self-hosted
startup do not contain/use those defaults. Selected hosted configuration rejects
invalid schemes, missing hosts, userinfo and fragments. HTTP/HTTPS are supported
for the current test integration.

## Billing is not payment

`internal/billing.Service` is the optional commercial boundary. It reuses:

- `commerce.PurchaseRequest`: customer/product/offer intent;
- `commerce.Purchase`: canonical snapshotted commercial terms and lifecycle;
- `payments.CheckoutSession`: payment initiation result;
- `commerce.PaymentEvent`: provider-neutral verified payment evidence.

It supports the actually implemented purchase and validated-event workflows.
There is no new speculative invoice or aggregated-usage submission API.

`internal/hostedbilling.Service` implements this boundary in process. Hosted
policy prepares fresh terms; the service persists/reuses the existing immutable
purchase/grant snapshot and invokes `payments.Provider`. Retries use persisted
amount/currency/provider/environment, not current prices. Existing Stripe test
idempotency keys remain identical. No second commercial database is introduced.

The current hosted product/rate-card policy is composed in
`cmd/tpahub/hosted_billing.go`. It remains Nano-specific. Mock billing in
`billing.go` remains an explicit local development simulator, not the semantic
implementation of none. The account preview link labels it accordingly.

Payment implementations do not choose operational grants at request time. A fake
invoice-like test provider exercises the same service/store path without importing
Stripe. This proves replaceability of the port; it does NOT implement an invoice
backend or expose additional configurable providers.

## Authority and event flow

```text
Authenticated Hub purchase route
  → billing.Service.StartPurchase(customer/offer intent)
  → current hosted commercial policy
  → durable immutable purchase + grant snapshot
  → payments.Provider.CreateCheckout

Stripe webhook HTTP edge
  → signature verification + legacy correlation
  → generic commerce.PaymentEvent
  → billing.Service.AcceptPayment
  → existing Store.ApplyProviderPaymentEvent transaction
  → validated, idempotent local grant activation

Publication / private access / strict egress
  → local commerce.GrantService
  → local durable grant facts
  → decision (no billing/payment request)
```

`AcceptPayment` accepts **already-authenticated** evidence. It is not an unsigned
generic webhook endpoint. Provider/environment authority is bound by the service,
not supplied by an arbitrary event payload. Store correlation, amount/currency,
snapshot validation, reconciliation and activation remain in the same production
transaction. No direct grant-write endpoint is introduced.

### Authoritative today

| Fact | Authority |
|---|---|
| Accounts, repository ownership, publication generation | Hub store/publication transaction |
| Operational permissions/allowances | Local grants through GrantService |
| Measured delivery facts | Local response-write measurement |
| Metrics-only totals | Volatile local read model, not billing evidence |
| Strict accounted balances/uncertainty | Existing Hub accounting ledger |
| Fresh offer/pricing decisions | Hosted commercial policy/catalog/rate card |
| Accepted purchase terms and grant provenance | Existing immutable local snapshots |
| Provider customer mapping and event receipts | Existing SQLite-backed adapters |
| Provider transaction evidence | Verified payment adapter events, locally validated |

Future process separation could move commercial policy, provider mappings,
provider ingress and invoice calculation to a billing service. It will require
an explicit authenticated protocol and authority/cutover design. This task does
not move or duplicate those persistent records remotely.

## Startup and dependency boundaries

`main.go` calls a commercial composition function rather than initializing
Stripe. With none, no service/provider is constructed. The existing
registration/provisioning guard remains independent and cannot be bypassed by
disabling billing. Startup still opens/validates the required local store and
performs the existing recovery and runner checks.

Stripe dependencies are confined to the existing `internal/payments/stripe`
adapter and hosted wiring/webhook edge in `cmd/tpahub/stripe_wiring.go` and
`stripe_webhook.go`. `provider.go` retains composition-aware validation of the
installed hosted adapter. Its legacy constructor name is retained for compatibility.

The executable remains combined. Dependency tests therefore enforce both:

1. Real transitive package graphs for measurement, commerce, the commercial port
   and the hosted service exclude Stripe (measurement also excludes billing/payment).
2. Delivery/publication/authentication/core purchase files contain no Stripe
   imports or Stripe identifiers; the hosted composition files may wire adapters.

## Tests and evidence

New tests:

- `TestInitializedSelfHostedProfileAndLegacyCompatibility`
- `TestProfileInitializationRejectsExistingStateAndMalformedProfiles`
- `TestSelfHostedProfileDoesNotOpenPublicRegistrationImplicitly`
- `TestBillingIntegrationSelection`
- `TestGenericHubPurchaseUsesReplaceableBillingService`
- `TestBillingNoneCompositionSkipsHostedRequirements`
- `TestHostedReturnURLsAreAdapterConfiguration`
- `TestCommercialDependencyBoundaries`
- `TestReplaceablePaymentAndLocalGrantAuthority`
- `TestFreshSelfHostedInitializationStartupPublicationAndUsage`

The fresh-install test launches the actual `main`/`serve` path in a child process,
uses the real TPA runner and signing verification, registers a local account,
creates/publishes a real package, serves signed metadata, verifies dashboard usage,
and checks that accounting/payment/preview tables remain empty. Invalid unused
Stripe/card/checkout settings deliberately prove that none does not initialize
or validate those integrations. `TestSelfHostedServerProcess` is its child entry.

Existing Stripe, purchase retry/idempotency/tax/reconciliation, local delivery,
strict settlement/uncertainty/restart recovery and grant tests are retained.
No old assertions are changed to hide failures. The unrelated installer-ID
assertion remains a known full-suite failure.

### Final validation

- `go test ./...`: same single inherited installer-ID assertion failure.
- `go test -race ./... -count=1 -timeout=60s`: same single failure; no race diagnostic.
- `go vet ./...`: passed.
- `go test -race ./... -count=1 -timeout=60s -skip '^TestPublicBootstrapAndVersionEndpoints$' -json`:
  passed, 313 top-level test/example results; no additional test skipped.
- `node tests/ops-resource-test.js`: passed.
- `git diff --check`: passed; code index refreshed.

The complete unfiltered suite is **not** claimed green. Existing Stripe tests,
historical-price retry tests, webhook reconciliation/activation tests, strict
egress restart/operator recovery and quota/throttling tests all remain passing.
The prior local APT shell qualification's HTTP 413 blocker was not bypassed or
requalified here; the new fresh-profile test qualifies local startup/publication/
delivery, not public fresh-VM or full APT installation.

Byte comparisons against the preserved local-delivery worktree confirm no
changes in `internal/store`, `internal/commerce`, `internal/tpahubproduct`,
`internal/usage`, `delivery.go`, `delivery_strict.go`, `egress.go` or
`repository_policy.go`.

## Deferred and remaining coupling

Not implemented: Metering Protocol, meter identities/signatures/sequences,
remote usage collector, report spool/retries, full remote billing transport,
invoice/SEPA/ERP providers, additional payment backends, usage-based live billing,
SSH, remote storage, PostgreSQL, message brokers or microservices.

Concrete remaining coupling:

- `hosted_billing.go` supplies existing Nano/rate-card policy through a callback;
  it has not been relocated wholesale into a standalone product/service package.
- `billing.go`, explicit account simulator previews and billing operator summaries
  still use the local store and existing simulator types.
- `auth.go:initialTPAGrants` retains the existing local provisioning templates,
  including finite Nano-derived capacities. Self-hosted does not imply unlimited.
- `server`/configuration still hold legacy payment/webhook wiring fields and
  Stripe settings, although generic request paths no longer use Stripe directly.
- Purchase routes and the account offer UI remain Nano-specific hosted features.
- Purchase snapshots, provider mappings and commercial evidence remain in the
  same SQLite authority. A future transport cannot simply replace these with
  remote mutable lookups without preserving activation/recovery invariants.

No deployment, production state/configuration change, schema migration, TPA
change, or implicit release-lineage repair was performed.
