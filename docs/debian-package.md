# TPA.run Debian package

The `tparun` package is the release-layer handoff for self-hosted TPA.run. It recommends the separate `tpa` administrative/build CLI; TPA is not part of the serving runtime. The application binary is installed at `/usr/bin/tparun` and runs directly with `/usr/bin/tparun serve`, independent of PID 1 or systemd. The optional systemd unit is `/lib/systemd/system/tparun.service`. The package does not contain or manage instance data.

## Ownership boundary

- Package-owned: binary, optional unit and sysusers/tmpfiles integration files,
  default environment conffile, portable account/filesystem maintainer setup,
  and the Mema Manage manifest.
- Mutable instance state: `/var/lib/tparun/data` (database, repositories,
  incoming objects, publication state, and generations).
- Critical identity: `/var/lib/tparun/gnupg`.
- Host configuration: the conffile `/etc/tparun/tparun.env` and the host nginx
  configuration. Secret values belong only in the protected host environment
  file; they are never built into the package.
- Rebuildable state: APT `Packages`, `Release`, and `InRelease` metadata.

The service account is created portably by the package using Debian `adduser`/`addgroup` with dynamic IDs. Existing compatible `tparun` accounts are preserved; an account whose primary group conflicts with `tparun` causes a clear configuration failure. The post-install setup creates the state directories without recursive ownership changes and applies conservative modes. It does not invoke systemd tools or automatically enable/start/restart the unit. The systemd unit remains available for explicit host integration. Removing or purging the package does not remove `/var/lib/tparun` or its signing state; `/etc/tparun/tparun.env` follows normal dpkg conffile behavior.

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
signing home, and environment configuration in place. State setup follows a
strict ownership rule: an absent root is created for `tparun`; an existing
`tparun:tparun` root is preserved; an empty `root:root` `/var/lib/tparun`
mountpoint may be adopted by changing ownership of that directory only; any
incompatible non-empty or ambiguous state fails without recursive ownership
changes. A host-specific
path migration or systemd cutover is a separate authorized operation; the
package itself does not copy, delete, or migrate instance state.

The package's hard dependencies do not include systemd. Its maintainer setup
uses `adduser`, `addgroup`, `install`, `stat`, `chown`, and `chmod`; it never
calls `systemd-sysusers` or `systemd-tmpfiles`. The sysusers/tmpfiles snippets
are optional host integration data and are not needed for installation. The
Mema service recipe also declares a systemd target for Mema-enabled hosts; that
adapter is separate from package dependencies and the direct runtime, and
systemd-specific Mema management is unavailable on a no-systemd host. The unit
uses `User=tparun`, `Group=tparun`, `WorkingDirectory=/var/lib/tparun`,
`EnvironmentFile=/etc/tparun/tparun.env`, explicit `ExecStart=/usr/bin/tparun
serve`, and `Restart=on-failure`. Package installation does not start or enable
it.

The next beta package uses the next immutable release identity,
`0.1.5~beta5`, matching `internal/version.Current`; do not republish Beta4 or
use a source suffix in place of the next beta release. The runtime `/version`
endpoint separately reports product version, build revision, and schema. The
Debian `Mema-Schema` field is obtained by querying the just-built binary, and
the package build fails if the field does not match that binary. The systemd
unit description intentionally carries no schema number.

Build with:

```sh
TPARUN_VERSION=0.1.5~beta5 SOURCE_DATE_EPOCH=... \
  packaging/tparun/build.sh dist/tparun_0.1.5~beta5_amd64.deb
```

The build embeds `git rev-parse HEAD` as the runtime revision by default. For a
beta package, use a stable identifier that distinguishes the exact source and
package inputs; `git diff` alone omits untracked files. For example:

```sh
SOURCE_DIGEST=$(
  {
    find cmd/tparun internal packaging/tparun -type f -print0 \
      | LC_ALL=C sort -z | xargs -0 sha256sum
    sha256sum go.mod go.sum
  } | sha256sum | awk '{print $1}'
)
TPARUN_BUILD_REVISION="$(git rev-parse HEAD)+src.${SOURCE_DIGEST}"
```

Do not let a dirty build report only the base commit as if it identified all
packaged source.

## Docker state volumes

Mount a persistent volume at `/var/lib/tparun`. A fresh empty Docker volume is
normally `root:root`; package setup safely adopts only that empty mountpoint,
then creates `data` and `gnupg` for `tparun`. Existing compatible state is not
recursively changed. Non-empty incompatible state, symlinks, non-directories,
and foreign ownership fail closed. Qualify persistence by removing and
recreating a disposable container with the same named volume; a container
restart alone does not prove volume persistence.
