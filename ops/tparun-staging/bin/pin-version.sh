#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

version=${1:?usage: bin/pin-version.sh VERSION}
printf '%s\n' "$version" | grep -Eq '^[0-9][0-9A-Za-z.+:~_-]*$' || {
  echo 'invalid Debian package version' >&2
  exit 2
}

before=$(installed_version)
echo "before: $before"
compose exec -T app apt-get update
compose exec -T app env DEBIAN_FRONTEND=noninteractive \
  apt-get install -y --allow-downgrades "tparun=$version"
after=$(installed_version)
echo "after:  $after"

if [ "$after" != "$before" ]; then
  echo 'TPA.run package changed; restarting the existing container (no recreation).'
  compose restart app
fi
wait_ready
"$(dirname "$0")/status.sh"
