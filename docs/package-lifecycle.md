# Package lifecycle: unlist and delete

TPA.run exposes package unlisting and artifact deletion through the same TPA
lifecycle operations used locally. TPA alone parses and changes repository
metadata, signs the updated `InRelease`, and verifies the result. TPA.run supplies
a private bounded repository copy, independently validates the complete signed
candidate, records the operation, enforces quota and retention policy, and
atomically activates a new immutable generation. The active generation is never
modified in place.

## CLI

```sh
tparun package unlist https://tpa.run/r/OWNER/REPOSITORY PACKAGE VERSION ARCHITECTURE
tparun package delete https://tpa.run/r/OWNER/REPOSITORY PACKAGE VERSION ARCHITECTURE
# Explicit non-interactive confirmation:
tparun package delete https://tpa.run/r/OWNER/REPOSITORY PACKAGE VERSION ARCHITECTURE --yes
```

Identity is the exact Debian `Package + Version + Architecture` tuple. `unlist`
removes only that stanza from APT indexes and regenerates/signs repository
metadata; its `.deb` stays in the active pool and remains charged as published
package storage. `delete` requires an interactive `y`/`yes` confirmation or
`--yes`; it first publishes and verifies metadata without the package, then
removes the now-unreferenced `.deb` from the new generation. Unlisting does not
remove an already-installed package from client machines.

Each CLI invocation creates a fresh idempotency key. If a client loses a
response, it should repeat the same API request with the same key; the original
job/result is returned. A new attempt after a partial cleanup requires a new key.

## API

The authenticated owner or an authorized `repository.publish` delegate can submit
an operation:

```http
POST /v1/repositories/REPOSITORY_ID/packages/unlist
Authorization: Bearer SESSION_TOKEN
Idempotency-Key: 0123456789abcdef0123456789abcdef
Content-Type: application/json

{"package":"PACKAGE","version":"VERSION","architecture":"ARCH"}
```

Use `/packages/delete` for artifact deletion. A delete request must include
`"confirmed": true`; omission or `false` is rejected before any job is created.
The idempotency key is exactly 32 lowercase hexadecimal characters and is scoped
to repository and authenticated actor. Reusing a key for a different operation
or identity returns a conflict.

The response identifies the durable job and reports `state`, `generation_id`,
`metadata_state`, and `artifact_state`. Query
`GET /v1/publish-jobs/JOB_ID` for the full job and the `lifecycle` audit record.
A normal completion is `200`. A safe partial delete is `207 Multi-Status`; its
job is `failed`, but the verified unlisted generation is active. The job record
states `metadata_state: "published"`, `artifact_state: "cleanup_failed"`, and
includes the bounded TPA cleanup diagnostic. The artifact is retained and
unreferenced; retry delete with a new idempotency key. A failure before activation
leaves the previous generation authoritative and records the job as failed.

## Quota, signing and retained generations

Storage usage is derived from `.deb` files in the active generation. Unlisting
leaves the payload charged. Successful deletion reduces active payload usage
only after the new generation is active and the artifact is physically absent
from it. Previous generations are independent complete copies; their bytes may
continue consuming physical capacity until `TPARUN_MAX_GENERATIONS` retention
collects them. Lifecycle history is retained in the database independently of
old-generation cleanup so remaining unlisted artifacts can be distinguished
from unexpected pool files during verification.

Server lifecycle mutations currently require the local managed-signing backend
and its configured TPA executable and signing key. The service will not borrow
a client's private key. SSH publication workers and client-signed prebuilt
generations are therefore rejected for server-side lifecycle mutation; a client
that controls the signing key can instead make the lifecycle change with TPA and
submit the resulting complete generation through `publish-generation`.

Lifecycle jobs share the repository publication slot and generation lock with
publications and signer changes. They use the existing account authorization,
physical-capacity reservations, independent APT verification, and generation
retention policy. Schema 15 durably records actor, identity, confirmation,
idempotency, parent/result generations, artifact digest, metadata state and
cleanup outcome. See [schema upgrades](schema-upgrades.md) and
[capacity reservations](capacity-reservations.md).
