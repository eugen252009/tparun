# Capacity reservation policy

TPA Hub serializes allocation decisions through `server.stagingMu` and uses
`reserveCapacity` before Hub-controlled filesystem growth. An upload releases
that mutex only after its physical reservation and incoming directory exist;
the untrusted body stream does not hold the global allocator lock. In-flight
incoming directories with no logical reservation contribute their observed
partial bytes to later same-account logical admission checks.

## Accounting

Incoming and work bytes are attributable to the owning account. Candidate output
is **global transient infrastructure**: it is a temporary duplicate used to
verify a publication and is not customer-visible persistent repository storage.
It therefore consumes global staging capacity and physical disk headroom, but is
not charged a second time to the customer's persistent storage quota. The complete active baseline plus incoming payload is reserved before the work
copy starts, including a conservative directory-entry block per copied package.
Global and per-account staging admission use allocated filesystem blocks, not
logical file lengths. Candidate output is bounded before the runner starts;
the production runner uses a fixed-size `tmpfs` mount with a bounded inode count,
then copies the completed tree into the host reservation. Writes beyond the data
or inode bound receive `ENOSPC` before host candidate growth. The post-copy block
allocation check is defense in depth.
`checkStorageCapacity` independently enforces the resulting persistent repository
size.

A same-filesystem candidate-to-generation rename is a kind-D ownership
transition, not a second allocation. The candidate reservation is cleared when
the rename succeeds, and the generation's bytes are subsequently measured as
actual filesystem bytes.

## Publication allocations

| Allocation | Classification | Policy |
| --- | --- | --- |
| incoming upload | A | reserve before payload creation |
| worker work tree | A | reserve full baseline + incoming bytes before copying |
| candidate and its indexes/signing output | B (inside candidate) | file-data bound plus inode/directory allocation reserve; fixed-size filesystem sandbox |
| candidate to immutable generation | D | same-filesystem rename; no duplicate reservation |
| temporary `current.<job>` symlink | C | fixed-size directory metadata; removed on failure |

There are no external signing or metadata directories in the publication path;
TPA runner output is written beneath the candidate directory. Reservations are
process-local by design. On restart, durable job/publication state and
filesystem accounting are authoritative; abandoned terminal transient trees are
reconciled before new work is admitted.
