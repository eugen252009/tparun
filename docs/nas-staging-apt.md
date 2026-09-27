# NAS TPA.run APT staging canary

## Current qualification state — 2026-09-26

The deployment bundle is prepared at `/volume1/docker/tparun-staging` and in
[`ops/tparun-staging`](../ops/tparun-staging/README.md). It is **not a running
container**. The agent account cannot access `/var/run/docker.sock` and
`sudo -n true` is denied. No password bypass was attempted, no NAS Docker
container/image/network was created or changed, and no production data or
credentials were copied.

NAS inspection established:

- Docker client `24.0.2`; Compose plugin `v2.20.1-6047-g6817716`.
- The client cannot reach the daemon: `/var/run/docker.sock` is `root:root`
  mode `0660`. Therefore daemon/server version, active container list, named
  volumes, and Docker network names/conventions could not be queried. The host
  has many Docker bridge interfaces, so no network assumptions should be made
  beyond the isolated Compose default network in this bundle.
- `/volume1/docker` is the existing per-service Compose layout. The staging
  path did not exist before this task; it now contains only the prepared
  deployment files and empty `config/` and `data/` bind-mount directories.
  Whether a container named `tparun-staging` already exists is **unknown** until
  an authorized Docker query is run. The deployment helper refuses an existing
  exact-name container.
- NAS addresses include `192.168.188.2` and `.3`. No listener on host port
  `7050` was found. Compose binds only `192.168.188.2:7050` to container
  `8082`, not all host interfaces.
- A request from the NAS host to
  `http://repo.home.arpa/beta/dists/beta/InRelease` returned HTTP 200. DNS
  resolution from a container was not testable; Compose supplies a narrow
  `extra_hosts` mapping of `repo.home.arpa` to the NAS `.2` address. DNS access
  to external `tpa.run` from containers remains unverified.

The files are designed for Debian Trixie with direct APT installation. They do
not build from source or contain a TPA.run executable or `.deb`. The official
`debian:trixie` image is used. At first start, the entrypoint installs generic
APT/cURL/certificate/`setpriv` tools, fetches the existing LAN beta repository
bootstrap, configures the official signed TPA stable APT source (needed for the
recommended `tpa` CLI and stable version history), and installs `tparun` from
APT. No public stable artifact or metadata is modified. The service runs as the
package-created `tparun` account through `/usr/bin/tparun serve`, without
systemd. Package config and state bind mounts are `/etc/tparun` and
`/var/lib/tparun`; the healthcheck uses internal `/ready`.

The package's Beta3 rule requires a fresh empty mountpoint to be root-owned.
Before first deployment, `bin/prepare-storage.sh` changes ownership only on the
two verified-empty host mountpoint directories. It fails on non-empty paths and
does no recursive chown. After installation, the state and config directories
are host-persistent; container recreation preserves them.

## Manual deployment after Docker authorization

First inspect the Docker daemon and confirm the name, port, and empty mounts.
Do not continue if an existing `tparun-staging` container or unreviewed data is
found; do not replace it. The commands below assume the Docker CLI path present
on this NAS:

```sh
DOCKER=/var/packages/ContainerManager/target/usr/bin/docker
sudo "$DOCKER" version
sudo "$DOCKER" ps -a --filter 'name=^/tparun-staging$'
sudo "$DOCKER" ps -a --format '{{.Names}} {{.Ports}}'
sudo "$DOCKER" network ls
sudo ss -lntp | grep -E '(^|[[:space:]])[^[:space:]]*:7050([[:space:]]|$)' || true
sudo find /volume1/docker/tparun-staging/config /volume1/docker/tparun-staging/data \
  -mindepth 1 -maxdepth 1 -print
```

For the known-fresh empty paths, initialize mountpoint ownership and deploy:

```sh
sudo /volume1/docker/tparun-staging/bin/prepare-storage.sh
sudo /volume1/docker/tparun-staging/bin/deploy.sh
sudo /volume1/docker/tparun-staging/bin/status.sh
```

On initial start, verify `apt-cache policy tparun` shows installed and candidate
`0.1.5~beta4`, the LAN beta URL, historical Beta3/Beta2/Beta1 entries, and stable
`0.1.4`; verify `/ready`, no systemd package, and the container health status.
The bootstrap requires the candidate origin to be LAN beta and the version to
be at least Beta4, so it fails closed rather than falling back to stable. The
stable source remains enabled for `tpa` and future promotion. After public
stable `0.1.5` is eventually published, the intended test is `apt update &&
apt upgrade` inside the existing container; this bypasses bootstrap selection
and follows normal APT ordering. If deliberately recreating after that
promotion, change `TPARUN_STAGING_BOOTSTRAP_ORIGIN=stable` explicitly first.

## Persistent lifecycle commands

```sh
sudo /volume1/docker/tparun-staging/bin/status.sh
sudo /volume1/docker/tparun-staging/bin/update.sh
sudo /volume1/docker/tparun-staging/bin/pin-version.sh '0.1.5~beta4'
sudo /volume1/docker/tparun-staging/bin/shell.sh
sudo /volume1/docker/tparun-staging/bin/logs.sh
sudo /volume1/docker/tparun-staging/bin/restart.sh
sudo /volume1/docker/tparun-staging/bin/reset.sh
```

`update.sh` performs APT update/upgrade inside the running container and
restarts that same container only if the TPA.run version changes. It does not
rebuild or recreate the container. `pin-version.sh VERSION` installs an exact
APT version (including an explicit downgrade if requested). `reset.sh` force-
recreates only the container while preserving `/etc/tparun` and
`/var/lib/tparun`; it never deletes staging data. A destructive state reset is
not implemented. Restart-policy behavior across an actual NAS Docker daemon
restart also remains untested; no NAS reboot or daemon restart was attempted.

## Results still requiring authorized execution

- Container existence/network preflight, image pull, first APT install and
  origin verification.
- Runtime PID, no-systemd assertion, `/ready`, and healthcheck.
- Persistence with the final NAS bind mounts across stop/remove/recreate/start,
  including config, database, data, and signing/repository state.
- In-container APT update/upgrade, exact-version pinning, restart policy after
  daemon restart, logs, and package history.

The separate local-Docker Beta3 named-volume and Beta2-to-Beta3 qualification
is recorded in `AGENTS.md`; it does not substitute for testing these final NAS
bind mounts.
