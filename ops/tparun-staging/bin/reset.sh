#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

# Recreate only the container from the plain Debian base. Bind-mounted config
# and state remain in place. This never removes volumes or host data.
compose up -d --force-recreate app
wait_ready
compose ps
