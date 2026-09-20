#!/usr/bin/env bash
set -euo pipefail
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SOURCES_URL=${TPARUN_SOURCES_URL:-https://tpa.run/tparun.sources}
SOURCES_SHA256=${TPARUN_SOURCES_SHA256:-d705adf0cc1943be3c46e11bab749710caaaf29e1d88fe956cdcd848cbfb073d}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM
cat >"$TMP/Dockerfile" <<'EOF'
FROM debian:bookworm-slim
ARG SOURCES_URL
ARG SOURCES_SHA256
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates
RUN curl -fsSL "$SOURCES_URL" -o /tmp/tparun.sources && echo "$SOURCES_SHA256  /tmp/tparun.sources" | sha256sum -c -
RUN install -m 0644 /tmp/tparun.sources /etc/apt/sources.list.d/tparun.sources
RUN apt-get update && apt-get install -y tparun && command -v tparun && dpkg --audit && apt-get check
EOF
docker build --no-cache --network=host \
  --build-arg SOURCES_URL="$SOURCES_URL" --build-arg SOURCES_SHA256="$SOURCES_SHA256" \
  -f "$TMP/Dockerfile" "$TMP" >/dev/null
printf '%s\n' 'TPA.RUN DEBIAN RELEASE BOOTSTRAP QUALIFIED'
