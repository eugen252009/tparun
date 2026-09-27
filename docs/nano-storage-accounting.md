# Nano published-package quota and service resources

## Product contract

Nano supplies exactly **5,000,000 bytes of published package storage** through the
account's resolved `repository.publish` grant (`NanoStorageBytes`). Customer usage
is the sum of regular canonical `.deb` file lengths in the current authoritative
generation of every repository owned by the account. Identical files in different
repositories count separately. It is not installed package size or disk allocation.

There is no per-repository storage allowance and no repository-count entitlement.
2,000,000 + 2,000,000 + 1,000,000 bytes in three repositories fits Nano; another
byte of active package payload does not.

The worker's authoritative check is:

```
resulting account payload = current account payload
                          - current target repository payload
                          + candidate target repository payload
```

The result must fit the current grant. Existing over-capacity data is not deleted;
non-growing/reducing corrections remain allowed. Errors for payload overage use
`storage-capacity-exceeded` and explicitly refer to package storage.

APT indexes, compressed indexes, Release/InRelease, signatures, other generated
metadata, SQLite rows, sessions, jobs, historical generations, incoming uploads,
queued input, in-flight reservations, work trees and candidates are NOT customer
package storage. Candidates are measured only as the proposed final state, not
added to the existing target repository or charged while temporary.

## Replacement behavior

The existing planner identifies packages by package name, version and architecture.
The worker now follows that same replacement contract: matching baseline packages
are removed from the disposable work copy before incoming files are copied. The
live generation is never changed in place. Other identities (including different
versions with the same original filename) remain present. Candidate validation,
upload/published digest matching and atomic authority promotion are unchanged.

This corrects a discovered gap: the planner previously projected a replacement,
but the worker preserved both conflicting identities and TPA rejected the result.
Bloom discovery returned no reusable capability for this need; existing planner
identity logic, stored artifact facts and `debIdentity` were composed instead.

## Internal active repository budget (not advertised capacity)

`checkActiveRepositoryCapacity`, called by the publication capacity guard, measures
logical regular-file bytes in all current authoritative repository generations.
It applies the same target-replacement delta. Its budget is:

```
resolved account payload grant + 1,000,000 bytes service technical headroom
Nano: 5,000,000 + 1,000,000 = 6,000,000 active file bytes
```

The extra amount is service-owned planning headroom, **not a customer entitlement**
and not a guarantee of usable metadata capacity. The independent package check
runs first: unused overhead can never permit 6 MB of `.deb` payload. A larger
resolved grant naturally increases this budget without repository migration.
Overflow of the budget addition saturates at the largest representable byte count;
measured-usage arithmetic still fails closed on overflow.

The budget is account-wide, not multiplied by repository count. Only active files
are measured: no staging, old generations, database rows, directory allocation or
GPG home. Existing over-budget active state may remain unchanged or shrink, but
cannot grow. Failure reports `repository generation exceeds service limits`, not
customer package overage. This is an admission bound, not an automatic deletion
policy or a hard filesystem quota. Disk/inode safeguards remain independently active.

**Multi-repository trade-off:** there is no fixed metadata cost per repository and
no count ceiling. Empty repositories cost zero active file bytes. Many independently
published repositories can exhaust shared metadata headroom before their package
payload reaches 5 MB. That is the explicit technical active-file resource limit,
not a promise that every possible subdivision of 5 MB can be published. Twenty
ordinary repositories are not assigned twenty separate headroom allowances.
A guarantee of arbitrarily many signed indexes within a fixed total overhead budget
would be impossible; this implementation does not make that guarantee.

## Temporary publication infrastructure

The durable queue and its real uploaded files are unchanged. Product capacity does
not limit upload admission; the worker checks the final candidate. A successfully
queued upload can therefore fail later on product capacity, with the old active
state preserved. CLI planning is advisory, never authoritative.

Physical controls remain: request-size and file-count bounds, global staging,
per-account staging, disk/inode headroom, candidate output bounds, publication
and upload timeouts, and cleanup/restart recovery. See
[capacity reservations](capacity-reservations.md). In particular, removing logical
input charging does NOT remove account attribution from physical staging limits.
CLI-local `TPARUN_STAGING_DIR` is unrelated and unchanged.

## Historical generations and recovery

The existing `TPARUN_MAX_GENERATIONS` policy is unchanged (default 5; a nonpositive
operator setting disables pruning). Retention can temporarily exceed the configured
count while nonterminal jobs defer collection. Once the queue drains, durable
cleanup tasks retry pruning. Failures remain observable and retry across restart.
Continuous backlog or persistent cleanup failure can prolong excess retention;
this is not a hard instantaneous history-byte cap. Physical disk safeguards remain.
Old generations are excluded from both customer quota and the active-file budget.

## Qualification

`nano_capacity_test.go` covers account-wide exact payload, another repository's
one-byte overage, persisted grant upgrade without repository changes, queued input
above Nano with physical account/global rejection, shared technical headroom,
pathological metadata rejection before promotion, and non-growing legacy overhead.

`nano_signed_publication_test.go` builds the real TPA executable, generates a local
GPG signing key and real extractable `.deb` packages, then exercises HTTP upload,
durable queue, the production worker/bounded runner, full signature/index/artifact
validation, promotion, and cleanup. Exact-sized packages use a trailing Debian ar
padding member; APT metadata is entirely real generated output. It covers:

- 5,000,000-byte initial package;
- different bytes with the same identity at full capacity;
- reduction to 4,000,000 bytes;
- growth through 4,800,000 to exactly 5,000,000;
- 5,000,001 rejected by the customer payload guard, preserving authority;
- another package version retained while replacing only the matching identity;
- actual measured payload, APT metadata and active total;
- retained history excluded from usage and pruned to the test policy;
- cleaned incoming/work directories and released reservations;
- The Nano correction left Schema 11 unchanged. Later Schema 12 adds publication-session state without changing Nano entitlement accounting.

Existing request/file/inode/disk, cleanup failure, restart, authorization, website,
store and generation-retention tests remain part of full qualification. Tests use
disposable state only. No production migration, pricing change, authentication
change, lifecycle change or future plan implementation is part of this correction.

### Observed qualification results

Base TPA.run revision: `894849d9a16a668ac68a7d61eeb40fe241a39619`.
Real TPA built from clean sibling revision:
`a5060659b747e75b201903f4828e065203526a9a`.
Environment: Go 1.26.4, Linux/amd64; all database/signing/package state disposable.

- `go test -count=1 ./...`: PASS (also repeated with JSON reporting: 775 passing
  test/subtest events, 9 tested packages; 6 packages have no tests, no test skips).
- `go vet ./...`: PASS.
- `git diff --check`: PASS.
- `node tests/ops-resource-test.js`: PASS; website Go/browser tests also included
  in the full suite.
- `go test -race -count=1 ./cmd/tparun ./internal/store`: PASS. After extending the
  signed sequence with version-retention cases, the final Nano boundary suite was
  also rerun under the race detector: PASS.
- Real initial Nano generation: 5,000,000 payload bytes + 1,536 generated APT
  metadata bytes = 5,001,536 active bytes. Other single-package signatures/indexes
  varied by a few bytes. Two-version active payload of 5,000,000 bytes generated
  1,890 metadata bytes. No metadata sizes were mocked for these measurements.

No commit, deployment or database migration was performed. The working-tree
correction is not a claim that production has activated this behavior.
