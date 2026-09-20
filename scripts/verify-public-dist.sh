#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BASE=${TPARUN_PUBLIC_REPOSITORY_URL:-https://tpa.run/r/tparun/bootstrap}
EXPECTED_VERSION=${TPARUN_PUBLIC_VERSION:-}
EXPECTED_SHA256=${TPARUN_PUBLIC_SHA256:-}
EXPECTED_SOURCES_SHA256=${TPARUN_PUBLIC_SOURCES_SHA256:-}
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

pkg_block=$(awk 'BEGIN{RS=""} $1 == "Package:" && $2 == "tparun" {print; exit}' "$TMP/Packages")
[[ -n "$pkg_block" ]] || { echo 'public repository does not contain Package: tparun' >&2; exit 1; }
version=$(printf '%s\n' "$pkg_block" | sed -n 's/^Version: //p')
filename=$(printf '%s\n' "$pkg_block" | sed -n 's/^Filename: //p')
package_sha=$(printf '%s\n' "$pkg_block" | sed -n 's/^SHA256: //p')
[[ -n "$version" && -n "$filename" && -n "$package_sha" ]] || { echo 'tparun package metadata is incomplete' >&2; exit 1; }
curl -fsSL "$BASE/$filename" -o "$TMP/tparun.deb"
[[ "$(sha256sum "$TMP/tparun.deb" | awk '{print $1}')" == "$package_sha" ]] || { echo 'published package hash mismatch' >&2; exit 1; }
[[ "$(dpkg-deb -f "$TMP/tparun.deb" Package)" == tparun ]] || { echo 'published package identity mismatch' >&2; exit 1; }
[[ "$(dpkg-deb -f "$TMP/tparun.deb" Architecture)" == amd64 ]] || { echo 'published package architecture mismatch' >&2; exit 1; }
[[ -z "$EXPECTED_VERSION" || "$version" == "$EXPECTED_VERSION" ]] || { echo "version mismatch: $version" >&2; exit 1; }
[[ -z "$EXPECTED_SHA256" || "$package_sha" == "$EXPECTED_SHA256" ]] || { echo "SHA-256 mismatch: $package_sha" >&2; exit 1; }

server=${BASE%%/r/*}
curl -fsSL "$server/tparun.sources" -o "$TMP/tparun.sources"
sources_sha=$(sha256sum "$TMP/tparun.sources" | awk '{print $1}')
[[ -z "$EXPECTED_SOURCES_SHA256" || "$sources_sha" == "$EXPECTED_SOURCES_SHA256" ]] || { echo "sources SHA-256 mismatch: $sources_sha" >&2; exit 1; }
grep -Fq 'Signed-By:' "$TMP/tparun.sources" || { echo 'public sources file does not embed a signing key' >&2; exit 1; }
key_start=$(grep -n '^ -----BEGIN PGP PUBLIC KEY BLOCK-----$' "$TMP/tparun.sources" | cut -d: -f1)
key_end=$(grep -n '^ -----END PGP PUBLIC KEY BLOCK-----$' "$TMP/tparun.sources" | cut -d: -f1)
sed -n "${key_start},${key_end}p" "$TMP/tparun.sources" | sed 's/^ //' | sed 's/^\.$//' >"$TMP/public.asc"
fingerprint=$(gpg --show-keys --with-colons "$TMP/public.asc" | awk -F: '$1=="fpr"{print $10; exit}')
[[ "$fingerprint" == EDCEE63AA803737C8ABBC1E4A34190A2829D5E54 ]] || { echo "sources signing fingerprint mismatch: $fingerprint" >&2; exit 1; }

echo "PUBLIC DIST PASS: tparun $version $package_sha"
