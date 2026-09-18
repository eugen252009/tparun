#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
RELEASE_URL=${TPAHUB_RELEASE_PACKAGE_URL:-https://tpa.lupricht.net/tpahub-release_0.1.1_all.deb}
RELEASE_SHA256=${TPAHUB_RELEASE_PACKAGE_SHA256:-f51804b008ca1e8fa668e5df3fa4b60d74f21528540060d6fee8c0abc8e3585c}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
cat >"$TMP/Dockerfile" <<'EOF'
FROM debian:bookworm-slim
ARG RELEASE_URL
ARG RELEASE_SHA256
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
RUN curl -fsSL "$RELEASE_URL" -o /tmp/tpahub-release.deb && echo "$RELEASE_SHA256  /tmp/tpahub-release.deb" | sha256sum -c -
RUN curl -fsSL https://tpa.lupricht.net/install.sh -o /tmp/install.sh && \
    TPAHUB_RELEASE_PACKAGE_PATH=/tmp/tpahub-release.deb TPAHUB_RELEASE_PACKAGE_SHA256="$RELEASE_SHA256" bash /tmp/install.sh
RUN test -s /usr/share/keyrings/tpahub-archive-keyring.gpg && \
    test -s /etc/apt/sources.list.d/tpahub.sources && \
    grep -F 'Signed-By: /usr/share/keyrings/tpahub-archive-keyring.gpg' /etc/apt/sources.list.d/tpahub.sources
RUN apt-get update && dpkg-query -W tpahub && tpahub --help
RUN TPAHUB_RELEASE_PACKAGE_PATH=/tmp/tpahub-release.deb TPAHUB_RELEASE_PACKAGE_SHA256="$RELEASE_SHA256" bash /tmp/install.sh && \
    test "$(find /etc/apt/sources.list.d -name 'tpahub.sources' | wc -l)" = 1 && apt-get update
EOF
docker build --no-cache --network=host \
  --build-arg RELEASE_URL="$RELEASE_URL" --build-arg RELEASE_SHA256="$RELEASE_SHA256" \
  -f "$TMP/Dockerfile" "$TMP" >/dev/null
printf '%s\n' 'TPA HUB DEBIAN RELEASE BOOTSTRAP QUALIFIED'
