#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DIST=${1:-$ROOT/dist}
VERSION=${TPARUN_VERSION:-0.1.1-1}
RELEASE_VERSION=${TPARUN_RELEASE_VERSION:-0.1.5}
EPOCH=${SOURCE_DATE_EPOCH:-0}
ARCH=${TPARUN_ARCH:-amd64}
mkdir -p "$DIST/pool/main/t/tparun" "$DIST/pool/main/t/tparun-release" "$DIST/dists/stable/main/binary-$ARCH" "$DIST/keyring"
rm -f "$DIST/pool/main/t/tparun"/*.deb "$DIST/pool/main/t/tparun-release"/*.deb "$DIST/dists/stable/main/binary-$ARCH"/* "$DIST/dists/stable/Release" "$DIST/dists/stable/InRelease"

command -v go >/dev/null || { echo 'go is required' >&2; exit 1; }
command -v dpkg-deb >/dev/null || { echo 'dpkg-deb is required' >&2; exit 1; }
command -v dpkg-scanpackages >/dev/null || { echo 'dpkg-scanpackages is required' >&2; exit 1; }
command -v apt-ftparchive >/dev/null || { echo 'apt-ftparchive is required' >&2; exit 1; }
[[ -f "$ROOT/tpahub.asc" ]] || { echo 'tpahub.asc is required' >&2; exit 1; }
cp "$ROOT/tpahub.asc" "$DIST/keyring/tpahub.asc"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/DEBIAN" "$work/usr/bin"
revision=$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || printf unknown)
go build -trimpath -ldflags="-s -w -X github.com/eugen252009/tpahub/internal/version.BuildRevision=$revision" -o "$work/usr/bin/tparun" "$ROOT/cmd/tparun"
chmod 0755 "$work/usr/bin/tparun"
cat >"$work/DEBIAN/control" <<EOF
Package: tparun
Version: $VERSION
Section: net
Priority: optional
Architecture: $ARCH
Depends: ca-certificates
Maintainer: TPA.run <tparun@lupricht.net>
Description: TPA.run application
 Persistent APT repository and package publishing service.
EOF
find "$work" -exec touch -h -d "@$EPOCH" {} +
dpkg-deb --build --root-owner-group "$work" "$DIST/pool/main/t/tparun/tparun_${VERSION}_${ARCH}.deb" >/dev/null

SOURCE_DATE_EPOCH="$EPOCH" TPARUN_RELEASE_VERSION="$RELEASE_VERSION" \
  "$ROOT/packaging/tparun-release/build.sh" "$DIST/pool/main/t/tparun-release/tparun-release_${RELEASE_VERSION}_all.deb" >/dev/null

(cd "$DIST" && dpkg-scanpackages pool /dev/null >"dists/stable/main/binary-$ARCH/Packages" 2>/dev/null)
gzip -n -f -k "$DIST/dists/stable/main/binary-$ARCH/Packages"
{
  printf 'Origin: TPA.run\nLabel: TPA.run\nSuite: stable\nCodename: stable\nArchitectures: %s\nComponents: main\n' "$ARCH"
  (cd "$DIST" && apt-ftparchive release dists/stable)
} >"$DIST/dists/stable/Release"
if [[ -n "${TPARUN_DIST_GNUPGHOME:-}" && -n "${TPARUN_DIST_SIGNING_KEY:-}" ]]; then
  GNUPGHOME="$TPARUN_DIST_GNUPGHOME" gpg --batch --yes --pinentry-mode loopback --passphrase '' --local-user "$TPARUN_DIST_SIGNING_KEY" --clearsign --digest-algo SHA256 -o "$DIST/dists/stable/InRelease" "$DIST/dists/stable/Release"
elif [[ "${TPARUN_DIST_ALLOW_UNSIGNED:-0}" != 1 ]]; then
  echo 'set TPARUN_DIST_GNUPGHOME and TPARUN_DIST_SIGNING_KEY, or explicitly set TPARUN_DIST_ALLOW_UNSIGNED=1 for local fixtures' >&2
  exit 1
fi
cp "$ROOT/tparun.sources" "$DIST/tparun.sources"
printf '%s\n' "$DIST"
