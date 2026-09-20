#!/usr/bin/env bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SOURCES=${TPARUN_SOURCES_PATH:-$ROOT/tparun.sources}
EXPECTED_SHA256=${TPARUN_SOURCES_SHA256:-d705adf0cc1943be3c46e11bab749710caaaf29e1d88fe956cdcd848cbfb073d}
EXPECTED_FINGERPRINT=EDCEE63AA803737C8ABBC1E4A34190A2829D5E54
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

sha256sum "$SOURCES" | grep -F "$EXPECTED_SHA256  $SOURCES"
start=$(grep -n '^ -----BEGIN PGP PUBLIC KEY BLOCK-----$' "$SOURCES" | cut -d: -f1)
end=$(grep -n '^ -----END PGP PUBLIC KEY BLOCK-----$' "$SOURCES" | cut -d: -f1)
sed -n "${start},${end}p" "$SOURCES" | sed 's/^ //' | sed 's/^\.$//' >"$TMP/public.asc"
fingerprint=$(gpg --show-keys --with-colons "$TMP/public.asc" | awk -F: '$1=="fpr"{print $10; exit}')
[[ "$fingerprint" == "$EXPECTED_FINGERPRINT" ]]

docker run --rm -v "$SOURCES:/tmp/tparun.sources:ro" debian:12 sh -eu -c '
  apt-get update -qq
  apt-get install -y -qq ca-certificates curl >/dev/null
  apt-get update -o Dir::Etc::sourcelist=/tmp/tparun.sources -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0 >/dev/null
'

# A tampered repository signature must not verify.
GNUPGHOME="$TMP/gnupg"; mkdir -m 700 "$GNUPGHOME"; GNUPGHOME="$GNUPGHOME" gpg --batch --import "$TMP/public.asc" >/dev/null 2>&1
curl -fsSL https://tpa.run/r/tparun/bootstrap/dists/stable/InRelease -o "$TMP/InRelease"
cp "$TMP/InRelease" "$TMP/tampered.InRelease"
printf '\n# tampered\n' >>"$TMP/tampered.InRelease"
! GNUPGHOME="$GNUPGHOME" gpg --batch --verify "$TMP/tampered.InRelease" >/dev/null 2>&1

printf '%s\n' 'TPA.RUN DEB822 SOURCES BOOTSTRAP QUALIFIED'
