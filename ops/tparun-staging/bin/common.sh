#!/bin/sh
set -eu

STAGING_ROOT=${TPARUN_STAGING_ROOT:-/volume1/docker/tparun-staging}
DOCKER_BIN=${DOCKER_BIN:-/var/packages/ContainerManager/target/usr/bin/docker}
APP_URL=${TPARUN_STAGING_URL:-http://192.168.188.2:7050}

[ -x "$DOCKER_BIN" ] || { echo "Docker CLI not found: $DOCKER_BIN" >&2; exit 1; }

compose() {
  "$DOCKER_BIN" compose \
    --project-directory "$STAGING_ROOT" \
    --project-name tparun-staging \
    -f "$STAGING_ROOT/compose.yml" "$@"
}

installed_version() {
  compose exec -T app dpkg-query -W -f='${Version}' tparun
}

wait_ready() {
  i=0
  while [ "$i" -lt 300 ]; do
    if curl --fail --silent --show-error --max-time 3 "$APP_URL/ready" >/dev/null 2>&1; then
      echo "ready: $APP_URL/ready"
      return 0
    fi
    i=$((i + 1))
    sleep 2
  done
  echo "readiness failed: $APP_URL/ready" >&2
  compose logs --tail=100 app >&2 || true
  return 1
}
