# Capacity reservation policy

TPA.run serializes allocation decisions through `server.stagingMu` and uses
`reserveCapacityWithResources` before Hub-controlled filesystem growth. An upload
releases that mutex only after its physical reservation and incoming directory
exist; the untrusted body stream does not hold the global allocator lock.
There are no customer-quota reservations for incoming or queued data.

## Three separate resource authorities

See [Nano storage accounting](nano-storage-accounting.md) for the full contract.

1. **Customer entitlement:** final active canonical `.deb` file bytes summed
   account-wide. `checkStorageCapacity` applies the target-repository replacement
   delta against the resolved grant. Temporary bytes never enter this arithmetic.
2. **Service-owned active files:** the same candidate replacement calculation for
   all active repository regular-file bytes, with a budget of the payload grant
   plus 1,000,000 bytes shared technical headroom. This is not customer capacity.
3. **Temporary infrastructure:** incoming, queued input, work and candidate files
   consume global/per-account physical staging capacity, disk and inode headroom.

## Physical accounting

Incoming, work and candidate allocations are attributable to the account for
per-account temporary limits. They also consume the global staging budget.
The complete active baseline plus incoming payload is reserved before work
copying, including conservative allocation overhead. Identity-matching baseline
packages are replaced only in that disposable work copy, consistent with the
publication planner; different package versions remain present.

Global and per-account staging admission use allocated filesystem blocks, not
logical file lengths. Candidate output is bounded before the runner starts:
the production runner uses a fixed-size `tmpfs` mount with a bounded inode count,
then copies the completed tree into the host reservation. Writes beyond the data
or inode bound receive `ENOSPC` before host candidate growth. The post-copy block
allocation check is defense in depth. Request bytes, upload file count, publication
timeout and upload read timeout remain separate limits.

A same-filesystem candidate-to-generation rename is a kind-D ownership transition,
not a second allocation. The candidate reservation is cleared when the rename
succeeds. Active-file usage is observed through durable generation authority;
historical generations remain governed by retention and physical disk safety,
not by the customer payload entitlement or active-file budget.

## Publication allocations

| Allocation | Classification | Policy |
| --- | --- | --- |
| incoming upload / durable queued input | A | reserve before payload creation; retain actual bytes until processing/cleanup |
| worker work tree | A | reserve full baseline + incoming bytes before copying |
| candidate and its indexes/signing output | B (inside candidate) | file-data bound plus inode/directory allocation reserve; fixed-size filesystem sandbox |
| candidate to immutable generation | D | same-filesystem rename; no duplicate reservation |
| temporary `current.<job>` symlink | C | fixed-size directory metadata; removed on failure |

TPA repository output is written beneath the candidate directory; the configured
GPG home remains separate signing state. Reservations are process-local by design.
On restart, durable job/publication state and filesystem accounting are
authoritative; abandoned terminal transient trees are reconciled before new work
is admitted. Cleanup failures are retried without treating surviving files as
free physical capacity. No schema or persistent reservation table is introduced.
