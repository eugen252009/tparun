#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"
exec "$DOCKER_BIN" compose \
  --project-directory "$STAGING_ROOT" \
  --project-name tparun-staging \
  -f "$STAGING_ROOT/compose.yml" logs --tail=100 --follow app
