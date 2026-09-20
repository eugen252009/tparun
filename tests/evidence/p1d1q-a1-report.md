# P1-D1Q-A1 — pending-purchase index parity investigation

## A. State

P1-D1Q-A1 PRE-EXISTING SCHEMA PARITY DEFECT REMEDIATED - READY TO COMPLETE Q-A

## B. Commits

```text
starting:
2e6daf62bce9e2621ef850bf9d8bb0683febcdb3

final:
PENDING (this report accompanies the remediation commit)

worktree:
clean after commit
```

## C–F. Semantics and dependency

`billing_purchases_pending` is durable unique authority: at most one
`(account_id, product_id, provider, billing_environment)` row may have
`status='pending'`. It is not a performance-only index.

`internal/store/store.go:1817-1860` (`CreatePendingPurchase`) explicitly queries
for an existing matching pending purchase, returns the existing compatible
purchase, or supersedes/fails the prior row before inserting. The transaction
and unique index jointly protect concurrent admission. Payment finalization and
recovery operate on the persisted purchase identity; no code catches the index
name or generic UNIQUE error specially. Direct dependency by index name: **NO**;
semantic purchase admission dependence: **YES**.

Duplicate pending rows are **forbidden** by current purchase admission semantics.
They are not a supported durable state. Historical duplicate-pending fixtures
were not found in the committed tests; the schema-v1/v2 lineage can represent
rows until the authority is applied, but admission prevents them. No duplicate
rows were deleted, status-mutated, superseded, or repaired in this task.

## D. Provenance

`git log -S'billing_purchases_pending'` identifies first introduction at:

```text
340ef44 feat: add Stripe test Nano purchase flow
```

The index was present in that revision's fresh schema and in its migration path.
`85b3a25 refactor: add product-scoped commerce boundary` changed the intended
key from `(account_id, provider, billing_environment)` to
`(account_id, product_id, provider, billing_environment)` and retained the
migration creation. Exact c73ecd fresh schema creation omitted the index, while
its explicit migration at `internal/store/store.go:528` still created it. Thus
this is old intended authority exposed by fresh-v2 schema drift, not new P1-D1
authority.

## G. Lineage matrix

| Lineage | Schema | Index Present | Index Count |
|---|---:|---:|---:|
| Fresh c73ecd | 2 | NO | 37 |
| c73ecd v1→v2 | 2 | YES | 38 |
| Fresh current | 3 | YES | 38 |
| Fresh-v2→v3 | 3 | YES | 38 |
| Migrated-v2→v3 | 3 | YES | 38 |

All rows are disposable local databases. The historical v1→v2 run used the
exact c73ecd binary and migration code; no production data was accessed.

## H–J. Structural comparison and classification

Fresh v2 versus migrated v2 differs in exactly one structural item:

```text
billing_purchases indexes:
  absent in fresh v2
  billing_purchases_pending present in migrated v2
```

Columns, types, nullability, defaults, CHECKs, UNIQUE authority, triggers, and
FK definitions otherwise match. This is **CASE B**: an older intended authority
with fresh-vs-migrated schema drift.

Remediation: add the already-established pending-purchase unique index to the
current fresh-schema creation SQL. No v2 historical rows are changed; the v2→v3
migration continues to preserve pre-existing authority and does not add a new
business invariant.

Fresh current, fresh-v2→v3, and migrated-v2→v3 now have exact structural parity.
Permanent executable coverage is in `tests/p1d1-structural-qualification.py`:
`fresh-migrated-current-schema-structural-parity` and
`current-schema-index-authority-parity` compare table metadata, columns, FK
metadata, trigger SQL, and PRAGMA index metadata (including autoindexes,
expressions, uniqueness, partial predicates, and normalized SQL).

## K–M. Authority

Pre-existing authority preserved: purchase primary identity, provider-event
identity, repository uniqueness, grant identity, snapshot identity,
provider/environment correlation uniqueness, provider-reference uniqueness, and
`billing_purchases_pending`.

New P1-D1 authority remains exactly the eleven approved CHECK constraints:
accounts status; repository visibility; purchase status; provider-event status;
three independent cadence checks; publish state; publish cleanup state; egress
uncertain state; and nonnegative egress reserved bytes.

## N–T. Preserved qualification

- Invalid migration matrix: **11 / 11 PASS**; rejected fixtures remain schema 2,
  unchanged, integrity OK, FK clean.
- Valid-domain matrix: **30 / 30 PASS**; expected 30, tested 30, missing 0,
  extra 0.
- Triggers: **13 pre / 13 post**, semantic differences NONE.
- FKs: **21 pre / 21 post**, semantic differences NONE.
- Index authority: **38 expected / 38 fresh / 38 migrated**, missing 0, extra 0.
- Business facts: preserved.
- Provider, environment, provider_state, event type, grant source, product,
  offer, and capability identifiers remain extensible.
- No product-policy leakage.
- Current schema startup no-op: PASS.
- P0-B/current startup: PASS.

Existing rollback regression remains: Q1 PASS; Q2 PASS; Q3-A PASS; Q3-B PASS
(28/28); Q3-C PASS.

## U. Validation

The following passed after remediation:

```text
bash -n deploy/deploy-tpahub
./tests/deploy-rollback-integration.sh
python3 tests/p1d1-structural-qualification.py
go test ./cmd/tparun -count=1
go test ./internal/store -count=1
go test ./internal/commerce -count=1
go test ./internal/payments/... -count=1
go test ./internal/tpahubproduct -count=1
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go list -deps ./...
go mod verify
git diff --check
node tests/ops-resource-test.js
./tests/qualification.sh
(cd ../tpa && go test ./... -count=1)
(cd ../tpa && go test -race ./... -count=1)
(cd ../tpa && go vet ./...)
```

## V. Findings

NONE. The previously observed blocker is remediated and classified as CASE B.
This does not claim complete P1-D1; Q-A structural completion still follows.

## W–Z. Remaining scope / TPA

```text
P1-D1Q-A:
finish structural completion gate

then

P1-D1Q-B:
exact c73ecd schema-v2→v3 rollback/WAL qualification

--extend:
NOT IMPLEMENTED IN THIS TASK
```

## AA–AD. Safety

```text
PUSH: NOT PERFORMED
PRODUCTION DEPLOY SCRIPT: NOT INSTALLED
DEPLOYMENT: NOT PERFORMED
PRODUCTION:
c73ecd8533da5a8efd2eeda019755a950489c6fe
schema 2
UNCHANGED
```

## AE. Final State

P1-D1Q-A1 PRE-EXISTING SCHEMA PARITY DEFECT REMEDIATED - READY TO COMPLETE Q-A
