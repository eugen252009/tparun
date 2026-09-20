#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
VERSION=${TPARUN_RELEASE_VERSION:-0.1.5}
REPOSITORY_URL=${TPARUN_RELEASE_REPOSITORY_URL:-https://tpa.run/r/tparun/bootstrap}
SUITE=${TPARUN_RELEASE_SUITE:-stable}
COMPONENT=${TPARUN_RELEASE_COMPONENT:-main}
KEY=${TPARUN_RELEASE_PUBLIC_KEY:-$ROOT/tpahub.asc}
OUTPUT=${1:-"$ROOT/dist/tparun-release_${VERSION}_all.deb"}
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-0}

[[ -f "$KEY" ]] || { echo "public key not found: $KEY" >&2; exit 1; }
command -v dpkg-deb >/dev/null || { echo 'dpkg-deb is required' >&2; exit 1; }
command -v gpg >/dev/null || { echo 'gpg is required' >&2; exit 1; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
install -d -m 0755 "$work/DEBIAN" "$work/usr/share/keyrings" "$work/etc/apt/sources.list.d"
gpg --batch --yes --dearmor --output "$work/usr/share/keyrings/tparun-archive-keyring.gpg" "$KEY"
chmod 0644 "$work/usr/share/keyrings/tparun-archive-keyring.gpg"
cat >"$work/etc/apt/sources.list.d/tparun.sources" <<EOF
Types: deb
URIs: ${REPOSITORY_URL%/}
Suites: $SUITE
Components: $COMPONENT
Signed-By: /usr/share/keyrings/tparun-archive-keyring.gpg
EOF
cat >"$work/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -eu
case "${1:-}" in
  remove|purge) rm -f /etc/apt/sources.list.d/tparun.sources ;;
esac
exit 0
EOF
chmod 0755 "$work/DEBIAN/postrm"
cat >"$work/DEBIAN/control" <<EOF
Package: tparun-release
Version: $VERSION
Section: net
Priority: optional
Architecture: all
Depends: ca-certificates
Maintainer: TPA.run <tparun@lupricht.net>
Description: TPA.run APT repository configuration
 This package installs the TPA.run repository source and scoped archive keyring.
 It does not contain or install the TPA.run application.
EOF
find "$work" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +
mkdir -p "$(dirname "$OUTPUT")"
dpkg-deb --build --root-owner-group "$work" "$OUTPUT" >/dev/null
printf '%s\n' "$OUTPUT"
