# Self-hosted NAS container bundle

This bundle is a single TPA Hub container. It does not change the public Hub and
is not a NAS deployment. The image contains the pinned TPA Hub and TPA binaries;
it does not use a source checkout, host Go installation, host TPA binary, Mema
store, or developer configuration at runtime.

## Files

- `deploy/container/Dockerfile`: reproducible multi-stage amd64 image.
- `deploy/container/build.sh`: stages the Hub and sibling TPA source, requires a
  clean Hub worktree, builds and tags the image by the exact Hub SHA.
- `deploy/container/compose.selfhost.yml`: one-service Compose deployment.
- `deploy/container/.env.selfhost.example`: placeholders only.
- `deploy/container/entrypoint.sh`: safe first-run profile/key initialization.

## Runtime model

The container listens on HTTP `0.0.0.0:8080` internally. The NAS reverse proxy
must expose only the configured HTTPS origin and forward to that port. The
client must use the HTTPS origin; the CLI rejects remote plaintext HTTP. Do not
publish the container port directly to an untrusted network.

The image runs as UID/GID `10001:10001` by default. Both bind-mounted
persistent directories must be writable by that identity. Compose permits an
explicit UID/GID override when the NAS requires it.

`TPAHUB_OPERATOR_ORIGIN` is the canonical external origin used by the existing
WebAuthn/operator origin checks. `TPAHUB_OPERATOR_RP_ID` is its DNS hostname.
No forwarded header is trusted to invent an origin.

## Build

Build on an amd64 Linux machine with Docker and with the unchanged TPA checkout
next to this checkout. The resulting image is published separately to the
internal registry; NAS deployment should use the digest-pinned reference above,
not a local build tag:

```sh
cd tpahub-p1
deploy/container/build.sh
```

Set `TPA_DIR=/path/to/tpa` when the sibling path is different. The script stops
on a dirty Hub worktree, embeds the full Hub Git SHA in `BuildRevision`, and
produces `tpahub:<short-sha>` and `tpahub:selfhost-<version>`. Record the image
ID/digest and emitted binary checksum; use the short-SHA tag or digest in the
NAS `.env`, never `latest`.

## Internal registry image

The qualified image is published as:

```text
192.168.188.2:9006/tpahub-server:dfc79c0c45ca
192.168.188.2:9006/tpahub-server:selfhost-0.1.1
```

Both tags resolve to this immutable digest:

```text
192.168.188.2:9006/tpahub-server@sha256:5143d77d87d3aca81fa46d4af95100bc11e6bf410e936025e2ba6fceaff149ad
```

The example environment file uses the digest-pinned reference. The registry is
HTTPS and requires the existing Lupricht CA trust on the NAS Docker daemon. The
CA fingerprint is `04:49:9A:41:11:9D:D1:3C:E7:6E:90:04:9D:CA:2B:47:D1:17:B0:30:6D:F3:E0:84:2A:85:A7:C4:4D:50:33:6D`; install the approved CA through the NAS trust mechanism rather than disabling TLS verification. No registry signature/referrer was present, so image signature verification is **NOT QUALIFIED**; the digest and embedded build revision remain the provenance checks.

## First start / initialization

Create empty directories and make them writable by the container identity:

```sh
sudo install -d -o 10001 -g 10001 -m 0700 /srv/tpahub/config /srv/tpahub/data
cp deploy/container/.env.selfhost.example /srv/tpahub/.env
# edit /srv/tpahub/.env, especially image and external origin
sudo docker compose --env-file /srv/tpahub/.env \
  -f deploy/container/compose.selfhost.yml run --rm tpahub init
sudo docker compose --env-file /srv/tpahub/.env \
  -f deploy/container/compose.selfhost.yml up -d
```

`init` creates the existing self-host profile and a persistent GPG signing key
under `/config/gnupg` when one is absent. It never replaces an existing profile,
database, or key. A non-empty uninitialized data directory fails closed. The
entrypoint repeats the same checks on `serve`, so container recreation cannot
silently regenerate identity.

Check:

```sh
docker compose --env-file /srv/tpahub/.env -f deploy/container/compose.selfhost.yml ps
docker compose --env-file /srv/tpahub/.env -f deploy/container/compose.selfhost.yml exec tpahub tpahub version
curl -fsS https://packages.home.example/version
curl -fsS https://packages.home.example/ready
```

Register the first account through the external HTTPS origin. There is no
Stripe, payment provider, subscription, remote metering, or commercial
entitlement dependency in this profile.

## Persistent state and backup

| Path | Contents | Backup |
|---|---|---|
| `/config` | GPG home/signing identity and container-local configuration/home | Required |
| `/data` | `server-profile.json`, Schema 5 SQLite state, repositories, generations, staging and durable jobs | Required |

Stop the service cleanly before backing up both directories together:

```sh
docker compose --env-file /srv/tpahub/.env -f deploy/container/compose.selfhost.yml stop
sudo tar --xattrs --acls -czf tpahub-backup.tar.gz -C /srv/tpahub config data
```

Restore only into empty directories with the same UID/GID, verify the archive
and permissions, then run `up -d`. Never merge a database with a different
repository tree or copy state while publication/cleanup is active. The matching
image tag/digest, Hub binary, TPA binary, non-secret environment, and any
operator recovery material are part of the recovery record. See
[backup-restore.md](backup-restore.md) for the complete recovery contract.

## Reverse proxy requirements

- HTTPS certificate and DNS for `TPAHUB_EXTERNAL_ORIGIN`.
- Proxy only to the private container port.
- Preserve request method/body and WebSocket support is not required.
- Forward normal `Host`/scheme for logging, but do not use arbitrary forwarded
  headers to change the configured origin.
- Do not expose the internal HTTP listener as a remote client endpoint.

## Next external Debian client

Use the client binary against the exact HTTPS origin:

```sh
TPAHUB_SERVER=https://packages.home.example tpahub register
TPAHUB_SERVER=https://packages.home.example tpahub login
tpahub --server https://packages.home.example repo list
tpahub --server https://packages.home.example repo create client-test
tpahub --server https://packages.home.example publish client-test ./test.deb
tpahub --server https://packages.home.example repo add client-test --dry-run
sudo tpahub --server https://packages.home.example repo add REPOSITORY_ID
sudo apt update
sudo apt install PACKAGE_NAME
```

`repo add` is the only command in this flow that writes system APT files and
therefore needs root. Publishing and all account/repository API operations do
not need root. Private repositories additionally require their scoped reader
token.
