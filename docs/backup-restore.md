# Offline backup and restore boundary

A supported backup is a **stopped, whole-instance** copy. There is no online
SQLite-only backup command: stop TPA Hub, wait for process exit, and verify the
data-directory lock is released before copying.

## Required recovery set

Keep these items together under one backup identity:

- the complete `TPAHUB_DATA_DIR` (SQLite database, journals, profile,
  `repos/`, active generations, staging and queued publication input);
- the exact TPA Hub binary and exact compatible TPA binary, each with a
  SHA-256 digest and build identity;
- the environment/configuration needed to start the instance, including
  listener, limits, profile, delivery/enforcement and billing selections;
- the configured GPG home and fingerprint, or the signing key material required
  to reproduce the repository signatures;
- external secrets and operator recovery material, stored in the operator's
  separate encrypted secret store rather than in logs or this repository.

Do not copy a database while publication or cleanup is running. A database,
repository tree, and queued input copied from different points in time are not a
coherent backup. Preserve file modes: the data directory is `0700`, the
SQLite/profile files are `0600`, and signing material must remain private.

## Restore procedure

1. Provision a disposable host with compatible OS/runtime prerequisites and
   verify the recorded binary digests before use.
2. Stop the service and restore the complete data directory, configuration,
   signing material and compatible TPA binary into private paths. Do not merge
   with an existing data directory.
3. Set `TPAHUB_DATA_DIR` and all recorded non-secret configuration explicitly;
   restore secrets through the secret manager. Start the matching binary.
4. Require startup schema/integrity validation and `GET /ready` before opening
   traffic. A schema mismatch or missing signing material is a failed restore,
   not an invitation to migrate or recreate state.
5. Verify the restored account/session authority, grants, repository ownership,
   reader credentials and revocations, active generations and queued/pending
   jobs. Compare the pre-backup logical digest where available.
6. Publish a disposable package and consume public and private APT metadata and
   a package. Confirm the signature, credential isolation and post-restore
   upgrade path before considering the recovery usable.

The restore drill must be destructive and disposable: restore into an empty
location, exercise the real server process, then remove the location. Record
backup identity, source/artifact digests, restore duration, observed data-loss
window and any failed assertion without exposing credentials.

The disposable qualification in `tests/qualification.sh` performs this drill:
it creates a Schema 5 instance, archives the data/profile, GPG home, compatible
Hub/TPA binaries and manifest after clean shutdown, destroys the original data
and signing home, restores into a new directory, then verifies identity,
credentials, grants, generations, private/public access, post-restore
publication and APT upgrade. The backup archive is checksummed before restore.

This document defines the recovery contract; it is not a claim that the
self-hosted beta has completed every release gate. Installer and fresh-machine
qualification remain separate gates.
