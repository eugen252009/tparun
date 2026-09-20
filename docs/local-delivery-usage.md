# Local repository delivery and usage

The subsequent [commercial composition refinement](commercial-composition.md)
adds initialized self-hosted defaults (metrics-only, billing none). The defaults
and validation results below describe the first extraction's legacy-compatible
baseline; strict accounting itself is unchanged.

## Scope and development baseline

This extraction starts at `94c226dae1999d27895d75fc14d27d790b0b5f2f`
(`fresh-onboarding-deploy`, the audited development baseline). The schema is **3**.
Work is isolated on `feature/local-delivery-usage`.

This historical extraction was not the previously qualified Schema 4 lineage.
The subsequent [Schema 5 compatibility cut](schema-upgrades.md) supersedes that
limitation. Results below describe the historical extraction, not current release
qualification or deployment authorization.

Bloom capability discovery returned no reusable result for this extraction. The
implementation reuses the existing local response counter, canonical storage
scan, strict accounting lifecycle, grants, and dashboard queries.

## Configuration and composition

`TPARUN_DELIVERY_MODE` accepts exactly:

| Value | Egress observation | Local metrics | Required accounting/enforcement |
|---|---|---|---|
| `unmetered` | Off | None | None |
| `metrics-only` | On | Volatile process-lifetime totals | None |
| `strict-accounted` | On | Existing accounting-backed usage | Existing strict lifecycle |

Unset/empty means **strict-accounted**, preserving existing installations and
zero-valued `config.Config` callers. Unknown values, including `reported`, fail
validation before startup touches the data directory.

The server resolves composition once, before exposing routes. Runtime behavior
contains measurement selection, an optional required accounting participant, an
optional local read model, and zero or more observational mailboxes. A request
never re-resolves its delivery mode between reservation and settlement. There is
no runtime configuration reload; change mode by intentionally restarting.

This is an egress-delivery policy, not a billing plan or an interpretation of quota
numbers. `commerce.GrantService`, grant resolution/provenance, validity, cadence,
and numeric allowance semantics remain unchanged. In particular, zero is still a
finite allowance in strict mode; it does not select unmetered behavior.

Storage/publication authorization, staging reservations, inode/free-space checks,
and existing storage high-water observation remain unchanged in all modes. There
is no new storage-enforcement toggle in this first extraction.

### Billing is independently optional

```sh
TPARUN_DELIVERY_MODE=metrics-only TPARUN_BILLING_PROVIDER=none tparun serve
```

`TPARUN_BILLING_PROVIDER=none` skips provider/rate-card validation and disables the
server's billing-preview UI and purchase availability. It does **not** bypass the
existing registration/provisioning guard. Purchases enabled with provider `none`
are rejected. Default billing configuration is unchanged.

No Stripe key or billing provider is needed for basic usage display. Existing
explicit billing tools remain downstream; disabling billing is not a new grant
or provisioning mechanism.

## Common delivery and strict preservation

```text
repository lookup
  → existing private grant + reader authorization
  → durable active-generation resolution (not the current symlink)
  → rooted regular-file resolution
  → composed usage/accounting behavior
  → one standard-library file-serving call
```

- **Unmetered:** direct serving; no usage clock sample, counter, accounting call,
  egress grant/limit lookup, ledger write, or observational fan-out.
- **Metrics-only:** serve through the existing response counter; on unwind, update
  local totals and offer one fact to each configured observational mailbox.
- **Strict-accounted:** the extracted `prepareStrictEgress` performs the original
  uncertainty/allowance lookup, `BeginEgressDelivery`, `ReserveEgressDelivery`,
  optional rate limiter, and deferred `CompleteEgressDelivery`.

Strict reservation still uses the existing requested-file-size semantics.
Settlement still uses actual counter bytes, the request-start usage timestamp,
and a background context so request cancellation does not cancel settlement.
Settlement failure still marks durable uncertainty, records the degraded latch,
and logs the failure. Existing restart and operator recovery remain authoritative.

Observation occurs after the settlement attempt and cannot repair, hide, or
re-settle an uncertain delivery. It is **not** a journal acknowledgment or proof
that bytes were successfully accounted. Early strict admission failures emit no
file-serving observation. Existing startup recovery is not disabled or erased by
selecting a different delivery mode.

