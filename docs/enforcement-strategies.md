# Local delivery enforcement strategies

## Historical scope and baseline

This refinement starts with the uncommitted optional-commercial and local-delivery
refactors, not bare Git HEAD. Git base is
`94c226dae1999d27895d75fc14d27d790b0b5f2f`; schema remains **3**. Input worktree:
`tpahub-optional-commercial`, branch `feature/optional-commercial-composition`.
The input archive `/tmp/tpahub-enforcement-baseline.tar` has SHA-256
`4efa656691f6c2ba3ad116e8f0206146fa9eca15fd71f0df9b03d315042a06f9`.
Implementation is isolated on `feature/delivery-enforcement-strategies`.

Before changes, ordinary and race suites failed only the inherited installer-ID
assertion, `TestPublicBootstrapAndVersionEndpoints`; vet passed. Bloom discovery
returned no reusable enforcement capability. The existing strict lifecycle and
response writer were inspected and reused instead of introducing a policy engine.

No schema, grant persistence/resolution, publication authority, accounting-store
method, recovery implementation, TPA source or production configuration is changed.
The subsequent [Schema 5 cut](schema-upgrades.md) supersedes that development-lineage
blocker. Results below describe the historical refinement, not current Schema 5
qualification. No deployment or implicit migration is authorized.

## Responsibilities

```text
Common repository resolution / authorization / active generation / rooted files
  → prepare selected enforcement strategy
  → register request-scoped completion
  → apply writer modifier
  → serve through the existing optional measurement writer
  → complete enforcement with actual written bytes
  → emit the existing local observation / update consumers
```

Measurement observes what happened. Grant/policy lookup supplies facts.
Enforcement chooses actions. Strict accounting owns required durable mutations.
Billing/payment remain optional downstream integrations.

Repository existence, visibility, private-grant and reader checks, ownership and
account authority, durable generation selection, rooted file safety and HTTP
serving remain outside the strategy. None is not an authentication bypass.

## Contract

`internal/enforcement` defines:

```go
type Strategy interface {
    Prepare(context.Context, Request) (Guard, error)
}

type Guard interface {
    Wrap(http.ResponseWriter) http.ResponseWriter
    Complete(written int64)
}
```

`Request` carries already-authorized repository/account IDs, the candidate file
size, and request start time. File size is an admission estimate, never the
measurement of delivered bytes. The strategy does not receive a counter, billing
provider, repository resolver or arbitrary mutable server configuration.

Why this lifecycle:

- Prepare can resolve policy, check authority and reserve before any body writes.
- Wrap can throttle while forwarding actual writes synchronously to the meter.
- Complete receives the actual accepted-write byte total on handler unwind.
- Partial/error/cancelled/panicking transfers must also complete. There is no
  discard-on-abort operation that could lose partially delivered usage.
- Completion is registered before Wrap, so an abnormal modifier unwind cannot
  strand an already-prepared reservation.
- Complete cannot write further response bytes. It owns settlement-error handling;
  already-written HTTP status/body cannot be undone.

Preparation failure returns no guard. The strategy owns uncertainty/recovery for
any partially prepared state. Built-in `Unavailable` errors preserve existing
public 503 messages. Unknown strategy errors also return 503 without exposing
internal diagnostics. Rejected attempts emit no admitted-delivery observation.

This is static Go composition. There is no dynamic registry, loader, executable,
shared library, plugin RPC or external enforcement service.

## Implemented strategies

### None

`enforcement.None` returns a tiny passthrough guard. It has no store, grant,
accounting, metering or commercial dependencies. It does not query allowance,
reserve, settle or throttle. With measurement enabled, the unchanged caller-owned
counter/read model still operates.

Historical accounting uncertainty is neither consulted for admission nor erased.
Existing startup/operator recovery remains available for that historical state.

### Strict accounting

`strictAccountingEnforcement` wraps the existing behavior in `delivery_strict.go`:

1. Repository accounting uncertainty check.
2. Existing account allowance/usage/uncertainty lookup.
3. `BeginEgressDelivery`.
4. Existing period resolution and its grant-denial fallback.
5. `ReserveEgressDelivery`, using the same candidate-size reservation semantics.
6. Optional rate-limited writer.
7. `CompleteEgressDelivery` with actual bytes and a background context.
8. On failure, existing durable uncertainty, degraded latch and logging.

The production ordering is retained, including the separate existing policy and
period lookups. This does not claim a new transactional grant-policy snapshot.

`enforcement_composition.go` supplies narrow dependencies: uncertainty authority,
begin/reserve/complete callbacks, policy/period readers, clock and degraded-state
handling. They are captured during preparation; an in-flight guard cannot pick
up a replacement completion callback or accounting backend afterwards. The strict
implementation itself has no whole-server field or accounting-store import.

The guard completes at most once, even if a decorator repeats Complete. Normal
handlers still call it once. Store settlement/idempotency/recovery predicates are
unchanged and remain the final durable authority.

The original `rateLimitedResponseWriter` moved from `egress.go` to the strict
strategy unchanged. `NanoOverageBytesPerSecond` remains the strict strategy's
current throttle policy, not measurement logic. A standalone throttle strategy
or decorator library is not justified yet and is not exposed.

Zero allowance remains finite. The existing store sentinel for a missing
applicable reservation limit is supplied by composition, never used to encode
strategy selection. Unknown/inactive grant handling, quotas, throttling scope,
settlement timestamps and recovery semantics remain unchanged.

## Configuration and compatibility

```text
TPAHUB_EGRESS_ENFORCEMENT=none
TPAHUB_EGRESS_ENFORCEMENT=strict-accounted
```

When absent/empty, enforcement inherits the existing delivery preset:

