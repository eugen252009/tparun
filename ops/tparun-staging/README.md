# NAS TPA.run APT staging

This is a prepared persistent NAS canary bundle, not a running container yet.
The NAS Docker daemon is currently inaccessible to the agent account, so no
container, image, network, or state was created or modified. See the repository
root `docs/nas-staging-apt.md` for the access finding, deployment checklist, and
manual commands.

## Runtime design

- Official `debian:trixie` image; no TPA.run binary or application `.deb` is in
  the image or this bundle.
- At first container start, `entrypoint.sh` installs only generic bootstrap
  tools, configures the official signed TPA stable source and LAN beta
  bootstrap, runs APT, and installs the selected `tparun` candidate.
- The beta bootstrap is fetched from
  `http://repo.home.arpa/lupricht-beta-repo_1.0_all.deb`; the stable TPA source
  file is fetched read-only from `https://tpa.run/tparun.sources`. The latter
  supplies the recommended `tpa` CLI and the stable APT history; no stable
  package or repository is modified.
- The persistent package conffile is loaded, then the process binds to
  `0.0.0.0:8082` inside the container. The NAS publish is restricted to
  `192.168.188.2:7050`.
- `setpriv` drops to the package-created `tparun` user and `exec`s
  `/usr/bin/tparun serve`; there is no systemd or wrapper process left in the
  steady-state process tree.
- `unless-stopped` restart policy; healthcheck calls local `/ready` and does
  not depend on Internet access.
- `./config` and `./data` are bind-mounted to `/etc/tparun` and
  `/var/lib/tparun`. The one-time storage preparation helper changes ownership
  only on the two empty mountpoint directories; it refuses non-empty paths and
  never recursively chowns state. Beta3 then adopts the empty root-owned data
  root using its qualified non-recursive package rule.

## Initial deployment (manual NAS Docker authorization required)

First inspect the exact container name and port manually because the agent
cannot query the daemon. Do not continue if `tparun-staging` already exists, if
7050 is occupied, or if either staging mount contains user data that has not
been explicitly reviewed:

```sh
DOCKER=/var/packages/ContainerManager/target/usr/bin/docker
sudo "$DOCKER" ps -a --filter 'name=^/tparun-staging$'
sudo "$DOCKER" ps -a --format '{{.Names}} {{.Ports}}'
sudo ss -lntp | grep -E '(^|[[:space:]])[^[:space:]]*:7050([[:space:]]|$)' || true
sudo find /volume1/docker/tparun-staging/config /volume1/docker/tparun-staging/data \
  -mindepth 1 -maxdepth 1 -print
```

If the exact container already exists, inspect and preserve it; do not let
Compose replace it. For a fresh empty layout, prepare only the mountpoint roots
and start the service:

```sh
sudo /volume1/docker/tparun-staging/bin/prepare-storage.sh
sudo /volume1/docker/tparun-staging/bin/deploy.sh
sudo /volume1/docker/tparun-staging/bin/status.sh
```

`deploy.sh` repeats the exact-name, empty/root-owned mountpoint, Compose
configuration, and port checks before pulling Debian and creating the service.
It refuses to replace an existing same-name container. The Compose project
creates only its own default bridge network; it does not join or alter other
NAS networks. `extra_hosts` maps the LAN repository hostname to the verified
NAS address `192.168.188.2`, avoiding dependence on Docker DNS for beta APT.
Container DNS access to external `tpa.run` still needs to be checked at first
startup.

## Management commands

Run these with NAS Docker authorization (for example, an authorized interactive
sudo session):

```sh
sudo /volume1/docker/tparun-staging/bin/status.sh
sudo /volume1/docker/tparun-staging/bin/update.sh
sudo /volume1/docker/tparun-staging/bin/pin-version.sh '0.1.5~beta4'
sudo /volume1/docker/tparun-staging/bin/shell.sh
# Explicit root shell only for container maintenance:
sudo /volume1/docker/tparun-staging/bin/shell.sh --root
sudo /volume1/docker/tparun-staging/bin/logs.sh
sudo /volume1/docker/tparun-staging/bin/restart.sh
sudo /volume1/docker/tparun-staging/bin/reset.sh
```

`update.sh` runs `apt-get update` and `apt-get upgrade -y` inside the running
container. It restarts that same container only if the installed TPA.run
version changed, then checks readiness. It does not rebuild the image or
recreate the container. `pin-version.sh VERSION` updates APT metadata, installs
that exact version (including an explicit downgrade when requested), and
restarts only if needed. `reset.sh` explicitly recreates only the container and
preserves both bind mounts; it is not a data reset. No helper deletes
`config/` or `data/`. A destructive data reset is intentionally not provided.

## APT promotion path

At current repository state, APT should select `0.1.5~beta4` from the LAN beta
source over stable `0.1.4`. The entrypoint requires a candidate at least
`0.1.5~beta4` and, by default, requires its origin to be LAN beta; this fails
closed instead of silently installing stable. The stable source uses its own
embedded `Signed-By` key and supplies `tpa` plus stable history. Later beta
upgrades happen inside the same running container with APT. Once `0.1.5` is
actually published to stable, the normal in-container `apt update && apt
upgrade` path should select it over `0.1.5~betaN`. This path does not run the
bootstrap entrypoint. If the container is intentionally recreated after that
promotion, set `TPARUN_STAGING_BOOTSTRAP_ORIGIN=stable` explicitly first; do not
publish stable as part of staging qualification.
