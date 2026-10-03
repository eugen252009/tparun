# Client-signed APT generation publication

This additive path accepts a complete TPA-built APT repository generation. It
is separate from the existing staged upload and managed-signing flow; the latter
remains supported and unchanged. Server-side package lifecycle mutation requires
the local managed signing key and is not available for client-signed generations;
clients can mutate a local complete tree with TPA and publish the replacement
through this endpoint. See [package lifecycle operations](package-lifecycle.md).

## Trust and ownership

- The authenticated TPA.run account must be authorized to publish to the
  repository. Existing repository policy, quotas, and lifecycle checks still
  apply.
- A repository owner registers one or more public OpenPGP APT signing keys.
  TPA.run imports and normalizes the public key, records its primary
  fingerprint scoped to that repository, and never accepts private key material.
- Each client generation must contain exactly one current `InRelease` signature
  verifiable by a currently registered full primary fingerprint. Expired keys,
  expired signatures, revoked-key status, unknown extra signatures, and multiple
  signatures are rejected. TPA.run independently checks the signature,
  Release/index checksums, package identities, and the exact inventory before
  storage. Registration/revocation is serialized with activation so a revoked
  signer cannot activate a concurrent upload.
- Signer registration accepts one armored public key (not secret-key armor) and
  permits at most 16 active signer records per repository. Expiration is checked
  at publication; registration itself does not reject an already expired public
  certificate. OpenPGP revocation packets present in the registered key are
  honored. TPA.run does not fetch later revocation certificates; use the
  repository signer-revocation API to revoke a stored trust record.
- SSH authentication, when used by the existing CLI, authenticates the account
  session; it is not the APT trust key. The OpenPGP APT key is what APT clients
  trust. No SSH remote or Git-like workspace is introduced.

Register, list, or revoke repository signers. Signer listing follows repository
visibility: public repositories expose their public-key records; private
repositories require the owning account. Registration and revocation are owner
only.

```sh
gpg --armor --export "$APT_FINGERPRINT" > apt-signing-public.asc
tparun signer add <repository-url> apt-signing-public.asc
tparun signer list <repository-url>
tparun signer revoke <repository-url> <primary-fingerprint>
```

Keep at least one trusted key registered while rotating. Register the new key,
publish a generation signed by it, update/verify consumer APT trust as needed,
then revoke the old key. Revocation prevents future client-signed publication;
it does not retroactively alter already activated repository content.

## Version 1 wire contract

The client sends `POST /v1/repositories/{repository-id}/generations` as a
streamed `multipart/form-data` body:

1. `manifest`: canonical, indented JSON followed by one newline.
2. `file:dists/stable/InRelease` as the first file part. TPA.run checks its
   declared size and SHA-256, then verifies its OpenPGP signer against the
   repository's currently registered keys before reading another part.
3. One field named `file:<relative-path>` for every remaining manifest file, in
   manifest order. File bytes are streamed without an extra filename component.

The version 1 manifest is UTF-8 JSON serialized with Go `json.MarshalIndent`
(two-space indentation, struct field order, HTML escaping) and exactly one final
newline. It contains `version`, `repository_id`, `generation_id`,
`parent_generation`, and a sorted, unique `files` array (`path`, `size`, and
lowercase SHA-256). The repository ID must match the route. Generation IDs are
32 lowercase hexadecimal characters; the parent is empty only for the initial
generation. Paths are relative slash-separated paths with no traversal,
backslashes, links, or special files. The inventory covers every regular file
in the repository tree and no file may be omitted or added. The manifest itself
is outside the tree and is not self-hashed. The manifest is limited to 16 MiB
and 65,536 files; paths are at most 4,096 bytes and 64 components, with at most
65,536 unique directories. Inventory and validation memory scale with file
count but remain bounded by these fixed limits. The whole multipart request is bounded by `MaxUpload`
(512 MiB by default); TPA.run also applies its configured upload-read timeout.
APT `Release` and `InRelease` files are separately size-bounded during
verification.

The expected parent is compare-and-swapped at activation. A durable SQLite
publication slot serializes each repository from admission through activation;
a slot is released on failure, atomically with successful activation, or during
startup recovery after an interrupted process. Managed publication workers skip
repositories with an active client slot. Different repositories can upload and
verify concurrently; only shared physical/account-quota accounting uses the
existing short process-local critical sections. A stale upload is rejected and
the live repository remains unchanged. Run a single writer process per shared
TPA.run data directory/database because filesystem reservations and storage
accounting are process-local. On success,
TPA.run stores the verified file bytes without rewriting them as an immutable
while-retained generation and atomically switches the active pointer. The
configured `MaxGenerations` retention policy may delete older generations; it
does not rewrite retained generations. The active generation is served through
the existing repository URL and accounted by the existing storage lifecycle.

## Build and publish

Use the repository ID returned by TPA.run and a fresh 32-character lowercase
hex generation ID. TPA builds and verifies the repository before writing the
inventory; select an APT OpenPGP signing key with `-gpg`:

```sh
tpa pack -in=artifacts -out=repo-generation -gpg="$APT_FINGERPRINT" \
  -generation-manifest=generation.json \
  -repository-id="$REPOSITORY_ID" \
  -generation-id="$(openssl rand -hex 16)" \
  -parent-generation="$ACTIVE_GENERATION"
tparun publish-generation "$REPOSITORY_ID" \
  --directory repo-generation --manifest generation.json
```

For the initial publication, omit `-parent-generation`. The client verifies its
local inventory and streams `InRelease` first; the server authenticates that
signer before reading the remaining files, then repeats complete inventory and
APT verification and enforces authorization, size/storage/inode quotas, signer
trust, and parent compare-and-swap. The manifest is an inventory, not an
independent signature. APT authenticity comes from `InRelease` and the
repository's trusted OpenPGP key. Server-side managed signing remains an
available, separate publication mode.
