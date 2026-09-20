#!/usr/bin/env bash
set -euo pipefail

BASE=${TPARUN_BOOTSTRAP_REPOSITORY_URL:-http://127.0.0.1:18080/r/tparun/bootstrap}
EXPECTED_SHA256=${TPARUN_BOOTSTRAP_SHA256:-4ed23aaa0995432f3612c083ff9c5a4b960632ba1eb680e815fb5faf57b921cd}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

curl -fsSL "$BASE/dists/stable/InRelease" -o "$TMP/InRelease"
curl -fsSL "$BASE/dists/stable/main/binary-all/Packages" -o "$TMP/Packages"
block=$(awk 'BEGIN{RS=""} /Package: tparun-release/ && /Version: 0.1.5/ {print; exit}' "$TMP/Packages")
filename=$(printf '%s\n' "$block" | sed -n 's/^Filename: //p')
sha=$(printf '%s\n' "$block" | sed -n 's/^SHA256: //p')
[[ -n "$filename" && "$sha" == "$EXPECTED_SHA256" ]]
curl -fsSL "$BASE/$filename" -o "$TMP/bootstrap.deb"
echo "$sha  $TMP/bootstrap.deb" | sha256sum -c -
[[ "$(dpkg-deb -f "$TMP/bootstrap.deb" Package)" == tparun-release ]]
printf '%s\n' 'TPA.RUN PUBLIC BOOTSTRAP REPOSITORY QUALIFIED'
