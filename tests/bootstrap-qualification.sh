#!/bin/sh
# Qualify the public zero-bootstrap installer in a clean Debian environment.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
INSTALLER_REPOSITORY=$(awk -F '"' '/^DEFAULT_REPOSITORY_ID=/{print $2}' "$ROOT/install.sh")
printf '%s\n' "$INSTALLER_REPOSITORY" | grep -Eq '^[a-f0-9]{32}$'
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
cat >"$TMP/Dockerfile" <<'EOF'
FROM debian:bookworm-slim
ARG INSTALLER_REPOSITORY
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
RUN curl -fsSL https://tpa.lupricht.net/install.sh | bash
RUN test -s /etc/apt/keyrings/tpahub-*.gpg && \
    test -s /etc/apt/sources.list.d/tpahub-*.list && \
    grep -F "https://tpa.lupricht.net/r/$INSTALLER_REPOSITORY/" /etc/apt/sources.list.d/tpahub-*.list
RUN apt-get update && dpkg-query -W tpahub && tpahub --help
RUN curl -fsSL https://tpa.lupricht.net/install.sh | bash && \
    test "$(find /etc/apt/sources.list.d -name 'tpahub-*.list' | wc -l)" = 1 && apt-get update
EOF
docker build --no-cache --network=host --build-arg INSTALLER_REPOSITORY="$INSTALLER_REPOSITORY" -f "$TMP/Dockerfile" "$TMP" >/dev/null
printf '%s\n' 'TPA HUB ZERO-BOOTSTRAP INSTALL QUALIFIED'
