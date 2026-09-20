#!/bin/sh
set -eu

: "${GNUPGHOME:=/config/gnupg}"
: "${TPARUN_DATA_DIR:=/data}"
export GNUPGHOME TPARUN_DATA_DIR
mkdir -p "$GNUPGHOME" "$HOME"
chmod 700 "$GNUPGHOME" "$HOME"

ensure_signing_key() {
    if [ -n "${TPARUN_GPG_FINGERPRINT:-}" ]; then
        gpg --batch --list-secret-keys "$TPARUN_GPG_FINGERPRINT" >/dev/null 2>&1 || {
            echo "configured TPARUN_GPG_FINGERPRINT is not present in $GNUPGHOME" >&2
            exit 1
        }
        return
    fi
    fingerprint=$(gpg --batch --with-colons --list-secret-keys 2>/dev/null | awk -F: '$1 == "fpr" {print $10; exit}')
    if [ -z "$fingerprint" ]; then
        gpg --batch --pinentry-mode loopback --passphrase '' --quick-generate-key "TPA.run self-hosted signing key" rsa2048 sign 0
        fingerprint=$(gpg --batch --with-colons --list-secret-keys | awk -F: '$1 == "fpr" {print $10; exit}')
    fi
    [ -n "$fingerprint" ] || { echo 'unable to determine persistent signing fingerprint' >&2; exit 1; }
    export TPARUN_GPG_FINGERPRINT="$fingerprint"
}

run_tparun() {
    /usr/local/bin/tparun "$@"
}

if [ "${1:-serve}" = "init" ]; then
    run_tparun init
    ensure_signing_key
    printf 'Initialized self-hosted state; signing fingerprint: %s\n' "$TPARUN_GPG_FINGERPRINT"
    exit 0
fi

if [ "${1:-serve}" = "serve" ]; then
    case "${TPARUN_OPERATOR_ORIGIN:-}" in
        https://*) ;;
        http://localhost*|http://127.0.0.1*|http://[::1]*) ;;
        *) echo 'TPARUN_OPERATOR_ORIGIN must be HTTPS outside loopback' >&2; exit 1 ;;
    esac
    if [ ! -f "$TPARUN_DATA_DIR/server-profile.json" ]; then
        run_tparun init
    fi
    ensure_signing_key
    shift
    exec /usr/local/bin/tparun serve "$@"
fi


exec "$@"
