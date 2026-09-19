#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DIST=${1:-$ROOT/dist}
VERSION=${TPAHUB_VERSION:-0.1.1-1}
RELEASE_VERSION=${TPAHUB_RELEASE_VERSION:-0.1.4}
EPOCH=${SOURCE_DATE_EPOCH:-0}
ARCH=${TPAHUB_ARCH:-amd64}
mkdir -p "$DIST/pool/main/t/tpahub" "$DIST/pool/main/t/tpahub-release" "$DIST/dists/stable/main/binary-$ARCH" "$DIST/keyring"
rm -f "$DIST/pool/main/t/tpahub"/*.deb "$DIST/pool/main/t/tpahub-release"/*.deb "$DIST/dists/stable/main/binary-$ARCH"/* "$DIST/dists/stable/Release" "$DIST/dists/stable/InRelease"

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
go build -trimpath -ldflags="-s -w -X github.com/eugen252009/tpahub/internal/version.BuildRevision=$revision" -o "$work/usr/bin/tpahub" "$ROOT/cmd/tpahub"
chmod 0755 "$work/usr/bin/tpahub"
cat >"$work/DEBIAN/control" <<EOF
Package: tpahub
Version: $VERSION
Section: net
Priority: optional
Architecture: $ARCH
Depends: ca-certificates
Maintainer: TPA Hub <tpahub@lupricht.net>
Description: TPA Hub application
 Persistent APT repository and package publishing service.
EOF
find "$work" -exec touch -h -d "@$EPOCH" {} +
dpkg-deb --build --root-owner-group "$work" "$DIST/pool/main/t/tpahub/tpahub_${VERSION}_${ARCH}.deb" >/dev/null

SOURCE_DATE_EPOCH="$EPOCH" TPAHUB_RELEASE_VERSION="$RELEASE_VERSION" \
  "$ROOT/packaging/tpahub-release/build.sh" "$DIST/pool/main/t/tpahub-release/tpahub-release_${RELEASE_VERSION}_all.deb" >/dev/null

(cd "$DIST" && dpkg-scanpackages pool /dev/null >"dists/stable/main/binary-$ARCH/Packages" 2>/dev/null)
gzip -n -f -k "$DIST/dists/stable/main/binary-$ARCH/Packages"
{
  printf 'Origin: TPA Hub\nLabel: TPA Hub\nSuite: stable\nCodename: stable\nArchitectures: %s\nComponents: main\n' "$ARCH"
  (cd "$DIST" && apt-ftparchive release dists/stable)
} >"$DIST/dists/stable/Release"
if [[ -n "${TPAHUB_DIST_GNUPGHOME:-}" && -n "${TPAHUB_DIST_SIGNING_KEY:-}" ]]; then
  GNUPGHOME="$TPAHUB_DIST_GNUPGHOME" gpg --batch --yes --pinentry-mode loopback --passphrase '' --local-user "$TPAHUB_DIST_SIGNING_KEY" --clearsign --digest-algo SHA256 -o "$DIST/dists/stable/InRelease" "$DIST/dists/stable/Release"
elif [[ "${TPAHUB_DIST_ALLOW_UNSIGNED:-0}" != 1 ]]; then
  echo 'set TPAHUB_DIST_GNUPGHOME and TPAHUB_DIST_SIGNING_KEY, or explicitly set TPAHUB_DIST_ALLOW_UNSIGNED=1 for local fixtures' >&2
  exit 1
fi
release_package="$DIST/pool/main/t/tpahub-release/tpahub-release_${RELEASE_VERSION}_all.deb"
release_sha=$(sha256sum "$release_package" | awk '{print $1}')
sed -e "s/tpahub-release_[0-9][^/ ]*\.deb/tpahub-release_${RELEASE_VERSION}_all.deb/" \
  -e "s/RELEASE_PACKAGE_SHA256=\${TPAHUB_RELEASE_PACKAGE_SHA256:-[0-9a-f]*}/RELEASE_PACKAGE_SHA256=\${TPAHUB_RELEASE_PACKAGE_SHA256:-$release_sha}/" \
  "$ROOT/install.sh" >"$DIST/install.sh"
printf '%s\n' "$DIST"
