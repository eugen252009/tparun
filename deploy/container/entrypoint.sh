#!/bin/sh
set -eu

: "${GNUPGHOME:=/config/gnupg}"
: "${TPAHUB_DATA_DIR:=/data}"
export GNUPGHOME TPAHUB_DATA_DIR
mkdir -p "$GNUPGHOME" "$HOME"
chmod 700 "$GNUPGHOME" "$HOME"

ensure_signing_key() {
    if [ -n "${TPAHUB_GPG_FINGERPRINT:-}" ]; then
        gpg --batch --list-secret-keys "$TPAHUB_GPG_FINGERPRINT" >/dev/null 2>&1 || {
            echo "configured TPAHUB_GPG_FINGERPRINT is not present in $GNUPGHOME" >&2
            exit 1
        }
        return
    fi
    fingerprint=$(gpg --batch --with-colons --list-secret-keys 2>/dev/null | awk -F: '$1 == "fpr" {print $10; exit}')
    if [ -z "$fingerprint" ]; then
        gpg --batch --quick-generate-key "TPA Hub self-hosted signing key" rsa2048 sign 0
        fingerprint=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1 == "fpr" {print $10; exit}')
    fi
    [ -n "$fingerprint" ] || { echo 'unable to determine persistent signing fingerprint' >&2; exit 1; }
    export TPAHUB_GPG_FINGERPRINT="$fingerprint"
}

if [ "${1:-serve}" = "init" ]; then
    /usr/local/bin/tpahub init
    ensure_signing_key
    printf 'Initialized self-hosted state; signing fingerprint: %s\n' "$TPAHUB_GPG_FINGERPRINT"
    exit 0
fi

if [ "${1:-serve}" = "serve" ]; then
    if [ ! -f "$TPAHUB_DATA_DIR/server-profile.json" ]; then
        /usr/local/bin/tpahub init
    fi
    ensure_signing_key
fi

exec "$@"
