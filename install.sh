#!/usr/bin/env bash
set -euo pipefail

if [[ ${EUID:-$(id -u)} -ne 0 ]]; then
  echo 'install.sh must run as root' >&2
  exit 1
fi
SERVER=${TPAHUB_SERVER:-https://tpa.lupricht.net}
DEFAULT_REPOSITORY_ID="a0d8fd0f82235ee431c41581aa253ec8"
REPOSITORY_ID=${TPAHUB_REPOSITORY_ID:-$DEFAULT_REPOSITORY_ID}
SERVER=${SERVER%/}
case "$SERVER" in https://*) ;; http://*) echo 'warning: using insecure HTTP override for development' >&2 ;; *) echo 'TPAHUB_SERVER must use HTTPS' >&2; exit 1;; esac
for command in apt-get install; do command -v "${command%% *}" >/dev/null || { echo "required command missing: ${command%% *}" >&2; exit 1; }; done
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl gnupg
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
