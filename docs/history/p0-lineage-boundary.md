# Historical P0 lineage investigation and migration boundary

> **Historical context:** This document records disposable pre-release Schema 3/4 investigation. Schemas 1–4 were internal development schemas, not supported persistent user-data formats. Schema 5 is the first supported persistent schema; it rejects Schemas 1–4 rather than reviving their migration behavior. This document is retained as architecture and qualification rationale, not as a migration or deployment promise.
>
> The source worktree described below was explicitly **not release qualified**. Its conclusions were later superseded by the supported Schema 5 boundary documented in [`../schema-upgrades.md`](../schema-upgrades.md).

**P0 is not complete. Do not deploy this tree as a compatible Schema 4 upgrade.**
The independent migration boundary and installer-test repairs below are implemented.
No reconciled release schema or Schema 3/4 forward migration is implemented.

## Baseline and isolation

- Worktree: `/home/eugen/projekte/tpahub-p0`, branch `fix/p0-release-lineage`.
- HEAD: `94c226dae1999d27895d75fc14d27d790b0b5f2f`.
- Input includes the entire uncommitted `/home/eugen/projekte/tpahub-r34` tree,
  including delivery, commercial separation, enforcement, provisioning, R3 and R4.
- Input archive: `/tmp/p0-input.tar`, SHA-256
  `6e314b4889633f65c8acfe745b470437dd7f0ca2b94a6ed1c1eaefd80e99e0ba`.
- Before changes, `go test ./...` failed only the stale installer-ID assertion.
- `bloom search "validate database schema authority before any migration mutation"`
  returned no results. Existing Store validation was reused and hardened; no new
  capability, authorization engine, provisioning policy or runtime plugin was added.
- All changes remain uncommitted and undeployed. No production access or TPA changes.

## Historical lineage reconstruction

The version-changing commits found by searching all available Git refs are:

| Revision | Schema | Change |
|---|---:|---|
| `623adb1` | 1 | Explicit migration/startup separation and recorded schema version. The original authority table contained only `version`. |
| `4ff5a1e` | 2 | Elimination of redundant purchase intent state. |
| `f07cd43` | 3 | Structural database constraints; `3220ac6` subsequently corrected fresh/migrated parity. |
| `a1315ad` | 4 | Source-scoped base-grant identities and coexisting migration/administrative grants. |
| `1b226391c6beeaa1dbf3ab99e1dfbe8a82ef2b73` | 4 | Immutable grant identity and fail-closed unknown-source handling. |

The current HEAD is **not a descendant** of qualified Schema 4. Schema 3 is a
separate surviving branch, not an intentional downgrade migration from Schema 4.

Schema 3 has one base row per customer/product/capability. Schema 4 adds a stable
`grant_id`, a uniqueness key including source, its identity immutability trigger,
and `multi-source-base-grants-v1`. Its bootstrap writes target administrative
source rows instead of overwriting another source. Its resolver recognizes
administrative grants and iterates multiple base rows. Unknown sources change
from ignored contributors to an error. Active paid or administrative grants
suppress the local fallback; bounded migration grants remain independent.

These are durable identity AND runtime authority differences. Restoring only the
DDL is insufficient; copying all historical runtime behavior is also unsafe.

## Reproduced authority conflict

`sh tests/lineage-authority-probe.sh` archives the two exact revisions, builds
against their actual production Store implementations, creates disposable
populated databases through account/purchase/payment APIs, and queries their
production `commerce.GrantService` implementation (`Store.GetGrant`). It compares
complete grants against the checked-in JSON under `tests/testdata/lineage/`.
No production database or patched copy of the historical resolver is used.

The fixture has a valid 10,000,000-byte local grant and a paid 5,000,000-byte grant:

| Point in time | Schema 3 authority | Qualified Schema 4 authority |
|---|---|---|
| Purchase active | 10,000,000 bytes; effective grant with local + purchase contributors | 5,000,000 bytes; purchase contributor only |
| Purchase expired | 10,000,000 bytes; original local grant | 10,000,000 bytes; original local grant |

