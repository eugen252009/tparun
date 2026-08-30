#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo 'install.sh must run as root' >&2
  exit 1
fi
SERVER=${TPAHUB_SERVER:-https://tpa.lupricht.net}
REPOSITORY_ID=${1:?usage: install.sh <public-repository-id>}
SERVER=${SERVER%/}
case "$SERVER" in https://*) ;; *) echo 'TPAHUB_SERVER must use HTTPS' >&2; exit 1;; esac
name="tpahub-$REPOSITORY_ID"
key="/etc/apt/keyrings/$name.gpg"
source="/etc/apt/sources.list.d/$name.list"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
install -d -m 0755 /etc/apt/keyrings
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
  "$SERVER/v1/repositories/$REPOSITORY_ID/signing-key" -o "$tmp"
gpg --batch --dearmor --yes -o "$key" "$tmp"
chmod 0644 "$key"
printf 'deb [signed-by=%s] %s/r/%s/ stable main\n' "$key" "$SERVER" "$REPOSITORY_ID" > "$source"
apt-get update
apt-get install -y tpahub
