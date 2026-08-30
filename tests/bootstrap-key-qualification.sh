#!/bin/sh
set -eu
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
awk '/^-----BEGIN PGP PUBLIC KEY BLOCK-----/{seen=1} seen{print} /^-----END PGP PUBLIC KEY BLOCK-----/{exit}' install.sh >"$TMP/embedded.asc"
curl -fsSL https://tpa.lupricht.net/tpahub.asc >"$TMP/public.asc"
fingerprint(){
 home=$1; file=$2; mkdir -m 700 "$home"
 GNUPGHOME="$home" gpg --batch --import "$file" >/dev/null 2>&1
 GNUPGHOME="$home" gpg --batch --with-colons --list-keys | awk -F: '$1=="fpr"{print $10;exit}'
}
embedded=$(fingerprint "$TMP/embedded-home" "$TMP/embedded.asc")
public=$(fingerprint "$TMP/public-home" "$TMP/public.asc")
[ "$embedded" = "$public" ]
printf 'TPA HUB BOOTSTRAP KEY QUALIFIED (%s)\n' "$embedded"
