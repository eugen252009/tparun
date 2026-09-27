#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

before=$(installed_version)
echo "before: $before"
compose exec -T app sh -ec 'apt-get update && DEBIAN_FRONTEND=noninteractive apt-get upgrade -y'
after=$(installed_version)
echo "after:  $after"

if [ "$after" != "$before" ]; then
  echo 'TPA.run package changed; restarting the existing container (no recreation).'
  compose restart app
fi
wait_ready
"$(dirname "$0")/status.sh"