| Delivery preset | Measurement | Default enforcement |
|---|---|---|
| `unmetered` | Off | none |
| `metrics-only` | On | none |
| `strict-accounted` or legacy empty value | On | strict-accounted |

An explicit enforcement value overrides only the enforcement part of that preset.
For example:

```sh
# Local metering + strict local enforcement; billing is still optional.
TPAHUB_DELIVERY_MODE=metrics-only TPAHUB_EGRESS_ENFORCEMENT=strict-accounted \
  TPAHUB_BILLING=none tpahub serve

# Explicitly unenforced, measured delivery, even with the old strict preset.
TPAHUB_DELIVERY_MODE=strict-accounted TPAHUB_EGRESS_ENFORCEMENT=none \
  TPAHUB_BILLING=none tpahub serve
```

The second example is an intentional operator change, not an upgrade default.
Unmetered + strict enforcement is rejected: required actual-byte accounting needs
measurement. Unknown values (`local-quota`, `throttle`, `remote`, etc.) are rejected,
not advertised as implemented. Invalid compositions fail before opening state.

Fresh initialized self-hosted profiles remain metrics-only + billing none and
therefore default to enforcement none. Existing legacy deployments remain strict.
No profile-file version or schema change is needed. No hot reload or per-account
strategy selection is implemented; composition is fixed before routes are exposed.

Read-model selection is explicit in composition: measured/unenforced uses the
existing volatile local totals; strict enforcement uses the existing accounting
authority. No observation is replayed into the ledger. Storage/publication policy
and infrastructure safety are unchanged by egress enforcement selection.

## Independence and future compositions

Supported now:

- measurement off + enforcement none;
- measurement on + enforcement none;
- measurement on + strict enforcement, with billing none or an optional hosted
  commercial integration.

Existing observational mailboxes remain bounded and nonblocking. Their failure
or overflow cannot change enforcement decisions or create another settlement.
The existing actual-write fact semantics, partial writes, HEAD/range/conditional
responses and request classification remain unchanged.

A future enterprise composition can attach aggregation/reporting and monthly
billing after local measurement while keeping enforcement none. Neither a report
protocol nor billing policy needs to alter the response counter or guard contract.
Those products/transports are not implemented here.

## Tests

New production-path tests:

- `TestExplicitNoEnforcementIgnoresAccountingAuthority`
- `TestExplicitEnforcementPreservesAuthorizationAndGeneration`
- `TestEnforcementChangesActionsNotMeasuredFacts`
- `TestEnforcementGuardFreezesSettlementAfterPreparation`
- `TestInvalidEnforcementFailsBeforeOpeningState`
- `TestStrictGuardCompletesOnceIncludingAbnormalUnwind`
- `TestUnknownStrategyFailureIsFailClosed`

Additional selection/dependency tests:

- `TestEgressEnforcementSelectionAndCompatibility`
- `TestSelfHostedEnforcementDefaultsAndExplicitOverride`
- `TestEnforcementDependencyBoundary`

Tests use real routes, actual response writes, store operations, persisted markers,
SQLite commit observations and billing/payment call counters. Static decorators
in tests exercise the same production guard; they are not shipped plugin modes.

Existing strict quota/throttle, partial-write, settlement rollback, uncertainty,
real store-reopen/operator recovery, no-double-accounting, consumer isolation,
HTTP behavior, and real fresh self-hosted startup/publication tests are retained.
Only an existing profile-test environment cleanup helper changes, to isolate the
new environment variable; existing assertions are not weakened.

The contract/none package's actual dependency graph excludes application authority.
Concrete strict implementation and composition files are checked for forbidden
billing/payment/Stripe/meter dependencies. Since the executable is deliberately
combined, this is not a claim that its entire package graph excludes hosted code.

### Final validation results

- `go test ./...`: same single inherited installer-ID assertion failure.
- `go test -race ./... -count=1 -timeout=60s`: same single failure, no race diagnostic.
- `go vet ./...`: passed.
- `git diff --check`: passed.
- `go test -race ./... -count=1 -timeout=60s -skip '^TestPublicBootstrapAndVersionEndpoints$' -json`:
  passed, 323 top-level test/example results; no other test skipped.
- `node tests/ops-resource-test.js`: passed; code index refreshed.

The unfiltered suite is not claimed green. Existing real restart/operator recovery,
settlement rollback/uncertainty, quota/throttle, billing-independence and fresh
self-hosted startup/publication tests all passed. No full APT/VM or deployment
qualification is claimed by these local results.

Byte comparisons against the input worktree confirm unchanged source in
`internal/store`, `internal/commerce`, `internal/tpahubproduct`, `internal/usage`,
`internal/billing` and `internal/hostedbilling`, and unchanged existing strict,
local-delivery, authorization and fresh-profile test assertions. TPA is unchanged.

## Deferred and remaining coupling

Not implemented: dynamic plugin loading, third-party enforcement modules, remote
enforcement, quota-only strategy, standalone throttle strategy, Metering Protocol,
billing transport, additional payment providers, remote storage or SSH.

Remaining concrete assumptions:

- `delivery_strict.go` uses the existing Nano overage rate. That is now contained
  in strict enforcement; it is not a new general-purpose configurable rate policy.
- `egress.go:accountEgress` still combines local accounting availability and grant
  allowance lookup; `egressPeriod` performs a separate existing grant read.
- `enforcement_composition.go` bridges the existing concrete SQLite-backed store
  and server-owned fault seams into narrow per-request dependencies.
- `auth.go:initialTPAGrants` retains finite Nano-derived local provisioning terms;
  private access still requires its independent capability, and publication still
  enforces its existing storage grant. No grants were rewritten to select modes.
- Operator summaries retain the historical local-store joins described in prior
  refinements. Hosted Nano branding/offer policy is not redesigned here.

The release/schema lineage and known installer assertion remain separate blockers.
