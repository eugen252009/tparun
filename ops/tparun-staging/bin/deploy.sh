#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"

name=$("$DOCKER_BIN" ps -a --filter 'name=^/tparun-staging$' --format '{{.Names}}')
if [ "$name" = tparun-staging ]; then
  echo 'tparun-staging already exists; inspect it rather than replacing it' >&2
  exit 1
fi

[ ! -L "$STAGING_ROOT" ] || { echo "refusing symlink staging root: $STAGING_ROOT" >&2; exit 1; }
for path in "$STAGING_ROOT/compose.yml" "$STAGING_ROOT/entrypoint.sh"; do
  [ ! -L "$path" ] && [ -f "$path" ] || { echo "invalid staging file: $path" >&2; exit 1; }
done
for path in "$STAGING_ROOT/config" "$STAGING_ROOT/data"; do
  [ ! -L "$path" ] || { echo "refusing symlink bind mount: $path" >&2; exit 1; }
  [ -d "$path" ] || { echo "missing staging bind mount: $path" >&2; exit 1; }
  [ "$(stat -c '%u:%g' "$path")" = 0:0 ] || {
    echo "fresh bind mount must be root:root: $path (run bin/prepare-storage.sh once)" >&2
    exit 1
  }
  first=$(find "$path" -mindepth 1 -maxdepth 1 -print -quit)
  [ -z "$first" ] || { echo "refusing non-empty first-deployment path: $path" >&2; exit 1; }
done

if ss -ltnH 2>/dev/null | awk '$4 ~ /:7050$/ { found=1 } END { exit !found }'; then
  echo 'NAS port 7050 is already listening; refusing deployment' >&2
  exit 1
fi
published_ports=$("$DOCKER_BIN" ps -a --format '{{.Ports}}')
if printf '%s\n' "$published_ports" | grep -Fq ':7050->'; then
  echo 'Docker already publishes host port 7050; refusing deployment' >&2
  exit 1
fi

compose config -q
compose pull app
compose up -d app
wait_ready
compose ps
