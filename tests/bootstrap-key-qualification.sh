#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
SOURCE_DATE_EPOCH=0 "$ROOT/packaging/tpahub-release/build.sh" "$TMP/release.deb" >/dev/null
dpkg-deb -x "$TMP/release.deb" "$TMP/root"
fingerprint(){
  home=$1; file=$2; mkdir -m 700 "$home"
  GNUPGHOME="$home" gpg --batch --import "$file" >/dev/null 2>&1
  GNUPGHOME="$home" gpg --batch --with-colons --list-keys | awk -F: '$1=="fpr"{print $10;exit}'
}
packaged=$(fingerprint "$TMP/packaged-home" "$TMP/root/usr/share/keyrings/tpahub-archive-keyring.gpg")
source=$(fingerprint "$TMP/source-home" "$ROOT/tpahub.asc")
[ "$packaged" = "$source" ]
printf 'TPA HUB BOOTSTRAP PACKAGE KEY QUALIFIED (%s)\n' "$source"
