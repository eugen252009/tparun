#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo 'install.sh must run as root' >&2
  exit 1
fi
SERVER=${TPAHUB_SERVER:-https://tpa.run}
RELEASE_PACKAGE_URL=${TPAHUB_RELEASE_PACKAGE_URL:-$SERVER/tpahub-release_0.1.3_all.deb}
RELEASE_PACKAGE_PATH=${TPAHUB_RELEASE_PACKAGE_PATH:-}
RELEASE_PACKAGE_SHA256=${TPAHUB_RELEASE_PACKAGE_SHA256:-759d05b1d450ae6ccfbb04a49b737aaf6e2abbd6af4e327318c339da5b8656cf}
SERVER=${SERVER%/}
case "$SERVER" in
  https://*) ;;
  http://*) echo 'warning: using insecure HTTP override for development' >&2 ;;
  *) echo 'TPAHUB_SERVER must use HTTPS' >&2; exit 1 ;;
esac
command -v apt-get >/dev/null || { echo 'apt-get is required' >&2; exit 1; }
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
release="$work/tpahub-release.deb"
if [[ -n "$RELEASE_PACKAGE_PATH" ]]; then
  [[ -f "$RELEASE_PACKAGE_PATH" ]] || { echo "TPAHUB_RELEASE_PACKAGE_PATH does not exist: $RELEASE_PACKAGE_PATH" >&2; exit 1; }
  cp -- "$RELEASE_PACKAGE_PATH" "$release"
else
  curl -fsSL --retry 3 -o "$release" "$RELEASE_PACKAGE_URL"
fi
actual=$(sha256sum "$release" | awk '{print $1}')
[[ "$actual" == "$RELEASE_PACKAGE_SHA256" ]] || { echo "TPA Hub release package checksum mismatch: expected $RELEASE_PACKAGE_SHA256 got $actual" >&2; exit 1; }
apt-get install -y "$release"
apt-get update
apt-get install -y tpahub