Throttling, exhausted/zero allowances, unknown-grant handling, and the existing
explicit reservation sentinel remain unchanged. The limiter still applies to
all admitted repository file responses when selected, not just `.deb` files.

### Explicit safety correction

The previous `os.Stat` + `http.ServeFile` pair followed content symlinks outside
the selected generation. Common delivery now uses `os.OpenRoot` and
`http.ServeFileFS` to confine file lookup to the selected root, including symlink
resolution. A symlink into a different generation is rejected in every mode.

This preserves standard-library range, conditional, HEAD, and index-redirect
mechanics rather than introducing a custom HTTP implementation. It assumes the
existing trusted generation-directory/publication lifecycle; it is not a general
remote-storage or hostile-filesystem adapter.

## Fact model and semantics

`internal/usage` contains no prices, currencies, allowances, rate cards, product
plans, payment IDs, or invoice IDs.

### Delivery

`usage.Delivery` contains:

- repository ID and the repository owner's account ID;
- UTC start and finish times;
- HTTP method and response status;
- additive body-byte delta;
- write-failure and interrupted-request classifications.

Each observation is **one admitted file-serving attempt**, so request count is an
additive event count. Rejections during repository/authentication/root/file
resolution or strict accounting admission do not count. Website/API traffic does
not count. This is not a count of every inbound HTTP request.

Bytes are the sum of successful `n` values returned by response `Write` calls:

- partial write followed by error: count only returned `n`;
- single range: count its written body;
- multipart ranges: count the actual response body, including MIME framing;
- ordinary HEAD/304 and empty files: zero body bytes, one admitted attempt;
- errors generated by the standard file server after admission: their accepted
  body writes count, matching the existing strict counter;
- aborted/cancelled transfers: retain accepted write counts and classify errors
  or interruption; delivery completion is not inferred from Content-Length.

These are **application response-write bytes**, not wire/TLS bytes or proof of
client receipt. The accounting amount is never inferred from reservation size,
package size, or repository size. Errors rejected before serving emit no fact.

Status tracking extends `repositoryCountingWriter`; its existing byte-counting
and flush behavior are retained. The final observation is emitted on handler
unwind, including abnormal unwind; it is not emitted on every `Write`.

### Storage

`usage.Storage` is a gauge from the existing canonical `.deb` payload scan:
repository/account identity, observation time, generation identity (empty for
legacy layout), scope `published-package-payloads`, and current bytes.

`repositoryStorageObservation` resolves a root once and reuses `publishedUsage`.
`repositoryUsage` and `accountUsage` reuse that path; account storage sums distinct
repositories' current gauges, never successive observations of one repository.
Storage is pull-observed, not inserted into delivery-byte totals or mailboxes.

Existing `ObserveStorageHighWater` / `StorageHighWater` remain maxima of observed
account storage in their existing operational periods, not a time-integral or
sum of gauges. Physical allocation/inode/free-space checks remain infrastructure
safety, separate from these logical payload observations.

## Read model and local consumer boundary

Metrics-only totals are held in memory per account/repository pair. They include
body bytes, admitted request count, and write-failure count. They are thread-safe
and **reset on restart**. Their period is server-composition time through now,
not a billing month or entitlement year. They are not replayable or billable
accounting authority. No schema or persistent telemetry table is introduced.

Strict usage reads continue using `AccountEgressWindow` through the existing
`accountEgress` authority path. Observations are never passed to
`AddRepositoryEgressDaily` or settlement a second time.

A `usage.Mailbox` is a bounded, receive-only local input for optional consumers:

- zero mailboxes is valid; no fake reporter is constructed;
- one or multiple inputs receive copies of the same value fact;
- offering never waits for a consumer;
- a full mailbox drops the new observation and increments `Dropped()`;
- each mailbox has independent capacity and loss state;
- there are no server-owned consumer goroutines or shutdown workers;
- consumers own processing/cancellation and cannot close the send side.

The local read model updates before mailbox offers. Required accounting is not
a mailbox consumer and cannot be made best-effort by consumer failure. This is
an observational attachment point, not a durable collection guarantee. A future
reporter can consume local aggregation or an explicitly aggregated observation
stream without changing byte measurement. It must not treat dropped input as
zero usage or replay observations into the strict ledger.

## Dashboard/billing separation

`/account` calls the usage read model and existing storage measurements. It no
longer calls `billingSnapshot`, selects a rate card, updates billing-preview
high-water, or creates mock billing events merely to display usage.

