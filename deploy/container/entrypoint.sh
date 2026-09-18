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
        gpg --batch --pinentry-mode loopback --passphrase '' --quick-generate-key "TPA Hub self-hosted signing key" rsa2048 sign 0
        fingerprint=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1 == "fpr" {print $10; exit}')
    fi
    [ -n "$fingerprint" ] || { echo 'unable to determine persistent signing fingerprint' >&2; exit 1; }
    export TPAHUB_GPG_FINGERPRINT="$fingerprint"
}

run_hub() {
    /usr/local/bin/tpahub "$@"
}

if [ "${1:-serve}" = "init" ]; then
    run_hub init
    ensure_signing_key
    printf 'Initialized self-hosted state; signing fingerprint: %s\n' "$TPAHUB_GPG_FINGERPRINT"
    exit 0
fi

if [ "${1:-serve}" = "serve" ]; then
    case "${TPAHUB_OPERATOR_ORIGIN:-}" in
        https://*) ;;
        http://localhost*|http://127.0.0.1*|http://[::1]*) ;;
        *) echo 'TPAHUB_OPERATOR_ORIGIN must be HTTPS outside loopback' >&2; exit 1 ;;
    esac
    if [ ! -f "$TPAHUB_DATA_DIR/server-profile.json" ]; then
        run_hub init
    fi
    ensure_signing_key
    shift
    exec /usr/local/bin/tpahub serve "$@"
fi

if [ "${1:-}" = "tpahub" ]; then
    shift
    exec /usr/local/bin/tpahub "$@"
fi

exec "$@"
