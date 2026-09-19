#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BASE=${TPAHUB_PUBLIC_REPOSITORY_URL:-https://tpa.run/r/tpahub-bootstrap/tpahub-bootstrap}
EXPECTED_VERSION=${TPAHUB_PUBLIC_VERSION:-}
EXPECTED_SHA256=${TPAHUB_PUBLIC_SHA256:-}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

command -v curl >/dev/null || { echo 'curl is required' >&2; exit 1; }
command -v gpg >/dev/null || { echo 'gpg is required' >&2; exit 1; }
command -v dpkg-deb >/dev/null || { echo 'dpkg-deb is required' >&2; exit 1; }

curl -fsSL "$BASE/dists/stable/InRelease" -o "$TMP/InRelease"
curl -fsSL "$BASE/dists/stable/Release" -o "$TMP/Release"
curl -fsSL "$BASE/dists/stable/main/binary-amd64/Packages" -o "$TMP/Packages"
curl -fsSL "$BASE/dists/stable/main/binary-amd64/Packages.gz" -o "$TMP/Packages.gz"
mkdir -m 700 "$TMP/gnupg"
GNUPGHOME="$TMP/gnupg" gpg --batch --quiet --homedir "$TMP/gnupg" --import "$ROOT/tpahub.asc" >/dev/null 2>&1
GNUPGHOME="$TMP/gnupg" gpg --batch --quiet --homedir "$TMP/gnupg" --output "$TMP/InRelease.payload" --decrypt "$TMP/InRelease" >/dev/null
cmp -s "$TMP/InRelease.payload" "$TMP/Release" || {
  echo 'InRelease payload does not match Release' >&2
  exit 1
}
gzip -t "$TMP/Packages.gz"
sha256=$(sha256sum "$TMP/Packages" | awk '{print $1}')
size=$(wc -c <"$TMP/Packages")
gzip_sha256=$(sha256sum "$TMP/Packages.gz" | awk '{print $1}')
gzip_size=$(wc -c <"$TMP/Packages.gz")
awk -v hash="$sha256" -v size="$size" '$1 == hash && $2 == size && $3 == "main/binary-amd64/Packages" { found=1 } END { exit found ? 0 : 1 }' "$TMP/Release"
awk -v hash="$gzip_sha256" -v size="$gzip_size" '$1 == hash && $2 == size && $3 == "main/binary-amd64/Packages.gz" { found=1 } END { exit found ? 0 : 1 }' "$TMP/Release"

pkg_block=$(awk 'BEGIN{RS=""} $1 == "Package:" && $2 == "tpahub" {print; exit}' "$TMP/Packages")
[[ -n "$pkg_block" ]] || { echo 'public repository does not contain Package: tpahub' >&2; exit 1; }
version=$(printf '%s\n' "$pkg_block" | sed -n 's/^Version: //p')
filename=$(printf '%s\n' "$pkg_block" | sed -n 's/^Filename: //p')
package_sha=$(printf '%s\n' "$pkg_block" | sed -n 's/^SHA256: //p')
[[ -n "$version" && -n "$filename" && -n "$package_sha" ]] || { echo 'tpahub package metadata is incomplete' >&2; exit 1; }
curl -fsSL "$BASE/$filename" -o "$TMP/tpahub.deb"
[[ "$(sha256sum "$TMP/tpahub.deb" | awk '{print $1}')" == "$package_sha" ]] || { echo 'published package hash mismatch' >&2; exit 1; }
[[ "$(dpkg-deb -f "$TMP/tpahub.deb" Package)" == tpahub ]] || { echo 'published package identity mismatch' >&2; exit 1; }
[[ "$(dpkg-deb -f "$TMP/tpahub.deb" Architecture)" == amd64 ]] || { echo 'published package architecture mismatch' >&2; exit 1; }
[[ -z "$EXPECTED_VERSION" || "$version" == "$EXPECTED_VERSION" ]] || { echo "version mismatch: $version" >&2; exit 1; }
[[ -z "$EXPECTED_SHA256" || "$package_sha" == "$EXPECTED_SHA256" ]] || { echo "SHA-256 mismatch: $package_sha" >&2; exit 1; }

release_block=$(awk 'BEGIN{RS=""} $1 == "Package:" && $2 == "tpahub-release" {print; exit}' "$TMP/Packages")
[[ -n "$release_block" ]] || { echo 'public repository does not contain Package: tpahub-release' >&2; exit 1; }
release_filename=$(printf '%s\n' "$release_block" | sed -n 's/^Filename: //p')
release_sha=$(printf '%s\n' "$release_block" | sed -n 's/^SHA256: //p')
curl -fsSL "$BASE/$release_filename" -o "$TMP/tpahub-release.deb"
[[ "$(sha256sum "$TMP/tpahub-release.deb" | awk '{print $1}')" == "$release_sha" ]] || { echo 'published release package hash mismatch' >&2; exit 1; }
dpkg-deb --extract "$TMP/tpahub-release.deb" "$TMP/release-root"
grep -Fq 'URIs: https://tpa.run/r/tpahub-bootstrap/tpahub-bootstrap' "$TMP/release-root/etc/apt/sources.list.d/tpahub.sources" || { echo 'public release package uses non-canonical repository URL' >&2; exit 1; }

echo "PUBLIC DIST PASS: tpahub $version $package_sha"
