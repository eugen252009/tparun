# Operator-local capability provisioning

## Historical baseline and scope

Input: the complete uncommitted `tpahub-enforcement` worktree, branch
`feature/delivery-enforcement-strategies`, Git base
`94c226dae1999d27895d75fc14d27d790b0b5f2f`, schema **3**.
Input archive SHA-256: `5dcb6a4e96761d5df5e9ea4c4b9aaf8817b2ab8f5d49f59f7f9bd7e51420fc92`.
Implementation: `tpahub-provisioning`, branch `feature/local-capability-provisioning`.
Git HEAD alone does not contain this or the preceding refinements.

That refinement did not change schema or deployment state. The subsequent
[Schema 5 compatibility cut](schema-upgrades.md) supersedes the development-lineage
limitation. Historical results here are not Schema 5 qualification.

Bloom discovery returned no reusable provisioning capability. Reuse here is the
existing `Store.CreateAccount` transaction and `commerce.GrantService`, not a new
entitlement system.

## Ownership and lifecycle

Before this change both API and website registration called:

```
initialTPAGrants → defaultTPAGrants → tpahubproduct.DefaultGrants → NanoOffer
```

The new explicit local path is:

```
trusted server configuration → provisioning.LocalPolicy ──────────┐
                                                                ↓
                                             CreateAccount(account, grants...)
                                                                ↓
                                                       durable grant facts
                                                                ↑
hosted catalog → immutable purchase → verified payment transaction
                                                                │
                                                       commerce.GrantService
                                                                ↓
                                                authorization / enforcement
```

`provisioning.Source.InitialGrants(time.Time)` is a narrow, statically composed
account-creation source. `LocalPolicy` and the explicit legacy-local adapter use
it. Hosted registration has **no initial source**: existing hosted billing and
payment activation remain its producer of durable grant facts. There is no dummy
hosted provisioner, remote service, or parallel runtime policy interface.

API `/v1/accounts` and website account creation use the same selection and the
same existing account-plus-grants transaction. Configuration is validated before
opening/locking state in server startup. Registration checks remain intact.
Creation writes account metadata, its compatibility entitlement row and grants
atomically; duplicate account/email creation fails and leaves all facts intact.
A failed grant insert rolls back the account too. Repetition is duplicate-safe,
not a new successful upsert/regrant operation.

No provisioning lookup is added to repository/publication/delivery requests.
Only registration constructs initial grant facts. Application code still consumes
`GrantService`. Active-account, private-read, publication ownership/storage,
generation, strict accounting and rooted-file guards are unchanged.

## Policy and configuration

```
TPAHUB_CAPABILITY_SOURCE=local|hosted-commercial|legacy-local
TPAHUB_LOCAL_STORAGE_BYTES=5000000
TPAHUB_LOCAL_EGRESS_BYTES=12000000000
TPAHUB_LOCAL_PRIVATE_READ_ENABLED=true
```

The local source provisions the existing identities under product `tpa-hub`:

| Capability | Local policy |
|---|---|
| `repository.create` | enabled |
| `repository.publish` | enabled; configured storage bytes |
| `repository.download` | enabled; configured egress bytes |
| `repository.read.private` | configured enabled/disabled flag |

These values are owned in `internal/provisioning`, not obtained from an offer.
Numeric defaults intentionally retain historical local bounds rather than invent
new capacity tiers. Cadence remains annual, anchored at account creation, with no
expiry (existing local grant validity semantics). Limits are not new commercial
SKUs, prices, subscriptions or promises of physical storage capacity.

Stable capability/product identifiers live in `internal/tpahubcapability`;
`tpahubproduct` retains aliases with identical values. No capability is renamed.

Byte values must be decimal int64 values in `[0, MaxInt64-1]`; malformed, negative,
overflowing or MaxInt64 values fail startup. Zero remains a finite allowance, not
unlimited. No new sentinel is introduced. Private-read accepts only `true` or
`false`. Explicit empty policy values fail rather than silently falling back.
The old unrelated resource-bound environment parsers are unchanged.

