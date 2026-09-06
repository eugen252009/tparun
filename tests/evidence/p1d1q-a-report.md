# P1-D1Q-A bounded qualification — stopped at structural-diff gate

## State

P1-D1 STRUCTURAL CONSTRAINT HARDENING NOT SAFE

Starting revision: `f07cd438c7a56a0298a8e855c54375f86304ba06`; worktree was clean.
No implementation, migration, deploy script, or production configuration changed.
This evidence commit does **not** close Q-A or qualify Q-B.

## Reproduce

```sh
python3 tests/p1d1-structural-qualification.py
```

Requires Go, Python 3 with SQLite, git, tar, and the exact c73ecd git object.
Builds both binaries in a temporary directory; binds serve only to loopback on
an ephemeral port. All databases are synthetic local fixtures, using the same
exact-release schema provenance as `make_v2_db` in the existing rollback harness.
No production database was accessed or copied. Fixtures and binaries are removed
on exit. The expected **current result is exit 1**, not an expected-failure PASS.
`p1d1q-a-execution.txt` records the final run, including exact inventories.

## Blocking finding / classification

The v2 database created by exact `c73ecd8533da5a8efd2eeda019755a950489c6fe`
normal startup does not contain `billing_purchases_pending`. The real explicit
v2→v3 migration adds:

```sql
CREATE UNIQUE INDEX billing_purchases_pending
ON billing_purchases(account_id,product_id,provider,billing_environment)
WHERE status='pending';
```

Pre: 37 indexes. Post: 38 indexes. All existing identities and definitions are
preserved; this additional unique partial index violates the required
“only eleven CHECK additions” structural-diff contract. It strengthens authority,
not merely formatting or lookup behavior. It is created in the shared explicit
migration path (`internal/store/store.go:541`), before structural reconstruction.
The same statement existed in the old release's migration path; its absence on
old fresh startup is not a test adapter deleting an index. No index, trigger,
or CHECK is disabled to construct these fixtures.

Per task section 39, implementation work stopped. The assertion has **not** been
relaxed, the old fixture has **not** been patched to manufacture a passing index
inventory, and no implementation fix has been attempted. Safety qualification
remains blocked; this is not evidence that production was damaged.

During fixture development, missing `account_metadata` caused its default row
to be populated on explicit migration. The final valid baseline includes this
supporting row as normal account creation does. The final business comparison
passes without filtering out any business table. Snapshot-set digests use the
actual persisted-template digest format, not placeholder strings.

## Invalid migration matrix

Every row below is an independently copied valid v2 schema, with all supporting
rows inserted under intact FKs/triggers. The target is introduced by INSERT
(or a permitted purchase-status UPDATE), not by bypassing immutability.
The resulting fixture differs from the valid baseline by exactly one durable
cell and violates exactly one of the eleven P1-D1 target predicates.

| Target | Invalid | Isolated | Rejected | Schema 2 | State preserved |
|---|---|---|---|---|---|
| accounts.status | unknown | YES | YES | YES | YES |
| repositories.visibility | internal | YES | YES | YES | YES |
| billing_purchases.status | unknown | YES | YES | YES | YES |
| billing_provider_events.status | unknown | YES | YES | YES | YES |
| product_grants.period_cadence | weekly | YES | YES | YES | YES |
| purchase_grant_snapshots.period_cadence | weekly | YES | YES | YES | YES |
| purchase_grant_instances.period_cadence | weekly | YES | YES | YES | YES |
| publish_jobs.state | paused | YES | YES | YES | YES |
| publish_jobs.cleanup_state | unknown | YES | YES | YES | YES |
| egress_accounting_uncertain.state | unknown | YES | YES | YES | YES |
| egress_accounting_uncertain.reserved_bytes >= 0 | -1 | YES | YES | YES | YES |

For every rejection: all table rows including schema authority unchanged; exact
metadata unchanged; canonical digest unchanged; integrity OK; FK clean.
Target values, row counts, and unrelated rows are included in the full-row
comparison. No repair or partial reconstruction survives rejection.

## Valid domain matrix

Each listed value was committed by direct SQLite writes to an independent fresh
v3 fixture, read back, and checked for integrity/FK cleanliness. Each value PASS:

| Column | Values (all PASS) |
|---|---|
| accounts.status | active; suspended |
| repositories.visibility | public; private |
| billing_purchases.status | pending; superseded; paid; failed; expired; reconciliation_required |
| billing_provider_events.status | processing; processed; reconciliation_required |
| product_grants.period_cadence | annual; monthly; continuous |
| purchase_grant_snapshots.period_cadence | annual; monthly; continuous |
| purchase_grant_instances.period_cadence | annual; monthly; continuous |
| publish_jobs.state | queued; running; succeeded; failed |
| publish_jobs.cleanup_state | complete; pending |
| egress_accounting_uncertain.state | streaming; uncertain |

Expected finite values: **30**. Tested: **30**. Missing: **0**. Extra: **0**.
The aggregate compares declared `(table,column,value)` tuples with successful
write/readback tuples; it is not a hard-coded boolean.
Reserved bytes: 0, 1, 1048576 accepted; -1 rejected by CHECK — PASS.
Purchase status tests retain provider_state `open` throughout construction.
`future_provider_state` separately accepted — PASS.
Future provider/environment/event/source/product/offer/capability identifiers
are present in the successfully committed fixtures — PASS.

## Exact reconstruction inventories

The execution transcript includes every pre/post trigger name, owning table,
and normalized definition; every index name, uniqueness, origin, partial flag,
`index_xinfo` (including expressions, collation, key/auxiliary columns), and SQL;
and every FK's source/target columns, update/delete actions, and match mode.
Autoindexes are included even when SQL is NULL. All 28 application tables are
inventoried, not only the nine tables containing the eleven targets.
Normalization preserves SQL string literals and removes formatting whitespace
and SQLite-added double quotes around identifiers outside literals.

