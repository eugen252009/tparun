#!/bin/sh
set -eu

BETA_BOOTSTRAP_URL=${TPARUN_BETA_BOOTSTRAP_URL:-http://repo.home.arpa/lupricht-beta-repo_1.0_all.deb}
STABLE_SOURCES_URL=${TPARUN_STABLE_SOURCES_URL:-https://tpa.run/tparun.sources}

log() { printf '%s\n' "[tparun-staging] $*" >&2; }
fail() { log "ERROR: $*"; exit 1; }

bootstrap_tparun() {
  export DEBIAN_FRONTEND=noninteractive
  export APT_LISTCHANGES_FRONTEND=none

  log 'Installing generic Debian bootstrap tools; TPA.run is not in the image.'
  apt-get update
  apt-get install -y --no-install-recommends ca-certificates curl util-linux

  install -d -m 0755 /etc/apt/sources.list.d
  curl --fail --show-error --silent --location --retry 3 \
    "$STABLE_SOURCES_URL" -o /etc/apt/sources.list.d/tparun.sources.new
  mv /etc/apt/sources.list.d/tparun.sources.new /etc/apt/sources.list.d/tparun.sources

  curl --fail --show-error --silent --location --retry 3 \
    "$BETA_BOOTSTRAP_URL" -o /tmp/lupricht-beta-repo_1.0_all.deb
  apt-get install -y /tmp/lupricht-beta-repo_1.0_all.deb
  rm -f /tmp/lupricht-beta-repo_1.0_all.deb

  apt-get update
  policy=$(LC_ALL=C apt-cache policy tparun)
  printf '%s\n' "$policy" >&2
  candidate=$(printf '%s\n' "$policy" | awk '$1 == "Candidate:" { print $2; exit }')
  [ -n "$candidate" ] && [ "$candidate" != '(none)' ] || fail 'APT has no tparun candidate'
  printf '%s\n' "$policy" | grep -Fq 'http://repo.home.arpa/beta' || \
    fail 'LAN beta repository is absent from tparun APT policy; refusing installation'

  minimum=${TPARUN_STAGING_MIN_VERSION:-0.1.5~beta4}
  dpkg --compare-versions "$candidate" ge "$minimum" || \
    fail "APT candidate $candidate is older than the staging floor $minimum"
  candidate_origin=$(apt-cache madison tparun | awk -v v="$candidate" '$3 == v { print $5; exit }')
  case "${TPARUN_STAGING_BOOTSTRAP_ORIGIN:-beta}" in
    beta) expected_origin=http://repo.home.arpa/beta ;;
    stable) expected_origin=https://tpa.run/r/tparun/bootstrap ;;
    *) fail 'TPARUN_STAGING_BOOTSTRAP_ORIGIN must be beta or stable' ;;
  esac
  if [ "$candidate_origin" != "$expected_origin" ]; then
    fail "candidate $candidate is from ${candidate_origin:-unknown}, expected $expected_origin"
  fi

  log "Installing APT candidate $candidate from $candidate_origin."
  apt-get install -y tparun
  installed=$(dpkg-query -W -f='${Version}' tparun)
  [ "$installed" = "$candidate" ] || \
    fail "installed tparun $installed does not match APT candidate $candidate"
  command -v tpa >/dev/null 2>&1 || fail 'the recommended tpa CLI was not installed from the configured TPA.run repository'
}

if [ ! -x /usr/bin/tparun ]; then
  bootstrap_tparun
fi

[ -x /usr/bin/tparun ] || fail '/usr/bin/tparun is missing'
command -v tpa >/dev/null 2>&1 || fail 'tpa CLI is missing from PATH'
[ -r /etc/tparun/tparun.env ] || fail '/etc/tparun/tparun.env is missing'

if dpkg-query -W -f='${db:Status-Status}' systemd 2>/dev/null | grep -qx installed; then
  fail 'systemd must not be installed in this staging container'
fi

# Load the persistent package conffile, then bind the service on the container
# interface. The host port is separately restricted to NAS LAN address .2.
set -a
. /etc/tparun/tparun.env
set +a
TPARUN_LISTEN=${TPARUN_STAGING_LISTEN:-0.0.0.0:8082}
export TPARUN_LISTEN

uid=$(id -u tparun)
gid=$(id -g tparun)
log "Starting /usr/bin/tparun serve directly as tparun (uid=$uid gid=$gid), no systemd."
exec setpriv --reuid="$uid" --regid="$gid" --init-groups -- /usr/bin/tparun serve
