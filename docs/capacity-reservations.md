# Capacity reservation policy

TPA Hub serializes upload and publication allocation through `server.stagingMu` and
uses `reserveCapacity` before Hub-controlled filesystem growth.

## Accounting

Incoming and work bytes are attributable to the owning account. Candidate output
is **global transient infrastructure**: it is a temporary duplicate used to
verify a publication and is not customer-visible persistent repository storage.
It therefore consumes global staging capacity and physical disk headroom, but is
not charged a second time to the customer's persistent storage quota. The
candidate is bounded before the runner starts and checked again after production;
`checkStorageCapacity` independently enforces the resulting persistent
repository size.

A same-filesystem candidate-to-generation rename is a kind-D ownership
transition, not a second allocation. The candidate reservation is cleared when
the rename succeeds, and the generation's bytes are subsequently measured as
actual filesystem bytes.

## Publication allocations

| Allocation | Classification | Policy |
| --- | --- | --- |
| incoming upload | A | reserve before payload creation |
| worker work tree | A | reserve before copying |
| candidate and its indexes/signing output | B (inside candidate) | covered by conservative candidate bound |
| candidate to immutable generation | D | same-filesystem rename; no duplicate reservation |
| temporary `current.<job>` symlink | C | fixed-size directory metadata; removed on failure |

There are no external signing or metadata directories in the publication path;
TPA runner output is written beneath the candidate directory. Reservations are
process-local by design. On restart, durable job/publication state and
filesystem accounting are authoritative; abandoned terminal transient trees are
reconciled before new work is admitted.
