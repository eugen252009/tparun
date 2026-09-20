#!/usr/bin/env bash
set -euo pipefail
DIST=${1:?usage: verify-dist.sh <dist>}
ARCH=${TPARUN_ARCH:-amd64}
PKG_INDEX="$DIST/dists/stable/main/binary-$ARCH/Packages"
[[ -f "$DIST/tparun.sources" ]] || { echo 'missing tparun.sources' >&2; exit 1; }
grep -Fq 'URIs: https://tpa.run/r/tparun/bootstrap' "$DIST/tparun.sources" || { echo 'sources file does not use canonical repository URL' >&2; exit 1; }
grep -Fq 'Signed-By:' "$DIST/tparun.sources" || { echo 'sources file does not embed a signing key' >&2; exit 1; }
[[ -f "$DIST/dists/stable/Release" ]] || { echo 'missing Release metadata' >&2; exit 1; }
[[ -f "$PKG_INDEX" ]] || { echo 'missing Packages metadata' >&2; exit 1; }
for package in "$DIST"/pool/main/*/*/*.deb; do
  [[ -f "$package" ]] || continue
  dpkg-deb --info "$package" >/dev/null
  filename=${package#"$DIST"/}
  grep -Fq "Filename: $filename" "$PKG_INDEX" || { echo "package missing from Packages: $filename" >&2; exit 1; }
  sha=$(sha256sum "$package" | awk '{print $1}')
  grep -A5 -F "Filename: $filename" "$PKG_INDEX" | grep -Fq "SHA256: $sha" || { echo "package hash mismatch: $filename" >&2; exit 1; }
done
grep -q '^Package: tparun$' "$PKG_INDEX" || { echo 'current tparun package missing' >&2; exit 1; }
grep -q '^Package: tparun-release$' "$PKG_INDEX" || { echo 'release package missing' >&2; exit 1; }
release_package=$(find "$DIST/pool/main/t/tparun-release" -name '*.deb' -print -quit)
release_root=$(mktemp -d)
trap 'rm -rf "$release_root"' EXIT
dpkg-deb --extract "$release_package" "$release_root"
grep -Fq 'URIs: https://tpa.run/r/tparun/bootstrap' "$release_root/etc/apt/sources.list.d/tparun.sources" || { echo 'release package does not use canonical bootstrap URL' >&2; exit 1; }
if [[ -f "$DIST/dists/stable/InRelease" ]]; then
  command -v gpg >/dev/null || { echo 'gpg is required to verify InRelease' >&2; exit 1; }
  keyring=$(mktemp -d)
  trap 'rm -rf "$release_root" "$keyring"' EXIT
  chmod 700 "$keyring"
  GNUPGHOME="$keyring" gpg --batch --quiet --import "$DIST/keyring/tpahub.asc" >/dev/null 2>&1
  GNUPGHOME="$keyring" gpg --batch --status-fd 1 --verify "$DIST/dists/stable/InRelease" >/dev/null
fi
printf '%s\n' 'TPA.RUN DISTRIBUTION SELF-CONSISTENCY PASS'
