#!/bin/sh
# Qualify execution after install.sh was transported without a downloader.
set -eu
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
cp install.sh "$TMP/install.sh"
cat >"$TMP/Dockerfile" <<'EOF'
FROM debian:bookworm-slim
COPY install.sh /install.sh
RUN ! command -v curl && ! command -v wget && ! command -v tpahub
RUN bash /install.sh
RUN dpkg-query -W tpahub && tpahub --help && apt-get update
EOF
docker build --no-cache --network=host -f "$TMP/Dockerfile" "$TMP" >/dev/null
printf '%s\n' 'TPA HUB LOCAL-FILE BOOTSTRAP QUALIFIED'
