# syntax=docker/dockerfile:1
# Build context is prepared by deploy/container/build.sh and contains ./tpahub
# and ./tpa. The runtime image contains no source or Go toolchain.
FROM golang:1.26-bookworm AS build
ARG TPAHUB_REVISION=unknown
ARG TPAHUB_VERSION=0.1.1
WORKDIR /src/tpahub
COPY tpahub/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/eugen252009/tpahub/internal/version.BuildRevision=${TPAHUB_REVISION}" -o /out/tpahub ./cmd/tpahub
WORKDIR /src/tpa
COPY tpa/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tpa .

FROM debian:bookworm-slim
ARG TPAHUB_REVISION=unknown
ARG TPAHUB_VERSION=0.1.1
LABEL org.opencontainers.image.title="TPA Hub self-hosted" \
      org.opencontainers.image.version="${TPAHUB_VERSION}" \
      org.opencontainers.image.revision="${TPAHUB_REVISION}"
RUN apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates curl gnupg util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 tpahub \
    && useradd --uid 10001 --gid 10001 --home-dir /config/home --create-home --shell /usr/sbin/nologin tpahub \
    && mkdir -p /config /data \
    && chown -R 10001:10001 /config /data
COPY --from=build /out/tpahub /usr/local/bin/tpahub
COPY --from=build /out/tpa /usr/local/bin/tpa
COPY deploy/container/entrypoint.sh /usr/local/bin/tpahub-entrypoint
RUN chmod 0755 /usr/local/bin/tpahub /usr/local/bin/tpa /usr/local/bin/tpahub-entrypoint
ENV HOME=/config/home \
    TPAHUB_PROFILE=self-hosted \
    TPAHUB_LISTEN=0.0.0.0:8080 \
    TPAHUB_DATA_DIR=/data \
    TPA_BIN=/usr/local/bin/tpa \
    GNUPGHOME=/config/gnupg \
    TPAHUB_REGISTRATION_ENABLED=true \
    TPAHUB_CAPABILITY_SOURCE=local \
    TPAHUB_BILLING=none \
    TPAHUB_BILLING_PROVIDER=none \
    TPAHUB_DELIVERY_MODE=metrics-only \
    TPAHUB_EGRESS_ENFORCEMENT=none
VOLUME ["/config", "/data"]
EXPOSE 8080
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/tpahub-entrypoint"]
CMD ["serve"]
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=6 CMD ["/usr/bin/curl", "-fsS", "http://127.0.0.1:8080/ready"]
