# Run and update a self-hosted TPA.run service

This guide covers the Debian package installation path. It assumes a fresh host and an HTTPS hostname with a reverse proxy. TPA.run listens on loopback; do not expose its internal HTTP listener directly to the internet. Container deployment is a separate path; see [selfhost-container.md](selfhost-container.md).

## 1. Install the package

Install the TPA.run APT bootstrap, then install the application through APT:

```sh
curl -fL https://tpa.run/tparun-bootstrap.deb -o tparun-bootstrap.deb
```

Install the bootstrap package, refresh APT, inspect the candidate, then install TPA.run:

```sh
# When Mayi is installed:
mayi /usr/bin/apt install ./tparun-bootstrap.deb
mayi /usr/bin/apt update
apt-cache policy tparun
mayi /usr/bin/apt install tparun
```

Otherwise use the host's documented administrator workflow. Do not install a beta from the public stable repository. For beta qualification, configure the separate beta source and inspect `apt-cache policy tparun` before selecting an explicit version.

The bootstrap adds the signed stable APT source; it does not install or start the application. Install the `tparun` package only after confirming the intended repository and version. The package recommends the separate `tpa` CLI, which TPA.run invokes for repository generation.

## 2. Configure the instance

The package provides:

- Configuration: `/etc/tparun/tparun.env`
- Persistent database and repository state: `/var/lib/tparun/data`
- Signing-key home: `/var/lib/tparun/gnupg`
- Optional systemd unit: `tparun.service`

The package creates the `tparun` service account and paths. It does not initialize the instance profile or automatically enable/start the unit. Keep the data and signing key on persistent storage, restrict access to them, and do not replace or regenerate an existing signing key.

Before first start, edit `/etc/tparun/tparun.env` and set the values appropriate to the host. In particular:

```ini
TPARUN_PROFILE=self-hosted
TPARUN_LISTEN=127.0.0.1:8082
TPARUN_DATA_DIR=/var/lib/tparun/data
GNUPGHOME=/var/lib/tparun/gnupg
TPA_BIN=tpa
TPARUN_GPG_FINGERPRINT=<primary fingerprint of the instance signing key>
TPARUN_OPERATOR_RP_ID=<public DNS hostname, without scheme>
TPARUN_OPERATOR_ORIGIN=https://<public DNS hostname>
```

The packaged defaults disable account registration. Enable it only when you are ready to accept registrations and have chosen the intended self-hosted provisioning policy:

```ini
TPARUN_REGISTRATION_ENABLED=true
TPARUN_LOCAL_PROVISIONING_ENABLED=true
TPARUN_CAPABILITY_SOURCE=local
```

Choose local storage/egress limits and private-read policy deliberately; see [local capability provisioning](local-capability-provisioning.md). Do not enable public registration on an unprotected development instance.

The package environment file also exposes server-enforced publication and host
resource bounds. Review these alongside the filesystem capacity and expected
package sizes before enabling publication:

```ini
TPARUN_MAX_UPLOAD_BYTES=536870912
TPARUN_MAX_STAGING_BYTES=4294967296
TPARUN_MAX_ACCOUNT_STAGING_BYTES=1073741824
TPARUN_MAX_CANDIDATE_INODES=65536
TPARUN_MIN_DISK_FREE_BYTES=104857600
TPARUN_MIN_FREE_INODES=1024
TPARUN_MAX_GENERATIONS=5
```

These are enforced by the `serve` process, not just CLI planning. Upload bytes
are aggregate per request. Global/per-account staging values count allocated
blocks and reservations; the headroom settings reserve, rather than cap, free
filesystem resources. Setting the optional staging/headroom/retention values to
zero disables that guard/collection behavior and is generally not recommended.
Invalid values fail server startup. These safeguards cover TPA.run-controlled
allocations only; they are not kernel quotas and cannot reserve disk against
unrelated host processes. See [capacity reservation policy](capacity-reservations.md).

### Initialize and create a signing identity

Run initialization **only for a new, empty data directory**. It records the self-hosted profile; it does not migrate existing data or create a signing key. Run it as the `tparun` service account, with the packaged data path:

```sh
runuser -u tparun -- /usr/bin/env TPARUN_DATA_DIR=/var/lib/tparun/data /usr/bin/tparun init
```

If Mayi is installed, submit the equivalent exact command through Mayi, for example:

```sh
mayi /usr/sbin/runuser -u tparun -- /usr/bin/env TPARUN_DATA_DIR=/var/lib/tparun/data /usr/bin/tparun init
```

If initialization says the directory is not empty, stop. Do not delete or reinitialize it; determine whether it contains an existing instance.

For a new instance only, create its unattended signing key as `tparun` and record the primary fingerprint. Protect the private key; do not copy it to the package or a client machine.

```sh
runuser -u tparun -- /usr/bin/gpg --homedir /var/lib/tparun/gnupg \
  --batch --pinentry-mode loopback --passphrase '' \
  --quick-generate-key 'TPA.run repository signing' rsa3072 sign 0
runuser -u tparun -- /usr/bin/gpg --homedir /var/lib/tparun/gnupg \
  --batch --with-colons --list-secret-keys
```

When Mayi is installed, submit the key-generation command through Mayi, for example:

```sh
mayi /usr/sbin/runuser -u tparun -- /usr/bin/gpg --homedir /var/lib/tparun/gnupg --batch --pinentry-mode loopback --passphrase '' --quick-generate-key 'TPA.run repository signing' rsa3072 sign 0
```

Set `TPARUN_GPG_FINGERPRINT` in `/etc/tparun/tparun.env` to that key's **primary fingerprint**. Verify the configuration and permissions before starting. Keep this signing identity with the instance backups; losing it prevents the instance from signing repositories with the same identity.

## 3. Start and verify

The package does not start or enable systemd automatically. On a systemd host, use the approved administrator workflow to enable and start it:

```sh
# When Mayi is installed:
mayi /usr/bin/systemctl enable --now tparun.service
systemctl is-active tparun.service
```

Otherwise use the host's documented administrator workflow. On a host without systemd, run `/usr/bin/tparun serve` as the `tparun` account under the host's existing process supervisor, supplying the same environment as `/etc/tparun/tparun.env`. Do not start a second instance against the same data directory.

Check local readiness and version, then check them through the HTTPS reverse proxy:

```sh
curl -fsS http://127.0.0.1:8082/ready
curl -fsS http://127.0.0.1:8082/version
curl -fsS https://<public DNS hostname>/ready
curl -fsS https://<public DNS hostname>/version
```

`/ready` confirms the application is ready; `/version` reports the running version, source revision, and schema. A successful package install is not proof that the service started or that the new runtime is active. Configure the reverse proxy to terminate HTTPS and forward only to `127.0.0.1:8082`; the configured operator origin and relying-party ID must match the public HTTPS hostname.

After confirming TLS, origin settings, and registration policy, create the first account through the public HTTPS origin. Then create a repository and complete a small test publication and APT-client install before inviting users.

## 4. Update safely

Before an update, make a coherent stopped-instance backup. The recovery set includes the complete data directory, signing home, `/etc/tparun/tparun.env`, and the exact TPA.run and TPA binaries/build identities. See [backup and restore](backup-restore.md); snapshots and restore orchestration are outside this service guide.

Inspect the available candidate, then update only TPA.run (or pin the reviewed version):

```sh
apt-cache policy tparun
# When Mayi is installed:
mayi /usr/bin/apt update
mayi /usr/bin/apt install --only-upgrade tparun
# Or install a specific version after confirming it is available:
mayi /usr/bin/apt install tparun=<version>
```

Otherwise use the host's documented administrator workflow. On systemd hosts, restart the service after package upgrade; package installation alone does not promise activation:

```sh
mayi /usr/bin/systemctl restart tparun.service
systemctl is-active tparun.service
```

Without systemd, restart the supervised `/usr/bin/tparun serve` process using the host's documented process manager. Then verify `/ready`, `/version`, repository access, and a test APT update. Confirm `/version` reports the intended version/revision; do not infer activation from `dpkg-query` alone.

Do not assume an application downgrade is a safe rollback after a schema migration. Follow the [schema upgrade contract](schema-upgrades.md) and restore a compatible, verified backup if rollback is required.

## Boundaries

- TPA.run serves repositories and coordinates publication; TPA generates and verifies repository metadata.
- The package does not own or delete instance data when removed.
- APT package installation, service startup/restart, and runtime activation are separate steps.
- Public stable, internal beta, and production deployment are separate channels/actions. Qualify changes in an isolated instance before production use.