Independent comparison of the recorded dictionaries:

- Triggers: **13 pre / 13 post; exact normalized definitions equal**.
- Indexes: **37 pre / 38 post; one addition above; no removal/modification**.
- Foreign keys: **21 pre / 21 post; definitions equal**.
- Existing purchase PK, provider-event composite PK, repository owner/name
  uniqueness, grant PK, snapshot PK, provider-account correlation PK, and
  purchase provider/environment/reference partial uniqueness are unchanged.
- Full table/CHECK-only comparison: **BLOCKED** at index assertion.
- Fresh schema exact-scope aggregate: **NOT COMPLETED** because execution stops
  at that gate; do not substitute the valid-domain tests for SQL scope proof.

Trigger identities (same pre/post):

```
billing_purchase_owner_immutable
billing_purchase_reconciliation_reason_immutable
billing_purchase_terms_immutable
migration_grants_commercial_terms_immutable
purchase_grant_instances_immutable_delete
purchase_grant_instances_immutable_update
purchase_grant_instances_no_unauthorized_insert
purchase_grant_snapshot_sets_immutable_delete
purchase_grant_snapshot_sets_immutable_update
purchase_grant_snapshot_sets_no_late_insert
purchase_grant_snapshots_immutable_delete
purchase_grant_snapshots_immutable_update
purchase_grant_snapshots_no_late_insert
```

## Production-derived schema fixture / business preservation

Exact-release-created synthetic v2 baseline migrated successfully to schema 3;
integrity OK; FK check clean. The all-table business snapshot is unchanged,
including accounts/metadata, purchase Status/ProviderState/terms, provider event
evidence, grants/sources, snapshots/sets/instances, repository ownership,
operator identity/credentials, publish jobs, and egress facts. Empty business
tables are also compared. Only `tpahub_schema` is excluded from the successful
business comparison, with its exact new value independently asserted.

This is schema provenance from the production release, not live production data.
The fixture's overall structural requalification nevertheless FAILS on the new
index. New fresh/migrated startup no-op assertions are implemented but not reached
following the mandatory stop. Existing startup tests passed in the Go suite.

## Provider-state inventory (informational, not a proposed domain)

Search: `rg -n 'ProviderState|provider_state' internal cmd --glob '*.go'`, plus
inspection of the event-kind mapping and fixture construction.

- v2/v3 fixture data: `open`; extensibility fixture: `future_provider_state`.
- Fresh schema and legacy missing-column migration default: `open`.
- Application/transition literals: `open`, `checkout_created`, `failed`, `paid`,
  `expired` (status-to-provider-state assignment).
- Recovery: `checkout_recovered_reconciliation_required`,
  `provider_success_unreconciled`.
- Generated recovery vocabulary: `provider_` + kind + `_unreconciled`, with
  kind `success`, `failed`, `expired`; hence `provider_success_unreconciled`,
  `provider_failed_unreconciled`, `provider_expired_unreconciled`.
- Recovery tests explicitly inspect `provider_success_unreconciled`.
- Empty Go input is defaulted to `open`; caller-supplied ProviderState is also
  persisted. These dynamic inputs mean this vocabulary is not a final domain.

Unique observed/derived nonempty vocabulary: `open`, `checkout_created`, `failed`,
`paid`, `expired`, `checkout_recovered_reconciliation_required`,
`provider_success_unreconciled`, `provider_failed_unreconciled`,
`provider_expired_unreconciled`, `future_provider_state`.
No provider_state constraint was added.

## Policy / P0-B / cumulative regressions

No generic schema or migration code changed, so new generic constraint policy
leakage: NONE. The test uses generic future identifiers. No new provider,
environment, grant-source, product, offer, capability, or event-type enum exists.
P0-B: existing cumulative tests retained; no new independent P0-B claim.
Existing rollback harness: Q1 PASS; Q2 PASS; Q3-A PASS; Q3-B PASS (28/28);
Q3-C PASS. This does not qualify the deferred exact-artifact v2→v3 WAL generation.

## Validation

All requested existing commands returned exit 0:

```
bash -n deploy/deploy-tpahub
./tests/deploy-rollback-integration.sh
go test ./cmd/tpahub -count=1
go test ./internal/store -count=1
go test ./internal/commerce -count=1
go test ./internal/payments/... -count=1
go test ./internal/tpahubproduct -count=1
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go list -deps ./...
go mod verify
node tests/ops-resource-test.js
./tests/qualification.sh
(cd ../tpa && go test ./... -count=1)
(cd ../tpa && go test -race ./... -count=1)
(cd ../tpa && go vet ./...)
git diff --check
```

New Q-A matrix: FAIL at structural-diff gate (recorded above). Existing green
regressions must not be interpreted as overriding this new executable failure.

## Remaining evidence

Q-A must first resolve/classify the unexpected unique-index addition and finish
its blocked scope/table/startup aggregates. Therefore Q-B is **not** the only
remaining evidence gap.

```
P1-D1Q-B:
exact schema-v2→v3 rollback and WAL/SHM qualification
using exact c73ecd8533da5a8efd2eeda019755a950489c6fe
previous release artifact
```

## Safety

PUSH: NOT PERFORMED. PRODUCTION DEPLOY SCRIPT: NOT INSTALLED.
DEPLOYMENT: NOT PERFORMED. Production was not accessed or modified.
Production remains as supplied: `c73ecd8533da5a8efd2eeda019755a950489c6fe`, schema 2,
UNCHANGED. The integration harness ran only its local adapters.

P1-D1 STRUCTURAL CONSTRAINT HARDENING NOT SAFE
