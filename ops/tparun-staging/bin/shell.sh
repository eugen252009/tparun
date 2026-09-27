#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

case "${1:-}" in
  '') user=tparun ;;
  --root) user=root ;;
  *) echo 'usage: bin/shell.sh [--root]' >&2; exit 2 ;;
esac

exec "$DOCKER_BIN" compose \
  --project-directory "$STAGING_ROOT" \
  --project-name tparun-staging \
  -f "$STAGING_ROOT/compose.yml" exec --user "$user" app /bin/sh