When billing is enabled, `/account?billing=preview` explicitly retains the
existing mock preview. That preview still reads accounting facts; metrics-only
or unmetered traffic is not magically converted into accounted usage. It must
not be interpreted as complete charging evidence for a non-strict deployment.

`/ops` uses the selected local/strict read model for account and repository
egress. Volatile counters are labeled since server start, not as 30-day or
entitlement-period counters. With billing disabled, the billing panel says so
rather than displaying misleading zero lifecycle counts.

## Validation and known baseline failures

Before extraction:

- `go test ./...`: failed only `TestPublicBootstrapAndVersionEndpoints`, which
  expects the stale installer repository ID.
- `go test -race ./... -count=1 -timeout=60s`: same baseline failure.
- `go vet ./...`: passed.

The unrelated installer assertion is intentionally unchanged. Full-suite green
status must not be claimed while it fails.

The non-destructive `tests/qualification.sh` was also attempted with an explicit
local port, disposable state/key, and the local TPA checkout. Both this extraction
and a clean archive of the baseline stop with HTTP 413 on a later publication,
then a JSON parse error in the script. It does not reach full APT qualification.
No quota was raised and no production state was touched to bypass that failure.

Added tests exercise actual handlers and database effects, including SQLite
`PRAGMA data_version` to observe even transient begin/complete commits. Existing
strict settlement, uncertainty, real reopen/operator recovery, grant-boundary,
throttling, and publication process-death tests remain unchanged. See the change
report for exact final validation commands/results.

### Final validation result

- `go test ./...`: the same single baseline installer-ID failure; no new failure.
- `go test -race ./... -count=1 -timeout=60s`: the same single failure, no race diagnostic.
- `go vet ./...`: passed.
- `go test -race ./... -count=1 -timeout=60s -skip '^TestPublicBootstrapAndVersionEndpoints$' -json`:
  passed; 302 top-level test/example results across eight tested packages, with
  no other test skipped (two additional packages contain no tests).
- `node tests/ops-resource-test.js`: passed.
- `git diff --check`: passed.

New production-path tests:

- `TestLocalDeliveryUnmeteredAndMetricsNeverAccount`
- `TestLocalDeliveryMetricsHTTPSemantics`
- `TestLocalDeliveryActualPartialWritesAndStrictOrdering`
- `TestLocalDeliveryConsumersNeverBlockAndPolicyIsFrozen`
- `TestLocalDeliveryAuthorizationAndFileAuthorityAllModes`
- `TestLocalUsageDashboardIndependentAndPreviewExplicit`
- `TestLocalDeliveryBillingDoesNotChangeMeasurementAndRestartResets`
- `TestLocalDeliveryConfigurationValidation`
- `TestLocalStorageObservationUsesCanonicalGauge`
- `TestLocalUsageOperatorDashboardReadsMetrics`
- `TestLocalDeliveryObservationDoesNotHideStrictUncertainty`
- `TestLocalDeliveryHTTPModesEquivalent`

Additional configuration, concurrency and dependency tests:
`TestDeliveryModeDefaultsAndValidation`,
`TestLocalAggregationAttributionAndConcurrency`,
`TestMeasurementDependencyBoundary`, and `Example_deliveryConfiguration`.

Existing test assertions were not changed. Only the shared `testServer` setup was
factored to allow configuration before route composition. In particular, the
real reopen/recovery suites (`TestEgressAdminCLIRecoversExactMarkerAcrossRestart`,
`TestEgressAdminCLIRecoversLiveSettlementFailure`,
`TestEgressUncertaintySurvivesRestartUntilExplicitRecovery`), store settlement
rollback/recovery-race tests, and existing quota/throttling tests all passed.

## Deferred

No remote reporter, Metering Protocol, meter signing identity/Ed25519, epoch,
sequence, ten-minute report, retry spool, acknowledgment, collector API,
Prometheus, SSH, nginx, remote storage, PostgreSQL, or live usage billing is added.
TPA is unchanged; the executable is not split.

Remaining work includes the release/schema-lineage repair, independent delivery
address/storage adapters, optional durable observational aggregation and collection,
metrics retention/cardinality policy, and separating fixed SQLite operator-summary
queries from their historical accounting joins. None is required for these local
compositions, and none is silently implemented here.