The source, contributor list, validity and period-authority representation differ
as well as the limit. This is not invalid or future-dated malformed metadata: both
historical production implementations accept their own populated fixture.
The custom fixture allowance does **not** change the 5,000,000-byte product or
Mema allowance, configuration defaults, grant catalog or package qualification.

Consequences:

- Replaying Schema 4 over Schema 3 would reduce valid existing authority.
- Applying Schema 3 resolution to Schema 4 would increase valid existing authority.
- Disabling/deleting/relabeling local rows is not equivalent: they become active
  again after the purchase expires, and their provenance must remain intact.
- Rejecting all mixed Schema 3 accounts is not a complete forward upgrade path.

**Stopped decision:** there is no selected authoritative release schema yet.
A Schema 5 compatibility representation preserving each historical resolution
contract appears necessary if neither history may change authority. This would
require narrowly scoped resolver handling, not simply a schema-number bump.
That representation is not implemented or claimed approved under the prohibition
on redesigning GrantService. No grants have been rewritten to conceal the conflict.

## Implemented migration boundary

Startup and `Store.Migrate` now share the pre-mutation version gate:

- Future versions, including genuine historical Schema 4, are rejected by this
  still-Schema-3 binary before live-database DDL, migration transactions or writes.
- Version rows must be single, positive SQLite integers. Duplicate, missing,
  malformed, fractional and text metadata are rejected.
- Missing metadata on recognized already-migrated state is rejected, not treated
  as permission to recreate a historical database.
- Unmarked nonempty databases must have the compatible legacy account columns;
  unrelated databases are rejected. This is not a claim that every unversioned
  historical variant has received new qualification.
- Original version-only Schema 1 metadata remains accepted by explicit migration,
  using the existing transactional singleton upgrade. Ordinary startup still
  requires the current schema.
- Current-schema structural validation runs before initialization can repair a
  missing required table or trigger.
- Constraint probes now run against the **actual authority table DDL** in a
  disposable in-memory SQLite database. They no longer DELETE/INSERT the live
  schema authority under a savepoint. The same production validator accepts the
  genuine constraints and rejects absent constraints.

The historical probe additionally builds the current CLI: current Schema 3
`migrate` is a byte-for-byte and logical-digest no-op on its populated historical
fixture; genuine Schema 4 `migrate` rejects without changing either digest.
The initial probe exposed physical bookkeeping writes caused by the live
constraint probes; moving those probes to memory removed the writes, and the
original byte-identity assertion now passes.

`internal/store/migration_boundary_test.go` checks rejection before migration
checkpoints, full logical digest, database-file SHA-256, and `sqlite_master` DDL.
Fixtures contain accounts, grants, sessions, a reader credential, public/private
repositories, active publication authority, egress and operator identity.
The historical CLI fixtures additionally contain finalized purchase snapshots,
paid grant instances and provider evidence.

Two existing R11 recovery tests previously removed four immutability triggers and
relied on migration silently recreating them. Their fixture setup now captures
and restores the exact production trigger DDL after injecting missing snapshot
facts, **before** payment/recovery assertions. Their reconciliation, replay,
atomicity and restart assertions remain intact. Missing current triggers now
have an explicit negative migration test rather than an implicit repair path.

## Installer identity

`install.sh` remains the single source of the installer repository identity:
`a0d8fd0f82235ee431c41581aa253ec8`. Commit
`a962a8ddbd8d45b2dfda200ef14ec3ab29d92b73` intentionally selected the published CLI
repository; it is not interchangeable with the historical global bootstrap role.

The public endpoint test validates the canonical ID's format and the complete
served installer body against that source. The public-bootstrap qualification
script derives its expected APT repository from the same file and passes it as a
Docker build argument. No installer target, trust key or public artifact changed.
The public-network bootstrap script was syntax-checked, **not executed**, because
this task prohibits production access.

## Qualification obtained

All commands below passed on the modified tree, with no test filters:

```sh
go test ./...
go test -race ./... -count=1 -timeout=60s
go vet ./...
git diff --check
node tests/ops-resource-test.js
sh tests/lineage-authority-probe.sh
TPA_DIR=/home/eugen/projekte/tpa TPAHUB_TEST_PORT=18175 sh tests/qualification.sh
```

The final disposable APT run printed `REAL APT SELF-HOSTING QUALIFIED`: real
TPA/GPG publication, public/private signed APT access, reader revocation, restart,
installation, upgrade, downgrade, dependencies and CLI registration/removal.
R3/R4 tests and the unchanged 5 MB allowance were included in the ordinary/race
suites. No claim is made that this qualifies a Schema 3/4 forward migration.
Container APT qualification is not a fresh-user VM/public-installer qualification.

Logs: `/tmp/p0-before.log`, `/tmp/p0-final-test.log`, `/tmp/p0-final-race.log`,
`/tmp/p0-final-vet.log`, `/tmp/p0-apt.log`, `/tmp/p0-self-repeat.log`,
`/tmp/p0-concurrent.log`. APT credentials were redacted during capture.

Separate observations, not silently fixed:

- Ten race-enabled repetitions of the fresh-process TPA publication test passed;
  the previous intermittent missing-`.deb` failure did not reproduce. Root cause
  is not established and it is not claimed fixed.
- Concurrent upload testing reproduced three fail-closed physical accounting
  underflows (507); exact-fit retry assertions passed. This independent physical
  reservation race remains, with no changes to its accounting or concurrency.

## Operational stop, backup and downgrade rules

There is **no approved release upgrade command for Schema 4 in this tree**.
`tpahub schema` reports 3. Historical unpatched Schema 3 migration binaries may
bypass future-version rejection: never run them against Schema 4 or newer state.
A schema-number bump alone is not a repair, and Schema 5 does not yet exist here.

Before any eventually qualified upgrade, stop the service and all writers; take
and verify a coherent whole-instance backup containing the database (and any
SQLite journal/WAL state), repository generations and packages, durable queued
input, local profile/environment, signing keys, external secrets/TLS material and
compatible binaries. Record binary/source identity and backup checksums. Keep the
old instance stopped while qualifying a restored disposable copy.

Rollback means restoring that coherent pre-upgrade backup with its compatible
binary, not starting an older binary against migrated data, modifying the version
row, or copying an older database over newer repository/publication state.
No production state or signing material was inspected or changed during this work.

## P0 checklist

| Requirement | Result |
|---|---|
| Complete accumulated isolated baseline; no deployment or policy changes | PASS |
| Relevant Schema 1/2/3/4 version commits and Schema 4 differences identified | PASS |
| Historical Schema 3/4 resolver conflict reproduced through production paths | PASS |
| One monotonic authoritative release lineage selected and implemented | **FAIL — blocked** |
| Populated Schema 3 and Schema 4 migrate forward without authority changes | **FAIL — no reconciled target** |
| Full auth/grants/publication/accounting/commercial preservation matrix after forward migration | **FAIL — not established** |
| Current Schema 3 no-op and historical Schema 4 rejection, real public CLI | PASS |
| Future/invalid metadata guard and observed rejection non-mutation | PASS for tested cases |
| Startup/migrate current-schema structural validation consistency | PASS for tested cases |
| New forward migration atomicity, immutable IDs and idempotence | **FAIL — not implemented** |
| Stale installer assertion resolved without changing installer policy | PASS |
| Full ordinary/race/vet/diff/resource checks | PASS |
| Complete available disposable APT and publication qualification | PASS, current Schema 3 only |
| Historical schema operations, backup and downgrade stop documented | PASS |
| Release schema, declared artifacts and upgrade documentation aligned | **FAIL — no release baseline** |
| Full P0 | **FAIL / NOT COMPLETE** |

Next decision: authorize a narrowly scoped Schema 5 compatibility representation
and resolver handling that preserves both historical contracts, or explicitly
approve a different policy with its account-level authority effects. Until then,
returning to UX/features or declaring release readiness would be premature.
