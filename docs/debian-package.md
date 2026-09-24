# TPA.run Debian package

The `tparun` package is the release-layer handoff for self-hosted TPA.run.
The application binary is installed at `/usr/bin/tparun`; its systemd unit is
`/lib/systemd/system/tparun.service`. The package does not contain or manage
instance data.

## Ownership boundary

- Package-owned: binary, unit, default environment file, sysusers/tmpfiles rules,
  and the Mema Manage manifest.
- Mutable instance state: `/var/lib/tparun/data` (database, repositories,
  incoming objects, publication state, and generations).
- Critical identity: `/var/lib/tparun/gnupg`.
- Host configuration: the conffile `/etc/tparun/tparun.env` and the host nginx
  configuration. Secret values belong only in the protected host environment
  file; they are never built into the package.
- Rebuildable state: APT `Packages`, `Release`, and `InRelease` metadata.

The service runs as the dedicated `tparun` user. `tparun.service` is shipped but
has no maintainer-script start or restart behavior. Removing the package does
not remove `/var/lib/tparun`.

## Mema Manage

The manifest is installed at `/etc/mema/recipe/manage/tparun.json`, which is the
manifest directory used by the currently deployed Mema 0.3.4 binary. It uses
only canonical package paths and declares the database/repository/publication
data and signing home as host state. The nginx target is a host integration
resource and is not packaged. The manifest has no production hostname, home
directory, secret value, or private key.

## Adoption

Adoption is deliberately a release-layer operation: install the package while
leaving the existing database, repository objects, publication generations,
signing home, and environment configuration in place. A host-specific path
migration or systemd cutover is a separate authorized operation; the package
itself does not copy, delete, or migrate instance state.

Build with:

```sh
SOURCE_DATE_EPOCH=... packaging/tparun/build.sh dist/tparun_0.1.2_amd64.deb
```
