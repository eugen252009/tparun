#!/usr/bin/env bash
set -euo pipefail
DIST=${1:?usage: verify-dist.sh <dist>}
ARCH=${TPAHUB_ARCH:-amd64}
PKG_INDEX="$DIST/dists/stable/main/binary-$ARCH/Packages"
[[ -f "$DIST/install.sh" ]] || { echo 'missing installer' >&2; exit 1; }
[[ -f "$DIST/dists/stable/Release" ]] || { echo 'missing Release metadata' >&2; exit 1; }
[[ -f "$PKG_INDEX" ]] || { echo 'missing Packages metadata' >&2; exit 1; }
grep -Fq 'https://tpa.run' "$DIST/install.sh" || { echo 'installer does not use canonical origin' >&2; exit 1; }
! grep -Fq 'tpa.lupricht.net' "$DIST/install.sh" || { echo 'installer contains legacy origin' >&2; exit 1; }
for package in "$DIST"/pool/main/*/*/*.deb; do
  [[ -f "$package" ]] || continue
  dpkg-deb --info "$package" >/dev/null
  filename=${package#"$DIST"/}
  grep -Fq "Filename: $filename" "$PKG_INDEX" || { echo "package missing from Packages: $filename" >&2; exit 1; }
  sha=$(sha256sum "$package" | awk '{print $1}')
  grep -A5 -F "Filename: $filename" "$PKG_INDEX" | grep -Fq "SHA256: $sha" || { echo "package hash mismatch: $filename" >&2; exit 1; }
done
grep -q '^Package: tpahub$' "$PKG_INDEX" || { echo 'current tpahub package missing' >&2; exit 1; }
grep -q '^Package: tpahub-release$' "$PKG_INDEX" || { echo 'release package missing' >&2; exit 1; }
if [[ -f "$DIST/dists/stable/InRelease" ]]; then
  command -v gpg >/dev/null || { echo 'gpg is required to verify InRelease' >&2; exit 1; }
  gpg --batch --status-fd 1 --verify "$DIST/dists/stable/InRelease" >/dev/null
fi
printf '%s\n' 'TPA HUB DISTRIBUTION SELF-CONSISTENCY PASS'
