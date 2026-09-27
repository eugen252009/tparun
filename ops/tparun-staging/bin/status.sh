#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

compose ps
if ! compose exec -T app true >/dev/null 2>&1; then
  echo 'container is not running; recent logs:'
  compose logs --tail=80 app || true
  exit 1
fi

printf 'installed: %s\n' "$(installed_version)"
printf 'APT policy:\n'
compose exec -T app apt-cache policy tparun
if compose exec -T app dpkg -s systemd >/dev/null 2>&1; then
  echo 'systemd package: PRESENT'
else
  echo 'systemd package: absent'
fi

if curl --fail --silent --show-error --max-time 5 "$APP_URL/ready"; then
  printf '\nreadiness: PASS (%s/ready)\n' "$APP_URL"
  ready=0
else
  echo 'readiness: FAIL' >&2
  ready=1
fi
printf '\nrecent logs:\n'
compose logs --tail=50 app
exit "$ready"
