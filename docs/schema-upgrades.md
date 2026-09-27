# Persistent schema and upgrades

**Schema 5 is the first supported persistent schema; Schema 12 is the current development schema.** Explicit migration supports Schema 5 through 11. Schemas 1–4 were internal,
disposable development schemas, with no supported user data or upgrade contract.
They are rejected, not migrated, reinterpreted or automatically deleted.

## Version handling

| State | `serve` / Store.Open | `migrate` |
|---|---|---|
| Fresh empty state | Creates current Schema 12 directly, atomically | Requires an initialized database |
| Schemas 1–4 | Reject: unsupported pre-release state | Same rejection, no mutation |
| Schema 5/6/7 | Reject until explicitly migrated to Schema 8 | Explicit atomic forward migration |
| Newer than this binary | Reject: use compatible binary/backup | Same rejection, no mutation |
| Missing/malformed metadata, unrelated or partial database | Reject without repair | Same rejection, no mutation |

`tparun schema` reports the supported database version. The configuration profile
version and application/package version are separate, unrelated numbers.
`internal/store/schema.sql` defines the durable structures; `CurrentSchemaVersion`
is the version authority. Fresh initialization installs this definition and records
8 in one transaction. Schema 5/6/7 upgrades preserve existing account, grant, repository, session, SSH credential, and operator rows. Schema 8 adds inert account lifecycle persistence (`membership_expires_at`, `purge_after`, and the validated `lifecycle_kind`) without enabling lifecycle enforcement. Startup validates tables, columns, constraints, foreign keys,
indexes and triggers, not just the version integer.

The singleton/version metadata header remains stable so older release binaries
can recognize and reject newer versions without modifying them.

Forward-compatibility guarantees begin at 5: future schema changes must provide
qualified, atomic, monotonic migrations **5 → 6 → 7 → 8 → …**. Schema 6 provides the
supported migrations, adding account SSH credential records, stable flags, audit history, session credential binding, and Schema 8 account lifecycle persistence. Never downgrade the version row or
run an older binary against a newer database. Restore a coherent matching backup
and compatible binary instead. Unpatched pre-release binaries are not subject to
this new compatibility contract and must not be used on release state.

## One grant rule

Application callers continue to use `commerce.GrantService`; there are no lineage
modes or legacy resolvers.

- Local policy and administrative grants are independent authority. Base identity
  is customer + product + capability + source; updates target that identity.
- Paid grants are immutable purchase/capability instances, activated from frozen
  snapshots and considered only when the associated purchase is paid.
- Only enabled grants active in their half-open validity window contribute.
  Disabled grants are not global deny overrides. Account suspension is checked by
  the existing application authorization boundary.
- Limits combine by **component-wise maximum**, never summation. A local 10 MB
  grant and active paid 5 MB grant yield 10 MB. Expiring the paid grant leaves the
  local authority intact. The product/Mema default remains **5,000,000 bytes**.
- Effective grants retain contributing provenance. Period ownership follows the
  winning egress limit, otherwise operation limit, otherwise storage limit. Ties
  use deterministic source/purchase/offer/valid-from/cadence ordering. Resolution
  is recomputed at the requested time; expired or future contributors are absent.
- No active contributor means denied. Unknown base sources are rejected; base
  grants cannot impersonate purchases. Identical facts resolve identically on
  every Schema 5 instance, including after restart.

## Durable model

Retained: accounts/classification, sessions, owned repositories, reader credentials,
publication jobs and active generations, source-scoped grants, immutable purchase
snapshots/instances and payment evidence, provider mappings/submissions, strict
usage/reservations/uncertainty, cleanup tasks, operator credentials/recovery and
audit records, and the bootstrap registry. Repository ownership is required.

Removed: the redundant `account_entitlements` projection, historical migration
ledger/materializers, legacy structural rebuilds and historical grant conversion.
The existing billing, provisioning, publication and accounting boundaries remain.

## Explicit development reset

For disposable Schema 1–4 state: **stop Hub and all writers**, back up anything
wanted, then manually move/remove the old state directory and initialize a fresh
one with `TPARUN_DATA_DIR=… tparun init`, followed by `serve`. Configure signing,
TLS and operator access normally. Hub never performs this destructive step.

For supported release state, take a stopped, coherent whole-instance backup:
database/journals, repositories/generations, queued input, configuration, signing
keys and external secrets, plus compatible binaries and source/artifact identities.
Qualify restore on a disposable copy before upgrading. A database-only copy taken
while publication continues is not a coherent instance backup.

## Qualification

See the [Schema 5 qualification report](schema5-qualification.md) for current
results, observed failures, narrowly scoped fixes and remaining release gaps.

`sh tests/lineage-authority-probe.sh` builds populated fixtures from frozen Schema
1–4 revisions solely to prove public `serve`/`migrate` rejection with unchanged
bytes, DDL and rows. It also proves Schema 5 rejection until migration, atomic Schema 5 → 6 migration, and current Schema 6 validation.
Store tests cover corrupt metadata/structures and canonical grants.
`tests/qualification.sh` exercises fresh init, authentication, real TPA/GPG,
publication, strict accounting, restart authority, private access/revocation and
container APT install/upgrade/downgrade. Containers are not fresh-user VM or public
installer qualification. Old pre-release upgrade qualification scripts are retired;
their historical evidence is not a supported upgrade promise.
