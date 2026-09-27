# Publication batching and repeated-work audit

## Current lifecycle

`tparun publish` loads and re-verifies the complete local staging manifest, then
creates one authenticated publication session containing the expected identities,
sizes, filenames, and SHA-256 values. The server performs normal publication
preflight once. The CLI sends files in sequential multipart requests of at most 64
files each. Accepted files are persisted under the session's incoming directory;
no batch is a publication job or can activate a generation. Finalization atomically
turns a complete session into one durable job.

The `maxUploadFiles = 64` check remains enforced for every request and rejects
part 65 with HTTP 400. The legacy direct-upload endpoint also retains its ordinary
single-job behavior. For a finalized session, the worker copies the entire active
`.deb` baseline plus all session batches, removes identity replacements in the
work copy, invokes TPA once for the complete resulting repository, validates the
full signed candidate, and atomically swaps the active-generation pointer with
rollback on failure. No intermediate generation is exposed.

## Waiting and admission

This checkout has no `tparun wait` command. `check --wait` polls one durable job;
normal `publish` calls `waitForPublication`, polling every second for up to ten
minutes. `publish --no-wait` returns after the single job is queued and preserves
local staging. The worker claims durable jobs serially. Upload admission uses the
server's existing per-request physical reservations, global/per-account staging
bounds, disk and inode checks; there is no separate queue-capacity query or wait
endpoint. Timeouts or client interruption do not cancel an already queued job.

## Repeated work and persisted verification

The local staging manifest stores SHA-256 values but `stagedPlanInputs` re-reads
and verifies every staged artifact when publishing. Session preflight still
re-walks and re-hashes active `.deb` files once. Each accepted batch hashes only
its newly received files, validates them against the session manifest, and records
its batch digest and artifact identities. Retries of a committed batch are
idempotent; a different payload for the same batch number is rejected. Finalizing
copies the session artifact records to the one job's `publication_artifacts`.
These rows are per-publication evidence, not a global immutable object store and
not authority to skip candidate checks in a later job.

Every ordinary job reconstructs and republishes the complete active package set.
Candidate verification still checks the complete resulting repository, including
Release checksums and package identities. Identity correlation now indexes
expected artifacts by size and digest, keeping matching linear rather than
scanning every expected artifact for every pool file. Candidate output's tmpfs
byte bound also includes block-rounded payload allocation: an initial 1,000-tiny-
package qualification exposed that logical-byte-only bounds exhausted tmpfs well
before the configured inode limit. There is deliberately no reuse of uploaded
hashes to skip final candidate verification. Planning hashes active packages once;
candidate validation performs the necessary full-repository work once for the
final publication, not once per batch.

## Implemented session lifecycle

Schema 12 adds publication-session manifests, batch receipts, and accepted artifact
metadata. Session creation and finalization are authenticated and repository
scoped. At most one open session is permitted per authenticated publisher account.
Sequential batches reuse the existing upload admission and 64-file guard; only
complete finalization creates a job. Open session paths participate in physical
staging accounting and repository-busy checks. Same-digest batch retries are
safe; conflicting retries and out-of-order batches are rejected. A client that
exits normally attempts to abort; after an abrupt client loss, the session ID in
the next create-session conflict response can be deleted through the authenticated
session endpoint. An incomplete session is discarded on service startup (local
staged source files remain the client's recovery copy). Final candidate
generation, verification, and atomic activation are unchanged.

Sequential acknowledgement provides client-side backpressure: a later batch is
not sent until the prior request has been accepted. Admission failures are
returned to the CLI, which preserves local staging and attempts to abort the
session. There is no queue-wait endpoint and no `tparun wait` command in this
checkout; after finalization, normal `publish` polls the one durable job, while
`--no-wait` reports that single job as queued. A lost finalization response can
leave a queued job while local staging remains; the CLI reports the transport
failure rather than claiming publication success.
