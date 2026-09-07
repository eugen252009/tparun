#!/bin/sh
# Qualify the public zero-bootstrap installer in a clean Debian environment.
set -eu
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
cat >"$TMP/Dockerfile" <<'EOF'
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
RUN curl -fsSL https://tpa.lupricht.net/install.sh | bash
RUN test -s /etc/apt/keyrings/tpahub-*.gpg && \
    test -s /etc/apt/sources.list.d/tpahub-*.list && \
    grep -F 'https://tpa.lupricht.net/r/748316a73ce0eff46f8888f956c5eeb0/' /etc/apt/sources.list.d/tpahub-*.list
RUN apt-get update && dpkg-query -W tpahub && tpahub --help
RUN curl -fsSL https://tpa.lupricht.net/install.sh | bash && \
    test "$(find /etc/apt/sources.list.d -name 'tpahub-*.list' | wc -l)" = 1 && apt-get update
EOF
docker build --no-cache --network=host -f "$TMP/Dockerfile" "$TMP" >/dev/null
printf '%s\n' 'TPA HUB ZERO-BOOTSTRAP INSTALL QUALIFIED'
