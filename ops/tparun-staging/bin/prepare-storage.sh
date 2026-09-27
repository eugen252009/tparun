#!/bin/sh
set -eu

ROOT=${TPARUN_STAGING_ROOT:-/volume1/docker/tparun-staging}

[ "$(id -u)" -eq 0 ] || { echo 'run once with sudo; root ownership is required for safe fresh mounts' >&2; exit 1; }
[ ! -L "$ROOT" ] || { echo "refusing symlink staging root: $ROOT" >&2; exit 1; }

for name in config data; do
  path="$ROOT/$name"
  [ ! -L "$path" ] || { echo "refusing symlink mountpoint: $path" >&2; exit 1; }
  [ -d "$path" ] || { echo "missing staging directory: $path" >&2; exit 1; }
  first=$(find "$path" -mindepth 1 -maxdepth 1 -print -quit)
  if [ -n "$first" ]; then
    echo "refusing non-empty staging path: $path (found $first)" >&2
    exit 1
  fi
  # Change only the empty bind-mount root. Never recurse into staging state.
  chown 0:0 "$path"
  chmod 0750 "$path"
done

echo 'fresh empty config/data mountpoints are root:root; ready for first package install'
