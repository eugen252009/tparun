# Schema 5 release-cut qualification

## Scope and source

Worktree `/home/eugen/projekte/tpahub-schema5`, branch `fix/schema5-release-cut`.
Git base `94c226dae1999d27895d75fc14d27d790b0b5f2f` **plus accumulated uncommitted
implementation**; the base SHA alone does not identify this implementation.
TPA remained clean at `a5060659b747e75b201903f4828e065203526a9a`.
No commit, push, deployment or production access occurred.

The [schema contract](schema-upgrades.md) is authoritative. Schema 5 is the first
supported persistent baseline; Schemas 1–4 are rejected, never converted or wiped.
Historical upgrade scripts and upgrade-only tests were retired, not counted as
passing current migrations. Current fixtures exercise grants and paid activation
rather than a removed entitlement projection.

## Final gates

All commands passed unfiltered on the final production/test source:

- `go test ./...` — PASS (`/tmp/schema5-final-test.log`).
- `go test -race ./... -count=1 -timeout=60s` — PASS (`/tmp/schema5-final-race.log`);
  command package 57.974 seconds. The timeout was not increased.
- `go vet ./...` — PASS (`/tmp/schema5-final-vet.log`).
- `git diff --check` — PASS.
- `node tests/ops-resource-test.js` — PASS.
- CCC index refreshed successfully.

`sh tests/lineage-authority-probe.sh` passed actual public `serve` and `migrate`
rejection for frozen development revisions:

| Schema | Fixture source |
|---|---|
| 1 | `623adb1369407ebe0e13313f6f6f7afe67f93d75` |
| 2 | `4ff5a1e0ae433cb08ba15faa35cedfea3d9099d0` |
| 3 | `94c226dae1999d27895d75fc14d27d790b0b5f2f` |
| 4 | `1b226391c6beeaa1dbf3ab99e1dfbe8a82ef2b73` |

Database bytes, DDL and logical rows remained unchanged. Repeated Schema 5 public
migration was a no-op; a future Schema 6 marker was rejected without mutation.
Store tests additionally cover malformed metadata/structures, atomic initialization,
source identity/provenance, ownership, canonical grant combinations and reopening.
Log: `/tmp/schema5-rejection.log`.

## Fresh publication, APT and restart

`TPA_DIR=/home/eugen/projekte/tpa TPAHUB_TEST_PORT=18176 sh tests/qualification.sh`
passed with fresh Schema 5 state, real TPA/GPG, strict accounting, isolated CLI
registration/login/repository creation/publication, public and private signed APT
consumption, install/upgrade/downgrade/dependencies, reader revocation and restart.
The default publishing grant was asserted to be **5,000,000 bytes**. Durable rows
and publication hashes survived restart; persisted sessions and reader revocation
were checked. Log `/tmp/schema5-apt.log` redacts disposable credentials and ends
`SCHEMA 5 RESTART AUTHORITY QUALIFIED` / `REAL APT SELF-HOSTING QUALIFIED`.

## Qualification failures investigated, not hidden

- The missing-`.deb` publication failure reproduced in 4/10 race-enabled repetitions
  (`/tmp/schema5-publication-repeat.log`). A deterministic real HTTP partial upload
  then proved that production orphan cleanup deleted live reserved input
  (`/tmp/schema5-cleanup-before.log`). Because this demonstrably broke current
  publication ownership, a minimal guard now retains incoming reservations under
  the existing staging lock, alongside durable job ownership. No quota counting
  changed. The regression proves live retention, actual orphan removal, byte
  preservation and reservation-to-job handoff. It passed 20 race repetitions;
  real publication passed 10/10 afterward (`/tmp/schema5-cleanup-after.log`,
  `/tmp/schema5-publication-after.log`). This fixes the demonstrated deletion path,
  not a claim that every possible publication race has been eliminated.
- Physical accounting independently produced two observed fail-closed
  `507 capacity accounting underflow` responses in concurrent upload qualification
  (`/tmp/schema5-concurrency.log`). That scan/subtraction race remains unfixed.
- An ordinary HTTP egress test queried settlement before the handler finished and
  closed the response without consuming it. Its fixture now consumes the body and
  observes completion of the real handler, including deferred settlement, before
  checking the same exact durable byte totals. No accounting implementation or
  assertion totals changed. Twenty race repetitions passed
  (`/tmp/schema5-egress-repeat.log`).
- One intermediate full race run timed out after adding structural introspection.
  The immutable built-in reference is now cached in process; every instance is
  still freshly inspected read-only, with batched column/FK/index queries. Final
  unfiltered runs passed within the original deadline, albeit with limited margin.
- An earlier Firefox session timeout did not recur in final qualification; no
  browser/runtime fix is claimed.

## Preservation and remaining boundaries

Compared with the complete P0 worktree, client origin implementation, CLI/manage
code, R4 admission/recovery tests, local provisioning, product definitions and
`commerce.GrantService` were byte-identical. The 5 MB allowance is unchanged.
The original temporary input archive is no longer available; it is not claimed as
an available immutable review artifact.

P0's schema cut and local qualification gates are complete. This is not public
installer/fresh-user VM qualification, coherent backup/restore qualification or
production release approval. Remaining self-hosted beta work includes traceable
release artifacts, public installer/VM validation, repository-name CLI consistency,
APT credential path scope, awaited shutdown, coherent backup/restore, and the
separate physical-accounting concurrency issue. Hosted Stripe remains test-only
and is not a billing-none self-hosting dependency.