Private-read false does **not** introduce a prohibition on creating a private
repository. Existing policy denies reader-credential operations and private
serving without the enabled read grant. Repository-count/operation limits are
not exposed: the current creation guard does not enforce such counts. Existing
upload/staging/filesystem safety bounds continue to apply independently.

Configuration is operator process environment/profile selection, never customer
JSON or form data. Tests submit malicious-looking policy fields during registration
and verify the configured limits and disabled private-read grant still win.

Local provenance uses existing fields: `source=local`,
`offer_id=local-policy-v1`, empty purchase ID. `offer_id` identifies the local
policy family here, not a purchasable offer. Exact limits and original timestamps
remain in the durable row; configuration is not consulted to reinterpret it.

## Initialization and compatibility

```
TPAHUB_DATA_DIR=./data ./tpahub init
# Existing TPA/GPG/signing configuration is still required.
TPAHUB_DATA_DIR=./data ./tpahub serve
```

Fresh initialized self-hosted configuration selects local policy, metrics-only,
enforcement none, billing none. No billing or payment object is required to
provision a usable account. Registration remains loopback-only by default.
The existing version-1 profile selector is sufficient; no profile/schema upgrade
or duplicated persistent copy of resolved policy is needed.

| Case | Behavior |
|---|---|
| Existing durable local/Nano/purchase/historical grants | unchanged; no automatic reprovisioning |
| Unprofiled or legacy profile, no explicit source | old switch behavior: legacy-local only if local provisioning enabled and purchases disabled; otherwise hosted-commercial |
| Fresh or already initialized self-hosted profile | new accounts use local policy by default; existing accounts stay unchanged |
| Self-hosted with legacy `TPAHUB_LOCAL_PROVISIONING_ENABLED=false` and no explicit source | hosted-commercial, preserving provisioning opt-out |
| Explicit capability source | overrides profile/legacy provisioning selection, independent of billing/purchases |
| Explicit `legacy-local` | intentional historical Nano-derived initial facts for new accounts |
| Operator changes local limits | affects subsequently created accounts only |
| Explicit reprovision | no new command, endpoint, or automatic conversion implemented |

`TPAHUB_LOCAL_PROVISIONING_ENABLED` remains a compatibility selector, not a second
veto on an explicit source. Setting an explicit local source with billing none
allows registration even when that old switch is false. Selecting hosted-commercial
with registration on still requires purchases enabled, retaining the no-unusable-
public-registration guard. Billing none still rejects enabled purchases.

Local provisioning does not imply disabled strict accounting or unlimited egress.
For intentional local accounting, select `TPAHUB_DELIVERY_MODE=strict-accounted`
(and optionally the explicit enforcement selector). The same durable grant facts
feed the existing strict strategy; none does not consult egress allowance at all.
Changing measurement or billing does not change explicit local grant facts.

## Nano/product call-site audit

Every production `NanoOffer`/equivalent use is classified below. Tests deliberately
covering Nano retain their fixtures and assertions.

| Site | Classification / disposition |
|---|---|
| Former `auth.go:initialTPAGrants` default-grant call | incorrect generic/self-hosted dependency; replaced by explicit source selection |
| `provisioning.go:legacyLocalProvisioner` → `DefaultGrants` | legacy compatibility only; explicit independent local branch never calls it |
| `tpahubproduct.NanoOffer`, `DefaultGrants` | hosted product definition and historical local adapter; preserved |
| `commerce_catalog.go` offer identity/snapshot calls | legitimate hosted commercial catalog; preserved |
| `billing.go:productionNanoRateCard`, `validateRateCard` | legitimate hosted pricing/validation; preserved |
| `provider.go:validateFinalized` | legitimate production-Nano finalized-price/period validation; preserved |
| `plan.go:nanoOfferLimits`, `planCatalog` | hosted/simulator plan display and rating; preserved, not local grant authority |
| `plan.go:tpaNanoOffer`, `defaultTPAGrants` | existing test-fixture helpers (no remaining production caller); preserved |
| Former `store.go:migrateLegacyAccessToSixMonthNano` | removed by the Schema 5 cut; no historical grant migration remains |
| `*_test.go` in main, store, hostedbilling and tpahubproduct | current fixtures cover Nano, payments, storage, egress and display; pre-release migration fixtures are retired |

