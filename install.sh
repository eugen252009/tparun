#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo 'install.sh must run as root' >&2
  exit 1
fi
SERVER=${TPAHUB_SERVER:-https://tpa.lupricht.net}
PACKAGE_PATH=${TPAHUB_PACKAGE_PATH:-}
PACKAGE_SHA256=${TPAHUB_PACKAGE_SHA256:-}
DEFAULT_REPOSITORY_ID="a0d8fd0f82235ee431c415ec8"
REPOSITORY_ID=${TPAHUB_REPOSITORY_ID:-$DEFAULT_REPOSITORY_ID}
SERVER=${SERVER%/}
case "$SERVER" in
  https://*) ;;
  http://*) echo 'warning: using insecure HTTP override for development' >&2 ;;
  *) echo 'TPAHUB_SERVER must use HTTPS' >&2; exit 1 ;;
esac
command -v apt-get >/dev/null || { echo 'apt-get is required' >&2; exit 1; }
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates gnupg
if [[ -n "$PACKAGE_PATH" || -n "$PACKAGE_SHA256" ]]; then
  [[ -n "$PACKAGE_PATH" && -n "$PACKAGE_SHA256" ]] || { echo 'TPAHUB_PACKAGE_PATH and TPAHUB_PACKAGE_SHA256 must be supplied together' >&2; exit 1; }
  [[ -f "$PACKAGE_PATH" ]] || { echo "TPAHUB_PACKAGE_PATH does not exist: $PACKAGE_PATH" >&2; exit 1; }
  actual=$(sha256sum "$PACKAGE_PATH" | awk '{print $1}')
  [[ "$actual" == "$PACKAGE_SHA256" ]] || { echo "TPAHUB package checksum mismatch: expected $PACKAGE_SHA256 got $actual" >&2; exit 1; }
  apt-get install -y "$PACKAGE_PATH"
  exit 0
fi
name="tpahub-$REPOSITORY_ID"
key="/etc/apt/keyrings/$name.gpg"
source="/etc/apt/sources.list.d/$name.list"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
cat >"$tmp" <<'TPAHUB_PUBLIC_KEY'
-----BEGIN PGP PUBLIC KEY BLOCK-----

mQGNBGqT++sBDAC2kYO3MbrJD04JE5w/Th6n2VJQtihhhNo4r2ePqS8YVqXnX77M
mEdnyEL0WyNOl4EeBTa6dltSdgUe18YAFezvk48ZvaT9AkKZbtnagVFYCWpAPJky
r9Q34hiHyJzH14iF+EUhb+Zyj3628JrkAp2moylt4kbw8/8On5MFv5jzcTp+BtlC
TJ/gpu8YHJ0Am/6ttD8L3PqW4q4BJ7dN8MCorBhDJDXR+YB82fEUds1WwBtIIsXM
XJb8tczUzeK/ATmmVD3kDX24VIc/RR9TmzQpmWn7pBVimHbq5bel0VdWgW85QZMg
9StcfgFVmhwyhYQWrV6++WyUmOjDCDIWWu2ZNAsoXm2v8MsVfOQkyY1CylUVHQZ3
JexA0uVVCzFTKjwDKI1FK2/oFriFhzxc2Iwd8x7TfYuE7id/NWlGbffYLjfEGL+N
Nahw3yu5iOxHGQ2AvK+npeqhgkvfsPdSuccSI3EOtNq6avXyXN7wrVExRzfsqXj8
IsJtRbR+TIL2upkAEQEAAbQpVFBBIEh1YiBSZXBvc2l0b3J5IDx0cGEtaHViQGx1
cHJpY2h0Lm5ldD6JAc4EEwEKADgWIQRCMT2fXx8xmrWhj2EiOAzuINcxxgUCapP7
6wIbLwULCQgHAgYVCgkICwIEFgIDAQIeAQIXgAAKCRAiOAzuINcxxvQVDACKoNbs
6YnBmvNB3lJxko903VLWcHQSaq9g8w2t7qOLBS/u0PTrjqO4uXo5HdXLY3mBF0nf
FBQ0WdH4xzqDheVenIBAEhaADbTnflTrK8X5h2ve7dxg8ZaUKgQdrW8CWDG+vGDE
aoUVzm3vPo+YRrjM4k9DP9F1pvsmJNTHedR+qRDbh1yrd9T9u0un40cAqX19I4pD
Brzo12RZsxhaHw6yeoN5bxpbRV48pyUEams0aaVsvdLKRzu/aXGkkoEI8d/0ztBR
j59g1LN/S9aKatVrIsMPzDOOBaYMoj+DSWc/OvKZB5FlR8RtuhIQ4DjNCOed5xd1
g/XFeJAE0yVdIjkbc0tcGJWYygUpIpT5q8RzU25Hvds1XRhKeK0umgIQQB7IKg0s
ouL1deCzzffyt0sVR6+TLEb2VAHGc5+jHAqTwnOocon5y/OMsspHR4STXRw9jEso
u5Cm4DCnwBcPS90XUuwRg/McHSBSAgj9T90phykAdZDCmRxqU7Nv/BO/Y9c=
=i38q
-----END PGP PUBLIC KEY BLOCK-----
TPAHUB_PUBLIC_KEY
gpg --batch --dearmor --yes -o "$key" "$tmp"
chmod 0644 "$key"
printf 'deb [signed-by=%s] %s/r/%s/ stable main\n' "$key" "$SERVER" "$REPOSITORY_ID" > "$source"
apt-get update
apt-get install -y tpahub