## Dependency evidence and remaining coupling

`TestLocalProvisioningDependencyBoundary` checks the transitive Go dependency
graph: local provisioning imports only generic commerce types and stable capability
identities from this project. It cannot import store, products, billing, payments,
Stripe, measurement or enforcement. It also exercises the production source
selector and checks that its local branch calls only the local policy resolver.
This is not a claim that the combined executable has no hosted imports.

Remaining concrete hosted assumptions:

- Combined-package global `planCatalog` and production-rate-card initialization
  still evaluate Nano definitions. **Whole-process absence of Nano calls is not claimed**;
  the local provisioning path has no such dependency or lookup.
- Generic handlers/store still reference product-package aliases for stable IDs;
  their values now have a shared noncommercial home. They do not inspect local
  provenance to authorize requests.
- Strict over-allowance rate remains the existing Nano-named 128,000 bytes/second.
  Changing this strategy's policy is outside the task.
- Existing compatibility `account_entitlements` rows and the hosted/simulator
  rating catalog remain. `local-policy-v1` is deliberately not a priced plan;
  selecting local provisioning does not create a billing-preview price for it.
- Public website/CLI hosted defaults and product messaging are not reworked here.

## Tests

New production-path tests cover registration and operator ownership, storage
admission accept/reject, private credential/read accept/reject, suspended-account
checks, duplicate safety, transaction rollback, unchanged old grants across
restart, web creation, provenance, invalid-before-state validation, independent
billing/measurement/enforcement and source dependency boundaries.

The existing real child-process initialization/startup/publication test now also
asserts four durable `local-policy-v1` grants with the finite defaults. It still
uses real TPA/GPG and verifies signed repository serving and absence of accounting,
payment and preview rows with unused invalid hosted settings.

Baseline `go test ./...` failed the known installer-ID assertion. Baseline race
also observed `TestRepositoryEgressHTTPSemantics` read zero egress immediately
after closing the HTTP response; that test does not wait for deferred settlement.
No race-detector diagnostic was observed. Baseline vet passed.

Final validation:

| Command | Result |
|---|---|
| `go test ./...` | fails only known `TestPublicBootstrapAndVersionEndpoints` installer-ID assertion |
| `go test -race ./... -count=1 -timeout=60s` | same sole failure; no race diagnostic |
| `go vet ./...` | passes |
| `git diff --check` | passes |
| `go test -race ./... -count=1 -timeout=60s -skip '^TestPublicBootstrapAndVersionEndpoints$' -json` | 330 top-level tests/examples pass; no additional skipped tests (packages without tests are reported separately by Go) |
| `go test -race ./cmd/tpahub -run '^TestRepositoryEgressHTTPSemantics$' -count=10 -timeout=60s` | passes all ten repetitions; pre-change intermittent observation is not claimed fixed |

Seven new top-level tests were added; existing Nano/purchase/strict tests were
preserved. Only the existing self-hosted process test gained grant assertions and
the profile-test environment cleanup gained the new configuration keys. No
frontend assets changed. CCC was refreshed.

Byte comparisons against the input archive confirm unchanged store, commerce,
enforcement, usage, billing, hostedbilling and payment packages; delivery/strict
composition, publication policy, purchase and Stripe wiring are also unchanged.
TPA remains clean at `a5060659b747e75b201903f4828e065203526a9a`.
Full suites are **not green** while the installer assertion remains.

## Deferred

No dynamic provisioning plugins, remote capability service, per-account local
policy editor, reprovisioning workflow, organizations/profiles beyond this one,
true unlimited grants, repository-count enforcement, enterprise identity
integration, remote metering, remote billing, remote storage or SSH. No release-
lineage repair, production upgrade, deployment or full public VM/APT qualification.
